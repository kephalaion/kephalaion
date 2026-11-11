// Package service erzeugt die Dateien, mit denen systemd bzw. launchd
// kephalaion serve als Dienst startet, und richtet den Dienst pro User ein:
// unter Linux die Benutzer-Unit (systemd --user), auf macOS den LaunchAgent.
// Für die globale Installation gibt es die System-Unit nur aus; ablegen tut
// sie Ansible oder der Verwalter. systemctl und launchctl laufen über einen
// Runner, den Tests ersetzen; alle Orte stehen im Manager und sind wie beim
// Upgrader überschreibbar.
//
// Das Paket kennt weder Hub noch Node; was der Dienst startet, ist der Aufruf,
// den der Aufrufer übergibt.
package service

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/kephalaion/kephalaion/internal/config"
)

// Die Namen des Dienstes.
const (
	// UnitName ist der Name der Unit, pro User wie global.
	UnitName = "kephalaion.service"
	// Label ist das Label des LaunchAgent auf macOS. Kephalaion hat keine
	// eigene Domain; das Repository liegt auf GitHub.
	Label = "io.github.kephalaion"
	// DocURL ist die Anleitung, auf die jede Unit verweist.
	DocURL = "https://github.com/kephalaion/kephalaion/blob/main/docs/installation.md"
	// RestartSec ist die Pause vor einem Neustart nach einem Fehler, in
	// Sekunden; launchd heißt das ThrottleInterval.
	RestartSec = 5
)

// checkArgs prüft den Aufruf für eine Unit: ein absoluter Pfad vorn, kein
// Steuerzeichen — eine Zeile in der Unit darf nicht umbrechen.
func checkArgs(args []string) error {
	if len(args) == 0 || !strings.HasPrefix(args[0], "/") {
		return fmt.Errorf("der Dienst braucht das Binary mit absolutem Pfad, nicht %q", strings.Join(args, " "))
	}
	for _, a := range args {
		for _, r := range a {
			if r < 0x20 || r == 0x7f {
				return fmt.Errorf("Steuerzeichen im Aufruf für den Dienst: %q", a)
			}
		}
	}
	return nil
}

// UserUnit liefert die Benutzer-Unit für systemd --user. args ist der
// Aufruf von serve, vorn das Binary mit absolutem Pfad.
func UserUnit(args []string) (string, error) {
	if err := checkArgs(args); err != nil {
		return "", err
	}
	return "[Unit]\n" +
		"Description=Kephalaion (kephalaion serve, pro User)\n" +
		"Documentation=" + DocURL + "\n" +
		"\n" +
		"[Service]\n" +
		"Type=simple\n" +
		"ExecStart=" + execLine(args) + "\n" +
		"Restart=on-failure\n" +
		fmt.Sprintf("RestartSec=%ds\n", RestartSec) +
		"\n" +
		"[Install]\n" +
		"WantedBy=default.target\n", nil
}

// SystemUnit liefert die System-Unit der globalen Installation: serve als
// Systembenutzer, die config aus KEPHALAION_CONFIG, das Datenverzeichnis als
// StateDirectory mit 0700. Die Härtung lässt serve nur dort schreiben; die
// config in /etc ändert nur der Verwalter (sudo -u kephalaion kephalaion …).
func SystemUnit() string {
	return "[Unit]\n" +
		"Description=Kephalaion (kephalaion serve, global)\n" +
		"Documentation=" + DocURL + "\n" +
		"After=network.target\n" +
		"\n" +
		"[Service]\n" +
		"Type=simple\n" +
		"User=" + config.SystemUser + "\n" +
		"Group=" + config.SystemUser + "\n" +
		"Environment=" + config.EnvConfig + "=" + config.SystemConfig + "\n" +
		"ExecStart=" + config.SystemBinary + " serve\n" +
		"Restart=on-failure\n" +
		fmt.Sprintf("RestartSec=%ds\n", RestartSec) +
		"StateDirectory=" + strings.TrimPrefix(config.SystemDataDir, "/var/lib/") + "\n" +
		"StateDirectoryMode=0700\n" +
		"NoNewPrivileges=yes\n" +
		"ProtectSystem=strict\n" +
		"ProtectHome=yes\n" +
		"PrivateTmp=yes\n" +
		"\n" +
		"[Install]\n" +
		"WantedBy=multi-user.target\n"
}

