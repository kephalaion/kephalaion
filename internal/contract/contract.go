// Package contract ist der Vertrag zwischen Node und Hub (docs/vertrag.md)
// als Go-Typen: Anfragen, Antworten, Fehler und die Schnittstelle Hub. Das
// Paket ist neutral — es kennt weder internal/hub noch internal/node. Der Hub
// setzt die Schnittstelle um, der Node benutzt sie; welche Umsetzung er
// bekommt (local oder HTTP), entscheidet cmd/kephalaion.
//
// Die Typen sind so geschnitten, dass sie unverändert als JSON laufen. Was
// über HTTP nicht im Body steht — die Fassung (im Pfad) und die Anmeldung des
// Nodes (in Headern) —, trägt das Tag json:"-".
package contract

import (
	"context"
	"errors"
)

// Version ist die Fassung des Vertrags, die dieses Binary spricht. Fassung 1
// ist die einzige.
const Version = 1

// DefaultPageSize ist die Seitengröße, mit der ein Node fragt, wenn nichts
// anderes eingestellt ist: Zeilen je Seite. Keine Nutzeroption; Tests fragen
// mit eigener Seitengröße.
const DefaultPageSize = 500

// MaxDocumentBytes ist die Obergrenze für den Inhalt eines Dokuments: 1 MiB.
// Der Hub nimmt nichts Größeres an; ein Transport muss jedes Dokument bis
// zu dieser Größe tragen.
const MaxDocumentBytes = 1 << 20

// Hub ist, was ein Node vom Hub braucht. Jeder Aufruf trägt die Anmeldung des
// Nodes und wird am Hub geprüft, auch auf dem lokalen Weg. Fehler sind *Error
// mit einem der Codes dieses Pakets oder Fehler des Transports bzw. der
// Datenbank.
type Hub interface {
	// Whoami bestätigt den Node und nennt seine erlaubten Collections; mit
	// einem Account-Teil prüft es zusätzlich den Account.
	Whoami(ctx context.Context, req WhoamiRequest) (WhoamiResponse, error)
	// Rotate ersetzt das Token eines Accounts: altes Token zur Anmeldung,
	// Hash des neuen. Nicht wiederholbar — danach gilt das alte Token nicht
	// mehr; ein Transport wiederholt es deshalb nie.
	Rotate(ctx context.Context, req RotateRequest) (RotateResponse, error)
	// Sync liefert eine Seite des Abgleichs.
	Sync(ctx context.Context, req SyncRequest) (SyncResponse, error)

	// Die Schreibvorgänge schreiben im Namen eines Accounts, getragen vom
	// Node. Wie Rotate nicht wiederholbar — ein zweiter Versuch ergäbe
	// name_taken oder stale_revision —; ein Transport wiederholt sie nie.
	// Ist nach dem Abschicken offen, ob der Hub geschrieben hat, trägt der
	// Fehler ErrOutcomeUnknown.

	// Create legt ein Dokument an.
	Create(ctx context.Context, req CreateRequest) (WriteResponse, error)
	// Write ersetzt den Inhalt eines Dokuments, wahlweise unter der
	// Vorbedingung einer Revision.
	Write(ctx context.Context, req WriteRequest) (WriteResponse, error)
	// Delete setzt eine Löschmarke auf ein Dokument, wahlweise unter der
	// Vorbedingung einer Revision.
	Delete(ctx context.Context, req DeleteRequest) (WriteResponse, error)
}

// NodeAuth ist die Anmeldung des Nodes am Hub: sein Name dort und sein Token.
// Über HTTP steht sie in Headern, nicht im Body.
type NodeAuth struct {
	Node  string `json:"-"`
	Token string `json:"-"`
}

// AccountAuth ist die Anmeldung eines Accounts: sein Name und sein Token. Sie
// steht im Body — der Node trägt die Anfrage, der Account sagt, in wessen
// Namen.
type AccountAuth struct {
	Account string `json:"account"`
	Token   string `json:"token"`
}

// WhoamiRequest fragt den Hub, wer der Node für ihn ist, und wahlweise, ob ein
// Account gilt.
type WhoamiRequest struct {
	// Version ist die Fassung des Nodes; über HTTP im Pfad.
	Version int `json:"-"`
	// Auth ist die Anmeldung des Nodes; über HTTP in Headern.
	Auth NodeAuth `json:"-"`
	// Account ist wahlweise ein Account, den der Hub prüfen soll.
	Account *AccountAuth `json:"account,omitempty"`
}

