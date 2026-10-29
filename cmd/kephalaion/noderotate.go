package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/ident"
	nodestore "github.com/kephalaion/kephalaion/internal/node/store"
)

// Das Token des Nodes selbst rotieren (Task 034, Vorgang node_rotate): für
// node hub rotate und node setup. Das neue Token liegt vor dem Abschicken in
// der .pending des Eintrags (nodestore.PendingTokenPath); übernommen heißt
// erst node.db, dann die Datei löschen. Verworfen wird sie nur, wenn der Hub
// eindeutig abgelehnt hat oder ein anderes Token per whoami nachweislich
// gilt — wie clarify bei node account setup.

// nodeRotation rotiert das Token eines Hub-Eintrags über hub.
type nodeRotation struct {
	ctx  context.Context
	s    nodestore.Store
	h    nodestore.Hub
	hub  contract.Hub
	logf func(format string, a ...any)
}

// Arten, wie ein node_rotate scheitert.
type rotateFailure int

const (
	// rotateRejected: Der Hub hat abgelehnt (ein Code des Vertrags), etwa
	// unauthenticated — das Token ist inzwischen ersetzt. .pending ist
	// gelöscht.
	rotateRejected rotateFailure = iota + 1
	// rotateOldHub: Der Hub kennt node_rotate nicht. .pending ist gelöscht.
	rotateOldHub
	// rotateNotReached: Die Anfrage hat den Hub nachweislich nicht erreicht.
	// .pending ist gelöscht.
	rotateNotReached
	// rotateUnclear: Ausgang unklar; .pending bleibt.
	rotateUnclear
	// rotateNotAdopted: Der Hub hat rotiert, aber node.db ließ sich nicht
	// schreiben; .pending bleibt und hält das einzige gültige Token.
	rotateNotAdopted
	// rotateLocal: vor dem Abschicken gescheitert (Token erzeugen, .pending
	// schreiben); nichts geändert.
	rotateLocal
)

// rotateError ist das Scheitern eines node_rotate.
type rotateError struct {
	kind rotateFailure
	err  error
}

func (e *rotateError) Error() string { return e.err.Error() }
func (e *rotateError) Unwrap() error { return e.err }

// pendingPath ist der Ort der .pending des Eintrags.
func (r *nodeRotation) pendingPath() string { return r.s.PendingTokenPath(r.h.EntryID) }

// hasPending sagt, ob für den Eintrag ein Token vorgemerkt ist.
func (r *nodeRotation) hasPending() (bool, error) {
	return fileExists(r.pendingPath())
}

// rotate erzeugt ein neues Token, merkt es vor, schickt node_rotate genau
// einmal und übernimmt es bei Erfolg. Fehler sind *rotateError.
func (r *nodeRotation) rotate() error {
	path := r.pendingPath()
	newToken, err := ident.NewToken()
	if err != nil {
		return &rotateError{rotateLocal, err}
	}
	if err := nodestore.WritePendingToken(path, r.h.EntryID, newToken); err != nil {
		return &rotateError{rotateLocal, err}
	}
	r.logf("Neues Token erzeugt (%s), vorgemerkt in %s (0600)", ident.MaskToken(newToken), path)
	r.logf("Rotiere das Token des Nodes %s am Hub %s (node_rotate) …", r.h.NodeName, r.h.Name)
	_, err = r.hub.RotateNode(r.ctx, contract.NodeRotateRequest{Version: contract.Version,
		Auth: contract.NodeAuth{Node: r.h.NodeName, Token: r.h.Token}, NewHash: ident.HashToken(newToken)})
	var cerr *contract.Error
	switch {
	case err == nil:
		if err := r.adopt(newToken); err != nil {
			return &rotateError{rotateNotAdopted, err}
		}
		return nil
	case errors.Is(err, contract.ErrOutcomeUnknown):
		return &rotateError{rotateUnclear, fmt.Errorf("Ausgang unklar: %v\nDas neue Token gilt vielleicht schon, "+
			"vielleicht noch das bisherige; %s bleibt liegen. Nicht von Hand wiederholen — der nächste Aufruf klärt es "+
			"über whoami, ohne zu rotieren", err, path)}
	}
	// Eindeutig ohne Wirkung: Der Hub hat nichts geändert.
	removed := r.discard()
	switch {
	case errors.Is(err, contract.ErrUnknownOperation):
		return &rotateError{rotateOldHub, fmt.Errorf("der Hub %s kennt node_rotate noch nicht (älter als Task 034): "+
			"erst den Hub aktualisieren (kephalaion upgrade auf seinem Rechner), dann erneut. Das Token ist nicht "+
			"rotiert und wird für nichts weiter benutzt; %s", r.h.Name, removed)}
	case errors.As(err, &cerr):
		return &rotateError{rotateRejected, fmt.Errorf("der Hub lehnt ab: %v; %s", cerr, removed)}
	}
	return &rotateError{rotateNotReached, fmt.Errorf("Hub %s nicht erreicht, nichts geändert: %v; %s", r.h.Name,
		explainHubError(r.h, err), removed)}
}

