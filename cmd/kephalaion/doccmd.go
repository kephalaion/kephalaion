package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	hubstore "github.com/kephalaion/kephalaion/internal/hub/store"
	"github.com/kephalaion/kephalaion/internal/ident"
)

const hubDocUsage = `Aufruf:
  kephalaion hub doc put  <collection> <name> [--file pfad]
  kephalaion hub doc get  <collection> <name>
  kephalaion hub doc list <collection> [verzeichnis]
  kephalaion hub doc rm   <collection> <name>

Kommandos:
  put    legt ein Dokument an oder ersetzt seinen Inhalt; der Inhalt kommt aus
         --file oder von der Standardeingabe. Unveränderter Inhalt schreibt
         nichts und zählt keine Revision.
  get    gibt den Inhalt eines Dokuments aus
  list   zeigt den Inhalt eines Verzeichnisses, nach Name sortiert;
         Unterverzeichnisse enden auf '/'
  rm     löscht ein Dokument: Es bleibt eine Löschmarke ohne Inhalt, der Name
         ist danach wieder frei

Der Name ist ein Pfad in der Collection, Segmente durch '/' getrennt: relativ,
kein leeres Segment, kein '.' oder '..', kein '\' und keine Steuerzeichen,
höchstens 1024 Bytes (255 je Segment). Ein Name kann nicht zugleich Dokument
und Verzeichnis sein. Namen mit dem Präfix SYSTEM: schreibt nur der Hub
selbst. Inhalt: UTF-8-Text, höchstens 1 MiB. Jede Änderung ist eine Revision
und steht im Protokoll (actions) als admin.

Optionen:
  --file pfad    Inhalt aus dieser Datei statt von der Standardeingabe (put)
  --config pfad  Ort der config (siehe kephalaion hub init --help)
`

const hubImportUsage = `Aufruf:
  kephalaion hub import <collection> <verzeichnis> [--prefix pfad/]

Spielt alle Dateien eines Verzeichnisses samt Unterverzeichnissen als
Dokumente ein: Der Name ist der relative Pfad, mit --prefix davor. Anlegen oder
Ersetzen wie bei hub doc put; unveränderte Dokumente bleiben unberührt.
Dokumente, die im Verzeichnis fehlen, bleiben am Hub stehen.

Der Import ist ein Schreibvorgang: eine Transaktion, eine Revision für alle
Dokumente. Scheitert ein Dokument (etwa ein ungültiger Name oder ein Konflikt
zwischen Dokument und Verzeichnis), wird nichts geschrieben.

Versteckte Dateien und Verzeichnisse (Name beginnt mit '.', etwa .git) werden
stillschweigend übergangen. Gemeldet und übersprungen werden Dateien, die kein
UTF-8-Text sind, die größer als 1 MiB sind oder keine gewöhnlichen Dateien
sind (etwa symbolische Links).

Optionen:
  --prefix pfad/  Verzeichnis in der Collection, unter dem die Dateien landen
  --config pfad   Ort der config (siehe kephalaion hub init --help)
`

func runHubDoc(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	u := hubDocUsage
	return dispatch("hub doc", u, args, stdout, stderr, map[string]func([]string) int{
		"put": func(a []string) int {
			c := newCommand("hub doc put", u, stdout, stderr, "<collection>", "<name>")
			file := c.fs.String("file", "", "")
			return c.hubDo(a, func(ctx context.Context, s hubstore.Store, pos []string) error {
				content, err := readContent(*file, stdin)
				if err != nil {
					return err
				}
				r, err := s.PutDocument(ctx, pos[0], pos[1], content)
				if err != nil {
					return err
				}
				fmt.Fprintf(stdout, "Dokument %s in %s %s (Revision %d, id %s).\n", pos[1], pos[0], r.Outcome, r.Revision, r.ID)
				return nil
			})
		},
		"get": func(a []string) int {
			c := newCommand("hub doc get", u, stdout, stderr, "<collection>", "<name>")
			return c.hubDo(a, func(ctx context.Context, s hubstore.Store, pos []string) error {
				d, err := s.Document(ctx, pos[0], pos[1])
				if err != nil {
					return err
				}
				_, err = io.WriteString(stdout, d.Content)
				return err
			})
		},
		"list": func(a []string) int {
			c := newCommand("hub doc list", u, stdout, stderr, "<collection>")
			c.optional = 1
			return c.hubDo(a, func(ctx context.Context, s hubstore.Store, pos []string) error {
				dir := ""
				if len(pos) > 1 {
					dir = pos[1]
				}
				docs, err := s.Documents(ctx, pos[0], dir)
				if err != nil {
					return err
				}
				listed := make([]listedDoc, 0, len(docs))
				for _, d := range docs {
					listed = append(listed, listedDoc{d.Name, d.Revision, d.UpdatedAt, d.UpdatedBy})
				}
				return printListing(stdout, pos[0], dir, listed)
			})
		},
		"rm": func(a []string) int {
			c := newCommand("hub doc rm", u, stdout, stderr, "<collection>", "<name>")
			return c.hubDo(a, func(ctx context.Context, s hubstore.Store, pos []string) error {
				d, err := s.DeleteDocument(ctx, pos[0], pos[1])
				if err != nil {
					return err
				}
				fmt.Fprintf(stdout, "Dokument %s in %s gelöscht (Löschmarke, Revision %d).\n", pos[1], pos[0], d.Revision)
				return nil
			})
		},
	})
}

