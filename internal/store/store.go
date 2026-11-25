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
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store is an open memory database.
type Store struct {
	db  *sql.DB
	now func() uint64
	// readOnly is why the store could only be opened for reading, nil when it
	// is open for writing too. A read-only store answers searches and lists but
	// notes nothing: see [OpenForRead].
	readOnly error
}

// UnavailableError is a database that could not be used: not that nothing was
// found in it. An agent that reads it as an empty memory goes on as if nothing
// had ever been remembered, so it says what failed, where, and that this is
// not an empty answer.
type UnavailableError struct {
	Path string
	// Step is what was being done when it failed.
	Step string
	Err  error
}

func (e *UnavailableError) Error() string {
	return fmt.Sprintf("memory unavailable: could not %s (%s): %v. The memory could not be used — this is not \"no memories\", so do not carry on as if nothing were remembered, and tell the person. If the directory is not writable here (a sandbox, a read-only mount), point MEMORY_HOME at one that is",
		e.Step, e.Path, e.Err)
}

func (e *UnavailableError) Unwrap() error { return e.Err }

// Open opens the database at path for reading and writing, creating it and its
// tables if needed.
func Open(path string) (*Store, error) { return openWritable(path) }

// OpenForRead is [Open] for a command that only reads. Where the database
// cannot be opened for writing — its directory or file is read-only, as in a
// sandbox — it is opened read-only instead, and [Store.ReadOnly] says why. A
// read-only store notes no search hits and refuses nothing else: its writers
// are not called.
func OpenForRead(path string) (*Store, error) {
	s, err := openWritable(path)
	if err == nil {
		return s, nil
	}
	if ro, roErr := openReadOnly(path); roErr == nil {
		ro.readOnly = err
		return ro, nil
	}
	return nil, err
}

// ReadOnly is why the store is open for reading only; nil when it can write.
func (s *Store) ReadOnly() error { return s.readOnly }

func openWritable(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, &UnavailableError{Path: path, Step: "create the memory directory", Err: err}
	}
	// WAL lets one memory process read while another writes; IMMEDIATE takes the
	// write lock when a transaction begins, so two writers wait on the busy
	// timeout instead of failing on a lock upgrade.
	query := url.Values{
		"_pragma": {"busy_timeout(5000)", "journal_mode(WAL)", "synchronous(NORMAL)"},
		"_txlock": {"immediate"},
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String()+"?"+query.Encode())
	if err != nil {
		return nil, &UnavailableError{Path: path, Step: "open the memory database", Err: err}
	}
	db.SetMaxOpenConns(1)
	if err := prepare(db); err != nil {
		db.Close()
		return nil, &UnavailableError{Path: path, Step: "prepare the memory database", Err: err}
	}
	return &Store{db: db, now: now}, nil
}

// openReadOnly opens an existing database for reading. A WAL database needs
// its -shm file to be read; where that cannot be made (a read-only directory)
// the file is read as it stands, immutable, which is complete whenever no
// writer has it open — and one that has, has its -shm and -wal beside it, so
// the first attempt would have worked.
func openReadOnly(path string) (*Store, error) {
	var last error
	for _, mode := range []url.Values{{"mode": {"ro"}}, {"mode": {"ro"}, "immutable": {"1"}}} {
		mode.Set("_pragma", "busy_timeout(5000)")
		db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String()+"?"+mode.Encode())
		if err != nil {
			last = err
			continue
		}
		db.SetMaxOpenConns(1)
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM memories").Scan(&n); err != nil {
			db.Close()
			last = err
			continue
		}
		return &Store{db: db, now: now}, nil
	}
	return nil, last
}

func now() uint64 { return uint64(max(time.Now().Unix(), 0)) }

// schemaVersion is stored in the database's user_version once [schema] has
// been applied, so that opening a database that is up to date reads and writes
// nothing: it is what lets every command, searches included, start on a
// database whose file cannot be written. A change to [schema] raises it.
const schemaVersion = 1

// prepare brings a database to the current schema: creating what is missing
// and upgrading what is old, and only when it is not already.
func prepare(db *sql.DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version < schemaVersion {
		if _, err := db.Exec(schema); err != nil {
			return err
		}
	}
	if err := dropTitles(db); err != nil {
		return fmt.Errorf("could not upgrade the memory database: %w", err)
	}
	if version < schemaVersion {
		// user_version takes no bound parameter.
		_, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion))
		return err
	}
	return nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// schema is every table, created when the database is new; opening an
// existing one changes nothing (but see [dropTitles]).
const schema = `
-- A memory's id is a number, 1, 2, 3…, never given again once used.
-- supersedes names the memory this one replaced, set once, when it is
-- written; whether a memory is replaced follows from the state of the ones
-- that name it (see replaced in record.go), so nothing is kept on the old row.
CREATE TABLE IF NOT EXISTS memories (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
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
` + ftsSchema

// ftsSchema is the full-text index.
const ftsSchema = `
-- Columns hold text already cut into words (tokens), joined by spaces, so
-- unicode61 only splits on those spaces: that is how a two-character Chinese
-- word like 报销 becomes searchable, which FTS5's own tokenizers cannot do.
CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
	memory_id UNINDEXED,
	keywords,
	body,
	tokenize = 'unicode61 remove_diacritics 2'
);
`

// dropTitles upgrades a database from when a memory had a title. Agents left
// it empty, and where one was given it only repeated the text, which a result
// shows whole. A title's words become the first of its memory's keywords, so
// whatever was found by its title still is; then the column goes, and the
// full-text index is built again without it. Inside one transaction, which
// checks again for the column: two processes may open an old database at once.
func dropTitles(db *sql.DB) error {
	// Looked at first outside a transaction: Begin takes the write lock, and
	// a database with no title column has nothing to upgrade.
	var titled int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('memories') WHERE name = 'title'`).Scan(&titled); err != nil || titled == 0 {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('memories') WHERE name = 'title'`).Scan(&titled); err != nil || titled == 0 {
		return err
	}
	type row struct {
		id                       int64
		title, keywords, content string
	}
	var all []row
	rows, err := tx.Query(`SELECT id, COALESCE(title, ''), keywords, content FROM memories`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.title, &r.keywords, &r.content); err != nil {
			rows.Close()
			return err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, stmt := range []string{`ALTER TABLE memories DROP COLUMN title`, `DROP TABLE memories_fts`, ftsSchema} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	for _, r := range all {
		keywords := cleanKeywords([]string{r.title, r.keywords})
		if _, err := tx.Exec(`UPDATE memories SET keywords = ?1 WHERE id = ?2`, strings.Join(keywords, " "), r.id); err != nil {
			return err
		}
		if err := index(tx, r.id, keywords, r.content); err != nil {
			return err
		}
	}
	return tx.Commit()
}

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
