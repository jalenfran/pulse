package notify

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jalenfran/pulse/backend/internal/store"
)

type recorder struct{ sent []Message }

func (r *recorder) Send(_ context.Context, m Message) error {
	r.sent = append(r.sent, m)
	return nil
}

func setup(t *testing.T, clock *time.Time) (*Notifier, *recorder, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	rec := &recorder{}
	n := New(st, rec, Rules{
		MinSeverity: 1, // warning
		Always:      map[string]bool{"nixos-upgrade": true},
		Ignore:      map[string]bool{"k8s-event": true},
		Cooldown:    6 * time.Hour,
		MaxAge:      time.Hour,
		QuietStart:  23,
		QuietEnd:    8,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	n.now = func() time.Time { return *clock }
	return n, rec, st
}

func add(t *testing.T, st *store.Store, at time.Time, kind, severity, key string) store.Event {
	t.Helper()
	e, err := st.AddEvent(context.Background(), store.Event{TS: at, Source: "host:node1", Kind: kind,
		Severity: severity, Title: kind + " " + severity, Key: key})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestQuietHoursWrapMidnight(t *testing.T) {
	r := Rules{QuietStart: 23, QuietEnd: 8}
	for h, want := range map[int]bool{22: false, 23: true, 0: true, 7: true, 8: false, 12: false} {
		if got := r.quiet(time.Date(2026, 1, 1, h, 30, 0, 0, time.Local)); got != want {
			t.Errorf("hour %d: quiet=%v, want %v", h, got, want)
		}
	}
	if (Rules{QuietStart: 5, QuietEnd: 5}).quiet(time.Date(2026, 1, 1, 5, 0, 0, 0, time.Local)) {
		t.Error("equal start and end should disable quiet hours")
	}
}

func TestDaytimeRules(t *testing.T) {
	noon := time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local)
	n, rec, st := setup(t, &noon)
	ctx := context.Background()

	n.handle(ctx, add(t, st, noon, "pod-crashloop", "warning", "crash:a"))
	n.handle(ctx, add(t, st, noon, "pod-crashloop", "warning", "crash:a")) // same key: cooldown
	n.handle(ctx, add(t, st, noon, "boot", "info", "boot:node1"))          // below min severity
	n.handle(ctx, add(t, st, noon, "nixos-upgrade", "info", "up:node1"))   // always
	n.handle(ctx, add(t, st, noon, "k8s-event", "warning", "k8s:x"))       // ignored kind
	n.handle(ctx, add(t, st, noon.Add(-2*time.Hour), "node-down", "critical", "down:old"))

	if len(rec.sent) != 2 {
		t.Fatalf("sent %d messages, want 2: %+v", len(rec.sent), rec.sent)
	}
	if rec.sent[0].Kind != "pod-crashloop" || rec.sent[1].Kind != "nixos-upgrade" {
		t.Errorf("unexpected messages: %+v", rec.sent)
	}
	for _, m := range rec.sent {
		if !strings.HasPrefix(m.Click, "pulse://events/") || len(m.Click) != len("pulse://events/")+26 {
			t.Errorf("click = %q, want pulse://events/<ulid>", m.Click)
		}
	}

	// After the cooldown the same key notifies again.
	later := noon.Add(7 * time.Hour)
	n.now = func() time.Time { return later }
	n.handle(ctx, add(t, st, later, "pod-crashloop", "warning", "crash:a"))
	if len(rec.sent) != 3 {
		t.Errorf("cooldown did not expire: sent %d", len(rec.sent))
	}
}

func TestQuietHoursDeferThenDigest(t *testing.T) {
	night := time.Date(2026, 10, 16, 4, 0, 0, 0, time.Local)
	clock := night
	n, rec, st := setup(t, &clock)
	ctx := context.Background()

	n.handle(ctx, add(t, st, night, "nixos-upgrade", "info", "up:node1"))
	n.handle(ctx, add(t, st, night, "node-notready", "warning", "nr:node1"))
	n.handle(ctx, add(t, st, night, "node-down", "critical", "down:node2")) // pages now
	n.flush(ctx)                                                            // still quiet: no-op

	if len(rec.sent) != 1 || rec.sent[0].Severity != "critical" {
		t.Fatalf("during quiet hours sent %+v, want only the critical one", rec.sent)
	}

	clock = time.Date(2026, 10, 16, 8, 1, 0, 0, time.Local)
	n.flush(ctx)
	if len(rec.sent) != 2 {
		t.Fatalf("after quiet hours sent %d, want 2", len(rec.sent))
	}
	d := rec.sent[1]
	if d.Kind != "digest" || d.Severity != "warning" || !strings.Contains(d.Title, "2 events") {
		t.Errorf("digest = %+v", d)
	}
	if d.Click != "pulse://events" {
		t.Errorf("digest click = %q, want pulse://events", d.Click)
	}

	// Digested keys are in cooldown, and the queue is empty.
	n.handle(ctx, add(t, st, clock, "node-notready", "warning", "nr:node1"))
	n.flush(ctx)
	if len(rec.sent) != 2 {
		t.Errorf("digested key notified again within cooldown: %+v", rec.sent[2:])
	}
}
