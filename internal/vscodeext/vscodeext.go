// Package vscodeext ist neutral und trägt die Erweiterung für VS Code im
// Binary: die eingebettete .vsix, ihre Version und die Erkennung, in welchen
// Editoren (VS Code, Insiders, Cursor, VSCodium) sie installiert ist.
//
// Die .vsix baut `make vscode-vsix` mit vsce nach vsix/kephalaion.vsix; ohne
// Node.js fehlt sie, und das Binary ist ohne Erweiterung gebaut. Ein
// eingecheckter Platzhalter (vsix/README.md) hält das Paket auch dann baubar.
package vscodeext

import (
	"archive/zip"
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
)

// ID ist die Kennung der Erweiterung (publisher.name aus vscode/package.json).
const ID = "kascada.kephalaion"

// DevVersion ist die Version der Erweiterung in einem dev build: Sie sagt
// nichts über ihren Stand und gilt nie als zu alt oder abweichend.
const DevVersion = "0.0.0"

// vsixPath ist der Ort der .vsix im eingebetteten Verzeichnis.
const vsixPath = "vsix/kephalaion.vsix"

//go:embed vsix
var embedded embed.FS

// VSIX liefert die eingebettete .vsix; ok ist false, wenn das Binary ohne
// Erweiterung gebaut ist.
func VSIX() (data []byte, ok bool) {
	return fromFS(embedded)
}

// fromFS liest die .vsix aus fsys; eine leere Datei zählt als fehlend.
func fromFS(fsys fs.FS) ([]byte, bool) {
	data, err := fs.ReadFile(fsys, vsixPath)
	if err != nil || len(data) == 0 {
		return nil, false
	}
	return data, true
}

// maxManifest begrenzt, wie viel von extension/package.json gelesen wird.
const maxManifest = 1 << 20

// Version liest die Version aus extension/package.json einer .vsix.
func Version(vsix []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(vsix), int64(len(vsix)))
	if err != nil {
		return "", fmt.Errorf("keine gültige .vsix: %w", err)
	}
	f, err := zr.Open("extension/package.json")
	if err != nil {
		return "", errors.New("keine gültige .vsix: extension/package.json fehlt")
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxManifest))
	if err != nil {
		return "", fmt.Errorf("keine gültige .vsix: %w", err)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(b, &pkg); err != nil || pkg.Version == "" {
		return "", errors.New("keine gültige .vsix: extension/package.json ohne version")
	}
	return pkg.Version, nil
}
