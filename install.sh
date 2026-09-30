#!/bin/sh
# Installiert kephalaion nach ~/.local/bin/kephalaion.
#
#   curl -fsSL https://github.com/kephalaion/kephalaion/releases/latest/download/install.sh | sh
#
# Ohne Angabe das neueste Release; eine bestimmte Version über
# KEPHALAION_VERSION=vX.Y.Z. Geladen werden das Binary der Plattform und
# SHA256SUMS; installiert wird nur, wenn die Prüfsumme stimmt.
#
# POSIX-sh. Braucht curl oder wget und sha256sum oder shasum.
#
# Alles steht in main und wird erst in der letzten Zeile aufgerufen: bricht
# `curl | sh` mitten im Skript ab, läuft kein halbes Skript.

set -eu

REPO="kephalaion/kephalaion"
BINARY="kephalaion"

say() {
	printf '%s\n' "$*"
}

die() {
	printf 'kephalaion-Installation: %s\n' "$*" >&2
	exit 1
}

detect_platform() {
	os="$(uname -s)"
	arch="$(uname -m)"
	case "$os" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*) die "Betriebssystem $os wird nicht unterstützt (nur Linux und macOS)." ;;
	esac
	case "$arch" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) die "Prozessor $arch wird nicht unterstützt (nur amd64 und arm64)." ;;
	esac
	# Eine Shell unter Rosetta meldet x86_64 auf einem Apple-Silicon-Mac; das
	# arm64-Binary ist dort das richtige.
	if [ "$os" = darwin ] && [ "$arch" = amd64 ] &&
		[ "$(sysctl -n sysctl.proc_translated 2>/dev/null || true)" = 1 ]; then
		arch=arm64
	fi
	PLATFORM="$os-$arch"
}

detect_tools() {
	if command -v curl >/dev/null 2>&1; then
		FETCH=curl
	elif command -v wget >/dev/null 2>&1; then
		FETCH=wget
	else
		die "Weder curl noch wget gefunden; eins von beiden wird gebraucht."
	fi
	if command -v sha256sum >/dev/null 2>&1; then
		SHASUM=sha256sum
	elif command -v shasum >/dev/null 2>&1; then
		SHASUM=shasum
	else
		die "Weder sha256sum noch shasum gefunden; ohne Prüfsumme wird nicht installiert."
	fi
}

# sha256_of DATEI: gibt die SHA-256-Summe von DATEI aus.
sha256_of() {
	if [ "$SHASUM" = sha256sum ]; then
		sha256sum "$1" | awk '{ print $1 }'
	else
		shasum -a 256 "$1" | awk '{ print $1 }'
	fi
}

host_of() {
	printf '%s\n' "$1" | sed 's|^[a-z]*://\([^/]*\).*|\1|'
}

# fetch URL DATEI: lädt URL nach DATEI und setzt STATUS auf den HTTP-Status.
# Scheitert die Verbindung selbst, bricht das Skript ab.
fetch() {
	case "$1" in
	https://api.github.com/*) accept="Accept: application/vnd.github+json" ;;
	*) accept="Accept: */*" ;;
	esac
	case "$FETCH" in
	curl)
		STATUS="$(curl -sSL -H "$accept" -o "$2" -D "$2.headers" -w '%{http_code}' "$1")" ||
			die "Keine Verbindung zu $(host_of "$1") — ist das Netz erreichbar?"
		;;
	wget)
		# wget schreibt die Antwortköpfe mit -S nach stderr; der letzte Status
		# zählt (nach Weiterleitungen).
		wget -q -S --header="$accept" -O "$2" "$1" 2>"$2.headers" || true
		STATUS="$(awk '$1 ~ /^HTTP\// { code = $2 } END { print code }' "$2.headers")"
		[ -n "$STATUS" ] || die "Keine Verbindung zu $(host_of "$1") — ist das Netz erreichbar?"
		;;
	esac
}

# check_status URL WAS: bricht mit einer klaren Meldung ab, wenn STATUS kein 200 ist.
check_status() {
	case "$STATUS" in
	200) return 0 ;;
	404) die "$2 nicht gefunden ($1)." ;;
	403 | 429)
		# Ein 403 der API auf ein öffentliches Repo ist praktisch immer das
		# Rate-Limit; busybox-wget zeigt bei einem Fehler weder Köpfe noch Body.
		if [ "$STATUS" = 429 ] ||
			grep -qi '^[[:space:]]*x-ratelimit-remaining:[[:space:]]*0' "$TMP/last.headers" 2>/dev/null ||
			grep -qi 'rate limit' "$TMP/last" 2>/dev/null ||
			case "$1" in https://api.github.com/*) true ;; *) false ;; esac; then
			die "Das Anfrage-Limit der GitHub-API ist erreicht (ohne Anmeldung 60 Anfragen je Stunde). Später erneut versuchen oder KEPHALAION_VERSION=vX.Y.Z setzen, dann wird die API nicht gefragt."
		fi
		die "GitHub verweigert den Zugriff ($STATUS) auf $1."
		;;
	*) die "Unerwartete Antwort $STATUS von $1." ;;
	esac
}

