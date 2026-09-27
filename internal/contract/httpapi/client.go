package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kephalaion/kephalaion/internal/contract"
)

// Client setzt contract.Hub über HTTP um. whoami und sync wiederholt er bei
// Fehlern des Transports mit wachsendem Abstand; rotate und die
// Schreibvorgänge nie — nach rotate gilt das alte Token nicht mehr, und ein
// zweiter Schreibversuch ergäbe name_taken oder stale_revision. Ist nach dem
// Abschicken offen, ob der Hub ausgeführt hat, trägt ihr Fehler
// contract.ErrOutcomeUnknown; kennt der Hub den Vorgang nicht,
// contract.ErrUnknownOperation. Jeder andere Fehler, der kein
// *contract.Error ist, heißt: Die Anfrage hat den Hub nicht erreicht.
type Client struct {
	base string
	http *http.Client
	// Retries ist die Zahl der Wiederholungen für whoami und sync, Backoff
	// der Abstand vor der ersten; er verdoppelt sich je Versuch.
	Retries int
	Backoff time.Duration
	// ShortTimeout gilt für whoami, rotate und die Schreibvorgänge,
	// SyncTimeout für sync.
	ShortTimeout time.Duration
	SyncTimeout  time.Duration
}

var _ contract.Hub = (*Client)(nil)

// NewClient liefert einen Client für den Hub unter address (http://… oder
// https://…, ohne Pfad). Welche Adressen ein Node benutzen darf, prüft er
// selbst; der Client nimmt, was er bekommt.
func NewClient(address string) (*Client, error) {
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.User != nil {
		return nil, fmt.Errorf("Adresse %q: erwartet http://host:port oder https://host:port", address)
	}
	// Die Standard-Transportschicht bittet von selbst um gzip und packt aus.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	return &Client{
		base: strings.TrimSuffix(u.String(), "/"),
		// Keiner Weiterleitung folgen: Go striche bei fremdem Host zwar
		// Authorization, schickte aber den Body mit — bei rotate samt dem
		// Token des Accounts. Die 3xx-Antwort selbst ist ein Fehler.
		http: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		Retries:      3,
		Backoff:      500 * time.Millisecond,
		ShortTimeout: ShortTimeout,
		SyncTimeout:  SyncTimeout,
	}, nil
}

// Whoami fragt den Hub; bei Fehlern des Transports mit Wiederholung.
func (c *Client) Whoami(ctx context.Context, req contract.WhoamiRequest) (contract.WhoamiResponse, error) {
	var resp contract.WhoamiResponse
	err := c.call(ctx, OpWhoami, req.Version, req.Auth, req, &resp, true, c.ShortTimeout)
	if err != nil {
		return contract.WhoamiResponse{}, err
	}
	return resp, nil
}

// Rotate schickt den Wechsel genau einmal. Ist nach dem Abschicken offen, ob
// der Hub ihn ausgeführt hat (Zeitüberschreitung, abgebrochene Verbindung,
// unlesbare Antwort, Fehler des Hubs), trägt der Fehler
// contract.ErrOutcomeUnknown.
func (c *Client) Rotate(ctx context.Context, req contract.RotateRequest) (contract.RotateResponse, error) {
	var resp contract.RotateResponse
	err := c.call(ctx, OpRotate, req.Version, req.Auth, req, &resp, false, c.ShortTimeout)
	if err != nil {
		return contract.RotateResponse{}, err
	}
	return resp, nil
}

// Sync holt eine Seite; bei Fehlern des Transports mit Wiederholung.
func (c *Client) Sync(ctx context.Context, req contract.SyncRequest) (contract.SyncResponse, error) {
	var resp contract.SyncResponse
	err := c.call(ctx, OpSync, req.Version, req.Auth, req, &resp, true, c.SyncTimeout)
	if err != nil {
		return contract.SyncResponse{}, err
	}
	return resp, nil
}

// Create legt ein Dokument an, genau einmal; Ausgang wie bei Rotate.
func (c *Client) Create(ctx context.Context, req contract.CreateRequest) (contract.WriteResponse, error) {
	if err := checkUTF8(req.Name, &req.Content); err != nil {
		return contract.WriteResponse{}, err
	}
	return c.write(ctx, OpCreate, req.Version, req.Auth, req)
}

// Write ersetzt ein Dokument, genau einmal; Ausgang wie bei Rotate.
func (c *Client) Write(ctx context.Context, req contract.WriteRequest) (contract.WriteResponse, error) {
	if err := checkUTF8(req.Name, &req.Content); err != nil {
		return contract.WriteResponse{}, err
	}
	return c.write(ctx, OpWrite, req.Version, req.Auth, req)
}

// Delete löscht ein Dokument oder ein Verzeichnis, genau einmal; Ausgang wie
// bei Rotate.
func (c *Client) Delete(ctx context.Context, req contract.DeleteRequest) (contract.WriteResponse, error) {
	if err := checkUTF8(req.Name, nil); err != nil {
		return contract.WriteResponse{}, err
	}
	return c.write(ctx, OpDelete, req.Version, req.Auth, req)
}

// Rename benennt ein Dokument oder ein Verzeichnis um, genau einmal; Ausgang
// wie bei Rotate.
func (c *Client) Rename(ctx context.Context, req contract.RenameRequest) (contract.WriteResponse, error) {
	if err := checkUTF8(req.Name, nil); err != nil {
		return contract.WriteResponse{}, err
	}
	if err := checkUTF8(req.NewName, nil); err != nil {
		return contract.WriteResponse{}, err
	}
	return c.write(ctx, OpRename, req.Version, req.Auth, req)
}

