package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// A recurring mirrored event gets a new id each time; ids handed out earlier
// (notification links, the quiet-hours queue) must keep resolving.
func TestOldIDsResolveAfterRefresh(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	var emitted []Event
	st.OnEvent(func(e Event) { emitted = append(emitted, e) })

	t0 := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	crash := func(at time.Time, body string) {
		t.Helper()
		if err := st.UpsertExternal(ctx, "crashloop:uid:app", Event{TS: at, Source: "k8s", Kind: "pod-crashloop",
			Severity: "warning", Title: "web is crashlooping", Body: body, Key: "pod-crashloop:ns/web"}); err != nil {
			t.Fatal(err)
		}
	}
	crash(t0, "3 restarts")
	if err := st.Defer(ctx, emitted[0]); err != nil { // held overnight under the first id
		t.Fatal(err)
	}
	crash(t0, "3 restarts")                    // re-sighting, same ts: no change
	crash(t0.Add(time.Minute), "4 restarts")   // recurrence: new id
	crash(t0.Add(2*time.Minute), "5 restarts") // and again

	if len(emitted) != 3 {
		t.Fatalf("emitted %d events, want 3 (the unchanged re-sighting must not emit)", len(emitted))
	}
	first, latest := emitted[0].ID, emitted[2].ID
	if first == latest {
		t.Fatal("recurrence should get a new id")
	}

	for _, id := range []string{first, emitted[1].ID, latest} {
		e, err := st.GetEvent(ctx, id)
		if err != nil {
			t.Fatalf("GetEvent(%s): %v", id, err)
		}
		if e.ID != latest || e.Body != "5 restarts" {
			t.Errorf("GetEvent(%s) = %s %q, want the current version %s", id, e.ID, e.Body, latest)
		}
	}

	held, err := st.TakeDeferred(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 1 || held[0].ID != latest {
		t.Errorf("deferred event lost after refresh: %+v", held)
	}

	if _, err := st.GetEvent(ctx, "01ZZZZZZZZZZZZZZZZZZZZZZZZ"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: err = %v, want ErrNotFound", err)
	}

	// Pruning the event takes its aliases with it.
	if _, err := st.Prune(ctx, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetEvent(ctx, first); !errors.Is(err, ErrNotFound) {
		t.Errorf("old id after prune: err = %v, want ErrNotFound", err)
	}
	var aliases int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM event_aliases`).Scan(&aliases); err != nil || aliases != 0 {
		t.Errorf("aliases left after prune: %d (err %v)", aliases, err)
	}
}
