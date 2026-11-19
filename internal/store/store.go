// Package store is the local memory: one SQLite file on this machine, read
// and written by the `memory` command line. The hooks never open it.
//
// Writing takes a sentence; the store keeps out only text too short to say
// anything and repeats of what it holds. Recall is full-text search ranked
// by match, project, use and confirmation.
package store

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Store is an open memory database.
type Store struct {
	db  *sql.DB
	now func() uint64
}

// Open opens the database at path, creating it and its tables if needed.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// WAL lets one memory process read while another writes; IMMEDIATE takes the
	// write lock when a transaction begins, so two writers wait on the busy
	// timeout instead of failing on a lock upgrade.
	query := url.Values{
		"_pragma": {"busy_timeout(5000)", "journal_mode(WAL)", "synchronous(NORMAL)"},
		"_txlock": {"immediate"},
	}
	dsn := (&url.URL{Scheme: "file", Path: path}).String() + "?" + query.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("could not prepare the memory database: %w", err)
	}
	return &Store{db: db, now: func() uint64 { return uint64(max(time.Now().Unix(), 0)) }}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// schema is every table, created when the database is new; opening an
// existing one changes nothing.
const schema = `
-- A memory's id is a number, 1, 2, 3…, never given again once used.
-- supersedes names the memory this one replaced, set once, when it is
-- written; whether a memory is replaced follows from the state of the ones
-- that name it (see replaced in record.go), so nothing is kept on the old row.
CREATE TABLE IF NOT EXISTS memories (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	title       TEXT,
	content     TEXT NOT NULL,
	kind        TEXT NOT NULL,
	keywords    TEXT NOT NULL DEFAULT '',
	confidence  TEXT NOT NULL DEFAULT 'observed',
	project     TEXT,
	supersedes  INTEGER,
	state       TEXT NOT NULL DEFAULT 'active',
	created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS memories_by_state ON memories (state, created_at);
CREATE INDEX IF NOT EXISTS memories_by_supersedes ON memories (supersedes);

-- How each memory was used: returned to a search, relied on, verified,
-- contradicted. Counts, the last use and decay are read from these.
CREATE TABLE IF NOT EXISTS memory_events (
	id        INTEGER PRIMARY KEY,
	memory_id INTEGER NOT NULL,
	at        INTEGER NOT NULL,
	kind      TEXT NOT NULL,
	note      TEXT
);
CREATE INDEX IF NOT EXISTS memory_events_by_memory ON memory_events (memory_id, at);

-- Columns hold text already cut into words (tokens), joined by spaces, so
-- unicode61 only splits on those spaces: that is how a two-character Chinese
-- word like 报销 becomes searchable, which FTS5's own tokenizers cannot do.
CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
	memory_id UNINDEXED,
	title,
	keywords,
	body,
	tokenize = 'unicode61 remove_diacritics 2'
);
`

func (s *Store) inTx(body func(*sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := body(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// queryer is what reads take, so they run on the database or inside a
// transaction alike.
type queryer interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

func clampI64(v uint64) int64 {
	if v > 1<<63-1 {
		return 1<<63 - 1
	}
	return int64(v)
}

func nonNegative(v int64) uint64 { return uint64(max(v, 0)) }

func optionalU64(v sql.NullInt64) *uint64 {
	if !v.Valid {
		return nil
	}
	n := nonNegative(v.Int64)
	return &n
}

func nullString(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
