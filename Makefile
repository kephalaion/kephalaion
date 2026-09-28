.DEFAULT_GOAL := help

# Bauen und Testen, lokal und in CI mit denselben Targets und Flags. Persönliche
# Abläufe (release, sichern) stehen in k-playbook-local/Makefile.

BINARY := kephalaion
PKG := ./cmd/kephalaion
MODULE := github.com/kephalaion/kephalaion
BUILDINFO := $(MODULE)/internal/buildinfo
DIST_DIR := dist
COVER_DIR := coverage
SUMS_FILE := SHA256SUMS
RELEASE_TARGETS := linux-amd64 linux-arm64 darwin-amd64 darwin-arm64
# Verzeichnisse mit Go-Code des Projekts. Nicht `.`: darunter liegt die
# k-playbook-Installation mit eigenem Go-Code, der hier nicht geprüft wird.
GO_DIRS := cmd internal

# Werkzeug für make mutate, per go run in genau dieser Fassung: Es landet weder
# in go.mod noch in ~/go/bin.
GREMLINS := github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0
# Was make mutate untersucht; ein Paket etwa mit MUTATE=./internal/ident.
MUTATE ?= .

# Die Version ist der Git-Tag; ohne Angabe entsteht ein dev build. Der
# Release-Workflow ruft `make dist VERSION=<tag>`.
VERSION ?= dev
# Mit = statt := : git und go laufen erst, wenn ein Target den Wert braucht,
# und nicht schon bei `make help`.
# --long: auch auf einem getaggten Commit steht der Hash dabei (v0.1.0-0-g<sha>).
COMMIT ?= $(shell git describe --tags --long --always --dirty 2>/dev/null)
GO_TOOLCHAIN = $(shell awk '$$1 == "toolchain" { print $$2 }' go.mod)
HOST_TARGET = $(shell go env GOOS)-$(shell go env GOARCH)

# -trimpath und CGO_ENABLED=0 machen das Binary unabhängig vom Bau-Rechner;
# -buildvcs=false, weil der Commit ausdrücklich per -ldflags kommt.
LDFLAGS = -s -w -X $(BUILDINFO).Version=$(VERSION) -X $(BUILDINFO).Commit=$(COMMIT)

.PHONY: help build test check check-quick check-toolchain race cover mutate dist dist-host dev-install vscode-vsix vscode-install clean

help: ## Zeigt diese Hilfe an
	@echo "Targets:"
	@echo ""
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "Parameter:"
	@echo "  VERSION=v0.1.0            Version im Binary, sonst dev"
	@echo "  MUTATE=./internal/ident   make mutate nur für dieses Paket"
	@echo ""

define build_binaries
	@mkdir -p "$(DIST_DIR)"
	@set -eu; \
	for target in $(1); do \
	  os="$${target%-*}"; \
	  arch="$${target#*-}"; \
	  output="$(DIST_DIR)/$(BINARY)-$${os}-$${arch}"; \
	  echo "Baue $$output ($(VERSION))"; \
	  CGO_ENABLED=0 GOOS="$$os" GOARCH="$$arch" \
	    go build -trimpath -buildvcs=false -ldflags="$(LDFLAGS)" -o "$$output" "$(PKG)"; \
	done
endef

build: dist-host ## Alias für dist-host

dist-host: ## Baut nur das Binary dieser Plattform nach ./dist/
	$(call build_binaries,$(HOST_TARGET))

# dist räumt vorher auf: SHA256SUMS soll genau die vier Binaries dieses Laufs
# decken, keine Reste eines früheren.
dist: ## Baut alle vier Plattformen nach ./dist/ und schreibt SHA256SUMS
	@rm -rf "$(DIST_DIR)"
	$(call build_binaries,$(RELEASE_TARGETS))
	@set -eu; \
	  if command -v sha256sum >/dev/null 2>&1; then \
	    checksum() { sha256sum "$$@"; }; \
	  else \
	    checksum() { shasum -a 256 "$$@"; }; \
	  fi; \
	  cd "$(DIST_DIR)"; \
	  for target in $(RELEASE_TARGETS); do \
	    checksum "$(BINARY)-$$target"; \
	  done > "$(SUMS_FILE)"; \
	  echo "Geschrieben: $(DIST_DIR)/$(SUMS_FILE)"

