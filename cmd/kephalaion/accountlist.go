package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/kephalaion/kephalaion/internal/assistant"
	"github.com/kephalaion/kephalaion/internal/ident"
)

// node account list (Task 028): alle Token-Dateien des Linux-Users unter
// tokens/ mit ihrem Zustand — gefragt beim Node über die Route zum Prüfen,
// je Datei genau eine Anfrage, nicht in einer eigenen Datenbank. Kein Token
// in der Ausgabe.

// tokenEntry ist eine Datei unter tokens/<hub>/: <account>.token oder
// <account>.token.pending.
type tokenEntry struct {
	Hub     string `json:"hub"`
	Account string `json:"account"`
	// Kind ist token oder pending.
	Kind  string     `json:"kind"`
	File  string     `json:"file"`
	State tokenState `json:"state"`
	User  string     `json:"user,omitempty"`
	// Collections sind bei gültigem Token die Collections des Accounts, die
	// der Node abgleichen darf; sonst leer.
	Collections []string `json:"collections"`
	// Reason sagt bei allem außer gültig, warum.
	Reason string `json:"reason,omitempty"`
}

const (
	kindToken   = "token"
	kindPending = "pending"
)

// tokenFiles sammelt die Token-Dateien unter dir, nach Hub, Account und Art
// (token vor pending). Verzeichnisse und Dateien, deren Name kein Alias bzw.
// Account sein kann, zählen nicht.
func tokenFiles(dir string) ([]tokenEntry, error) {
	hubs, err := assistant.Hubs(dir)
	if err != nil {
		return nil, err
	}
	var out []tokenEntry
	for _, hub := range hubs {
		entries, err := os.ReadDir(filepath.Join(dir, hub))
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name, kind := e.Name(), kindToken
			if rest, ok := strings.CutSuffix(name, ".pending"); ok {
				name, kind = rest, kindPending
			}
			account, ok := strings.CutSuffix(name, ".token")
			if !ok || ident.CheckPrincipalName("Account", account) != nil {
				continue
			}
			out = append(out, tokenEntry{Hub: hub, Account: account, Kind: kind,
				File: filepath.Join(dir, hub, e.Name()), Collections: []string{}})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Hub != b.Hub {
			return a.Hub < b.Hub
		}
		if a.Account != b.Account {
			return a.Account < b.Account
		}
		return a.Kind == kindToken && b.Kind == kindPending
	})
	return out, nil
}

// accountListOutput ist die Ausgabe von list --json.
type accountListOutput struct {
	Node  string       `json:"node"`
	Files []tokenEntry `json:"files"`
}

func runNodeAccountList(args []string, stdout, stderr io.Writer) int {
	c := newCommand("node account list", nodeAccountUsage, stdout, stderr)
	flags := newAccountNodeFlags(c)
	asJSON := c.fs.Bool("json", false, "")
	if _, code, ok := c.parse(args); !ok {
		return code
	}
	addr, rootCAs, _, code := flags.resolve(c)
	if code != 0 {
		return code
	}
	dir, err := assistant.TokensDir()
	if err != nil {
		return c.fail(err)
	}
	files, err := tokenFiles(dir)
	if err != nil {
		return c.fail(err)
	}
	exit := 0
	if len(files) > 0 {
		// Erst den Node ohne Token prüfen: Antwortet er nicht, geht keine
		// Anfrage mit Token hinaus, und jede Datei ist „nicht geprüft“.
		if _, err := probeNode(context.Background(), addr, rootCAs, *flags.caFile); err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", c.name, err)
			for i := range files {
				files[i].State, files[i].Reason = stateUnchecked, "Node nicht erreichbar"
			}
			exit = 1
		} else {
			client := newAccountClient(addr, rootCAs, *flags.caFile)
			for i := range files {
				checkFile(client, &files[i])
			}
		}
	}
	if *asJSON {
		data, err := json.MarshalIndent(accountListOutput{Node: addr.Base, Files: append([]tokenEntry{}, files...)},
			"", "  ")
		if err != nil {
			return c.fail(err)
		}
		fmt.Fprintf(stdout, "%s\n", data)
		return exit
	}
	fmt.Fprintf(stdout, "Node: %s\n", addr.Base)
	if len(files) == 0 {
		fmt.Fprintf(stdout, "Keine Token-Dateien unter %s. Einen Account einrichten: kephalaion node account setup "+
			"<hub> <account> <einrichtungstoken>\n", dir)
		return exit
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HUB\tACCOUNT\tDATEI\tZUSTAND\tUSER\tCOLLECTIONS")
	for _, f := range files {
		fmt.Fprintf(tw, "%s\t%s\t.%s\t%s\t%s\t%s\n", f.Hub, f.Account, f.Kind, f.State.text(), orDash(f.User),
			joinOrDash(f.Collections))
	}
	if err := tw.Flush(); err != nil {
		return c.fail(err)
	}
	listNotes(stdout, files)
	return exit
}

