package store

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestTokensKeepOrderAndRepeatsAndCutChineseIntoPairs(t *testing.T) {
	got := tokens("报销流程，Deploy 报销 the gateway")
	want := []string{"报销", "销流", "流程", "deploy", "报销", "gateway"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tokens = %q, want %q", got, want)
	}
}

// The index and the query share one cut; the query's terms are that cut,
// distinct, in the order written, and capped to the first few.
func TestSearchTermsAreTheDistinctTokensInOrder(t *testing.T) {
	for _, text := range []string{
		"这次报销怎么走",
		"How do I deploy the gateway from the release branch?",
		"APP_GATEWAY_FILE 和 e2e 用例",
		"",
	} {
		var want []string
		for _, tok := range tokens(text) {
			if !slices.Contains(want, tok) {
				want = append(want, tok)
			}
		}
		if len(want) > maxTerms {
			want = want[:maxTerms]
		}
		if got := searchTerms(text); !slices.Equal(got, want) {
			t.Fatalf("searchTerms(%q) = %q, want %q", text, got, want)
		}
	}
}

// A long prompt keeps its opening words, which say what it is about, not the
// ones that happen to sort first.
func TestALongQueryKeepsItsFirstWords(t *testing.T) {
	long := "zebra giraffe " + strings.Repeat("alpha bravo charlie delta echo foxtrot ", 2)
	for i := range 30 {
		long += fmt.Sprintf(" aaa%02d", i)
	}
	terms := searchTerms(long)
	if len(terms) != maxTerms || terms[0] != "zebra" || terms[1] != "giraffe" || terms[2] != "alpha" {
		t.Fatalf("terms %q", terms)
	}
}

func TestIndexTextIsTheTokensJoinedBySpaces(t *testing.T) {
	if got := indexText("报销流程 deploy"); got != "报销 销流 流程 deploy" {
		t.Fatalf("indexText = %q", got)
	}
	if got := indexText("a an to"); got != "" {
		t.Fatalf("short words index as nothing, got %q", got)
	}
}

func TestMatchQueryQuotesEveryTermAndJoinsWithOr(t *testing.T) {
	if got := matchQuery([]string{"报销", "deploy"}); got != `"报销" OR "deploy"` {
		t.Fatalf("matchQuery = %q", got)
	}
}

// Two-letter abbreviations are what a developer searches for; two-letter
// function words are not.
func TestAbbreviationsAreWordsAndShortFunctionWordsAreNot(t *testing.T) {
	got := tokens("CI is red: check the DB, then OA to MR of PR in UI, ok?")
	want := []string{"ci", "red", "check", "db", "oa", "mr", "pr", "ui"}
	if !slices.Equal(got, want) {
		t.Fatalf("tokens = %q, want %q", got, want)
	}
	// A single letter is still no word.
	if got := tokens("a b c x"); len(got) != 0 {
		t.Fatalf("single letters: %q", got)
	}
}

// Chinese meeting ASCII with no space is two words, not one that matches
// neither: 用CI跑 must be found by ci.
func TestAChineseAsciiBoundaryCutsAWord(t *testing.T) {
	for text, want := range map[string][]string{
		"用CI跑回归":       {"ci", "跑回", "回归"},
		"OA提单":         {"oa", "提单"},
		"k8s集群":        {"k8s", "集群"},
		"v2版本和e2e用例":   {"v2", "版本", "本和", "e2e", "用例"},
		"部署到staging环境": {"部署", "署到", "staging", "环境"},
		"ci":           {"ci"},
		// One script stays whole: an accent is no boundary, and Cyrillic is
		// cut into windows as before.
		"café":   {"café"},
		"привет": {"пр", "ри", "ив", "ве", "ет"},
	} {
		if got := tokens(text); !slices.Equal(got, want) {
			t.Fatalf("tokens(%q) = %q, want %q", text, got, want)
		}
	}
}
