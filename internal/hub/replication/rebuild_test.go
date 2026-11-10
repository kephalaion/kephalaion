package replication

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/contract"
)

func (f *fixture) rebuild(acc contract.AccountAuth, coll string) (contract.RebuildDocsResponse, error) {
	return f.api().RebuildDocs(context.Background(), contract.RebuildDocsRequest{Version: contract.Version,
		Auth: f.node(), Account: acc, Collection: coll})
}

// rebuild_docs über local und HTTP (Task 033): Prüfreihenfolge, no_docs,
// rebuilt, current; ein Leser darf anstoßen, eine Collection mit Quelle
// nicht. Danach ein rename eines Verzeichnisses unter docs/: Die Antwort
// trägt die Löschmarken von index.md und Mahnung.
func TestRebuildDocs(t *testing.T) {
	for _, tr := range transports {
		t.Run(tr, func(t *testing.T) {
			f, _, acc := fetchFixture(t, tr)
			leser := acc["leser"]
			cases := []struct {
				name string
				acc  contract.AccountAuth
				coll string
				want error
			}{
				{"Account ungültig", contract.AccountAuth{Account: "leser", Token: acc["bob"].Token}, "a",
					contract.ErrAccountUnauthenticated},
				{"gesperrt", acc["gesperrt"], "a", contract.ErrAccountUnauthenticated},
				{"unbekannte Collection", leser, "fehlt", contract.ErrNotReadable},
				{"Node darf c nicht", acc["bob"], "c", contract.ErrNotReadable},
				{"Quelle", leser, "k-playbook", contract.ErrForbidden},
			}
			for _, c := range cases {
				if _, err := f.rebuild(c.acc, c.coll); !errors.Is(err, c.want) {
					t.Errorf("%s: %v, erwartet %v", c.name, err, c.want)
				}
			}
			// Öffentlich, ohne Zeile, ohne docs/; eigene Collection ohne docs/.
			for _, coll := range []string{"b", "a"} {
				resp, err := f.rebuild(leser, coll)
				if err != nil || resp.Status != contract.RebuildNoDocs || resp.Revision != 0 || resp.Collection != coll {
					t.Errorf("%s ohne docs/: %+v, %v", coll, resp, err)
				}
			}
			ctx := context.Background()
			if _, err := f.api().Create(ctx, f.createReq(acc["bob"], "a", "docs/x/y.md", "# Y\n")); err != nil {
				t.Fatal(err)
			}
			resp, err := f.rebuild(leser, "a")
			if err != nil || resp.Status != contract.RebuildRebuilt || resp.Revision == 0 || resp.Created != 8 ||
				resp.HubID == "" || resp.Version != contract.Version {
				t.Fatalf("neu gebaut: %+v, %v", resp, err)
			}
			resp, err = f.rebuild(acc["bob"], "a")
			if err != nil || resp.Status != contract.RebuildCurrent || resp.Revision != 0 {
				t.Errorf("schon aktuell: %+v, %v", resp, err)
			}
			// Erzeugte Dateien: generated_file über den Vertrag.
			_, err = f.api().Write(ctx, f.writeReq(acc["bob"], "a", "docs/x/index.md", "x", nil))
			if !errors.Is(err, contract.ErrGeneratedFile) {
				t.Errorf("write index.md: %v", err)
			}
			// Die Mahnung ersetzt bob mit write.
			if _, err := f.api().Write(ctx, f.writeReq(acc["bob"], "a", "docs/manual/README.md", "# M\n", nil)); err != nil {
				t.Errorf("Mahnung ersetzen: %v", err)
			}
			w, err := f.api().Rename(ctx, f.renameReq(acc["bob"], "a", "docs/x", "docs/z", nil))
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, r := range w.Rows {
				names = append(names, r.Name)
			}
			if got := strings.Join(names, " "); got != "docs/z/y.md docs/x/README.md docs/x/index.md" {
				t.Errorf("rename: %s", got)
			}
		})
	}
}