// WhoamiResponse bestätigt den Node.
type WhoamiResponse struct {
	HubID   string `json:"hub_id"`
	Version int    `json:"version"`
	// Node ist der Name des Nodes am Hub.
	Node string `json:"node"`
	// Allowed sind die Collections, die der Node abgleichen darf, sortiert.
	Allowed []string `json:"allowed"`
	// Account ist gesetzt, wenn die Anfrage einen Account-Teil hatte.
	Account *AccountStatus `json:"account,omitempty"`
}

// AccountStatus sagt, ob ein Account gilt. Unbekannt, falsches Token und
// gesperrt sind dieselbe Antwort: Valid false, kein User, keine Collections.
type AccountStatus struct {
	Account string `json:"account"`
	Valid   bool   `json:"valid"`
	// User ist, wem der Account gehört; nur wenn Valid gilt, sonst leer.
	User string `json:"user"`
	// Collections sind die Collections des Accounts, die dieser Node
	// abgleichen darf, sortiert; nur wenn Valid gilt.
	Collections []string `json:"collections"`
}

// RotateRequest ersetzt das Token eines Accounts.
type RotateRequest struct {
	// Version ist die Fassung des Nodes; über HTTP im Pfad.
	Version int `json:"-"`
	// Auth ist die Anmeldung des Nodes, des Trägers; über HTTP in Headern.
	Auth NodeAuth `json:"-"`
	// Account ist der Name des Accounts.
	Account string `json:"account"`
	// Token ist das bisherige Token des Accounts.
	Token string `json:"token"`
	// NewHash ist sha256 des neuen Tokens, 64 Zeichen hex. Das neue Token
	// selbst verlässt den Node nie.
	NewHash string `json:"new_hash"`
}

// RotateResponse liefert die Zeilen des Accounts nach dem Wechsel,
// beschränkt auf die Collections, die der Node abgleichen darf. Den User
// nennt der Inhalt jeder Zeile (AccountContent).
type RotateResponse struct {
	HubID   string `json:"hub_id"`
	Version int    `json:"version"`
	// Rows sind die Account-Zeilen (SYSTEM:A:<account>) mit dem neuen Hash
	// und dem User, je eine erlaubte Collection des Accounts, nach
	// Collection; updated_by ist der User.
	Rows []Row `json:"rows"`
}

// CreateRequest legt ein Dokument an: ein lebendes Dokument mit dem Namen
// ist name_taken; eine Löschmarke hindert nicht.
type CreateRequest struct {
	// Version ist die Fassung des Nodes; über HTTP im Pfad.
	Version int `json:"-"`
	// Auth ist die Anmeldung des Nodes, des Trägers; über HTTP in Headern.
	Auth NodeAuth `json:"-"`
	// Account ist der Account, in dessen Namen geschrieben wird.
	Account    AccountAuth `json:"account"`
	Collection string      `json:"collection"`
	Name       string      `json:"name"`
	// Content ist der Inhalt: UTF-8 ohne NUL, höchstens MaxDocumentBytes,
	// auch leer. Über HTTP Pflicht.
	Content string `json:"content"`
}

// WriteRequest ersetzt den Inhalt eines lebenden Dokuments.
type WriteRequest struct {
	// Version ist die Fassung des Nodes; über HTTP im Pfad.
	Version int `json:"-"`
	// Auth ist die Anmeldung des Nodes, des Trägers; über HTTP in Headern.
	Auth       NodeAuth    `json:"-"`
	Account    AccountAuth `json:"account"`
	Collection string      `json:"collection"`
	Name       string      `json:"name"`
	// Content wie bei CreateRequest; über HTTP Pflicht.
	Content string `json:"content"`
	// BaseRevision ist wahlweise die Revision, auf der der Vorgang beruht
	// (≥ 1); nil heißt ohne Vorbedingung.
	BaseRevision *int64 `json:"base_revision,omitempty"`
}

// DeleteRequest setzt eine Löschmarke auf ein lebendes Dokument.
type DeleteRequest struct {
	// Version ist die Fassung des Nodes; über HTTP im Pfad.
	Version int `json:"-"`
	// Auth ist die Anmeldung des Nodes, des Trägers; über HTTP in Headern.
	Auth       NodeAuth    `json:"-"`
	Account    AccountAuth `json:"account"`
	Collection string      `json:"collection"`
	Name       string      `json:"name"`
	// BaseRevision wie bei WriteRequest.
	BaseRevision *int64 `json:"base_revision,omitempty"`
}

// WriteResponse ist die Antwort jedes Schreibvorgangs (create, write,
// delete). Sie ist die Wahrheit, nicht die Anfrage: Der Node übernimmt Rows
// in seine Replica.
type WriteResponse struct {
	HubID   string `json:"hub_id"`
	Version int    `json:"version"`
	// Revision ist die Revision des Vorgangs; bei unverändertem Inhalt
	// (write) die bestehende des Dokuments.
	Revision int64 `json:"revision"`
	// Rows sind die geschriebenen Zeilen in der Form von sync, bei delete
	// die Löschmarken; bei unverändertem Inhalt die bestehende Zeile.
	Rows []Row `json:"rows"`
}

