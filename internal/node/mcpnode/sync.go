package mcpnode

import (
	"errors"

	"github.com/kephalaion/kephalaion/internal/node/replica"
)

// Der Abgleich auf Anforderung (Task 042, docs/konzept.md, „Abgleich“, „Auf
// Anforderung“): Ein Client fordert den Abgleich eines Hub-Eintrags an und
// bekommt dessen Ergebnis. Der Abgleich läuft im Prozess von serve
// (cmd/kephalaion, backgroundSync); dieses Paket kennt ihn nur als wartenden
// Anstoß (HubLink.SyncWait) und dessen Ergebnis.

// SyncOutcome ist das Ergebnis eines abgewarteten Abgleichs: des ersten
// Abgleichs eines Hub-Eintrags, der nach dem Aufruf des wartenden Anstoßes
// begonnen hat. Es stammt aus diesem Abgleich selbst, nicht aus hub_sync —
// dort kann schon ein späterer stehen. Den Fehlertext des Abgleichs trägt es
// nicht: Er kann die Adresse des Hubs nennen.
type SyncOutcome struct {
	// At ist bei einem Erfolg der Zeitpunkt (ms seit 1970), den der Abgleich
	// als letzten Erfolg in hub_sync festgehalten hat.
	At int64
	// Kind ist die Art des Fehlers eines gescheiterten Abgleichs; leer heißt
	// Erfolg.
	Kind replica.ErrorKind
	// Contract sagt bei einem gescheiterten Abgleich, ob sein Fehler ein
	// Fehler des Vertrags ist (*contract.Error) — der Hub hat geantwortet und
	// abgelehnt. Nur zur Einordnung der Art hub.
	Contract bool
}

// Die Fehler des wartenden Anstoßes neben ErrUnsupported (Transport ssh) und
// dem ctx des Aufrufers: Der Abgleich, auf den gewartet wurde, hat kein
// Ergebnis.
var (
	// ErrSyncGone: Der Hub-Eintrag wurde während des Wartens entfernt oder
	// unter demselben Alias neu angelegt.
	ErrSyncGone = errors.New("Hub-Eintrag entfernt oder neu angelegt")
	// ErrSyncStopped: serve endet; es beginnt kein Abgleich mehr, ein
	// laufender ist abgebrochen.
	ErrSyncStopped = errors.New("serve endet")
)
