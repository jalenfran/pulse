// Package store is pulse's SQLite persistence: the event timeline and the
// API tokens.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS tokens (
	kind         TEXT    NOT NULL CHECK (kind IN ('user', 'host')),
	name         TEXT    NOT NULL,
	hash         TEXT    NOT NULL UNIQUE,
	created_at   INTEGER NOT NULL,
	last_used_at INTEGER,
	PRIMARY KEY (kind, name)
);

CREATE TABLE IF NOT EXISTS events (
	id       TEXT    PRIMARY KEY,         -- ULID, doubles as the pagination cursor
	ext_id   TEXT    UNIQUE,              -- set for events mirrored from elsewhere, so re-seeing them updates in place
	ts       INTEGER NOT NULL,            -- unix millis
	source   TEXT    NOT NULL,
	kind     TEXT    NOT NULL,
	severity INTEGER NOT NULL,            -- 0 info, 1 warning, 2 critical
	title    TEXT    NOT NULL,
	body     TEXT    NOT NULL DEFAULT '',
	key      TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS events_source_kind ON events (source, kind, id);
CREATE INDEX IF NOT EXISTS events_ts ON events (ts);
`

var severities = []string{"info", "warning", "critical"}

// SeverityRank maps a severity name to its stored rank.
func SeverityRank(s string) (int, bool) {
	for i, name := range severities {
		if name == s {
			return i, true
		}
	}
	return 0, false
}

type Event struct {
	ID       string    `json:"id"`
	TS       time.Time `json:"ts"`
	Source   string    `json:"source"`
	Kind     string    `json:"kind"`
	Severity string    `json:"severity"`
	Title    string    `json:"title"`
	Body     string    `json:"body,omitempty"`
	Key      string    `json:"key"`
}

type Token struct {
	Kind       string     `json:"kind"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: writes are tiny and rare, and it rules out SQLITE_BUSY
	// inside the server. The token CLI is a separate process and relies on
	// busy_timeout instead.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// ── Events ─────────────────────────────────────────────────────────────────

// AddEvent stores a new event and returns it with its ID filled in.
func (s *Store) AddEvent(ctx context.Context, e Event) (Event, error) {
	rank, ok := SeverityRank(e.Severity)
	if !ok {
		return e, fmt.Errorf("unknown severity %q", e.Severity)
	}
	e.TS = e.TS.UTC().Truncate(time.Millisecond)
	e.ID = newID(e.TS)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO events (id, ts, source, kind, severity, title, body, key) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.TS.UnixMilli(), e.Source, e.Kind, rank, e.Title, e.Body, e.Key)
	return e, err
}