resolve_version() {
	if [ -n "${KEPHALAION_VERSION:-}" ]; then
		VERSION="$KEPHALAION_VERSION"
	else
		url="https://api.github.com/repos/$REPO/releases/latest"
		fetch "$url" "$TMP/last"
		check_status "$url" "Ein veröffentlichtes Release"
		# Das erste tag_name ist das des Releases; Assets tragen keins.
		VERSION="$(tr ',' '\n' <"$TMP/last" |
			sed -n 's/^[[:space:]{]*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' |
			head -n 1)"
		[ -n "$VERSION" ] || die "Die Antwort der GitHub-API nennt keine Version."
	fi
	printf '%s\n' "$VERSION" |
		grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$' ||
		die "Keine gültige Version: $VERSION (erwartet vX.Y.Z oder vX.Y.Z-suffix)."
}

download() {
	base="https://github.com/$REPO/releases/download/$VERSION"
	ASSET="$BINARY-$PLATFORM"

	fetch "$base/SHA256SUMS" "$TMP/last"
	check_status "$base/SHA256SUMS" "SHA256SUMS zu $VERSION"
	mv "$TMP/last" "$TMP/SHA256SUMS"

	fetch "$base/$ASSET" "$TMP/last"
	check_status "$base/$ASSET" "Das Binary $ASSET zu $VERSION"
	mv "$TMP/last" "$TMP/$ASSET"
}

verify() {
	want="$(awk -v n="$ASSET" '$2 == n || $2 == "*" n { print $1; exit }' "$TMP/SHA256SUMS")"
	[ -n "$want" ] || die "SHA256SUMS zu $VERSION enthält keine Summe für $ASSET."
	got="$(sha256_of "$TMP/$ASSET")"
	[ "$got" = "$want" ] ||
		die "Prüfsumme von $ASSET stimmt nicht (erwartet $want, erhalten $got). Nichts installiert."
}

# Atomar: erst neben das Ziel kopieren, dann umbenennen — dasselbe
# Dateisystem, ein Schritt. Ein laufendes kephalaion bleibt dabei heil.
install_binary() {
	DIR="$HOME/.local/bin"
	TARGET="$DIR/$BINARY"
	mkdir -p "$DIR" || die "$DIR lässt sich nicht anlegen."
	[ -w "$DIR" ] || die "Kein Schreibrecht in $DIR."
	PART="$DIR/.kephalaion-install-$$"
	cp "$TMP/$ASSET" "$PART" || die "Schreiben nach $DIR fehlgeschlagen."
	chmod 0755 "$PART"
	mv -f "$PART" "$TARGET" || die "$TARGET lässt sich nicht ersetzen."
	PART=""
}

path_hint() {
	case ":${PATH:-}:" in
	*":$DIR:"*) return 0 ;;
	esac
	case "${SHELL:-}" in
	*/zsh) profile="$HOME/.zshrc" ;;
	*/bash) profile="$HOME/.bashrc" ;;
	*) profile="$HOME/.profile" ;;
	esac
	say ""
	say "$DIR liegt nicht im PATH. Diese Zeile ins Shell-Profil ($profile) eintragen:"
	say ""
	# shellcheck disable=SC2016 # $HOME und $PATH sollen wörtlich im Profil stehen
	say '  export PATH="$HOME/.local/bin:$PATH"'
	say ""
	say "und eine neue Shell öffnen."
}

next_steps() {
	say ""
	say "Nächste Schritte (docs/installation.md):"
	say "  kephalaion node init          Rollen einrichten, siehe README (Einrichten)"
	say "  kephalaion service install    den Dienst einrichten, der kephalaion serve startet"
	say "                                (systemd --user bzw. LaunchAgent)"
	say "  kephalaion node mcp add       nach dem ersten Account: den Node bei den"
	say "                                KI-Assistenten anmelden (Claude Code, OpenCode, Codex)"
}

# register_assistants: der automatische Anstoß bei den KI-Assistenten. Ob es
# eine config mit Node gibt, stellt das Binary fest (ohne endet es still mit
# 0); es ändert nur Assistenten, die schon einen Eintrag haben, und trägt
# überall ein, wenn noch keiner einen hat. Kennt das Binary node mcp nicht
# (eine ältere Version), entfällt es. Scheitert es, nur ein Hinweis: Die
# Installation ist trotzdem gelungen.
register_assistants() {
	"$TARGET" node mcp --help >/dev/null 2>&1 || return 0
	if ! "$TARGET" node mcp add --auto </dev/null; then
		say ""
		say "Hinweis: Die Anmeldung bei den KI-Assistenten ist nicht vollständig (siehe oben)."
		say "Nachsehen mit: kephalaion node mcp status"
	fi
}

cleanup() {
	[ -z "${PART:-}" ] || rm -f "$PART"
	[ -z "${TMP:-}" ] || rm -rf "$TMP"
}

main() {
	[ -n "${HOME:-}" ] || die "HOME ist nicht gesetzt."
	PART=""
	TMP=""
	trap cleanup EXIT
	trap 'exit 130' INT
	trap 'exit 143' TERM

	detect_platform
	detect_tools
	TMP="$(mktemp -d 2>/dev/null || mktemp -d -t kephalaion)"

	resolve_version
	say "Installiere kephalaion $VERSION für $PLATFORM ..."
	download
	verify
	install_binary

	say "Installiert: $TARGET"
	"$TARGET" version || true
	path_hint
	register_assistants
	next_steps
}

main "$@"
