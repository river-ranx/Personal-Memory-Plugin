package store

import (
	"slices"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// tokens cuts text into the words retrieval matches on, in order and with
// repeats: a space-delimited word whole (single letters and stop words
// dropped), a run without spaces — Chinese, Japanese — in two-character
// windows. Where Chinese meets ASCII with no space, as in 用CI跑 or OA提单, the
// boundary is a space too: ci and oa are words of their own.
//
// Text is [fold]ed first, so that the same word written in another width or
// kana is the same word.
//
// It is what a memory is measured and compared by: the content gate, the
// duplicate check and [KeywordsInText] count these words. What the full-text
// index stores and a query matches is [indexTokens], these words and more.
func tokens(text string) []string { return cut(text, false) }

// indexTokens is [tokens] and, for a run without spaces, its three-character
// windows too, each after the two-character window it starts with. It is the
// only cutter of the full-text index and of a query, so the two sides cannot
// disagree on what a word is. Two-character windows find a word inside
// running text, but エンジン shares two of its three windows with エンジニア;
// the three-character windows are rarer, so the run written as asked ranks
// above the one that merely shares a pair. Changing how it cuts leaves the
// index of an existing database cut the old way: raise [schemaVersion], which
// builds the index again.
func indexTokens(text string) []string { return cut(text, true) }

func cut(text string, triples bool) []string {
	var out []string
	isSeparator := func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && !unicode.Is(unicode.Other_Alphabetic, r)
	}
	for _, field := range strings.FieldsFunc(fold(text), isSeparator) {
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
					if triples && i+cjkTriple <= len(chars) {
						out = append(out, string(chars[i:i+cjkTriple]))
					}
				}
			} else if token := string(chars); len(chars) >= minTermChars && !isStopTerm(token) {
				out = append(out, token)
			}
		}
	}
	return out
}

// fold is text in the one form words are cut from: compatibility-composed
// (full-width ＣＩ is ci, half-width ｶﾞ is ガ, か and a combining mark are one
// か), lower case, and hiragana as katakana, so that らーめん and ラーメン are
// one word. Chinese is left as written: nobody writes one memory in both
// scripts, and the table to fold them is large.
func fold(text string) string {
	return strings.Map(func(r rune) rune {
		if r >= 0x3041 && r <= 0x3096 || r == 0x309D || r == 0x309E {
			return r + hiraganaToKatakana
		}
		return r
	}, strings.ToLower(norm.NFKC.String(text)))
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

// KeywordsInText is the keywords that add nothing to finding a memory with
// this text: every word a search would cut from them is a word of the text
// already (or there is none, as with a single letter), so the text is found by
// them without the keyword. A keyword earns its place with a word the text
// lacks: a synonym, the name in another language, an abbreviation.
func KeywordsInText(content string, keywords []string) []string {
	text := wordSet(content)
	var redundant []string
	for _, k := range keywords {
		if !slices.ContainsFunc(tokens(k), func(t string) bool { _, in := text[t]; return !in }) {
			redundant = append(redundant, k)
		}
	}
	return redundant
}

// indexText is text as the full-text index stores it: its [indexTokens]
// joined by single spaces, which the index's unicode61 tokenizer splits on and
// nothing else, since a token holds only letters and digits.
func indexText(text string) string {
	return strings.Join(indexTokens(text), " ")
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

// searchTerms is the distinct [indexTokens] of a query, in the order they were
// written and capped: a prompt is not a search-engine query, and a bounded
// handful keeps the statement small even for a long paragraph. The cap keeps
// the first words, which is where a question says what it is about.
func searchTerms(query string) []string {
	var terms []string
	seen := map[string]struct{}{}
	for _, t := range indexTokens(query) {
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
// 报销 inside 这次报销怎么走; the index and a query add three-character ones.
// A query keeps maxTerms terms: that many words, or half as many characters of
// a CJK run, whose every position gives two.
const (
	minTermChars = 2
	cjkWindow    = 2
	cjkTriple    = 3
	maxTerms     = 48

	// hiraganaToKatakana is the distance between the two kana blocks.
	hiraganaToKatakana = 0x60
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
