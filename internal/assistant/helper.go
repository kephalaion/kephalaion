package assistant

import (
	"strings"

	"github.com/kephalaion/kephalaion/internal/ident"
)

// HelperArgs ist der Aufruf des Helfers, den ein Assistent bei jeder
// Verbindung startet: <binary> node mcp headers --tokens-dir <pfad>, dazu je
// gewähltem Hub --hub <alias> (Wahl der Hubs eines Eintrags mit entfernter
// Adresse; ohne Wahl alle Hubs mit Token-Datei) und je Hub der Wahl --account
// <hub>=<account>. binary und tokensDir sind absolut und aufgelöst: Ein
// Assistent aus einer GUI erbt kein PATH, und Codex leert die Umgebung (kein
// XDG_CONFIG_HOME).
func HelperArgs(binary, tokensDir string, hubs []string, choice Choice) []string {
	args := []string{binary, "node", "mcp", "headers", "--tokens-dir", tokensDir}
	for _, hub := range hubs {
		args = append(args, "--hub", hub)
	}
	for _, hub := range choice.Hubs() {
		args = append(args, "--account", hub+"="+choice[hub])
	}
	return args
}

// ParseHelperArgs liest einen Aufruf, wie HelperArgs ihn baut, zurück. ok ist
// false, wenn args kein solcher Aufruf ist.
func ParseHelperArgs(args []string) (binary, tokensDir string, hubs []string, choice Choice, ok bool) {
	if len(args) < 6 || args[1] != "node" || args[2] != "mcp" || args[3] != "headers" || args[4] != "--tokens-dir" {
		return "", "", nil, nil, false
	}
	binary, tokensDir = args[0], args[5]
	rest := args[6:]
	if len(rest)%2 != 0 {
		return "", "", nil, nil, false
	}
	var values []string
	for i := 0; i < len(rest); i += 2 {
		switch {
		case rest[i] == "--hub" && len(values) == 0:
			if ident.CheckName("Hub", rest[i+1]) != nil {
				return "", "", nil, nil, false
			}
			hubs = append(hubs, rest[i+1])
		case rest[i] == "--account":
			values = append(values, rest[i+1])
		default:
			return "", "", nil, nil, false
		}
	}
	choice, err := ParseChoice(values)
	if err != nil {
		return "", "", nil, nil, false
	}
	return binary, tokensDir, hubs, choice, true
}

// shellSafe sind die Zeichen, die in einer Shell ohne Anführungszeichen für
// sich selbst stehen.
const shellSafe = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789@%+=:,./-_"

// ShellQuote schreibt ein Argument so, dass eine POSIX-Shell es unverändert
// weitergibt.
func ShellQuote(arg string) string {
	if arg != "" && strings.Trim(arg, shellSafe) == "" {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// ShellJoin verbindet einen Aufruf zu einer Kommandozeile für eine
// POSIX-Shell.
func ShellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = ShellQuote(a)
	}
	return strings.Join(quoted, " ")
}

// ShellSplit zerlegt eine Kommandozeile, wie ShellJoin sie schreibt: Wörter,
// getrennt durch Leerzeichen, mit einfachen Anführungszeichen und \' dazwischen.
// ok ist false bei allem anderen (doppelte Anführungszeichen, Variablen,
// Umleitungen) — so eine Zeile stammt nicht von hier.
func ShellSplit(line string) (args []string, ok bool) {
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == ' ':
			if inWord {
				args = append(args, cur.String())
				cur.Reset()
				inWord = false
			}
		case c == '\'':
			end := strings.IndexByte(line[i+1:], '\'')
			if end < 0 {
				return nil, false
			}
			cur.WriteString(line[i+1 : i+1+end])
			i += end + 1
			inWord = true
		case c == '\\':
			if i+1 >= len(line) || line[i+1] != '\'' {
				return nil, false
			}
			cur.WriteByte('\'')
			i++
			inWord = true
		case strings.IndexByte(shellSafe, c) >= 0:
			cur.WriteByte(c)
			inWord = true
		default:
			return nil, false
		}
	}
	if inWord {
		args = append(args, cur.String())
	}
	return args, true
}
