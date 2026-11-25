package cli

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// age makes every memory written so far older by d seconds.
func (r run) age(seconds int) {
	r.t.Helper()
	db, err := sql.Open("sqlite", r.home+"/memory.db")
	if err != nil {
		r.t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("UPDATE memories SET created_at = created_at - ?1", seconds); err != nil {
		r.t.Fatal(err)
	}
}

// A database that cannot be used is not an empty one: the command exits with
// its own status, and says what failed, where, and that this is not "no
// memories" — for a search as for a write.
func TestAnUnusableDatabaseIsNotAnEmptyAnswer(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := run{t: t, home: blocker, cwd: t.TempDir()} // MEMORY_HOME is a file: no directory can be made
	for _, args := range [][]string{{"search", "发版"}, {"add", "发版只走 release 分支", "--kind", "rule"}, {"list"}} {
		code, out := r.memory("", args...)
		if code != ExitUnavailable || ExitUnavailable != 2 {
			t.Fatalf("%v: exit %d, want 2: %q", args, code, out)
		}
		for _, want := range []string{"memory unavailable", blocker, "not \"no memories\"", "MEMORY_HOME"} {
			if !strings.Contains(out, want) {
				t.Fatalf("%v lacks %q: %q", args, want, out)
			}
		}
		if strings.Contains(out, "No memories") && !strings.Contains(out, "not \"no memories\"") {
			t.Fatalf("%v answers as if nothing were found: %q", args, out)
		}
	}
	// An ordinary mistake is still 1.
	if code, _ := newRun(t).memory("", "show", "99"); code != 1 {
		t.Fatalf("a missing id exits %d, want 1", code)
	}
}

// In a directory that cannot be written, a search still finds what is there and
// says on stderr that it noted nothing; a write is unavailable.
func TestASearchStillWorksWhereTheDatabaseCannotBeWritten(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	r := newRun(t)
	r.add("发版只走 release 分支，因为 main 不触发签名流水线")
	for _, side := range []string{"-wal", "-shm"} {
		os.Remove(r.home + "/memory.db" + side)
	}
	os.Chmod(r.home+"/memory.db", 0o444)
	os.Chmod(r.home, 0o555)
	t.Cleanup(func() { os.Chmod(r.home, 0o755) })

	code, out := r.memory("", "search", "发版", "分支")
	if code != 0 || !strings.Contains(out, "发版只走 release 分支") || !strings.Contains(out, "opened read-only") {
		t.Fatalf("search: %d %q", code, out)
	}
	if code, out := r.memory("", "list"); code != 0 || !strings.Contains(out, "发版只走") {
		t.Fatalf("list: %d %q", code, out)
	}
	code, out = r.memory("", "add", "发版也要先打 tag", "--kind", "rule")
	if code != ExitUnavailable || !strings.Contains(out, r.home) {
		t.Fatalf("a write where nothing can be written: %d %q", code, out)
	}
	if code, _ := r.memory("", "delete", "1"); code != ExitUnavailable {
		t.Fatalf("delete exits %d, want %d", code, ExitUnavailable)
	}
}

func TestAnAddNamesNumbersAndStatusesButStoresThem(t *testing.T) {
	r := newRun(t)
	code, out := r.memory("", "add", "控件 28 高、12.5 中粗、右缘 14、同宽 150/170、内缩 12、说明行 32", "--kind", "practice")
	if code != 0 || !strings.HasPrefix(out, "Stored #1.\n") || !strings.Contains(out, "holds 7 numbers") || !strings.Contains(out, "memory edit 1 --content") {
		t.Fatalf("numbers: %d %q", code, out)
	}
	code, out = r.memory("", "add", "Swift 的 RightPanelDocxPageFormat 仍是下拉，待用户确认后再实现", "--kind", "fact")
	if code != 0 || !strings.Contains(out, "reads like progress") || !strings.Contains(out, "memory edit 2 --content") {
		t.Fatalf("status: %d %q", code, out)
	}
	// Stored all the same.
	if _, out := r.memory("", "search", "RightPanelDocxPageFormat"); !strings.Contains(out, "仍是下拉") {
		t.Fatalf("a hinted memory was not stored: %q", out)
	}
	// A lesson draws none; JSON carries them as data.
	if _, out := r.memory("", "add", "城镇化率一律用常住口径，因为 2018 年以前的户籍口径和之后不可比", "--kind", "decision"); out != "Stored #3.\n" {
		t.Fatalf("a lesson drew a note: %q", out)
	}
	_, out = r.memory("", "add", "弹窗 664、520、560、500、28、30、12 都要对齐", "--kind", "practice", "--json")
	if !strings.Contains(out, `"hints":[{"code":"manyNumbers"`) {
		t.Fatalf("json: %q", out)
	}
}

// Replacing a memory just written is said to be what an edit is for; replacing
// an old one is not remarked on.
func TestReplacingAMemoryJustWrittenPointsToEdit(t *testing.T) {
	r := newRun(t)
	r.add("VibeClub 重传后权限不会变化", "--kind", "fact")
	_, out := r.memory("", "add", "VibeClub 重传后权限可能被重置，要核对", "--supersedes", "1")
	if !strings.Contains(out, "#1 was written only 0 min ago") || !strings.Contains(out, "memory edit 1 --content") {
		t.Fatalf("recent: %q", out)
	}
	_, out = r.memory("", "add", "VibeClub 重传后权限可能被重置，要核对", "--supersedes", "2", "--json")
	if !strings.Contains(out, `"replacedRecently":{"id":2,"minutes":0}`) {
		t.Fatalf("json: %q", out)
	}

	r.age(2 * 3600)
	_, out = r.memory("", "add", "VibeClub 重传后每次都要核对 accessLevel", "--supersedes", "3")
	if out != "Stored #4.\n" {
		t.Fatalf("an old memory replaced drew a note: %q", out)
	}
}

func TestKeywordsAreAddedWithoutLosingTheOnesAMemoryHas(t *testing.T) {
	r := newRun(t)
	r.add("截图用 ScreenCaptureKit 抓帧", "--kind", "practice", "--keywords", "截图,screenshot")
	if _, out := r.memory("", "search", "截屏"); !strings.Contains(out, "No memories match") {
		t.Fatalf("setup: %q", out)
	}
	code, out := r.memory("", "edit", "1", "--add-keywords", "截屏,闪退")
	if code != 0 || !strings.HasPrefix(out, "Updated #1.\n") || strings.Contains(out, "past the limit") {
		t.Fatalf("add-keywords: %d %q", code, out)
	}
	if _, out := r.memory("", "show", "1"); !strings.Contains(out, "keywords: 截图, screenshot, 截屏, 闪退") {
		t.Fatalf("show: %q", out)
	}
	if _, out := r.memory("", "search", "截屏"); !strings.Contains(out, "ScreenCaptureKit") {
		t.Fatalf("not found by the added word: %q", out)
	}
	// The shell splits a flag's words; they are still keywords, and the alias works.
	r.memory("", "edit", "1", "--add-keyword", "崩溃", "EXC_BREAKPOINT")
	if _, out := r.memory("", "show", "1"); !strings.Contains(out, "截屏, 闪退, 崩溃, EXC_BREAKPOINT") {
		t.Fatalf("spilled words: %q", out)
	}
	// Past the limit is said, with the words.
	code, out = r.memory("", "edit", "1", "--add-keywords", "k1,k2,k3,k4,k5,k6")
	if code != 0 || !strings.Contains(out, "Keywords past the limit of 10 were not stored: k5, k6.") {
		t.Fatalf("limit: %d %q", code, out)
	}
	// Both ways at once is a misuse that says so; nothing to add is one too.
	if code, out := r.memory("", "edit", "1", "--keywords", "a1", "--add-keywords", "b1"); code != 1 || !strings.Contains(out, "not both") || !strings.Contains(out, "usage: memory edit") {
		t.Fatalf("both: %d %q", code, out)
	}
	if code, out := r.memory("", "edit", "1"); code != 1 || !strings.Contains(out, "--add-keywords") {
		t.Fatalf("nothing: %d %q", code, out)
	}
	// add takes no --add-keywords.
	if code, out := r.memory("", "add", "发版只走 release 分支", "--kind", "rule", "--add-keywords", "x"); code != 1 || !strings.Contains(out, "no --add-keywords flag") {
		t.Fatalf("add: %d %q", code, out)
	}
}

// Removing the newer memory brings the older back; the answer says which, so a
// later search is no surprise. Restoring puts it away again.
func TestRemovingAMemoryThatReplacedAnotherNamesTheOneBroughtBack(t *testing.T) {
	r := newRun(t)
	r.add("访谈稿外包给供应商转录", "--kind", "decision")
	r.add("访谈稿统一由课题组自己转录，不再外包", "--supersedes", "1")

	_, out := r.memory("", "archive", "2")
	if out != "Done.\n#1, which it replaced, is current again and searches return it. Archive or delete #1 too if it should go as well.\n" {
		t.Fatalf("archive: %q", out)
	}
	_, out = r.memory("", "restore", "2")
	if out != "Done.\n#1 is replaced again, so searches no longer return it.\n" {
		t.Fatalf("restore: %q", out)
	}
	_, out = r.memory("", "delete", "2")
	if !strings.HasPrefix(out, "Deleted.\n#1, which it replaced, is current again") {
		t.Fatalf("delete: %q", out)
	}
	// Taking away both at once leaves nothing to announce; an ordinary memory none.
	r.add("访谈稿统一由课题组自己转录，不再外包", "--supersedes", "1")
	if _, out := r.memory("", "delete", "1", "3"); out != "Deleted.\n" {
		t.Fatalf("both: %q", out)
	}
	r.add("发版只走 release 分支", "--kind", "rule")
	if _, out := r.memory("", "archive", "4"); out != "Done.\n" {
		t.Fatalf("ordinary: %q", out)
	}
}