// Since ist ein Paar der Anfrage: alles aus Collection mit einer Revision
// größer als Since.
type Since struct {
	Collection string `json:"collection"`
	Since      int64  `json:"since"`
}

// SyncRequest ist die Anfrage des Abgleichs.
type SyncRequest struct {
	// Version ist die Fassung des Nodes; über HTTP im Pfad.
	Version int `json:"-"`
	// Auth ist die Anmeldung des Nodes; über HTTP in Headern.
	Auth NodeAuth `json:"-"`
	// Collections sind die gewünschten Collections, jede höchstens einmal.
	Collections []Since `json:"collections"`
	// PageSize ist die gewünschte Seitengröße, > 0. Der Hub begrenzt sie
	// nach oben auf eine eigene Obergrenze.
	PageSize int `json:"page_size"`
}

// CollectionStatus sagt zu einer angefragten Collection, ob der Node sie
// abgleichen darf. Unbekannt und nicht erlaubt sind dieselbe Antwort.
type CollectionStatus struct {
	Collection string `json:"collection"`
	Allowed    bool   `json:"allowed"`
}

// Row ist eine Zeile aus documents mit allen Spalten, so wie sie am Hub
// steht — auch Löschmarken und SYSTEM:-Zeilen. Nullbare Spalten sind Zeiger:
// nil heißt NULL, der Node speichert es genau so.
type Row struct {
	ID         string `json:"id"`
	Collection string `json:"collection"`
	Name       string `json:"name"`
	// Content ist nil bei einer Löschmarke.
	Content *string `json:"content"`
	// Meta ist freies JSON als Text, oder nil; der Hub deutet es nicht.
	Meta      *string `json:"meta"`
	Deleted   bool    `json:"deleted"`
	Revision  int64   `json:"revision"`
	CreatedAt int64   `json:"created_at"`
	CreatedBy string  `json:"created_by"`
	UpdatedAt int64   `json:"updated_at"`
	UpdatedBy string  `json:"updated_by"`
}

// SyncResponse ist eine Seite des Abgleichs.
type SyncResponse struct {
	// HubID ist die Kennung des Hubs.
	HubID string `json:"hub_id"`
	// Version ist die Fassung, in der der Hub antwortet.
	Version int `json:"version"`
	// Collections nennt jede angefragte Collection, in der Reihenfolge der
	// Anfrage, mit erlaubt oder nicht erlaubt.
	Collections []CollectionStatus `json:"collections"`
	// Allowed sind alle Collections, die der Node abgleichen darf, sortiert.
	Allowed []string `json:"allowed"`
	// Rows sind die Zeilen der erlaubten angefragten Collections mit
	// revision > Since der jeweiligen Collection und ≤ HubRevision, sortiert
	// nach Revision, dann id. Die Seite endet an einer Revisionsgrenze.
	Rows []Row `json:"rows"`
	// HubRevision ist die Revision H des Hubs, gelesen vor den Zeilen.
	HubRevision int64 `json:"hub_revision"`
	// Until ist die Revision, bis zu der der Node nach dieser Seite alles
	// hat: die Revision der letzten Zeile, wenn More gilt, sonst HubRevision.
	// Der Node setzt je Collection max(Since, Until), nie zurück.
	Until int64 `json:"until"`
	// More sagt, dass nach Until weitere Zeilen folgen; der Node fragt dann
	// ab Until weiter.
	More bool `json:"more"`
}

// Code ist die Art eines Fehlers des Vertrags; über HTTP steht er im Body.
type Code string