test: ## Führt die Tests aus
	go test ./...

# check und check-quick unterscheiden sich nur in den Flags für go test.
# install.sh bekommt hier nur die Syntaxprüfung; shellcheck läuft in CI.
define check_steps
	sh -n install.sh
	@set -eu; \
	  unformatted="$$(gofmt -l $(GO_DIRS))"; \
	  if [ -n "$$unformatted" ]; then \
	    printf 'gofmt: diese Dateien sind nicht formatiert:\n%s\n' "$$unformatted" >&2; \
	    exit 1; \
	  fi
	go vet ./...
	go test $(1) ./...
endef

check: ## gofmt-Prüfung, go vet, alle Tests und Syntax von install.sh
	$(call check_steps,)

# -short überspringt die langsamen Tests (slow in cmd/kephalaion: sie warten
# auf Runden, Timer oder Fristen); kompiliert und von go vet geprüft werden
# sie trotzdem. Für Zwischenstände und CI auf dev; vor dem Abschluss einer
# Task, bei Änderungen an serve oder dem Abgleich und für ein Release gilt
# check.
check-quick: ## Wie check, aber ohne die langsamen Tests (go test -short)
	$(call check_steps,-short)

# CI ruft das vor dem Bauen: mit GOTOOLCHAIN=local soll eine abweichende
# Toolchain den Lauf scheitern lassen, statt still anders zu bauen.
check-toolchain: ## Prüft, ob die Toolchain aus go.mod läuft
	@set -eu; \
	  want="$(GO_TOOLCHAIN)"; \
	  test -n "$$want" || { printf 'In go.mod fehlt die toolchain-Zeile.\n' >&2; exit 1; }; \
	  have="$$(go env GOVERSION)"; \
	  test "$$have" = "$$want" || { \
	    printf 'Go %s läuft, verlangt ist %s (go.mod, toolchain).\n' "$$have" "$$want" >&2; \
	    exit 1; \
	  }; \
	  echo "Toolchain: $$have"

# Der Race-Detector braucht cgo und damit einen C-Compiler; das Binary selbst
# baut weiter ohne.
race: ## Tests mit dem Race-Detector
	CGO_ENABLED=1 go test -race ./...

# -coverpkg zählt auch, was die Tests anderer Pakete abdecken: loopback etwa
# prüfen nur die Tests von serve und mcpnode. go test meldet dann je Testpaket
# dessen Anteil am ganzen Modul, deshalb bleibt seine Ausgabe im Log, und die
# Werte je Paket rechnet awk aus dem Profil. Ein Block steht darin einmal je
# Testpaket; abgedeckt ist er, wenn eines ihn ausgeführt hat.
cover: ## Abdeckung je Paket und gesamt, HTML-Bericht nach ./coverage/
	@set -eu; \
	  mkdir -p "$(COVER_DIR)"; \
	  profile="$(COVER_DIR)/cover.out"; \
	  if ! go test -coverpkg=./... -coverprofile="$$profile" ./... > "$(COVER_DIR)/test.log" 2>&1; then \
	    cat "$(COVER_DIR)/test.log" >&2; \
	    exit 1; \
	  fi; \
	  awk -v mod="$(MODULE)/" ' \
	    NR > 1 { \
	      pkg = $$1; sub(/\/[^\/]*$$/, "", pkg); \
	      if (index(pkg, mod) == 1) pkg = substr(pkg, length(mod) + 1); \
	      stmts[$$1] = $$2; where[$$1] = pkg; \
	      if ($$3 > 0) hit[$$1] = 1; \
	    } \
	    END { \
	      for (b in stmts) { all[where[b]] += stmts[b]; if (b in hit) cov[where[b]] += stmts[b] } \
	      for (p in all) printf "  %-28s %5.1f %%\n", p, 100 * cov[p] / all[p]; \
	    }' "$$profile" | sort; \
	  go tool cover -func="$$profile" | awk 'END { sub(/%/, "", $$NF); printf "  %-28s %5.1f %%\n", "gesamt", $$NF }'; \
	  go tool cover -html="$$profile" -o "$(COVER_DIR)/cover.html"; \
	  echo "Bericht: $(COVER_DIR)/cover.html"