// execLine setzt den Aufruf für ExecStart zusammen: jedes Argument für sich
// in Anführungszeichen, wenn es Leerraum, Anführungszeichen oder einen
// Backslash enthält; % und $ verdoppelt, damit systemd sie nicht ersetzt.
func execLine(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		a = strings.ReplaceAll(a, "%", "%%")
		a = strings.ReplaceAll(a, "$", "$$")
		if a != "" && !strings.ContainsAny(a, " \t\"'\\") {
			parts[i] = a
			continue
		}
		a = strings.ReplaceAll(a, `\`, `\\`)
		a = strings.ReplaceAll(a, `"`, `\"`)
		parts[i] = `"` + a + `"`
	}
	return strings.Join(parts, " ")
}

// LaunchAgent liefert die plist des LaunchAgent: startet beim Laden, startet
// nach einem Fehler neu (KeepAlive nur bei erfolglosem Ende, wie
// Restart=on-failure), Ausgaben nach logPath — launchd hat kein Journal.
func LaunchAgent(args []string, logPath string) (string, error) {
	if err := checkArgs(args); err != nil {
		return "", err
	}
	if err := checkArgs([]string{logPath}); err != nil {
		return "", fmt.Errorf("Log: %w", err)
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + Label + `</string>
	<key>ProgramArguments</key>
	<array>
`)
	for _, a := range args {
		b.WriteString("\t\t<string>" + xmlText(a) + "</string>\n")
	}
	b.WriteString(`	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>` + fmt.Sprint(RestartSec) + `</integer>
	<key>StandardOutPath</key>
	<string>` + xmlText(logPath) + `</string>
	<key>StandardErrorPath</key>
	<string>` + xmlText(logPath) + `</string>
</dict>
</plist>
`)
	return b.String(), nil
}

func xmlText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// InstalledCall liest aus der installierten Unit bzw. plist pro User den
// Aufruf, den der Dienst startet: unter Linux ExecStart, auf macOS
// ProgramArguments — das Gegenstück zu UserUnit und LaunchAgent. So prüft
// upgrade --auto, ob der Dienst dieses Binary mit dieser config startet.
func (m *Manager) InstalledCall() ([]string, error) {
	data, err := os.ReadFile(m.UnitPath())
	if err != nil {
		return nil, err
	}
	switch m.OS {
	case "darwin":
		return plistArguments(data)
	case "linux":
		for _, line := range strings.Split(string(data), "\n") {
			if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ExecStart="); ok {
				return parseExecLine(rest)
			}
		}
		return nil, fmt.Errorf("%s: keine Zeile ExecStart", m.UnitPath())
	}
	return nil, ErrUnsupported
}

// parseExecLine zerlegt eine Zeile ExecStart, wie execLine sie schreibt:
// Argumente durch Leerraum getrennt, in Anführungszeichen mit \" und \\,
// %% und $$ für % und $.
func parseExecLine(line string) ([]string, error) {
	var args []string
	for i := 0; i < len(line); {
		if line[i] == ' ' || line[i] == '\t' {
			i++
			continue
		}
		var b strings.Builder
		if line[i] == '"' {
			i++
			closed := false
			for i < len(line) {
				c := line[i]
				if c == '\\' && i+1 < len(line) {
					b.WriteByte(line[i+1])
					i += 2
					continue
				}
				i++
				if c == '"' {
					closed = true
					break
				}
				b.WriteByte(c)
			}
			if !closed {
				return nil, fmt.Errorf("ExecStart %q: Anführungszeichen nicht geschlossen", line)
			}
		} else {
			for i < len(line) && line[i] != ' ' && line[i] != '\t' {
				b.WriteByte(line[i])
				i++
			}
		}
		a := strings.ReplaceAll(b.String(), "%%", "%")
		args = append(args, strings.ReplaceAll(a, "$$", "$"))
	}
	if len(args) == 0 {
		return nil, errors.New("ExecStart ist leer")
	}
	return args, nil
}

// plistArguments liest die Strings unter ProgramArguments einer plist.
func plistArguments(data []byte) ([]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	var lastKey string
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("plist ohne ProgramArguments (%v)", err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "key":
			var k string
			if err := dec.DecodeElement(&k, &start); err != nil {
				return nil, err
			}
			lastKey = k
		case "array":
			if lastKey != "ProgramArguments" {
				continue
			}
			var arr struct {
				Strings []string `xml:"string"`
			}
			if err := dec.DecodeElement(&arr, &start); err != nil {
				return nil, err
			}
			if len(arr.Strings) == 0 {
				return nil, errors.New("plist: ProgramArguments ist leer")
			}
			return arr.Strings, nil
		default:
			lastKey = ""
		}
	}
}
