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

.PHONY: help build test check check-quick check-toolchain race cover mutate dist dist-host dev-install vscode-test vscode-vsix vscode-verify vscode-install clean

# Die Erweiterung für VS Code steckt im Binary (internal/vscodeext, go:embed).
# vscode-vsix baut sie mit vsce in fester Fassung per npx (braucht Node.js und
# beim ersten Mal Netz) und legt sie in das Verzeichnis, das go:embed nimmt.
VSCODE_DIR := vscode
VSCE := @vscode/vsce@4.0.0
VSCODE_EMBED := internal/vscodeext/vsix/kephalaion.vsix
# Die Version der Erweiterung ist die des Binarys: vX.Y.Z und vX.Y.Z-… werden
# X.Y.Z (VS Code nimmt nur x.y.z; vsce prüft das nicht), alles andere — der
# dev build — 0.0.0. vscode/package.json bleibt auf 0.0.0, die Version geht
# als Argument an vsce.
VSCODE_VERSION = $(or $(shell printf '%s\n' '$(VERSION)' | sed -nE 's/^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-.*)?$$/\1.\2.\3/p'),0.0.0)
# REQUIRE_VSCODE=1: fehlt Node.js oder scheitert vsce, bricht der Bau ab
# (Release, CI); sonst nur eine Warnung und ein Binary ohne Erweiterung.
REQUIRE_VSCODE ?=

help: ## Zeigt diese Hilfe an
	@echo "Targets:"
	@echo ""
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "Parameter:"
	@echo "  VERSION=v0.1.0            Version im Binary, sonst dev"
	@echo "  REQUIRE_VSCODE=1          ohne Erweiterung für VS Code nicht bauen (Release, CI)"
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

dist-host: vscode-vsix ## Baut nur das Binary dieser Plattform nach ./dist/
	$(call build_binaries,$(HOST_TARGET))

# dist räumt vorher auf: SHA256SUMS soll genau die vier Binaries dieses Laufs
# decken, keine Reste eines früheren.
dist: vscode-vsix ## Baut alle vier Plattformen nach ./dist/ und schreibt SHA256SUMS
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
# install.sh bekommt hier nur die Syntaxprüfung; shellcheck läuft in CI. Die
# Tests der Erweiterung für VS Code laufen mit, wenn Node.js da ist.
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
	@if command -v node >/dev/null 2>&1; then \
	  node --test $(VSCODE_DIR)/test/*.test.js; \
	else \
	  echo "node fehlt: Tests der Erweiterung für VS Code übergangen (make vscode-test; CI hat Node.js)" >&2; \
	fi
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

# Die Regeln der Erweiterung (vscode/rules.js: Adresse, Wahl der Hubs, Antworten eines Proxys)
# prüft node --test ohne VS Code. make check ruft es mit, wenn Node.js da ist.
vscode-test: ## Tests der Regeln der Erweiterung für VS Code (braucht Node.js)
	node --test $(VSCODE_DIR)/test/*.test.js

# Fehlt Node.js oder scheitert vsce (etwa ohne Netz), entfernt vscode-vsix eine
# liegende .vsix, warnt und endet mit 0: build, dist und dev-install bauen dann
# ein Binary ohne Erweiterung, das das selbst sagt. Mit REQUIRE_VSCODE=1 ist
# beides ein Abbruch. Grenze: Ein schlichtes go build nach einem make bettet
# die liegende .vsix ein (mit der Version jenes Laufs); make clean räumt sie weg.
vscode-vsix: ## Baut die Erweiterung für VS Code mit der Version aus VERSION zum Einbetten (braucht Node.js)
	@set -eu; \
	  out="$(VSCODE_EMBED)"; \
	  part="$${out%.vsix}.part.vsix"; \
	  rm -f "$$out" "$$part"; \
	  fail() { \
	    if [ "$(REQUIRE_VSCODE)" = 1 ]; then \
	      printf 'Fehler: %s — mit REQUIRE_VSCODE=1 kein Binary ohne Erweiterung für VS Code.\n' "$$1" >&2; \
	      exit 1; \
	    fi; \
	    printf 'Warnung: %s — das Binary wird ohne Erweiterung für VS Code gebaut.\n' "$$1" >&2; \
	    exit 0; \
	  }; \
	  command -v node >/dev/null 2>&1 && command -v npx >/dev/null 2>&1 || fail "node fehlt"; \
	  echo "Baue Erweiterung für VS Code $(VSCODE_VERSION) ($$out)"; \
	  if ! (cd "$(VSCODE_DIR)" && npx --yes $(VSCE) package "$(VSCODE_VERSION)" \
	    --no-update-package-json --skip-license --out "$(abspath $(VSCODE_EMBED:.vsix=.part.vsix))"); then \
	    rm -f "$$part"; \
	    fail "vsce ist gescheitert"; \
	  fi; \
	  mv -f "$$part" "$$out"

# Release und CI weisen damit nach, dass ein gebautes Binary die Erweiterung
# trägt, und zwar mit der Version nach der Regel oben: Es schreibt sie mit
# vscode vsix heraus, die Version kommt aus extension/package.json der .vsix
# (braucht unzip und Node.js). VSCODE_BIN wählt das Binary, Vorgabe das dieser
# Plattform in dist/.
VSCODE_BIN ?= $(DIST_DIR)/$(BINARY)-$(HOST_TARGET)
vscode-verify: ## Prüft, ob VSCODE_BIN die Erweiterung mit der Version aus VERSION trägt (Release, CI)
	@set -eu; \
	  tmp="$$(mktemp -d)"; \
	  trap 'rm -rf "$$tmp"' EXIT; \
	  "$(VSCODE_BIN)" vscode vsix -o "$$tmp/k.vsix" >/dev/null; \
	  have="$$(unzip -p "$$tmp/k.vsix" extension/package.json | \
	    node -e 'let s = ""; process.stdin.on("data", (d) => { s += d; }).on("end", () => console.log(JSON.parse(s).version));')"; \
	  want="$(VSCODE_VERSION)"; \
	  if [ "$$have" != "$$want" ]; then \
	    printf 'Fehler: %s trägt die Erweiterung %s, erwartet %s (VERSION=%s).\n' "$(VSCODE_BIN)" "$$have" "$$want" "$(VERSION)" >&2; \
	    exit 1; \
	  fi; \
	  echo "Erweiterung für VS Code im Binary: $$have ($(VSCODE_BIN))"

# code aus einem Terminal der WSL, eines SSH-Remotes oder Devcontainers
# installiert in den VS-Code-Server dort — dorthin gehört eine Erweiterung der
# Art workspace. --force: auch bei gleicher oder höherer Version ersetzen. Für
# die Entwicklung; sonst kephalaion vscode install.
vscode-install: ## Baut die Erweiterung und installiert sie mit code (Entwicklung); danach „Developer: Reload Window“
	@$(MAKE) --no-print-directory vscode-vsix REQUIRE_VSCODE=1
	code --install-extension "$(VSCODE_EMBED)" --force
	@echo 'In VS Code: „Developer: Reload Window“'

clean: ## Entfernt ./dist/, ./coverage/ und die eingebettete .vsix
	rm -rf "$(DIST_DIR)" "$(COVER_DIR)"
	rm -f "$(VSCODE_EMBED)" "$(VSCODE_EMBED:.vsix=.part.vsix)"
