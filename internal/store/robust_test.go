package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A search that found its memories answers with them even when it cannot note
// that it returned them: the bookkeeping must not take the answer with it.
func TestASearchStillAnswersWhenItCannotNoteTheHits(t *testing.T) {
	s := open(t)
	write(t, s, Draft{Content: "发版只走 release 分支", Kind: KindRule})
	if _, err := s.db.Exec(`CREATE TRIGGER no_events BEFORE INSERT ON memory_events
	                          BEGIN SELECT RAISE(ABORT, 'database is locked'); END`); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Search("release 分支", nil, 5)
	if err != nil || len(hits) != 1 {
		t.Fatalf("a failed note must not fail the search: %v %v", hits, err)
	}
}

// readOnlyDir leaves a database holding one memory in a directory that cannot
// be written, as a sandbox does.
func readOnlyDir(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	write(t, s, Draft{Content: "发版只走 release 分支", Kind: KindRule})
	s.Close()
	for _, side := range []string{"-wal", "-shm"} { // a clean close leaves none, but be sure
		os.Remove(path + side)
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	return path
}

// Where the database cannot be written, a command that only reads still
// answers from it, and says it could not note anything; a command that writes
// is told the memory is unavailable, with the path and what failed.
func TestAReadOnlyDatabaseStillAnswersSearchesAndNothingElse(t *testing.T) {
	path := readOnlyDir(t)

	s, err := OpenForRead(path)
	if err != nil {
		t.Fatalf("a read-only database cannot be searched: %v", err)
	}
	defer s.Close()
	if s.ReadOnly() == nil {
		t.Fatal("a read-only store does not say it is")
	}
	hits, err := s.Search("release 分支", nil, 5)
	if err != nil || len(hits) != 1 {
		t.Fatalf("search: %v %v", hits, err)
	}
	if list, err := s.List(ListQuery{}); err != nil || len(list) != 1 {
		t.Fatalf("list: %v %v", list, err)
	}

	_, err = Open(path)
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("a store that cannot be written to is opened for writing: %v", err)
	}
	for _, want := range []string{path, "memory unavailable", "not \"no memories\"", "MEMORY_HOME"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the error lacks %q: %v", want, err)
		}
	}
}

// A database that is not there is not read-only: reading it is unavailable too,
// and says where it looked.
func TestAMissingDatabaseIsUnavailableNotEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-dir", "sub", "memory.db")
	if os.Geteuid() != 0 {
		blocked := t.TempDir()
		os.Chmod(blocked, 0o555)
		t.Cleanup(func() { os.Chmod(blocked, 0o755) })
		path = filepath.Join(blocked, "x", "memory.db")
	}
	_, err := OpenForRead(path)
	var unavailable *UnavailableError
	if os.Geteuid() != 0 && (!errors.As(err, &unavailable) || !strings.Contains(err.Error(), path)) {
		t.Fatalf("a database that cannot be had must be unavailable, naming its path: %v", err)
	}
}

// Opening a database that is up to date writes nothing: it takes no write lock
// to upgrade what has nothing to upgrade, which is what lets a search start
// where the file cannot be written.
func TestOpeningAnUpToDateDatabaseWritesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, "PRAGMA user_version"); n != schemaVersion {
		t.Fatalf("user_version %d after the schema was applied", n)
	}
	s.Close()
	before, _ := os.Stat(path)
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	after, _ := os.Stat(path)
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatal("opening an up-to-date database changed the file")
	}
}

func TestKeywordsAreAddedAfterTheOnesAMemoryHas(t *testing.T) {
	s := open(t)
	m := write(t, s, Draft{Content: "截图用 ScreenCaptureKit 抓帧", Kind: KindPractice, Keywords: []string{"截图", "screenshot"}})
	got, err := s.Update(m.ID, Edit{AddKeywords: []string{"截屏", "SCREENSHOT", "闪退"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Keywords, " ") != "截图 screenshot 截屏 闪退" {
		t.Fatalf("added once each, after the old ones: %v", got.Keywords)
	}
	// Found by the word that was added.
	if hits, _ := s.Search("截屏", nil, 5); len(hits) != 1 {
		t.Fatalf("not found by the added keyword: %v", hits)
	}
	// Past the limit, the old ones stay and the new ones are what is left out.
	many := []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "a9", "b1", "b2"}
	got, err = s.Update(m.ID, Edit{AddKeywords: many})
	if err != nil || len(got.Keywords) != KeywordLimit || got.Keywords[0] != "截图" {
		t.Fatalf("limit: %v %v", got.Keywords, err)
	}
	// Replacing wins when both are given.
	got, err = s.Update(m.ID, Edit{Keywords: []string{"x1"}, AddKeywords: []string{"y1"}})
	if err != nil || strings.Join(got.Keywords, " ") != "x1" {
		t.Fatalf("replace: %v %v", got.Keywords, err)
	}
}

func TestAMemoryKnowsWhatItReplacedAndWhetherThatIsCurrentAgain(t *testing.T) {
	s := open(t)
	old := write(t, s, Draft{Content: "访谈稿外包给供应商转录", Kind: KindDecision})
	next := write(t, s, Draft{Content: "访谈稿统一由课题组自己转录，不再外包", Supersedes: &old.ID})

	if id, ok, err := s.Replaces(next.ID); err != nil || !ok || id != old.ID {
		t.Fatalf("replaces: %d %v %v", id, ok, err)
	}
	if _, ok, _ := s.Replaces(old.ID); ok {
		t.Fatal("a memory that replaced nothing replaces nothing")
	}
	if cur, _ := s.IsCurrent(old.ID); cur {
		t.Fatal("a replaced memory is not current")
	}
	if err := s.SetArchived(next.ID, true); err != nil {
		t.Fatal(err)
	}
	if cur, _ := s.IsCurrent(old.ID); !cur {
		t.Fatal("archiving the newer one makes the older current again")
	}
	if cur, _ := s.IsCurrent(next.ID); cur {
		t.Fatal("an archived memory is not current")
	}
	if cur, err := s.IsCurrent(999); cur || err != nil {
		t.Fatalf("a memory that does not exist: %v %v", cur, err)
	}
}