// UpsertExternal stores an event mirrored from an outside source, identified
// by extID. Seeing it again with a newer timestamp refreshes it and moves it
// to the top of the timeline. Seeing it again unchanged (an informer resync,
// or a restart re-listing everything) does nothing.
func (s *Store) UpsertExternal(ctx context.Context, extID string, e Event) error {
	rank, ok := SeverityRank(e.Severity)
	if !ok {
		return fmt.Errorf("unknown severity %q", e.Severity)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO events (id, ext_id, ts, source, kind, severity, title, body, key)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (ext_id) DO UPDATE SET
			id = excluded.id, ts = excluded.ts, severity = excluded.severity,
			title = excluded.title, body = excluded.body
		WHERE excluded.ts > events.ts`,
		newID(e.TS), extID, e.TS.UnixMilli(), e.Source, e.Kind, rank, e.Title, e.Body, e.Key)
	return err
}

// newID makes a ULID whose time part is the event's own timestamp, not the
// time it was stored. IDs are the sort order and the cursor, so this keeps the
// timeline chronological when events arrive late or out of order (a restart
// re-listing every Kubernetes event at once, a host posting a backdated one).
func newID(ts time.Time) string {
	return ulid.MustNew(ulid.Timestamp(ts), ulid.DefaultEntropy()).String()
}

type EventQuery struct {
	Before      string // ULID cursor, exclusive
	MinSeverity int
	Source      string // exact, or a bare prefix such as "host" matching "host:*"
	Kind        string
	Limit       int
}

func (s *Store) ListEvents(ctx context.Context, q EventQuery) ([]Event, error) {
	var where []string
	var args []any
	if q.Before != "" {
		where = append(where, "id < ?")
		args = append(args, q.Before)
	}
	if q.MinSeverity > 0 {
		where = append(where, "severity >= ?")
		args = append(args, q.MinSeverity)
	}
	if q.Source != "" {
		if strings.Contains(q.Source, ":") {
			where = append(where, "source = ?")
			args = append(args, q.Source)
		} else {
			where = append(where, "(source = ? OR source LIKE ? ESCAPE '\\')")
			args = append(args, q.Source, escapeLike(q.Source)+":%")
		}
	}
	if q.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, q.Kind)
	}
	query := `SELECT id, ts, source, kind, severity, title, body, key FROM events`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY id DESC LIMIT ?"
	args = append(args, q.Limit)
	return s.queryEvents(ctx, query, args...)
}

// LatestHostEvents returns, for every host source, the newest event of each
// of the given kinds.
func (s *Store) LatestHostEvents(ctx context.Context, kinds []string) ([]Event, error) {
	if len(kinds) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(kinds)), ",")
	args := make([]any, len(kinds))
	for i, k := range kinds {
		args[i] = k
	}
	return s.queryEvents(ctx, `
		SELECT e.id, e.ts, e.source, e.kind, e.severity, e.title, e.body, e.key
		FROM events e
		JOIN (
			SELECT MAX(id) AS id FROM events
			WHERE source LIKE 'host:%' AND kind IN (`+placeholders+`)
			GROUP BY source, kind
		) latest ON latest.id = e.id`, args...)
}

func (s *Store) queryEvents(ctx context.Context, query string, args ...any) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		var e Event
		var ts int64
		var rank int
		if err := rows.Scan(&e.ID, &ts, &e.Source, &e.Kind, &rank, &e.Title, &e.Body, &e.Key); err != nil {
			return nil, err
		}
		e.TS = time.UnixMilli(ts).UTC()
		if rank >= 0 && rank < len(severities) {
			e.Severity = severities[rank]
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// Prune deletes events older than cutoff.
func (s *Store) Prune(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE ts < ?`, cutoff.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// ── Tokens ─────────────────────────────────────────────────────────────────

const tokenPrefix = "pls_"

var ErrTokenExists = errors.New("a token with that name already exists")

// CreateToken mints a token and returns the raw value. Only its SHA-256 is
// stored, so this is the one time it can be seen.
func (s *Store) CreateToken(ctx context.Context, kind, name string) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	raw := tokenPrefix + base64.RawURLEncoding.EncodeToString(buf)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tokens (kind, name, hash, created_at) VALUES (?, ?, ?, ?)`,
		kind, name, hashToken(raw), time.Now().UnixMilli())
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: tokens.kind, tokens.name") {
		return "", ErrTokenExists
	}
	return raw, err
}

func (s *Store) ListTokens(ctx context.Context) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, name, created_at, last_used_at FROM tokens ORDER BY kind, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tokens []Token
	for rows.Next() {
		var t Token
		var created int64
		var used sql.NullInt64
		if err := rows.Scan(&t.Kind, &t.Name, &created, &used); err != nil {
			return nil, err
		}
		t.CreatedAt = time.UnixMilli(created).UTC()
		if used.Valid {
			u := time.UnixMilli(used.Int64).UTC()
			t.LastUsedAt = &u
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

func (s *Store) RevokeToken(ctx context.Context, kind, name string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM tokens WHERE kind = ? AND name = ?`, kind, name)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Authenticate resolves a raw bearer token. It returns nil, nil for a token
// that doesn't exist.
func (s *Store) Authenticate(ctx context.Context, raw string) (*Token, error) {
	if !strings.HasPrefix(raw, tokenPrefix) {
		return nil, nil
	}
	h := hashToken(raw)
	var t Token
	var created int64
	var used sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT kind, name, created_at, last_used_at FROM tokens WHERE hash = ?`, h).
		Scan(&t.Kind, &t.Name, &created, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.CreatedAt = time.UnixMilli(created).UTC()
	now := time.Now()
	// Throttled so polling clients don't turn every read into a write.
	if !used.Valid || now.Sub(time.UnixMilli(used.Int64)) > time.Minute {
		_, _ = s.db.ExecContext(ctx, `UPDATE tokens SET last_used_at = ? WHERE hash = ?`, now.UnixMilli(), h)
	}
	return &t, nil
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