// readContent liest den Inhalt für put aus einer Datei oder von stdin, nie
// mehr als die Obergrenze und ein Byte — genug, um „zu groß“ zu erkennen.
func readContent(file string, stdin io.Reader) (string, error) {
	r := stdin
	if file != "" {
		f, err := os.Open(file)
		if err != nil {
			return "", err
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, hubstore.MaxDocumentBytes+1))
	if err != nil {
		return "", fmt.Errorf("Inhalt lesen: %w", err)
	}
	if err := hubstore.CheckContent(string(b)); err != nil {
		return "", err
	}
	return string(b), nil
}

// listedDoc ist, was printListing von einem Dokument zeigt — am Hub wie
// aus der Replica des Nodes.
type listedDoc struct {
	Name      string
	Revision  int64
	UpdatedAt int64
	UpdatedBy string
}

// printListing zeigt die Einträge eines Verzeichnisses: Dokumente direkt
// darin und Unterverzeichnisse, diese einmal und mit '/' am Ende. docs sind
// nach Name sortiert. collection nennt die Collection in der Meldung, am
// Node als Adresse <hub>:<collection>.
func printListing(w io.Writer, collection, dir string, docs []listedDoc) error {
	prefix, err := ident.DocDirPrefix(dir)
	if err != nil {
		return err
	}
	if len(docs) == 0 {
		if prefix == "" {
			fmt.Fprintf(w, "Keine Dokumente in %s.\n", collection)
		} else {
			fmt.Fprintf(w, "Keine Dokumente unter %s in %s.\n", prefix, collection)
		}
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tREVISION\tGEÄNDERT")
	lastDir := ""
	for _, d := range docs {
		child, isDir, ok := ident.DocChild(prefix, d.Name)
		if !ok {
			continue
		}
		if isDir {
			if child != lastDir {
				fmt.Fprintf(tw, "%s/\t–\t–\n", child)
				lastDir = child
			}
			continue
		}
		fmt.Fprintf(tw, "%s\t%d\t%s von %s\n", child, d.Revision, formatMillis(d.UpdatedAt), d.UpdatedBy)
	}
	return tw.Flush()
}

func runHubImport(args []string, stdout, stderr io.Writer) int {
	c := newCommand("hub import", hubImportUsage, stdout, stderr, "<collection>", "<verzeichnis>")
	prefixFlag := c.fs.String("prefix", "", "")
	return c.hubDo(args, func(ctx context.Context, s hubstore.Store, pos []string) error {
		prefix, err := ident.DocDirPrefix(*prefixFlag)
		if err != nil {
			return fmt.Errorf("--prefix: %w", err)
		}
		docs, skipped, err := readImportDir(pos[1], prefix)
		if err != nil {
			return err
		}
		for _, sk := range skipped {
			fmt.Fprintf(stderr, "übersprungen: %s (%s)\n", sk.path, sk.reason)
		}
		res, err := s.ImportDocuments(ctx, pos[0], docs)
		if err != nil {
			return err
		}
		for _, name := range res.Generated {
			fmt.Fprintf(stderr, "übersprungen: %s (erzeugte Datei; neu bauen mit kephalaion hub collection "+
				"rebuild-docs %s)\n", name, pos[0])
		}
		counts := map[hubstore.Outcome]int{}
		for _, r := range res.Results {
			counts[r.Outcome]++
			if r.Outcome != hubstore.Unchanged {
				fmt.Fprintf(stdout, "%-9s %s\n", r.Outcome.String()+":", r.Name)
			}
		}
		rev := "keine neue Revision"
		if res.Revision > 0 {
			rev = fmt.Sprintf("Revision %d", res.Revision)
		}
		fmt.Fprintf(stdout, "Import nach %s: %d angelegt, %d ersetzt, %d unverändert, %d übersprungen — %s.\n",
			pos[0], counts[hubstore.Created], counts[hubstore.Replaced], counts[hubstore.Unchanged],
			len(skipped)+len(res.Generated), rev)
		return nil
	})
}

// skippedFile ist eine Datei, die der Import meldet und übergeht.
type skippedFile struct {
	path   string
	reason string
}

// readImportDir liest ein Verzeichnis rekursiv, in lexikalischer Reihenfolge.
// Versteckte Dateien und Verzeichnisse übergeht es stillschweigend; was kein
// UTF-8-Text, zu groß oder keine gewöhnliche Datei ist, meldet es in
// skipped. Die Namen prüft erst der Store.
func readImportDir(root, prefix string) (docs []hubstore.DocumentInput, skipped []skippedFile, err error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, nil, err
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("%s ist kein Verzeichnis", root)
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !d.Type().IsRegular() {
			skipped = append(skipped, skippedFile{rel, "keine gewöhnliche Datei"})
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if fi.Size() > hubstore.MaxDocumentBytes {
			skipped = append(skipped, skippedFile{rel, "größer als 1 MiB"})
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := hubstore.CheckContent(string(b)); err != nil {
			reason := "kein UTF-8-Text"
			if errors.Is(err, hubstore.ErrTooLarge) {
				reason = "größer als 1 MiB"
			}
			skipped = append(skipped, skippedFile{rel, reason})
			return nil
		}
		docs = append(docs, hubstore.DocumentInput{Name: prefix + rel, Content: string(b)})
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return docs, skipped, nil
}