# gremlins verändert den Code an vielen Stellen einzeln und führt je Mutant die
# Tests seines Pakets aus; LIVED heißt, kein Test hat die Änderung bemerkt.
# Es kopiert je Worker das Verzeichnis, in dem es läuft; hier scheiterte das an
# der schreibgeschützten k-playbook-Installation. Es läuft deshalb in einer
# Kopie von go.mod, go.sum und GO_DIRS, samt seinen Arbeitskopien unter TMPDIR
# daneben; die Kopie hält auch den Stand beim Start fest. Die Zeitgrenze je
# Mutant ist die Dauer des ersten Testlaufs mal dem Faktor; mit dem Vorgabewert
# 3 laufen schnelle Pakete schon beim Kompilieren hinein. Gezeigt werden nur
# LIVED und TIMED OUT, danach die Summen.
mutate: ## Mutationstests mit gremlins (dauert)
	@set -eu; \
	  work="$$(mktemp -d)"; \
	  trap 'rm -rf "$$work"' EXIT; \
	  trap 'exit 130' INT TERM; \
	  mkdir "$$work/src" "$$work/tmp"; \
	  cp -R go.mod go.sum $(GO_DIRS) "$$work/src/"; \
	  cd "$$work/src"; \
	  TMPDIR="$$work/tmp" go run $(GREMLINS) unleash \
	    --timeout-coefficient 30 --output-statuses lt "$(MUTATE)"

# Läuft der Dienst pro User (kephalaion service install), startet dev-install
# ihn neu — sonst liefe nach dem Bauen weiter das alte Binary. Unit und Label
# wie in internal/service.
dev-install: dist-host ## Baut diese Plattform, ersetzt ~/.local/bin/kephalaion, startet den Dienst neu
	@set -eu; \
	  binary="$(DIST_DIR)/$(BINARY)-$(HOST_TARGET)"; \
	  target="$$HOME/.local/bin/$(BINARY)"; \
	  mkdir -p "$${target%/*}"; \
	  command -p install -m 755 "$$binary" "$$target.tmp"; \
	  mv -f "$$target.tmp" "$$target"; \
	  printf 'Installiert: %s\n' "$$target"; \
	  case "$$(uname -s)" in \
	  Linux) \
	    if command -v systemctl >/dev/null 2>&1 && \
	      systemctl --user is-active --quiet kephalaion.service 2>/dev/null; then \
	      systemctl --user restart kephalaion.service; \
	      echo "Dienst neu gestartet (systemctl --user restart kephalaion.service)"; \
	    fi ;; \
	  Darwin) \
	    agent="gui/$$(id -u)/io.github.kephalaion"; \
	    if launchctl print "$$agent" >/dev/null 2>&1; then \
	      launchctl kickstart -k "$$agent"; \
	      echo "Dienst neu gestartet (launchctl kickstart -k $$agent)"; \
	    fi ;; \
	  esac

# Die Erweiterung: Version aus vscode/package.json, gebaut mit vsce per npx
# (braucht Node.js). Die .vsix landet in ./dist/ neben den Binaries.
VSCODE_DIR := vscode
VSCODE_VSIX = $(DIST_DIR)/$(BINARY)-$(shell node -p "require('./$(VSCODE_DIR)/package.json').version").vsix

vscode-vsix: ## Baut die VS-Code-Erweiterung nach ./dist/ (braucht Node.js)
	@mkdir -p "$(DIST_DIR)"
	cd "$(VSCODE_DIR)" && npx --yes @vscode/vsce package --skip-license --out "$(abspath $(VSCODE_VSIX))"

# code aus einem Terminal der WSL, eines SSH-Remotes oder Devcontainers
# installiert in den VS-Code-Server dort — dorthin gehört eine Erweiterung der
# Art workspace. --force: auch bei gleicher Version ersetzen.
vscode-install: vscode-vsix ## Baut die Erweiterung und installiert sie mit code; danach „Developer: Reload Window“
	code --install-extension "$(VSCODE_VSIX)" --force
	@echo 'In VS Code: „Developer: Reload Window“'

clean: ## Entfernt ./dist/ und ./coverage/
	rm -rf "$(DIST_DIR)" "$(COVER_DIR)"
