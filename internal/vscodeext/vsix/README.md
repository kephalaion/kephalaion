# Die eingebettete Erweiterung für VS Code

`make vscode-vsix` (und damit `make build`, `dist`, `dev-install`) legt hier
`kephalaion.vsix` ab; `go:embed` in `internal/vscodeext` nimmt sie ins Binary.
Die Datei ist gitignored; diese README hält das Verzeichnis baubar, auch ohne
sie. `make clean` entfernt sie.
