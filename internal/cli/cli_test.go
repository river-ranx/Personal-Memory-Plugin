package cli

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"regexp"
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

// add takes a sentence and nothing else; the answer is its number.
func (r run) add(args ...string) string {
	r.t.Helper()
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
	r.add("提交信息用中文，前缀照 git log 的写法", "--kind", "rule", "--title", "提交规范")
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
	for _, want := range []string{"#1 · rule · 提交规范 · written today", " · verified\n", "#2 · fact · written today", " · contradicted 1x · /work/other\n"} {
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
	if _, out := r.memory("", "list"); !strings.Contains(out, "superseded by #"+fresh) {
		t.Fatalf("a list shows what was replaced: %q", out)
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
	if code, out := r.memory("", "add", "ok"); code != 1 || !strings.HasPrefix(out, "Not stored: refused") || !strings.Contains(out, "too short to be a memory") {
		t.Fatalf("thin: %d %q", code, out)
	}
	if code, out := r.memory("", "add", "报销", "--title", "报销流程", "--keywords", "OA,发票,财务"); code != 1 || !strings.Contains(out, "too short to be a memory") {
		t.Fatalf("a label with keywords: %d %q", code, out)
	}
	if code, out := r.memory("", "add", strings.Repeat("字", 1200)); code != 1 || !strings.Contains(out, "at most 1000") || !strings.Contains(out, "split it") {
		t.Fatalf("too long: %d %q", code, out)
	}
	// Private-looking text is no concern of the gate: this is the owner's machine.
	r.add("值班手机 13812345678，测试库口令放在 1Password")
	first := r.add("升 daemon 协议号前先在 reserved-numbers.md 占号")
	// A repeat shows what it repeats and the exact command to replace it.
	code, out := r.memory("", "add", "改协议号之前，先在 reserved-numbers.md 里占号")
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

// The header is one line, whatever the title was.
func TestATitleWithNewlinesStaysOnTheHeaderLine(t *testing.T) {
	r := newRun(t)
	r.add("提交信息用中文，前缀照 git log 的写法", "--kind", "RULE", "--title", "第一行\n第二行")
	_, out := r.memory("", "search", "提交信息")
	header, body, _ := strings.Cut(out, "\n")
	if !strings.HasPrefix(header, "#1 · rule · 第一行 第二行 · written today") || !strings.HasPrefix(body, "提交信息用中文") {
		t.Fatalf("%q", out)
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
	if code, out := r.memory("{}", "hook", "claude", "UserPromptSubmit"); code != 0 || strings.Contains(out, "additionalContext") {
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