// discard löscht die .pending und sagt das in einem Halbsatz.
func (r *nodeRotation) discard() string {
	if err := nodestore.RemovePendingToken(r.pendingPath()); err != nil {
		return fmt.Sprintf("%s ließ sich nicht löschen: %v", r.pendingPath(), err)
	}
	return r.pendingPath() + " ist gelöscht"
}

// adopt übernimmt ein Token, das am Hub gilt: erst node.db, dann .pending
// löschen.
func (r *nodeRotation) adopt(token string) error {
	if err := r.s.ReplaceHubToken(r.ctx, r.h.Name, r.h.EntryID, token); err != nil {
		return fmt.Errorf("das neue Token gilt am Hub, ließ sich aber nicht in node.db schreiben: %v\nEs steht in %s; "+
			"der nächste Aufruf übernimmt es", err, r.pendingPath())
	}
	r.h.Token = token
	if err := nodestore.RemovePendingToken(r.pendingPath()); err != nil {
		r.logf("Warnung: %s ließ sich nicht löschen: %v", r.pendingPath(), err)
	}
	r.logf("Token rotiert und in node.db übernommen (%s); das bisherige gilt nicht mehr.", ident.MaskToken(token))
	return nil
}

// tokenValidity ist, was whoami über ein Token des Nodes sagt.
type tokenValidity int

const (
	// tokenUnknown: nicht zu entscheiden — der Hub nicht erreicht, ein
	// unklarer Ausgang, ein anderer Fehler.
	tokenUnknown tokenValidity = iota
	tokenValid
	tokenInvalid
)

// check fragt den Hub per whoami, ob token das Token des Nodes ist. Bei
// tokenValid liefert es die Antwort, bei tokenUnknown den Grund.
func (r *nodeRotation) check(token string) (tokenValidity, contract.WhoamiResponse, error) {
	resp, err := r.hub.Whoami(r.ctx, contract.WhoamiRequest{Version: contract.Version,
		Auth: contract.NodeAuth{Node: r.h.NodeName, Token: token}})
	switch {
	case err == nil:
		return tokenValid, resp, nil
	case errors.Is(err, contract.ErrUnauthenticated):
		return tokenInvalid, contract.WhoamiResponse{}, nil
	}
	return tokenUnknown, contract.WhoamiResponse{}, explainHubError(r.h, err)
}

func (v tokenValidity) text() string {
	switch v {
	case tokenValid:
		return "gilt"
	case tokenInvalid:
		return "gilt nicht"
	}
	return "nicht geprüft"
}

// Ausgänge von clarify.
type clarifyOutcome int

const (
	// clarifyAdopted: Das vorgemerkte Token gilt und ist übernommen.
	clarifyAdopted clarifyOutcome = iota + 1
	// clarifyDiscarded: Es gilt nicht, aber eines der anderen; .pending ist
	// verworfen. valid nennt es.
	clarifyDiscarded
)

// errNoTokenValid: Weder das vorgemerkte noch eines der anderen Tokens gilt;
// .pending bleibt.
var errNoTokenValid = errors.New("kein Token gilt")

// clarify klärt eine liegende .pending über whoami, ohne zu rotieren. Gilt
// ihr Token, wird es übernommen. Gilt es nicht, prüft es others der Reihe
// nach (leere und doppelte übergeht es): Gilt eines, wird die .pending
// verworfen und valid ist dieses Token. Gilt keins, bleibt sie
// (errNoTokenValid); ebenso, wenn sich etwas nicht entscheiden lässt.
func (r *nodeRotation) clarify(others ...string) (out clarifyOutcome, valid string, err error) {
	path := r.pendingPath()
	r.logf("%s liegt von einem früheren node_rotate mit unklarem Ausgang; prüfe, welches Token gilt (kein "+
		"rotate) …", path)
	pending, err := nodestore.ReadPendingToken(path, r.h.EntryID)
	if err != nil {
		return 0, "", fmt.Errorf("%v; %s bleibt liegen — von Hand prüfen", err, path)
	}
	v, _, err := r.check(pending)
	r.logf("Vorgemerktes Token (%s): %s", ident.MaskToken(pending), v.text())
	switch v {
	case tokenValid:
		if err := r.adopt(pending); err != nil {
			return 0, "", err
		}
		return clarifyAdopted, pending, nil
	case tokenUnknown:
		return 0, "", fmt.Errorf("nicht zu entscheiden: %v\n%s bleibt liegen; später erneut aufrufen", err, path)
	}
	seen := map[string]bool{pending: true}
	for _, tok := range others {
		if tok == "" || seen[tok] {
			continue
		}
		seen[tok] = true
		v, _, err := r.check(tok)
		r.logf("Token %s: %s", ident.MaskToken(tok), v.text())
		switch v {
		case tokenValid:
			if err := nodestore.RemovePendingToken(path); err != nil {
				return 0, "", fmt.Errorf("%s ließ sich nicht löschen: %v", path, err)
			}
			r.logf("Der node_rotate kam nicht an; %s verworfen.", path)
			return clarifyDiscarded, tok, nil
		case tokenUnknown:
			return 0, "", fmt.Errorf("nicht zu entscheiden: %v\n%s bleibt liegen; später erneut aufrufen", err, path)
		}
	}
	return 0, "", fmt.Errorf("%w: weder das Token in %s noch das gespeicherte gilt am Hub. %s bleibt liegen — sonst "+
		"ginge ein gültiges Token verloren. Einen neuen Einrichtungscode erzeugt der Admin: kephalaion hub node token %s",
		errNoTokenValid, path, path, r.h.NodeName)
}

