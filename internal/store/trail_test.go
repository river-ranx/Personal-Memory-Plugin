package store

import (
	"errors"
	"path/filepath"
	"strconv"
	"testing"
)

func logged(t *testing.T, s *Store) (ids []string) {
	t.Helper()
	rows, err := s.db.Query(`SELECT ids FROM command_log ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, v)
	}
	return ids
}

// The ids noted while a command runs belong to its row alone: the next
// command starts with none.
func TestARowCarriesOnlyTheIdsOfItsOwnCommand(t *testing.T) {
	s := open(t)
	a := write(t, s, Draft{Content: "发版前先跑 make check", Kind: KindPractice})
	if err := s.LogCommand(CommandRun{Command: "add", OK: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.LogCommand(CommandRun{Command: "list", OK: true}); err != nil {
		t.Fatal(err)
	}
	got := logged(t, s)
	if len(got) != 2 || got[0] != "["+strconv.FormatInt(a.ID, 10)+"]" || got[1] != "[]" {
		t.Fatalf("rows %v", got)
	}
}

// A store open for reading only writes no log, and says nothing about it.
func TestAReadOnlyStoreLogsNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	write(t, w, Draft{Content: "发版前先跑 make check", Kind: KindPractice})
	w.Close()

	ro, err := openReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	ro.readOnly = errors.New("read-only")
	if err := ro.LogCommand(CommandRun{Command: "search", Query: "发版", OK: true}); err != nil {
		t.Fatal(err)
	}
	if got := logged(t, ro); len(got) != 0 {
		t.Fatalf("rows %v", got)
	}
}

// A database from before the log has the table added when it is opened.
func TestADatabaseFromBeforeTheLogGetsTheTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE command_log`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`PRAGMA user_version = 2`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.LogCommand(CommandRun{Command: "search", Query: "发版", OK: true}); err != nil {
		t.Fatal(err)
	}
	if got := logged(t, s); len(got) != 1 {
		t.Fatalf("rows %v", got)
	}
}
