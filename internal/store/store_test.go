package store

import (
	"errors"
	"path/filepath"
	"testing"
)

// open is a fresh store whose clock the test sets through clock.
func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	at := uint64(1_800_000_000)
	s.now = func() uint64 { return at }
	return s
}

func setClock(s *Store, at uint64) { s.now = func() uint64 { return at } }

func write(t *testing.T, s *Store, d Draft) Record {
	t.Helper()
	r, err := s.Write(d)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func countRows(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOpeningTwiceKeepsTheSchemaAndTheRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write(Draft{Content: "部署走 release 分支", Kind: KindRule}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n := countRows(t, s, "SELECT COUNT(*) FROM memories"); n != 1 {
		t.Fatalf("%d rows", n)
	}
}

// A memory needs only its words; everything else is filled in or optional.
func TestAMemoryNeedsOnlyItsWords(t *testing.T) {
	s := open(t)
	for _, d := range []Draft{
		{Content: "  "},
		{Content: "x", Kind: "note"},
	} {
		if _, err := s.Write(d); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", d, err)
		}
	}
	r := write(t, s, Draft{Content: "喜欢先写测试"})
	if r.Kind != KindFact || r.Confidence != ConfidenceObserved || r.Title != "" || len(r.Keywords) != 0 {
		t.Fatalf("stored %+v", r)
	}
	r = write(t, s, Draft{Content: "新增接口先在 proto 里占号", Kind: KindRule,
		Title: "接口占号", Keywords: []string{" tdd ", "test first", "TDD", ""}})
	if len(r.Keywords) != 3 || r.Keywords[0] != "tdd" || r.Keywords[1] != "test" || r.Keywords[2] != "first" || r.Title != "接口占号" {
		t.Fatalf("stored %+v", r)
	}
}

// Recall finds a two-character Chinese word inside a sentence, ranks the
// asking project first without hiding the other, and counts each hit.
func TestSearchFindsChineseRanksTheProjectAndCountsHits(t *testing.T) {
	s := open(t)
	alpha, beta := "/work/alpha", "/work/beta"
	there := write(t, s, Draft{Content: "报销需附发票原件", Kind: KindRule, Project: &alpha})
	here := write(t, s, Draft{Content: "报销需附行程截图", Kind: KindRule, Project: &beta})
	write(t, s, Draft{Content: "deploy uses the release branch", Kind: KindRule})

	hits, err := s.Search("这次报销怎么走", &beta, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].ID != here.ID || hits[1].ID != there.ID {
		t.Fatalf("hits %v", ids(hits))
	}
	if hits, _ := s.Search("这次报销怎么走", &alpha, 5); len(hits) != 2 || hits[0].ID != there.ID {
		t.Fatalf("from alpha: %v", ids(hits))
	}
	// The second search came the same day: a memory's hit is noted once a day.
	if n := countRows(t, s, "SELECT COUNT(*) FROM memory_events WHERE kind = 'hit'"); n != 2 {
		t.Fatalf("%d hits recorded", n)
	}
	if hits, _ := s.Search("Deploy RELEASE", nil, 5); len(hits) != 1 {
		t.Fatalf("english: %v", ids(hits))
	}
	if hits, _ := s.Search("the and", nil, 5); len(hits) != 0 {
		t.Fatal("stop words alone match nothing")
	}
}

func TestAnArchivedMemoryIsNeverRetrievedUntilRestored(t *testing.T) {
	s := open(t)
	r := write(t, s, Draft{Content: "staging 的数据库只读", Kind: KindFact})
	if err := s.SetArchived(r.ID, true); err != nil {
		t.Fatal(err)
	}
	if hits, _ := s.Search("staging 数据库", nil, 5); len(hits) != 0 {
		t.Fatal("archived memory retrieved")
	}
	if list, _ := s.List(ListQuery{Archived: true}); len(list) != 1 {
		t.Fatal("not in the archived list")
	}
	if err := s.SetArchived(r.ID, false); err != nil {
		t.Fatal(err)
	}
	if hits, _ := s.Search("staging 数据库", nil, 5); len(hits) != 1 {
		t.Fatal("restored memory not retrieved")
	}
}

func TestDeleteLeavesNothingBehind(t *testing.T) {
	s := open(t)
	r := write(t, s, Draft{Content: "staging 的数据库只读", Kind: KindFact})
	if err := s.Delete(r.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"memories", "memories_fts", "memory_events"} {
		if n := countRows(t, s, "SELECT COUNT(*) FROM "+table); n != 0 {
			t.Fatalf("%s keeps %d rows", table, n)
		}
	}
	if err := s.Delete(r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

// Three verifications, on three days, promote; one contradiction demotes at
// once.
func TestConfidenceFollowsConfirmations(t *testing.T) {
	s := open(t)
	r := write(t, s, Draft{Content: "签名由 CI 完成", Kind: KindFact})
	for i := range 3 {
		setClock(s, 1_800_000_000+uint64(i)*2*86400)
		if _, err := s.Confirm([]int64{r.ID}, OutcomeVerified, ""); err != nil {
			t.Fatal(err)
		}
		got, _ := s.Entry(r.ID)
		want := ConfidenceObserved
		if i == 2 {
			want = ConfidenceVerified
		}
		if got.Confidence != want {
			t.Fatalf("after %d verifications: %s", i+1, got.Confidence)
		}
	}
	done, err := s.Confirm([]int64{r.ID}, OutcomeContradicted, "改了")
	if err != nil || len(done.Missing) != 0 {
		t.Fatal(done, err)
	}
	if got, _ := s.Entry(r.ID); got.Confidence != ConfidenceObserved || got.Contradicted != 1 {
		t.Fatalf("after a contradiction: %s, %d", got.Confidence, got.Contradicted)
	}
	if _, err := s.Confirm([]int64{r.ID}, "maybe", ""); !errors.Is(err, ErrInvalid) {
		t.Fatal("an unknown outcome is accepted")
	}
}

// Decay runs from the last use: a memory found again does not fade.
func TestDecayRunsFromTheLastUse(t *testing.T) {
	s := open(t)
	r := write(t, s, Draft{Content: "发版前先跑 make check", Kind: KindRule})
	setClock(s, 1_800_000_000+180*86400)
	if got, _ := s.Entry(r.ID); got.Decay > 0.31 {
		t.Fatalf("unused for 180 days keeps %v", got.Decay)
	}
	if _, err := s.Search("发版", nil, 5); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Entry(r.ID); got.Decay != 1 {
		t.Fatalf("just used keeps %v", got.Decay)
	}
}

func TestPruneDropsOldHitsButKeepsTheLastUse(t *testing.T) {
	s := open(t)
	r := write(t, s, Draft{Content: "发版前先跑 make check", Kind: KindRule})
	s.Search("发版", nil, 5)
	setClock(s, 1_800_000_000+2*86400)
	s.Search("make check", nil, 5)
	setClock(s, 1_800_000_000+200*86400)
	if err := s.Prune(); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, "SELECT COUNT(*) FROM memory_events WHERE kind = 'hit'"); n != 1 {
		t.Fatalf("%d hits kept", n)
	}
	if got, _ := s.Entry(r.ID); got.LastUsed == nil {
		t.Fatal("the last use was pruned")
	}
}

func ids(hits []Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.Content
	}
	return out
}

// A search is a read: asking again within the day writes nothing, and a
// memory that keeps being asked about does not climb the ranking for it.
func TestAMemoryIsCountedAsUsedOncePerDay(t *testing.T) {
	s := open(t)
	r := write(t, s, Draft{Content: "发版前先跑 make check", Kind: KindRule})
	for range 5 {
		if _, err := s.Search("发版", nil, 5); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := s.Entry(r.ID); got.Uses != 1 {
		t.Fatalf("5 searches in a day count %d uses", got.Uses)
	}
	setClock(s, 1_800_000_000+86400+1)
	s.Search("发版", nil, 5)
	if got, _ := s.Entry(r.ID); got.Uses != 2 {
		t.Fatalf("a second day counts %d uses", got.Uses)
	}
}

// A question is cut into words and any one of them matches; a memory that
// shares only a stray word with it must not take the place of an answer.
func TestAStrayWordDoesNotFillTheResults(t *testing.T) {
	s := open(t)
	// Thirty memories that share nothing but the phrase 怎么处理.
	names := []string{"赵钱孙", "李周吴", "郑王冯", "陈褚卫", "蒋沈韩", "杨朱秦", "尤许何", "吕施张", "孔曹严", "华金魏",
		"陶姜戚", "谢邹喻", "柏水窦", "章云苏", "潘葛奚", "范彭郎", "鲁韦昌", "马苗凤", "花方俞", "任袁柳",
		"酆鲍史", "唐费廉", "岑薛雷", "贺倪汤", "滕殷罗", "毕郝邬", "安常乐", "于时傅", "皮卞齐", "康伍余"}
	for i := range names {
		unique := names[i] + names[(i+7)%len(names)] + names[(i+13)%len(names)]
		write(t, s, Draft{Content: unique + "怎么处理", Kind: KindFact})
	}
	answer := write(t, s, Draft{Content: "部署超时先查网关日志再重启", Kind: KindRule})
	hits, err := s.Search("部署超时怎么处理", nil, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != answer.ID {
		t.Fatalf("the stray 怎么处理 filled the results: %v", ids(hits))
	}
	// Asking only with the stray words still finds them: the cut is relative
	// to the best match, so it never empties a search.
	if hits, _ := s.Search("怎么处理", nil, 5); len(hits) == 0 {
		t.Fatal("nothing found for the words that do match")
	}
}

// Ids are 1, 2, 3…; a deleted memory's number is never given to another, so
// an id an agent remembers cannot come to mean something else.
func TestIDsAreCountedUpAndNeverReused(t *testing.T) {
	s := open(t)
	a := write(t, s, Draft{Content: "发版前先跑 make check", Kind: KindRule})
	b := write(t, s, Draft{Content: "staging 的数据库只读", Kind: KindFact})
	if a.ID != 1 || b.ID != 2 {
		t.Fatalf("ids %d, %d", a.ID, b.ID)
	}
	if err := s.Delete(b.ID); err != nil {
		t.Fatal(err)
	}
	c := write(t, s, Draft{Content: "签名由 CI 完成", Kind: KindFact})
	if c.ID != 3 {
		t.Fatalf("the number of a deleted memory was given again: %d", c.ID)
	}
	if _, err := s.Entry(b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a deleted id: %v", err)
	}
}

// In a small store bm25 cannot tell a telling word from a common one, so a
// query about two topics returns both, not only the one that matches harder.
func TestAQueryAboutTwoTopicsFindsBothInASmallStore(t *testing.T) {
	s := open(t)
	strong := write(t, s, Draft{Title: "提交规范", Content: "提交信息用中文，前缀照 git log 的写法", Kind: KindRule, Keywords: []string{"提交", "commit"}})
	a := write(t, s, Draft{Content: "发版只走 release 分支，因为 main 不触发签名流水线", Kind: KindRule})
	b := write(t, s, Draft{Content: "签名由 CI 完成，本机不再有签名证书", Kind: KindFact})
	hits, err := s.Search("签名 提交", nil, 5)
	if err != nil || len(hits) != 3 {
		t.Fatalf("hits %v err %v", ids(hits), err)
	}
	got := map[int64]bool{}
	for _, h := range hits {
		got[h.ID] = true
	}
	if !got[strong.ID] || !got[a.ID] || !got[b.ID] {
		t.Fatalf("hits %v", ids(hits))
	}
}

func TestAnIDIsANumberWithOrWithoutTheHash(t *testing.T) {
	for text, want := range map[string]int64{"12": 12, "#12": 12, " #7 ": 7, "1": 1} {
		if got, err := ParseID(text); err != nil || got != want {
			t.Fatalf("%q: %d %v", text, got, err)
		}
	}
	for _, text := range []string{"", "#", "0", "-3", "abc", "080c7163", "1.5", "#12x"} {
		if _, err := ParseID(text); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q: %v", text, err)
		}
	}
	if FormatID(12) != "#12" {
		t.Fatal(FormatID(12))
	}
}

// An abbreviation is found by itself, written alone, among spaces or against
// Chinese; a function word is found by nothing.
func TestAnAbbreviationIsFound(t *testing.T) {
	s := open(t)
	a := write(t, s, Draft{Content: "CI 流水线失败时先看 DB 迁移日志", Kind: KindPractice})
	b := write(t, s, Draft{Content: "用CI跑回归测试前先清缓存", Kind: KindRule})
	c := write(t, s, Draft{Content: "OA提单后再把发票寄给财务", Kind: KindRule})
	for query, want := range map[string][]int64{
		"ci":    {a.ID, b.ID},
		"CI":    {a.ID, b.ID},
		"db":    {a.ID},
		"oa":    {c.ID},
		"跑ci":   {b.ID, a.ID},
		"提单":    {c.ID},
		"oa 发票": {c.ID},
	} {
		hits, err := s.Search(query, nil, 5)
		if err != nil {
			t.Fatal(err)
		}
		got := map[int64]bool{}
		for _, h := range hits {
			got[h.ID] = true
		}
		for _, id := range want {
			if !got[id] {
				t.Fatalf("%q: hits %v lack #%d", query, ids(hits), id)
			}
		}
	}
	if hits, _ := s.Search("to is of the", nil, 5); len(hits) != 0 {
		t.Fatalf("function words matched %v", ids(hits))
	}
}