const nodeHubRotateUsage = `Aufruf:
  kephalaion node hub rotate <alias> [--config pfad]

Ersetzt das Token dieses Nodes beim Hub des Eintrags (Vorgang node_rotate):
erzeugt ein neues Token, merkt es in hub-<entry_id>.pending neben der node.db
vor (0600), schickt dem Hub das bisherige zur Anmeldung und den Hash des
neuen, genau einmal, und übernimmt es bei Erfolg in node.db. Das neue Token
verlässt den Rechner nie; angezeigt wird es nur gekürzt. Über local, http und
https; ssh noch nicht.

Ist der Ausgang unklar, bleibt die .pending liegen, und der nächste Aufruf
rotiert nicht, sondern klärt sie über whoami: Gilt ihr Token, wird es
übernommen; gilt es nicht, aber das gespeicherte, wird sie verworfen (dann
erneut aufrufen); gilt keins, bleibt sie, mit dem Hinweis auf
kephalaion hub node token. Kennt der Hub node_rotate nicht (älter als Task
034), bricht es ab: erst den Hub aktualisieren.

Exit-Code: 0 rotiert (auch: die liegende .pending galt und ist übernommen),
1 Fehler, 2 falscher Aufruf, 3 die liegende .pending galt nicht und ist
verworfen — erneut aufrufen.

Optionen:
  --config pfad   Ort der config (siehe kephalaion node init --help)
`

// runNodeHubRotate ist node hub rotate <alias>.
func runNodeHubRotate(args []string, stdout, stderr io.Writer) int {
	c := newCommand("node hub rotate", nodeHubRotateUsage, stdout, stderr, "<alias>")
	pos, code, ok := c.parse(args)
	if !ok {
		return code
	}
	ctx := context.Background()
	s, _, err := c.openNode(ctx)
	if err != nil {
		return c.fail(err)
	}
	defer s.Close()
	h, err := s.Hub(ctx, pos[0])
	if err != nil {
		return c.fail(err)
	}
	cfg, err := c.loadConfig()
	if err != nil {
		return c.fail(err)
	}
	// Erst verbinden: ssh und eine ungültige Adresse scheitern hier, bevor
	// eine .pending entsteht.
	hub, closeHub, err := connectHub(ctx, cfg, h)
	if err != nil {
		return c.fail(err)
	}
	defer closeHub()
	r := &nodeRotation{ctx: ctx, s: s, h: h, hub: hub, logf: func(format string, a ...any) {
		fmt.Fprintf(stdout, format+"\n", a...)
	}}
	if ok, err := r.hasPending(); err != nil {
		return c.fail(err)
	} else if ok {
		out, _, err := r.clarify(h.Token)
		switch {
		case err != nil:
			return c.fail(err)
		case out == clarifyDiscarded:
			fmt.Fprintf(stdout, "Das gespeicherte Token gilt weiter — erneut aufrufen:\n  kephalaion node hub rotate %s\n",
				h.Name)
			return exitRetry
		}
		return 0
	}
	if err := r.rotate(); err != nil {
		var re *rotateError
		if errors.As(err, &re) && re.kind == rotateRejected {
			return c.fail(fmt.Errorf("%w\nDas gespeicherte Token gilt nicht (mehr). Ein neues erzeugt der Admin: kephalaion "+
				"hub node token %s — eintragen mit kephalaion node setup <code> oder kephalaion node hub token %s "+
				"--token-stdin", err, h.NodeName, h.Name))
		}
		return c.fail(err)
	}
	return 0
}

// pendingExists sagt für einen Eintrag, ob seine .pending liegt; für die
// Übersicht von node setup.
func pendingExists(s nodestore.Store, h nodestore.Hub) bool {
	ok, err := fileExists(s.PendingTokenPath(h.EntryID))
	return ok || (err != nil && !errors.Is(err, fs.ErrNotExist))
}