// checkUTF8 lehnt einen Namen oder Inhalt ab, der kein gültiges UTF-8 ist:
// encoding/json ersetzte die Bytes still durch U+FFFD, der Hub schriebe
// einen anderen Namen oder Inhalt als gemeint. Die Antwort ist dieselbe wie
// über local — invalid —, abgeschickt wird nichts. Collection und Account
// brauchen das nicht: Ersetzt gibt es sie nicht, auf beiden Wegen dieselbe
// Antwort.
func checkUTF8(name string, content *string) error {
	if !utf8.ValidString(name) {
		return contract.Invalid(fmt.Sprintf("Dokument %q: der Name ist kein gültiges UTF-8", name))
	}
	if content != nil && !utf8.ValidString(*content) {
		return contract.Invalid(fmt.Sprintf("Dokument %s: Inhalt ist kein UTF-8-Text", name))
	}
	return nil
}

// write schickt einen Schreibvorgang ohne Wiederholung.
func (c *Client) write(ctx context.Context, op string, version int, auth contract.NodeAuth, req any) (contract.WriteResponse, error) {
	var resp contract.WriteResponse
	if err := c.call(ctx, op, version, auth, req, &resp, false, c.ShortTimeout); err != nil {
		return contract.WriteResponse{}, err
	}
	return resp, nil
}

// callError ist ein Fehler eines Aufrufs, der kein Fehler des Vertrags ist.
// sent sagt, ob die Anfrage den Hub erreicht haben kann; retry, ob ein
// weiterer Versuch sinnvoll ist.
type callError struct {
	err   error
	sent  bool
	retry bool
}

func (e *callError) Error() string { return e.err.Error() }
func (e *callError) Unwrap() error { return e.err }

func (c *Client) call(ctx context.Context, op string, version int, auth contract.NodeAuth, body, out any,
	retry bool, timeout time.Duration) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	delay := c.Backoff
	for attempt := 0; ; attempt++ {
		err := c.once(ctx, op, version, auth, payload, out, timeout)
		var ce *callError
		if !errors.As(err, &ce) {
			return err // nil oder ein Fehler des Vertrags
		}
		if !retry {
			if ce.sent {
				return fmt.Errorf("%w: %v", contract.ErrOutcomeUnknown, ce.err)
			}
			return ce.err
		}
		if !ce.retry || attempt >= c.Retries || ctx.Err() != nil {
			return ce.err
		}
		select {
		case <-ctx.Done():
			return ce.err
		case <-time.After(delay):
		}
		delay *= 2
	}
}

// once schickt die Anfrage einmal.
func (c *Client) once(ctx context.Context, op string, version int, auth contract.NodeAuth, payload []byte, out any,
	timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	target := c.base + Path(version, op)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderNode, auth.Node)
	req.Header.Set("Authorization", "Bearer "+auth.Token)
	resp, err := c.http.Do(req)
	if err != nil {
		// Die url.Error nennt Methode und Adresse, keine Header.
		return &callError{err: fmt.Errorf("Hub %s: %w", c.base, unwrapURL(err)), sent: !notSent(err), retry: true}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return &callError{err: fmt.Errorf("Hub %s: Antwort nicht lesbar: %w", c.base, err), sent: true, retry: true}
	}
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(data, out); err != nil {
			return &callError{err: fmt.Errorf("Hub %s: Antwort ist kein gültiges JSON", c.base), sent: true}
		}
		return nil
	}
	var eb errorBody
	_ = json.Unmarshal(data, &eb)
	if resp.StatusCode == http.StatusNotFound && eb.Code == string(contract.CodeInvalid) {
		// Ein unbekannter Vorgang (docs/vertrag.md, „HTTP“): ein Hub, der
		// älter ist als der Vorgang. Er hat nichts ausgeführt.
		return fmt.Errorf("%w: Hub %s: %s", contract.ErrUnknownOperation, c.base, eb.Message)
	}
	if slices.Contains(contract.Codes, contract.Code(eb.Code)) {
		msg := eb.Message
		if msg == "" {
			msg = eb.Code
		}
		return &contract.Error{Code: contract.Code(eb.Code), Message: msg}
	}
	msg := eb.Message
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		// Eine Weiterleitung: Der Hub hat nicht ausgeführt, der Ausgang ist
		// eindeutig — auch bei rotate. Nicht wiederholen.
		return &callError{err: fmt.Errorf("Hub %s antwortet mit HTTP %d (Weiterleitung nach %q); "+
			"der Client folgt keinen Weiterleitungen", c.base, resp.StatusCode, resp.Header.Get("Location"))}
	}
	return &callError{err: fmt.Errorf("Hub %s antwortet mit HTTP %d: %s", c.base, resp.StatusCode, msg),
		sent: true, retry: resp.StatusCode >= 500}
}

// notSent sagt, ob ein Fehler von http.Client.Do sicher vor dem Abschicken
// entstand: Die Verbindung kam nicht zustande.
func notSent(err error) bool {
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return true
	}
	var dns *net.DNSError
	return errors.As(err, &dns)
}

// unwrapURL nimmt die Hülle von url.Error ab; sie wiederholte Methode und
// Adresse.
func unwrapURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}
