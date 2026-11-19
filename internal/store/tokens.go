package store

import (
	"slices"
	"strings"
	"unicode"
)

// tokens cuts text into the words retrieval matches on, in order and with
// repeats: a space-delimited word whole (single letters and stop words
// dropped), a run without spaces — Chinese, Japanese — in two-character
// windows. Where Chinese meets ASCII with no space, as in 用CI跑 or OA提单, the
// boundary is a space too: ci and oa are words of their own.
//
// It is the only word-cutter. The full-text index stores these tokens and a
// query is matched with them too, so the two sides cannot disagree on what a
// word is; the duplicate check compares the same tokens. Changing how it cuts
// leaves the index of an existing database cut the old way.
func tokens(text string) []string {
	var out []string
	isSeparator := func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && !unicode.Is(unicode.Other_Alphabetic, r)
	}
	for _, field := range strings.FieldsFunc(strings.ToLower(text), isSeparator) {
		for _, chars := range splitScripts([]rune(field)) {
			spaceless := true
			for _, c := range chars {
				if c < 0x80 && (unicode.IsLetter(c) || unicode.IsDigit(c)) {
					spaceless = false
					break
				}
			}
			if spaceless {
				if len(chars) < cjkWindow {
					continue
				}
				for i := 0; i+cjkWindow <= len(chars); i++ {
					out = append(out, string(chars[i:i+cjkWindow]))
				}
			} else if token := string(chars); len(chars) >= minTermChars && !isStopTerm(token) {
				out = append(out, token)
			}
		}
	}
	return out
}

// splitScripts cuts a word where Chinese, Japanese or Korean meets any other
// script: 用ci跑 is 用, ci, 跑. A word of one script, an accented one (café)
// included, comes back whole.
func splitScripts(word []rune) [][]rune {
	var parts [][]rune
	start := 0
	for i := 1; i < len(word); i++ {
		if isCJK(word[i]) != isCJK(word[i-1]) {
			parts = append(parts, word[start:i])
			start = i
		}
	}
	return append(parts, word[start:])
}

func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

func isStopTerm(token string) bool {
	_, stop := slices.BinarySearch(stopTerms, token)
	return stop
}

// indexText is text as the full-text index stores it: its tokens joined by
// single spaces, which the index's unicode61 tokenizer splits on and nothing
// else, since a token holds only letters and digits.
func indexText(text string) string {
	return strings.Join(tokens(text), " ")
}

// matchQuery is the FTS5 query for terms: any one of them, each quoted so
// none is read as an operator (a token has no quote in it to escape).
func matchQuery(terms []string) string {
	quoted := make([]string, len(terms))
	for i, term := range terms {
		quoted[i] = `"` + term + `"`
	}
	return strings.Join(quoted, " OR ")
}

// searchTerms is the distinct [tokens] of a query, in the order they were
// written and capped: a prompt is not a search-engine query, and a bounded
// handful keeps the statement small even for a long paragraph. The cap keeps
// the first words, which is where a question says what it is about.
func searchTerms(query string) []string {
	var terms []string
	seen := map[string]struct{}{}
	for _, t := range tokens(query) {
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		terms = append(terms, t)
		if len(terms) == maxTerms {
			break
		}
	}
	return terms
}

// A space-delimited word is used whole, and short ones are dropped — a
// two-letter word matches most of any corpus. A run of CJK has no spaces, so
// it is cut into two-character windows instead, which is what actually finds
// 报销 inside 这次报销怎么走.
const (
	minTermChars = 2
	cjkWindow    = 2
	maxTerms     = 24
)

// stopTerms are function words, dropped because they carry no signal and
// one of them is enough to recall the entire corpus: to, is, the, with. (A
// two-letter word that says something — ci, db, oa, mr, pr, ui, go — is not
// here: those are what a developer searches for.) Deliberately short and
// English-only. Sorted, for binary search.
var stopTerms = []string{
	"about", "after", "all", "also", "am", "an", "and", "any", "are", "as", "at", "be", "but", "by",
	"can", "did", "do", "does", "for", "from", "had", "has", "have", "he", "here", "how", "if", "in",
	"into", "is", "it", "its", "just", "like", "make", "me", "my", "no", "not", "now", "of", "ok",
	"on", "one", "only", "or", "our", "out", "over", "should", "so", "some", "than", "that", "the",
	"their", "them", "then", "there", "these", "they", "this", "those", "to", "up", "us", "use",
	"using", "was", "we", "were", "what", "when", "where", "which", "who", "why", "will", "with",
	"would", "you", "your",
}
