package cli

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

type run struct {
	t    *testing.T
	home string
	cwd  string
}

func newRun(t *testing.T) run { return run{t: t, home: t.TempDir(), cwd: t.TempDir()} }

// memory runs one command and returns its exit code and what it printed.
func (r run) memory(stdin string, args ...string) (int, string) {
	r.t.Helper()
	var out, errw bytes.Buffer
	code := Main(args, Env{
		Getenv: func(k string) string {
			if k == "MEMORY_HOME" {
				return r.home
			}
			return ""
		},
		Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errw, Cwd: r.cwd,
	})
	return code, out.String() + errw.String()
}

// backdate makes what was reported so far days older, which is as good as
// waiting: a memory is reported once a day.
func (r run) backdate(days int) {
	r.t.Helper()
	db, err := sql.Open("sqlite", r.home+"/memory.db")
	if err != nil {
		r.t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("UPDATE memory_events SET at = at - ?1", days*86400); err != nil {
		r.t.Fatal(err)
	}
}

var stored = regexp.MustCompile(`^Stored #(\d+)\.\n$`)

// add writes a memory, a fact unless the arguments name a kind or a memory
// it supersedes; the answer is its number.
func (r run) add(args ...string) string {
	r.t.Helper()
	if !slices.Contains(args, "--kind") && !slices.Contains(args, "--supersedes") {
		args = append(args, "--kind", "fact")
	}
	code, out := r.memory("", append([]string{"add"}, args...)...)
	m := stored.FindStringSubmatch(out)
	if code != 0 || m == nil {
		r.t.Fatalf("add %q: %d %q", args, code, out)
	}
	return m[1]
}

func TestAMemoryIsWrittenWithJustItsWordsAndFoundByItsNumber(t *testing.T) {
	r := newRun(t)
	id := r.add("提交信息用中文，前缀照 git log 的写法")
	if id != "1" || r.add("发版只走 release 分支") != "2" {
		t.Fatalf("ids count up from 1: %s", id)
	}

	// A result is a header, then the text as written. The header says how to
	// treat the text, so it comes first: the id, the kind, how old.
	code, out := r.memory("", "search", "提交信息")
	want := regexp.MustCompile(`^#1 · fact · written today \(\d{4}-\d{2}-\d{2}\)\n提交信息用中文，前缀照 git log 的写法\n$`)
	if code != 0 || !want.MatchString(out) {
		t.Fatalf("search: %d %q", code, out)
	}
	if strings.Contains(out, "observed") {
		t.Fatalf("an ordinary confidence says nothing: %q", out)
	}
	for _, text := range []string{"1", "#1", " #1 "} {
		if code, out := r.memory("", "show", text); code != 0 || !strings.Contains(out, "提交信息用中文") {
			t.Fatalf("show %q: %d %q", text, code, out)
		}
	}
	if code, out := r.memory("", "show", "080c7163"); code != 1 || !strings.Contains(out, "is not an id") || !strings.Contains(out, "usage: memory show") {
		t.Fatalf("show by a UUID's digits: %d %q", code, out)
	}
	if code, out := r.memory("", "show", "99"); code != 1 || !strings.Contains(out, "no such memory") {
		t.Fatalf("show of an unknown id: %d %q", code, out)
	}
	// The id in JSON is the number.
	_, out = r.memory("", "search", "提交信息", "--json")
	var got struct {
		Memories []struct {
			ID            int64
			Kind, Written string
		} `json:"memories"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got.Memories) != 1 || got.Memories[0].ID != 1 || got.Memories[0].Kind != "fact" {
		t.Fatalf("json: %q %v", out, err)
	}
}

// The header names what is out of the ordinary and nothing else.
func TestTheHeaderNamesOnlyWhatIsOutOfTheOrdinary(t *testing.T) {
	r := newRun(t)
	r.add("提交信息用中文，前缀照 git log 的写法", "--kind", "rule")
	r.add("签名由 CI 完成", "--project", "/work/other")
	// Three verifications, on three days, make a memory verified.
	for range 3 {
		if _, out := r.memory("", "confirm", "1", "--outcome", "verified"); out != "Recorded.\n" {
			t.Fatal(out)
		}
		r.backdate(2)
	}
	r.memory("", "confirm", "2", "--outcome", "contradicted", "--note", "改了")
	_, out := r.memory("", "search", "提交 签名")
	for _, want := range []string{"#1 · rule · written today", " · verified\n", "#2 · fact · written today", " · contradicted 1x · /work/other\n"} {
		if !strings.Contains(out, want) {
			t.Fatalf("lacks %q:\n%s", want, out)
		}
	}
	// What is this directory's carries no path; only another's does.
	if strings.Count(out, r.cwd) != 0 {
		t.Fatalf("the asking directory is named: %q", out)
	}
	if blocks := strings.Split(strings.TrimSpace(out), "\n\n"); len(blocks) != 2 {
		t.Fatalf("results are separated by a blank line: %q", out)
	}
}

// A flag that does nothing must not be taken for one that worked: a mistyped
// flag is refused, with what the command takes and how to use it.
func TestAMistypedFlagIsRefusedNotIgnored(t *testing.T) {
	r := newRun(t)
	code, out := r.memory("", "add", "发版只走 release 分支", "--kinds", "rule")
	if code != 1 || !strings.Contains(out, "no --kinds flag") || !strings.Contains(out, "--kind,") || !strings.Contains(out, "usage: memory add") {
		t.Fatalf("%d %q", code, out)
	}
	if code, out := r.memory("", "search", "发版", "--limt", "3"); code != 1 || !strings.Contains(out, "no --limt flag") || !strings.Contains(out, "--limit") {
		t.Fatalf("%d %q", code, out)
	}
	if code, out := r.memory("", "show", "1", "--kind", "rule"); code != 1 || !strings.Contains(out, "no --kind flag") {
		t.Fatalf("%d %q", code, out)
	}
	if code, out := r.memory("", "add", "x", "--kind"); code != 1 || !strings.Contains(out, "--kind needs a value") || !strings.Contains(out, "usage: memory add") {
		t.Fatalf("%d %q", code, out)
	}
	if _, err := os.Stat(r.home + "/memory.db"); err == nil {
		t.Fatal("a refused command opened the database")
	}
}

// A command used wrongly answers with how to use it.
func TestMisuseIsAnsweredWithTheUsage(t *testing.T) {
	r := newRun(t)
	for args, usage := range map[string]string{
		"add":     "usage: memory add",
		"search":  "usage: memory search",
		"confirm": "usage: memory confirm",
		"show":    "usage: memory show",
		"delete":  "usage: memory delete",
	} {
		if code, out := r.memory("", args); code != 1 || !strings.Contains(out, usage) {
			t.Fatalf("%s: %d %q", args, code, out)
		}
	}
	if code, out := r.memory("", "serach", "x"); code != 1 || !strings.Contains(out, "unknown command") || !strings.Contains(out, "search, add") {
		t.Fatalf("an unknown command: %d %q", code, out)
	}
}

// Keywords are written as an agent likes: one flag with a list, or several.
func TestKeywordsAreAListOrSeveralFlags(t *testing.T) {
	r := newRun(t)
	r.add("发版只走 release 分支", "--keywords", "签名,notarize，上线")
	r.add("staging 的数据库只读", "--keyword", "mysql", "--keyword", "只读库", "--keywords", "replica")
	for query, want := range map[string]string{"notarize": "#1", "上线": "#1", "签名": "#1", "mysql": "#2", "replica": "#2", "只读库": "#2"} {
		if _, out := r.memory("", "search", query); !strings.HasPrefix(out, want+" ") {
			t.Fatalf("%q: %q", query, out)
		}
	}
	_, out := r.memory("", "show", "1")
	if !strings.Contains(out, "keywords: 签名, notarize, 上线 ·") {
		t.Fatalf("%q", out)
	}
}

func TestConfirmingWithoutAnOutcomeMeansItWasUsed(t *testing.T) {
	r := newRun(t)
	r.add("签名已改由 CI 完成")
	if code, out := r.memory("", "confirm", "1"); code != 0 || out != "Recorded.\n" {
		t.Fatalf("%d %q", code, out)
	}
	if _, out := r.memory("", "show", "1"); !strings.Contains(out, "used 1,") && !strings.Contains(out, "used 1 ") {
		t.Fatalf("%q", out)
	}
	if code, out := r.memory("", "confirm", "1", "--outcome", "maybe"); code != 1 || !strings.Contains(out, "outcome must be") {
		t.Fatalf("%d %q", code, out)
	}
}

func TestSupersedingAndConfirmingTakeIDs(t *testing.T) {
	r := newRun(t)
	old := r.add("签名由开发者本机完成")
	fresh := r.add("签名已改由 CI 完成", "--supersedes", old)
	_, out := r.memory("", "search", "签名")
	if !strings.Contains(out, "#"+fresh+" ·") || strings.Contains(out, "#"+old+" ·") {
		t.Fatalf("only the newer one is returned: %q", out)
	}
	// A list shows what is current, as a search does; --all adds what was replaced.
	if _, out := r.memory("", "list"); strings.Contains(out, "#"+old+" ·") {
		t.Fatalf("a list shows a replaced memory: %q", out)
	}
	if _, out := r.memory("", "list", "--all"); !strings.Contains(out, "superseded by #"+fresh) {
		t.Fatalf("list --all shows what was replaced: %q", out)
	}
	if code, out := r.memory("", "confirm", fresh, "--outcome", "verified"); code != 0 || out != "Recorded.\n" {
		t.Fatalf("confirm: %d %q", code, out)
	}
	// An id nobody has is reported, not an error: the rest are still recorded.
	if code, out := r.memory("", "confirm", fresh, "999", "--outcome", "used"); code != 0 || !strings.Contains(out, "No memory has these ids: #999.") {
		t.Fatalf("confirm of a missing id: %d %q", code, out)
	}
	// Archive the newer one: it is never returned, and what it replaced is
	// current again.
	if code, _ := r.memory("", "archive", fresh); code != 0 {
		t.Fatal("archive")
	}
	if _, out := r.memory("", "search", "签名"); !strings.Contains(out, "#"+old+" ·") || strings.Contains(out, "#"+fresh+" ·") {
		t.Fatalf("after archiving the newer one: %q", out)
	}
	if code, _ := r.memory("", "restore", fresh); code != 0 {
		t.Fatal("restore")
	}
	if _, out := r.memory("", "search", "签名"); !strings.Contains(out, "#"+fresh+" ·") || strings.Contains(out, "#"+old+" ·") {
		t.Fatalf("after restoring it: %q", out)
	}
	alone := r.add("staging 的数据库账号只读，写入要走变更工单")
	r.memory("", "archive", alone)
	if _, out := r.memory("", "search", "staging 数据库"); !strings.Contains(out, "No memories match") {
		t.Fatalf("archived memory returned: %q", out)
	}
}

// Nothing found says what to try next.
func TestNothingFoundSaysWhatToTryNext(t *testing.T) {
	r := newRun(t)
	code, out := r.memory("", "search", "不存在的东西")
	if code != 0 || !strings.HasPrefix(out, "No memories match.") || !strings.Contains(out, "synonym") {
		t.Fatalf("%d %q", code, out)
	}
}

func TestWhatIsNotWorthKeepingIsNotStoredAndTheAnswerSaysWhy(t *testing.T) {
	r := newRun(t)
	if code, out := r.memory("", "add", "ok", "--kind", "fact"); code != 1 || !strings.HasPrefix(out, "Not stored: refused") || !strings.Contains(out, "too short to be a memory") {
		t.Fatalf("thin: %d %q", code, out)
	}
	if code, out := r.memory("", "add", "报销", "--kind", "rule", "--keywords", "OA,发票,财务"); code != 1 || !strings.Contains(out, "too short to be a memory") {
		t.Fatalf("a label with keywords: %d %q", code, out)
	}
	if code, out := r.memory("", "add", strings.Repeat("字", 1200), "--kind", "fact"); code != 1 || !strings.Contains(out, "at most 1000") || !strings.Contains(out, "split it") {
		t.Fatalf("too long: %d %q", code, out)
	}
	// Private-looking text is no concern of the gate: this is the owner's machine.
	r.add("值班手机 13812345678，测试库口令放在 1Password")
	first := r.add("升 daemon 协议号前先在 reserved-numbers.md 占号")
	// A repeat shows what it repeats and the exact command to replace it.
	code, out := r.memory("", "add", "改协议号之前，先在 reserved-numbers.md 里占号", "--kind", "fact")
	if code != 1 || !strings.Contains(out, "already remembered") || !strings.Contains(out, "#"+first+" ·") || !strings.Contains(out, "--supersedes #"+first+";") {
		t.Fatalf("a repeat: %d %q", code, out)
	}
}

// Content on stdin, for text with quotes or a lot of it.
func TestContentCanComeFromStdin(t *testing.T) {
	r := newRun(t)
	code, out := r.memory(`部署前执行 "make check"`+"\n", "add", "-", "--kind", "practice", "--keywords", "release")
	if code != 0 || !stored.MatchString(out) {
		t.Fatalf("%d %q", code, out)
	}
	if _, out := r.memory("", "search", "release"); !strings.Contains(out, `"make check"`) {
		t.Fatalf("%q", out)
	}
	if code, out := r.memory("  \n", "add", "-"); code != 1 || !strings.Contains(out, "usage: memory add") {
		t.Fatalf("empty stdin: %d %q", code, out)
	}
}

// A project is the directory the agent worked in; a memory from another one
// says so, and one from the asking directory need not.
func TestOnlyAnotherProjectsMemoryNamesItsProject(t *testing.T) {
	r := newRun(t)
	r.add("报销需附发票原件", "--project", "/work/alpha")
	r.add("报销需附行程截图", "--project", "/work/beta")
	_, out := r.memory("", "search", "报销", "--project", "/work/beta")
	if strings.Count(out, "/work/alpha") != 1 || strings.Contains(out, "/work/beta") {
		t.Fatalf("%q", out)
	}
	// A subdirectory is the same project: not named, and listed with it.
	r.add("报销单据按月装订，月底前交财务", "--project", "/work/beta/2026")
	_, out = r.memory("", "search", "报销", "--project", "/work/beta")
	if strings.Contains(out, "/work/beta/2026") {
		t.Fatalf("a subdirectory's memory named as another project's: %q", out)
	}
	_, out = r.memory("", "list", "--project", "/work/beta")
	if !strings.Contains(out, "月底前交财务") || !strings.Contains(out, "行程截图") || strings.Contains(out, "发票原件") {
		t.Fatalf("list --project: %q", out)
	}
}

// With no --project the project is the directory the command runs in.
func TestTheProjectIsTheWorkingDirectory(t *testing.T) {
	r := newRun(t)
	r.add("发版只走 release 分支")
	_, out := r.memory("", "search", "release", "--json")
	var got struct {
		Memories []struct{ Project string } `json:"memories"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got.Memories) != 1 || got.Memories[0].Project != r.cwd {
		t.Fatalf("project %v, want %q: %v", got, r.cwd, err)
	}
	// From another directory the same memory is another project's.
	other := run{t: t, home: r.home, cwd: t.TempDir()}
	if _, out := other.memory("", "search", "release"); !strings.Contains(out, r.cwd) {
		t.Fatalf("not named as another project's: %q", out)
	}
	if _, out := r.memory("", "search", "release"); strings.Contains(out, r.cwd) {
		t.Fatalf("named as another project's from its own directory: %q", out)
	}
}

func TestAnAgeReadsInTheUnitItIsMeasuredIn(t *testing.T) {
	now := timeAt(1_800_000_000)
	for age, want := range map[int]string{0: "today", 1: "1d ago", 59: "59d ago", 60: "2mo ago", 400: "13mo ago", 730: "2y ago", 1100: "3y ago"} {
		if got := ago(uint64(1_800_000_000-age*86400), now); got != want {
			t.Fatalf("%d days: %q, want %q", age, got, want)
		}
	}
}

func timeAt(unix int64) time.Time { return time.Unix(unix, 0) }

// Reporting a memory again the same day is said not to have counted.
func TestAReportRepeatedTheSameDayIsSaidToCountOnce(t *testing.T) {
	r := newRun(t)
	r.add("签名已改由 CI 完成")
	r.add("发版只走 release 分支")
	if _, out := r.memory("", "confirm", "1"); out != "Recorded.\n" {
		t.Fatal(out)
	}
	_, out := r.memory("", "confirm", "1", "2", "999")
	want := "Recorded. Already reported today, counted once: #1. No memory has these ids: #999.\n"
	if out != want {
		t.Fatalf("%q, want %q", out, want)
	}
	if _, out := r.memory("", "confirm", "1"); out != "Already reported today, counted once: #1.\n" {
		t.Fatalf("only a repeat: %q", out)
	}
	_, out = r.memory("", "confirm", "1", "999", "--json")
	var got struct {
		Missing, Repeated []int64
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got.Repeated) != 1 || got.Repeated[0] != 1 || len(got.Missing) != 1 {
		t.Fatalf("json: %q %v", out, err)
	}
	if _, out := r.memory("", "show", "1"); !strings.Contains(out, "used 1,") {
		t.Fatalf("a day's reports count once: %q", out)
	}
}

// A new memory names its kind: left to a default, agents left it, and the
// person's preferences were kept as facts that go stale. The answer lists the
// kinds to choose from. A memory that supersedes another keeps that one's.
func TestANewMemoryNamesItsKind(t *testing.T) {
	r := newRun(t)
	code, out := r.memory("", "add", "提交信息用中文，前缀照 git log 的写法")
	if code != 1 || !strings.HasPrefix(out, "Not stored: invalid memory: say which kind") || !strings.Contains(out, kindGuide) {
		t.Fatalf("no kind: %d %q", code, out)
	}
	if code, out := r.memory("", "add", "提交信息用中文，前缀照 git log 的写法", "--json"); code != 1 || !strings.Contains(out, `"error":"invalid"`) {
		t.Fatalf("no kind, as JSON: %d %q", code, out)
	}
	old := r.add("提交信息用中文，前缀照 git log 的写法", "--kind", "RULE")
	r.add("提交信息用英文，前缀照 git log 的写法", "--supersedes", old)
	if _, out := r.memory("", "search", "提交信息"); !strings.HasPrefix(out, "#2 · rule · ") {
		t.Fatalf("the kind carried over: %q", out)
	}
}

// Keywords that only repeat the text are kept, and the answer names them, so
// the next write gives words the text lacks.
func TestKeywordsAlreadyInTheTextAreNamed(t *testing.T) {
	r := newRun(t)
	code, out := r.memory("", "add", "发版只走 release 分支，因为 main 不触发签名流水线", "--kind", "rule", "--keywords", "发版,签名,codesign")
	if code != 0 || !strings.HasPrefix(out, "Stored #1.\nKeywords already in the text add nothing to finding it: 发版, 签名.") {
		t.Fatalf("%d %q", code, out)
	}
	if _, out := r.memory("", "search", "codesign"); !strings.HasPrefix(out, "#1 · ") {
		t.Fatalf("found by the keyword the text lacks: %q", out)
	}
	_, out = r.memory("", "add", "staging 的数据库账号只读，写入要走变更工单", "--kind", "rule", "--keywords", "数据库,mysql", "--json")
	if !strings.Contains(out, `"keywordsInText":["数据库"]`) {
		t.Fatalf("json: %q", out)
	}
	if r.add("签名由 CI 完成，本机不再有签名证书", "--keywords", "codesign") != "3" {
		t.Fatal("keywords the text lacks get no remark")
	}
}

// A write prunes old hits: it is rare, so it is where the housekeeping rides.
func TestAWritePrunesOldHits(t *testing.T) {
	r := newRun(t)
	r.add("发版前先跑 make check")
	r.memory("", "search", "发版")
	r.backdate(200)
	r.memory("", "search", "发版") // a hit today, and one 200 days old
	count := func() int {
		db, _ := sql.Open("sqlite", r.home+"/memory.db")
		defer db.Close()
		var n int
		db.QueryRow("SELECT COUNT(*) FROM memory_events WHERE kind = 'hit'").Scan(&n)
		return n
	}
	if count() != 2 {
		t.Fatalf("%d hits before", count())
	}
	r.add("staging 的数据库账号只读，写入要走变更工单")
	if count() != 1 {
		t.Fatalf("%d hits after a write", count())
	}
}

// The hook is the one thing the agents' configs call: it answers a session
// start with the prompt, and touches no database.
func TestTheHookAnswersASessionStartAndOpensNoDatabase(t *testing.T) {
	r := newRun(t)
	code, out := r.memory(`{"session_id":"s"}`, "hook", "claude", "SessionStart")
	if code != 0 || !strings.Contains(out, "additionalContext") || !strings.Contains(out, "memory search") {
		t.Fatalf("%d %q", code, out)
	}
	if code, out := r.memory("{}", "hook", "claude", "PostToolUse"); code != 0 || strings.Contains(out, "additionalContext") {
		t.Fatalf("a message: %d %q", code, out)
	}
	if _, err := os.Stat(r.home + "/memory.db"); err == nil {
		t.Fatal("the hook opened the database")
	}
}

// A fact written a quarter ago says it may be out of date; a recent one, and a
// rule or decision of any age, do not: a fact is how things were, the others
// are what to do.
func TestAnOldFactSaysItMayBeOutOfDate(t *testing.T) {
	r := newRun(t)
	r.add("签名已改由 CI 完成，本机不再有签名证书")
	r.add("发版只走 release 分支，因为 main 不触发签名流水线", "--kind", "rule")
	r.add("staging 的数据库账号只读，写入要走变更工单")
	db, err := sql.Open("sqlite", r.home+"/memory.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	old := time.Now().AddDate(0, 0, -100).Unix()
	if _, err := db.Exec("UPDATE memories SET created_at = ?1 WHERE id IN (1, 2)", old); err != nil {
		t.Fatal(err)
	}
	_, out := r.memory("", "list")
	headers := map[string]string{}
	for _, block := range strings.Split(strings.TrimSpace(out), "\n\n") {
		header := strings.SplitN(block, "\n", 2)[0]
		headers[strings.SplitN(header, " ", 2)[0]] = header
	}
	if !strings.Contains(headers["#1"], " · may be out of date") {
		t.Errorf("an old fact: %q", headers["#1"])
	}
	if strings.Contains(headers["#2"], "out of date") || strings.Contains(headers["#3"], "out of date") {
		t.Errorf("an old rule or a new fact: %q %q", headers["#2"], headers["#3"])
	}
}

// A memory written with the wrong kind is corrected where it stands: the same
// id (so what cites #1 still does), the same project — even from another
// directory — and no new memory to supersede it.
func TestAMemoryIsCorrectedInPlaceNotReplaced(t *testing.T) {
	r := newRun(t)
	r.add("城镇化率用常住口径，因为户籍口径不可比", "--project", "/work/stats")
	if code, out := r.memory("", "edit", "1", "--kind", "decision"); code != 0 || !strings.HasPrefix(out, "Updated #1.\n") || !strings.Contains(out, "#1 · decision ·") {
		t.Fatalf("%d %q", code, out)
	}
	if _, out := r.memory("", "list"); strings.Contains(out, "#2 ·") {
		t.Fatalf("editing made a new memory: %q", out)
	}
	if _, out := r.memory("", "show", "1"); !strings.Contains(out, "/work/stats") {
		t.Fatalf("the project moved: %q", out)
	}
	// Nothing to change is a misuse answered with the usage.
	if code, out := r.memory("", "edit", "1"); code != 1 || !strings.Contains(out, "usage: memory edit") {
		t.Fatalf("%d %q", code, out)
	}
	if code, out := r.memory("", "edit", "1", "--kind", "prefer"); code != 1 || !strings.Contains(out, "must be one of") {
		t.Fatalf("a mistyped kind: %d %q", code, out)
	}
	if code, out := r.memory("", "edit", "99", "--kind", "rule"); code != 1 || !strings.Contains(out, "no such memory") {
		t.Fatalf("an id nobody has: %d %q", code, out)
	}
	if code, out := r.memory("", "edit", "1", "--keywords", "-"); code != 0 {
		t.Fatalf("clearing keywords: %d %q", code, out)
	}
	_, out := r.memory("", "show", "1", "--json")
	if !strings.Contains(out, `"keywords":[]`) {
		t.Fatalf("keywords not cleared: %q", out)
	}
}

// A rewrite keeps the memory's project, and says so: an agent rewriting from
// another directory would otherwise never learn the memory had a home.
func TestARewriteKeepsTheProjectAndSaysSo(t *testing.T) {
	r := newRun(t)
	r.add("城镇化率用常住口径，因为户籍口径不可比", "--project", "/work/stats")
	_, out := r.memory("", "add", "城镇化率一律用常住口径，因为 2018 年前后的户籍口径不可比", "--supersedes", "1")
	if !strings.Contains(out, "keeps its project: /work/stats") {
		t.Fatalf("%q", out)
	}
	if _, out := r.memory("", "show", "2"); !strings.Contains(out, "/work/stats") {
		t.Fatalf("the project was not inherited: %q", out)
	}
}

// --json carries what the text answer carries: keywords and counts, always a
// number, and when it was last used.
func TestTheJSONCarriesTheKeywordsAndTheCounts(t *testing.T) {
	r := newRun(t)
	r.add("发版只走 release 分支", "--kind", "rule", "--keywords", "上线")
	_, out := r.memory("", "show", "1", "--json")
	var got struct {
		Memory struct {
			Keywords             []string
			Uses, Verified       *int
			Contradicted         *int
			LastUsed             string
			Current, Superseding bool
		} `json:"memory"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%q: %v", out, err)
	}
	m := got.Memory
	if len(m.Keywords) != 1 || m.Keywords[0] != "上线" {
		t.Fatalf("keywords: %q", out)
	}
	if m.Uses == nil || m.Verified == nil || m.Contradicted == nil || *m.Uses != 0 {
		t.Fatalf("counts are missing or zero-less: %q", out)
	}
	if !m.Current || m.Superseding {
		t.Fatalf("current/superseding: %q", out)
	}
	r.memory("", "confirm", "1")
	_, out = r.memory("", "show", "1", "--json")
	if !strings.Contains(out, `"uses":1`) || !strings.Contains(out, `"lastUsed":"20`) {
		t.Fatalf("a use is not in the JSON: %q", out)
	}
}

// A limit that drops results says so; a typo in --kind is an error, not an
// empty answer; a single character says why it cannot match.
func TestASearchSaysWhenItShowsFewerThanItFoundAndWhy(t *testing.T) {
	r := newRun(t)
	for _, text := range []string{
		"报销单据要在每月五号前交给财务",
		"差旅报销需附上行程单和发票原件",
		"招待费报销先由部门负责人签批",
		"报销金额超过五千要附审批邮件",
		"电子发票报销直接在 OA 里上传",
		"报销被退回时先在系统里看清原因",
		"跨月报销要有情况说明",
	} {
		r.add(text)
	}
	code, out := r.memory("", "search", "报销", "--limit", "100")
	if code != 0 || strings.Contains(out, "matched; showing") {
		t.Fatalf("seven match and all are shown, so there is nothing to say: %d %q", code, out)
	}
	_, out = r.memory("", "search", "报销", "--limit", "3")
	if !strings.Contains(out, "7 matched; showing 3") {
		t.Fatalf("a limit does not say what it left out: %q", out)
	}
	if code, out := r.memory("", "list", "--kind", "prefer"); code != 1 || !strings.Contains(out, "must be one of") {
		t.Fatalf("a mistyped kind: %d %q", code, out)
	}
	if code, out := r.memory("", "search", "图"); code != 0 || !strings.Contains(out, "single character") {
		t.Fatalf("%d %q", code, out)
	}
}

// Keywords past the limit are named in the answer: silently losing a way to
// find a memory is worse than refusing the write.
func TestKeywordsPastTheLimitAreNamed(t *testing.T) {
	r := newRun(t)
	code, out := r.memory("", "add", "发版只走 release 分支，因为 main 不触发签名流水线", "--kind", "rule",
		"--keywords", "a1,b2,c3,d4,e5,f6,g7,h8,i9,j10,k11,l12")
	if code != 0 || !strings.Contains(out, "past the limit of 10 were not stored: k11, l12") {
		t.Fatalf("%d %q", code, out)
	}
	_, out = r.memory("", "add", "staging 的数据库账号只读，写入要走变更工单", "--kind", "rule",
		"--keywords", "a1,b2,c3,d4,e5,f6,g7,h8,i9,j10,k11", "--json")
	if !strings.Contains(out, `"keywordsDropped":["k11"]`) {
		t.Fatalf("%q", out)
	}
}

// A list shows what is current, as a search does; --all adds what was
// replaced, and names what replaced it.
func TestAListShowsWhatIsCurrentUnlessAsked(t *testing.T) {
	r := newRun(t)
	r.add("签名由开发者本机完成")
	r.add("签名已改由 CI 完成", "--supersedes", "1")
	if _, out := r.memory("", "list"); strings.Contains(out, "#1 ·") {
		t.Fatalf("a replaced memory is listed: %q", out)
	}
	if _, out := r.memory("", "list", "--all"); !strings.Contains(out, "superseded by #2") {
		t.Fatalf("--all does not name what replaced it: %q", out)
	}
}

// A shell splits an unquoted --keywords value on its space; the words after
// the first are keywords too, not a tail of the memory's text.
func TestWordsSplitFromKeywordsStayKeywords(t *testing.T) {
	r := newRun(t)
	r.add("标点统一用全角，中英文之间不加空格", "--keywords", "别名", "punctuation", "--keywords", "typography")
	_, out := r.memory("", "show", "1")
	if !strings.Contains(out, "keywords: 别名, punctuation, typography") {
		t.Fatalf("keywords: %q", out)
	}
	if strings.Contains(out, "空格punctuation") || strings.Contains(out, "空格 typography") {
		t.Fatalf("a keyword was appended to the text: %q", out)
	}
	// edit takes one positional, the id: words after --keywords never become
	// a second id.
	if code, out := r.memory("", "edit", "1", "--keywords", "别名", "alias"); code != 0 {
		t.Fatalf("%d %q", code, out)
	}
	if _, out := r.memory("", "show", "1", "--json"); !strings.Contains(out, `"keywords":["别名","alias"]`) {
		t.Fatalf("%q", out)
	}
}

// The flags may come before the text: --keywords there takes its one value,
// and every word after it is the text, not more keywords. (The command's own
// name is not a positional, or this would look like a text already given.)
func TestFlagsBeforeTheText(t *testing.T) {
	r := newRun(t)
	if code, out := r.memory("", "add", "--keywords", "别名", "报销流程", "怎么写", "--kind", "practice"); code != 0 {
		t.Fatalf("%d %q", code, out)
	}
	_, out := r.memory("", "show", "1")
	if !strings.Contains(out, "\n报销流程 怎么写\n") || !strings.Contains(out, "keywords: 别名") {
		t.Fatalf("text or keywords: %q", out)
	}
}
