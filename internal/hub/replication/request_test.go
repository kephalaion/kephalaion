package replication

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kephalaion/kephalaion/internal/contract"
)

// Der Idempotenzschlüssel über jeden Transport (Task 032).

func checkReplayed(t *testing.T, f *fixture, what string, resp contract.WriteResponse, err error, rev int64, ids []string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	info, _ := f.st.Info(context.Background())
	if !resp.Replayed || resp.Revision != rev || !reflect.DeepEqual(resp.IDs, ids) || resp.Rows == nil ||
		len(resp.Rows) != 0 || resp.HubID != info.HubID || resp.Version != contract.Version {
		t.Errorf("%s: %+v, erwartet replayed mit Revision %d und ids %v", what, resp, rev, ids)
	}
}

// Der Schlüssel kommt über local und HTTP an: Die Wiederholung ist
// replayed, der Erfolg nicht; Form und anderer Vorgang sind invalid.
func TestRequestID(t *testing.T) {
	forTransports(t, func(t *testing.T, newFixture func(*testing.T) *fixture) {
		f := newFixture(t)
		ctx := context.Background()
		acc := f.writers(t)
		req := f.createReq(acc["bob"], "a", "neu.md", "eins")
		req.RequestID = "550e8400-e29b-41d4-a716-446655440000"
		first, err := f.api().Create(ctx, req)
		row := f.checkWritten(t, "create", first, err)
		if first.Replayed || first.IDs != nil {
			t.Errorf("erster Aufruf: %+v", first)
		}
		before := f.revision(t)
		resp, err := f.api().Create(ctx, req)
		checkReplayed(t, f, "Wiederholung", resp, err, first.Revision, []string{row.ID})
		if f.revision(t) != before {
			t.Error("Wiederholung hat geschrieben")
		}

		other := req
		other.Content = "zwei"
		_, err = f.api().Create(ctx, other)
		var ce *contract.Error
		if !errors.As(err, &ce) || ce.Code != contract.CodeInvalid || !strings.Contains(ce.Message, "anderen Vorgang") {
			t.Errorf("anderer Inhalt: %v", err)
		}
		bad := f.createReq(acc["bob"], "a", "x.md", "")
		bad.RequestID = "kein schlüssel"
		if _, err := f.api().Create(ctx, bad); !errors.As(err, &ce) || ce.Code != contract.CodeInvalid ||
			!strings.Contains(ce.Message, "request_id") {
			t.Errorf("ungültiger Schlüssel: %v", err)
		}
		// Ein gesperrter Account bekommt die Ablehnung, nicht replayed.
		locked := f.createReq(acc["gesperrt"], "a", "x.md", "")
		locked.RequestID = req.RequestID
		if _, err := f.api().Create(ctx, locked); !errors.Is(err, contract.ErrAccountUnauthenticated) {
			t.Errorf("gesperrt: %v", err)
		}
	})
}

// Geht die Antwort verloren (hier: der Hub schreibt, ein Proxy antwortet
// 502), ist der Ausgang unklar; der Client wiederholt nicht selbst. Der
// Aufrufer schickt denselben Aufruf mit demselben Schlüssel noch einmal und
// bekommt replayed — je Vorgang genau eine Revision.
func TestRequestIDAfterLostResponse(t *testing.T) {
	f := newLocalFixture(t)
	ctx := context.Background()
	acc := f.writers(t)
	var calls atomic.Int32
	var lose atomic.Bool
	c := overHTTP(t, f.st, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if lose.Load() {
				next.ServeHTTP(httptest.NewRecorder(), r) // der Hub schreibt, die Antwort geht verloren
				http.Error(w, "Bad Gateway", http.StatusBadGateway)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	create := f.createReq(acc["bob"], "a", "d/neu.md", "eins")
	create.RequestID = "c-1"
	write := f.writeReq(acc["bob"], "a", "d/neu.md", "zwei", nil)
	write.RequestID = "w-1"
	rename := f.renameReq(acc["bob"], "a", "d", "e", nil)
	rename.RequestID = "r-1"
	del := f.deleteDirReq(acc["bob"], "a", "e")
	del.RequestID = "d-1"
	ops := []struct {
		name string
		call func() (contract.WriteResponse, error)
	}{
		{"create", func() (contract.WriteResponse, error) { return c.Create(ctx, create) }},
		{"write", func() (contract.WriteResponse, error) { return c.Write(ctx, write) }},
		{"rename", func() (contract.WriteResponse, error) { return c.Rename(ctx, rename) }},
		{"delete", func() (contract.WriteResponse, error) { return c.Delete(ctx, del) }},
	}
	var id string
	for _, op := range ops {
		before := f.revision(t)
		lose.Store(true)
		calls.Store(0)
		if _, err := op.call(); !errors.Is(err, contract.ErrOutcomeUnknown) || calls.Load() != 1 {
			t.Fatalf("%s: %d Aufrufe, %v, erwartet einen mit unklarem Ausgang", op.name, calls.Load(), err)
		}
		written := f.revision(t)
		if written != before+1 {
			t.Fatalf("%s: Revision %d → %d, erwartet geschrieben", op.name, before, written)
		}
		lose.Store(false)
		resp, err := op.call()
		if id == "" && err == nil && len(resp.IDs) == 1 {
			id = resp.IDs[0]
		}
		checkReplayed(t, f, op.name, resp, err, written, []string{id})
		if f.revision(t) != written {
			t.Errorf("%s: Wiederholung hat geschrieben", op.name)
		}
	}
	if _, err := f.st.Document(ctx, "a", "e/neu.md"); err == nil {
		t.Error("e/neu.md lebt noch")
	}
}
