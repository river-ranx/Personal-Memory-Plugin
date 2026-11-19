package store

import (
	"errors"
	"strings"
	"testing"
)

// A word or two is a label, not a memory: it says nothing to whoever finds it,
// whatever its kind, and keywords or a title do not make up for it.
func TestTextTooShortToSayAnythingIsRefused(t *testing.T) {
	for _, text := range []string{"ok", "好", "好的", "ok!!", "…", "a b", "报销", "release", "报销单", "用 pnpm", "CI DB"} {
		for _, kind := range Kinds {
			d := Draft{Content: text, Kind: kind, Title: "发版规则", Keywords: []string{"release", "发版", "签名"}}
			if err := screen(d); !errors.Is(err, ErrRefused) {
				t.Fatalf("%q as a %s: %v", text, kind, screen(d))
			}
		}
	}
	// Three words is a sentence, short or not.
	for _, text := range []string{"提交用中文", "用 pnpm 不用 npm", "never force-push main", "发版走 release", "报销先找财务"} {
		if err := screen(Draft{Content: text, Kind: KindFact}); err != nil {
			t.Fatalf("%q: %v", text, err)
		}
	}
}

// The refusal says what to do next and never repeats the text; nothing is
// stored, and nothing is noted.
func TestARefusalSaysWhatToDoNextAndLeavesNothing(t *testing.T) {
	s := open(t)
	_, err := s.Write(Draft{Content: "报销单", Kind: KindFact})
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "in a sentence") || strings.Contains(err.Error(), "报销") {
		t.Fatalf("err %v", err)
	}
	for _, table := range []string{"memories", "memory_events", "memories_fts"} {
		if n := countRows(t, s, "SELECT COUNT(*) FROM "+table); n != 0 {
			t.Fatalf("%s has %d rows", table, n)
		}
	}
}
