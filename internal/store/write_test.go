package store

import (
	"strings"
	"testing"
)

// A rewording says the new text and nothing else; what the memory was and the
// words it was found by carry over. A rule that came back as a fact, with no
// keywords, would be found by nothing it used to be found by.
func TestSupersedingKeepsWhatTheNewTextDoesNotSay(t *testing.T) {
	s := open(t)
	old := write(t, s, Draft{Content: "发版只走 release 分支，因为 main 不触发签名流水线", Kind: KindRule,
		Keywords: []string{"发版", "release", "notarize"}})
	fresh := write(t, s, Draft{Content: "发版只走 release 分支，main 已经配置了签名流水线", Supersedes: &old.ID})
	if fresh.Kind != KindRule || strings.Join(fresh.Keywords, ",") != "发版,release,notarize" {
		t.Fatalf("stored %+v", fresh)
	}
	if hits, _ := s.Search("notarize", nil, 5); len(hits) != 1 || hits[0].ID != fresh.ID {
		t.Fatalf("found by the old keyword: %v", ids(hits))
	}

	// What is said is what is kept.
	third := write(t, s, Draft{Content: "发版改走 main 分支，签名由 CI 自动完成", Kind: KindDecision,
		Keywords: []string{"main"}, Supersedes: &fresh.ID})
	if third.Kind != KindDecision || strings.Join(third.Keywords, ",") != "main" {
		t.Fatalf("stored %+v", third)
	}
	// Blank keywords say nothing either.
	fourth := write(t, s, Draft{Content: "发版改走 main 分支，签名由 CI 自动完成并通知", Keywords: []string{" ", ""}, Supersedes: &third.ID})
	if strings.Join(fourth.Keywords, ",") != "main" || fourth.Kind != KindDecision {
		t.Fatalf("stored %+v", fourth)
	}
}

// What is replaced follows from the memory that replaced it: while that one is
// in use the old one is out of the way, and when it is deleted or archived the
// old one is current again — never lost to a pointer at nothing.
func TestWhatIsReplacedFollowsTheOneThatReplacedIt(t *testing.T) {
	s := open(t)
	old := write(t, s, Draft{Content: "迁移前先备份再执行，备份放在 backup 桶", Kind: KindRule})
	fresh := write(t, s, Draft{Content: "迁移前先备份再执行，备份放在 backup 桶并保留七天", Supersedes: &old.ID})
	only := func(want int64, when string) {
		t.Helper()
		hits, err := s.Search("迁移 备份", nil, 5)
		if err != nil || len(hits) != 1 || hits[0].ID != want {
			t.Fatalf("%s: %v (want #%d) %v", when, ids(hits), want, err)
		}
	}
	only(fresh.ID, "replaced")
	if got, _ := s.Entry(old.ID); got.SupersededBy == nil || *got.SupersededBy != fresh.ID {
		t.Fatalf("the replaced one says so: %+v", got.Record)
	}

	if err := s.SetArchived(fresh.ID, true); err != nil {
		t.Fatal(err)
	}
	only(old.ID, "the newer one archived")
	if got, _ := s.Entry(old.ID); got.SupersededBy != nil {
		t.Fatal("the old one still says it is replaced by an archived memory")
	}
	if err := s.SetArchived(fresh.ID, false); err != nil {
		t.Fatal(err)
	}
	only(fresh.ID, "the newer one restored")

	if err := s.Delete(fresh.ID); err != nil {
		t.Fatal(err)
	}
	only(old.ID, "the newer one deleted")
	got, err := s.Entry(old.ID)
	if err != nil || got.SupersededBy != nil {
		t.Fatalf("after the delete: %+v %v", got.Record, err)
	}
	// It is current, so it can be replaced again, and is not a repeat's blocker.
	again := write(t, s, Draft{Content: "迁移前先备份再执行，备份放在 backup 桶并保留三十天", Supersedes: &old.ID})
	only(again.ID, "replaced again")
}

// Reporting a memory is counted once a day for each outcome: a memory cannot
// rank itself up, or be made verified, by being confirmed over and over in one
// sitting.
func TestAMemoryIsReportedOncePerDayPerOutcome(t *testing.T) {
	s := open(t)
	r := write(t, s, Draft{Content: "staging 的数据库账号只读，写入要走变更工单", Kind: KindRule})
	var repeated int
	for range 20 {
		done, err := s.Confirm([]int64{r.ID}, OutcomeUsed, "")
		if err != nil {
			t.Fatal(err)
		}
		repeated += len(done.Repeated)
	}
	if repeated != 19 {
		t.Fatalf("%d repeats of 20", repeated)
	}
	for range 3 {
		if _, err := s.Confirm([]int64{r.ID}, OutcomeVerified, ""); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.Entry(r.ID)
	if got.Uses != 2 || got.Verified != 1 || got.Confidence != ConfidenceObserved {
		t.Fatalf("uses %d, verified %d, %s: a day's confirmations count once each", got.Uses, got.Verified, got.Confidence)
	}
	// The same id twice in one call is a repeat too; an unknown one is missing.
	done, _ := s.Confirm([]int64{r.ID, r.ID, 999}, OutcomeContradicted, "改了")
	if len(done.Repeated) != 1 || len(done.Missing) != 1 || done.Missing[0] != 999 {
		t.Fatalf("%+v", done)
	}
	// The next day counts again.
	setClock(s, 1_800_000_000+86400+1)
	if done, _ := s.Confirm([]int64{r.ID}, OutcomeUsed, ""); len(done.Repeated) != 0 {
		t.Fatalf("a new day: %+v", done)
	}
}

// A kind is understood however it is capitalised; keywords are a few words,
// once each.
func TestKindAndKeywordsAreTidied(t *testing.T) {
	s := open(t)
	r := write(t, s, Draft{Content: "提交信息用中文，前缀照 git log 的写法", Kind: " Rule "})
	if r.Kind != KindRule {
		t.Fatalf("stored kind %q", r.Kind)
	}
	var words []string
	for i := range 25 {
		words = append(words, "word"+strings.Repeat("x", i%3)+string(rune('a'+i)))
	}
	r = write(t, s, Draft{Content: "staging 的数据库只读，写入要走变更工单", Keywords: append(words, "WORDA", "worda")})
	if len(r.Keywords) != KeywordLimit {
		t.Fatalf("%d keywords", len(r.Keywords))
	}
	if got := cleanKeywords([]string{"CI", "ci", "Ci cd", "cd"}); strings.Join(got, ",") != "CI,cd" {
		t.Fatalf("keywords %q", got)
	}
}
