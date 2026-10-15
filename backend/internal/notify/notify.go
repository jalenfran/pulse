// Package notify decides which events become push notifications and sends
// them. Rules, in order:
//
//   - kinds in Ignore never notify
//   - events older than MaxAge never notify (a restart re-reading history, a
//     host posting something from hours ago)
//   - kinds in Always notify whatever their severity; everything else needs
//     at least MinSeverity
//   - one notification per event key per Cooldown
//   - during quiet hours only critical events go out; the rest are held and
//     sent as one digest when quiet hours end
package notify

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jalenfran/pulse/backend/internal/store"
)

type Rules struct {
	MinSeverity int
	Always      map[string]bool
	Ignore      map[string]bool
	Cooldown    time.Duration
	MaxAge      time.Duration
	// Quiet hours in local time (TZ), [QuietStart, QuietEnd). Equal values
	// disable them. They may wrap midnight, e.g. 23 to 8.
	QuietStart, QuietEnd int
}

func (r Rules) quiet(t time.Time) bool {
	h := t.Hour()
	switch {
	case r.QuietStart == r.QuietEnd:
		return false
	case r.QuietStart < r.QuietEnd:
		return h >= r.QuietStart && h < r.QuietEnd
	default:
		return h >= r.QuietStart || h < r.QuietEnd
	}
}

// Message is one push notification, independent of the delivery service.
type Message struct {
	Title    string
	Body     string
	Severity string
	Kind     string
	// Click is a deep link opened when the notification is tapped (see
	// "Deep links" in api/openapi.yaml). Empty means just open the app.
	Click string
}

type Sender interface {
	Send(ctx context.Context, m Message) error
}

type Notifier struct {
	store  *store.Store
	sender Sender
	rules  Rules
	log    *slog.Logger
	events chan store.Event
	now    func() time.Time // swapped in tests
}

func New(st *store.Store, sender Sender, rules Rules, log *slog.Logger) *Notifier {
	return &Notifier{store: st, sender: sender, rules: rules, log: log, events: make(chan store.Event, 256), now: time.Now}
}

// Consider queues an event for evaluation. It never blocks: it is called from
// the store's write path, including informer callbacks.
func (n *Notifier) Consider(e store.Event) {
	select {
	case n.events <- e:
	default:
		n.log.Warn("notify queue full, dropping", "kind", e.Kind, "key", e.Key)
	}
}

// Run evaluates events and flushes the quiet-hours digest until ctx ends.
func (n *Notifier) Run(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	n.flush(ctx) // in case pulse restarted after quiet hours ended
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-n.events:
			n.handle(ctx, e)
		case <-tick.C:
			n.flush(ctx)
		}
	}
}

func (n *Notifier) wants(e store.Event, now time.Time) bool {
	if n.rules.Ignore[e.Kind] || now.Sub(e.TS) > n.rules.MaxAge {
		return false
	}
	if n.rules.Always[e.Kind] {
		return true
	}
	rank, _ := store.SeverityRank(e.Severity)
	return rank >= n.rules.MinSeverity
}

func (n *Notifier) handle(ctx context.Context, e store.Event) {
	now := n.now()
	if !n.wants(e, now) {
		return
	}
	last, ok, err := n.store.LastNotified(ctx, e.Key)
	if err != nil {
		n.log.Error("notify: read cooldown", "err", err)
		return
	}
	if ok && now.Sub(last) < n.rules.Cooldown {
		n.log.Debug("notify: cooling down", "key", e.Key)
		return
	}
	if e.Severity != "critical" && n.rules.quiet(now) {
		if err := n.store.Defer(ctx, e); err != nil {
			n.log.Error("notify: defer", "err", err)
		}
		return
	}
	n.deliver(ctx, eventMessage(e), e.Key)
}

// flush sends whatever quiet hours held back, as one message.
func (n *Notifier) flush(ctx context.Context) {
	if n.rules.quiet(n.now()) {
		return
	}
	held, err := n.store.TakeDeferred(ctx)
	if err != nil {
		n.log.Error("notify: read deferred", "err", err)
		return
	}
	switch len(held) {
	case 0:
		return
	case 1:
		e := held[0]
		n.deliver(ctx, eventMessage(e), e.Key)
		return
	}
	n.deliver(ctx, digest(held), "")
	now := n.now()
	for _, e := range held {
		if err := n.store.MarkNotified(ctx, e.Key, now); err != nil {
			n.log.Error("notify: mark sent", "err", err)
		}
	}
}

// eventMessage is the notification for a single event, linking to it.
func eventMessage(e store.Event) Message {
	return Message{Title: e.Title, Body: e.Body, Severity: e.Severity, Kind: e.Kind, Click: "pulse://events/" + e.ID}
}

const digestLines = 15

func digest(events []store.Event) Message {
	worst := 0
	var b strings.Builder
	for i, e := range events {
		if rank, _ := store.SeverityRank(e.Severity); rank > worst {
			worst = rank
		}
		if i == digestLines {
			fmt.Fprintf(&b, "…and %d more", len(events)-digestLines)
			break
		}
		fmt.Fprintf(&b, "%s %s  %s\n", marker(e.Severity), e.TS.Local().Format("15:04"), e.Title)
	}
	severity := []string{"info", "warning", "critical"}[worst]
	return Message{
		Title:    fmt.Sprintf("%d events overnight", len(events)),
		Body:     strings.TrimRight(b.String(), "\n"),
		Severity: severity,
		Kind:     "digest",
		Click:    "pulse://events",
	}
}

func marker(severity string) string {
	switch severity {
	case "critical":
		return "‼"
	case "warning":
		return "!"
	}
	return "·"
}

// deliver sends with a few retries, then records the send against key (if
// any) so the cooldown starts.
func (n *Notifier) deliver(ctx context.Context, m Message, key string) {
	var err error
	for attempt, wait := range []time.Duration{0, 2 * time.Second, 10 * time.Second} {
		if wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
		sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = n.sender.Send(sendCtx, m)
		cancel()
		if err == nil {
			break
		}
		n.log.Warn("notify: send failed", "attempt", attempt+1, "err", err)
	}
	if err != nil {
		n.log.Error("notify: giving up", "title", m.Title, "err", err)
		return
	}
	n.log.Info("notified", "title", m.Title, "severity", m.Severity)
	if key != "" {
		if err := n.store.MarkNotified(ctx, key, n.now()); err != nil {
			n.log.Error("notify: mark sent", "err", err)
		}
	}
}