// Fehlercodes der Fassung 1.
const (
	// CodeUnauthenticated: Node unbekannt, Token falsch oder Node gesperrt —
	// dieselbe Antwort für alle drei.
	CodeUnauthenticated Code = "unauthenticated"
	// CodeInvalid: die Anfrage ist ungültig.
	CodeInvalid Code = "invalid"
	// CodeUnsupportedVersion: der Hub kennt oder bedient die Fassung des
	// Nodes nicht.
	CodeUnsupportedVersion Code = "unsupported_version"
	// CodeAccountUnauthenticated: der Node ist angemeldet, der Account nicht
	// — unbekannt, Token falsch oder gesperrt, dieselbe Antwort.
	CodeAccountUnauthenticated Code = "account_unauthenticated"
	// CodeNoSharedCollection: der Account hat keine der Collections, die der
	// Node abgleichen darf (rotate).
	CodeNoSharedCollection Code = "no_shared_collection"

	// Codes der Schreibvorgänge; alle endgültig.

	// CodeNotReadable: die Collection gibt es nicht, der Node darf sie nicht
	// abgleichen, oder der Account hat keine lebende Zeile in ihr —
	// dieselbe Antwort für alle drei.
	CodeNotReadable Code = "not_readable"
	// CodeForbidden: dem Account fehlt das Recht — write für Neues und
	// Eigenes, supersede für Fremdes; die Meldung nennt den Grund.
	CodeForbidden Code = "forbidden"
	// CodeNotFound: kein lebendes Dokument mit dem Namen (write, delete).
	CodeNotFound Code = "not_found"
	// CodeNameTaken: ein lebendes Dokument trägt den Namen schon (create).
	CodeNameTaken Code = "name_taken"
	// CodePathConflict: der Name wäre zugleich Datei und Verzeichnis.
	CodePathConflict Code = "path_conflict"
	// CodeStaleRevision: das Dokument hat nicht die Revision, auf der der
	// Vorgang beruht; die Meldung nennt die aktuelle.
	CodeStaleRevision Code = "stale_revision"
)

// Codes sind alle Fehlercodes des Vertrags.
var Codes = []Code{CodeUnauthenticated, CodeInvalid, CodeUnsupportedVersion, CodeAccountUnauthenticated,
	CodeNoSharedCollection, CodeNotReadable, CodeForbidden, CodeNotFound, CodeNameTaken, CodePathConflict,
	CodeStaleRevision}

// Error ist ein Fehler des Vertrags: ein Code und eine Meldung. errors.Is
// vergleicht nur den Code, so dass jeder *Error mit gleichem Code die
// passende Fehlervariable trifft.
type Error struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// Is meldet gleichen Code.
func (e *Error) Is(target error) bool {
	var t *Error
	return errors.As(target, &t) && t.Code == e.Code
}

// Fehlervariablen für errors.Is, je Code eine, mit der Standardmeldung.
var (
	ErrUnauthenticated        = &Error{Code: CodeUnauthenticated, Message: "nicht angemeldet"}
	ErrInvalid                = &Error{Code: CodeInvalid, Message: "ungültige Anfrage"}
	ErrUnsupportedVersion     = &Error{Code: CodeUnsupportedVersion, Message: "Fassung nicht unterstützt"}
	ErrAccountUnauthenticated = &Error{Code: CodeAccountUnauthenticated,
		Message: "Account nicht angemeldet"}
	ErrNoSharedCollection = &Error{Code: CodeNoSharedCollection,
		Message: "der Account hat keine der Collections, die dieser Node abgleichen darf"}
	ErrNotReadable   = &Error{Code: CodeNotReadable, Message: "Collection nicht lesbar"}
	ErrForbidden     = &Error{Code: CodeForbidden, Message: "Recht fehlt"}
	ErrNotFound      = &Error{Code: CodeNotFound, Message: "Dokument gibt es nicht"}
	ErrNameTaken     = &Error{Code: CodeNameTaken, Message: "Name vergeben"}
	ErrPathConflict  = &Error{Code: CodePathConflict, Message: "Name wäre zugleich Datei und Verzeichnis"}
	ErrStaleRevision = &Error{Code: CodeStaleRevision, Message: "Revision veraltet"}
)

// ErrOutcomeUnknown meldet einen Transportfehler, nach dem offen ist, ob der
// Hub die Anfrage ausgeführt hat — etwa eine Zeitüberschreitung, nachdem sie
// abgeschickt war. Für rotate heißt das: prüfen (whoami mit Account-Teil),
// nicht wiederholen; für einen Schreibvorgang: abgleichen und nachsehen. Ein
// Fehler, der vor dem Abschicken entstand (Verbindung abgelehnt), ist es
// nicht.
var ErrOutcomeUnknown = errors.New("Ausgang unklar")

// ErrUnknownOperation meldet, dass der Hub den Vorgang nicht kennt: ein Hub
// derselben Fassung, der älter ist als der Vorgang (über HTTP 404 mit
// invalid). Der Hub hat nichts ausgeführt. Der Hub schickt keinen eigenen
// Code dafür; der Transport erkennt den Fall und meldet ihn so, nicht als
// ErrInvalid.
var ErrUnknownOperation = errors.New("der Hub kennt den Vorgang nicht")

// Invalid liefert einen Fehler mit Code CodeInvalid und einem Grund.
func Invalid(reason string) *Error {
	return &Error{Code: CodeInvalid, Message: "ungültige Anfrage: " + reason}
}