// checkFile fragt den Node nach einer Datei — genau eine Anfrage; eine
// Datei ohne gültiges Token fragt es nicht.
func checkFile(client *accountClient, f *tokenEntry) {
	tok, err := readTokenFile(f.File)
	if err != nil {
		var pe *os.PathError
		reason := err.Error()
		if errors.As(err, &pe) {
			reason = pe.Err.Error()
		}
		f.State, f.Reason = stateUnreadable, reason
		return
	}
	st := client.check(f.Hub, f.Account, tok)
	f.State, f.Reason = st.state, st.reason
	if st.state == stateValid {
		f.User, f.Collections, f.Reason = st.result.User, append([]string{}, st.result.Collections...), ""
	}
}

// listNotes erklärt unter der Tabelle, was nicht gültig ist und was zu tun
// ist.
func listNotes(w io.Writer, files []tokenEntry) {
	hidden := false
	for _, f := range files {
		switch f.State {
		case stateUnchecked, stateUnreadable, stateUnknownHub:
			fmt.Fprintf(w, "%s/%s (.%s): %s — %s\n", f.Hub, f.Account, f.Kind, f.State.text(), f.Reason)
		case stateInvalidOrUnknown:
			hidden = true
		}
		if f.Kind != kindPending {
			continue
		}
		if hasToken(files, f.Hub, f.Account) {
			fmt.Fprintf(w, "%s/%s: .pending neben der Token-Datei — ein rotate mit unklarem Ausgang; mit eigenem Node "+
				"klärt es kephalaion node account check %s %s --token-file %s\n", f.Hub, f.Account, f.Hub, f.Account,
				strings.TrimSuffix(f.File, ".pending"))
		} else {
			fmt.Fprintf(w, "%s/%s: .pending offen — ein setup mit unklarem Ausgang; derselbe Aufruf klärt es: "+
				"kephalaion node account setup %s %s …\n", f.Hub, f.Account, f.Hub, f.Account)
		}
	}
	if hidden {
		fmt.Fprintln(w, "„ungültig oder unbekannt“: Über einen Proxy unterscheidet der Node ein falsches Token nicht von "+
			"einem Hub, den er nicht kennt (<hub> ist der Alias des Hub-Eintrags am Node).")
	}
	for _, f := range files {
		if f.Kind == kindToken && (f.State == stateInvalid || f.State == stateInvalidOrUnknown) {
			fmt.Fprintln(w, "Eine ungültige Token-Datei ist bei jedem Aufruf ein Fehlversuch am Node. Gilt sie nicht "+
				"mehr (rotiert, gesperrt, Account entfernt), sie löschen; ein neues Einrichtungstoken erzeugt der Admin "+
				"(kephalaion hub account token <account>), dann kephalaion node account setup.")
			return
		}
	}
}

func hasToken(files []tokenEntry, hub, account string) bool {
	for _, f := range files {
		if f.Hub == hub && f.Account == account && f.Kind == kindToken {
			return true
		}
	}
	return false
}

// joinOrDash liefert die Liste mit Komma oder „–“.
func joinOrDash(list []string) string {
	if len(list) == 0 {
		return "–"
	}
	return strings.Join(list, ", ")
}
