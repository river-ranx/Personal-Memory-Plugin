package store

import (
	"errors"
	"testing"
)

// A reworded sentence about the same thing is a repeat; two memories that
// merely share a topic are not.
func TestTheDuplicateThresholdSeparatesRewordingFromSharedTopic(t *testing.T) {
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"报销先在 OA 提单，再把发票寄给财务", "报销要先在 OA 提单，然后把发票寄给财务", true},
		{"release builds are signed and notarized in CI by build-release", "release builds are signed and notarized by the build-release CI job", true},
		{"升 daemon 协议号前先在 reserved-numbers.md 占号", "改协议号之前，先在 reserved-numbers.md 里占号", true},
		{"deploy waits for the pipeline to finish", "deploy uses the release branch, never main", false},
		{"客户端的测试默认是 iOS，桌面的用例要写 platform: macos", "客户端测试默认跑 iOS，桌面用例需要写 platform macos", true},
		{"报销先在 OA 提单", "差旅先在 OA 申请，回来再报销", false},
		{"提交信息用中文，前缀照 git log 的写法", "提交前缀写组件段，比如 fix(next-ui)", false},
	} {
		got := jaccard(wordSet(c.a), wordSet(c.b)) >= duplicateThreshold
		if got != c.same {
			t.Fatalf("%q vs %q: overlap %.2f, repeat %v, want %v", c.a, c.b, jaccard(wordSet(c.a), wordSet(c.b)), got, c.same)
		}
	}
}

func TestARepeatIsNotStoredAndPointsAtWhatItRepeats(t *testing.T) {
	s := open(t)
	kept := write(t, s, Draft{Title: "协议号先占号", Content: "升 daemon 协议号前先在 reserved-numbers.md 占号", Kind: KindRule})
	_, err := s.Write(Draft{Content: "改协议号之前，先在 reserved-numbers.md 里占号", Kind: KindRule})
	var dup *Duplicate
	if !errors.As(err, &dup) || len(dup.Existing) != 1 || dup.Existing[0].ID != kept.ID {
		t.Fatalf("err %v", err)
	}
	if n := countRows(t, s, "SELECT COUNT(*) FROM memories"); n != 1 {
		t.Fatalf("%d rows", n)
	}
	// What the agent does next: supersede it with the newer wording, which
	// is never a repeat.
	newer, err := s.Write(Draft{Content: "改协议号之前，先在 reserved-numbers.md 里占号", Kind: KindRule, Supersedes: &kept.ID})
	if err != nil {
		t.Fatalf("superseding: %v", err)
	}
	old, _ := s.Entry(kept.ID)
	if old.SupersededBy == nil || *old.SupersededBy != newer.ID {
		t.Fatalf("the old one is not marked superseded: %+v", old.Record)
	}
	if _, err := s.Write(Draft{Content: "再取代一次", Kind: KindRule, Supersedes: &kept.ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("superseding twice: %v", err)
	}
}

// A superseded or archived memory is history, not something to repeat.
func TestOnlyCurrentMemoriesCountAsRepeated(t *testing.T) {
	s := open(t)
	old := write(t, s, Draft{Content: "deploy waits for the pipeline to finish", Kind: KindRule})
	if err := s.SetArchived(old.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write(Draft{Content: "deploy waits for the pipeline to finish", Kind: KindRule}); err != nil {
		t.Fatalf("an archived one blocked the write: %v", err)
	}
}
