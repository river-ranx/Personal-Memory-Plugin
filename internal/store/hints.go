package store

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Hints are what a memory's text says about how it was written, beyond what
// the gate keeps out. Nothing here refuses a text, for the reason the gate
// gives: no pattern tells a record of what happened from a lesson, and a rule
// that guesses throws away what should be kept. So a text is stored all the
// same and the answer says what it noticed, as it does for a text over
// [LongContent]; the agent that wrote it can still cut it.
//
// Two things are noticed, both seen in a store where the prompts alone had not
// kept them out: a spec sheet (pixel values, sizes, counts, which the source
// holds and which go stale here) and a status ("fixed, to be confirmed", which
// is progress, not something a later session can use).

// ManyNumbers is how many numbers make a text read as a table of values.
// Dates, versions and numbers inside a word (utf8, k8s) are not counted: a
// memory may say when something was decided or which version it concerns.
const ManyNumbers = 6

var (
	// A date or a version is one thing, not several numbers.
	datesAndVersions = regexp.MustCompile(`\d{4}-\d{1,2}-\d{1,2}|\d{1,2}/\d{1,2}/\d{2,4}|v?\d+(?:\.\d+){2,}`)
	// A unit that may follow a number without a space: 150ms, 12px.
	units = []string{"px", "pt", "ms", "dp", "em", "rem", "fps", "kb", "mb", "gb", "s", "%", "x"}
)

// statusWords say that something is done, not done, or waiting on someone:
// progress, which goes stale. "now" and "currently" are left out on purpose: a
// fact is how things are now; nor is a bare "已改", which is as often how things
// are ("已改由 CI 完成") as a status.
var statusWords = []string{
	"已实现", "已修复", "未改", "还没改", "尚未", "仍是旧", "待用户", "待确认", "待核对", "下一步", "待办", "TODO",
}

// Hint is something noticed about a text, for the answer to a write.
type Hint struct {
	// Code names it for a program: "manyNumbers" or "status".
	Code string
	// Text is what to tell the agent.
	Text string
}

// WriteHints is what is worth telling the agent about content, in a fixed
// order; none for a text that is only a sentence or two of lesson.
func WriteHints(content string) []Hint {
	var hints []Hint
	if n := countNumbers(content); n >= ManyNumbers {
		hints = append(hints, Hint{"manyNumbers", fmt.Sprintf(
			"This text holds %d numbers. Values the source already shows (pixels, sizes, counts) are looked up there, and a copy here goes stale: keep the principle and the reason, and say where the numbers live. Cut it with `memory edit <id> --content …` if it is a spec sheet.", n)})
	}
	if w := statusWordIn(content); w != "" {
		hints = append(hints, Hint{"status", fmt.Sprintf(
			"This reads like progress or a to-do (%q). What is unfinished or waiting goes stale, and a later session would take it for how things are: keep the conclusion and what it rests on, and drop the status with `memory edit <id> --content …`.", w)})
	}
	return hints
}

// countNumbers is how many numbers stand on their own in content: not part
// of a word (utf8, k8s, sha256) or an id (#12), but with a unit or a Chinese
// word right behind them all the same (150ms, 28高), which is how a spec sheet
// is written. Dates and versions are one thing each and are not counted.
func countNumbers(content string) int {
	runes := []rune(datesAndVersions.ReplaceAllString(content, " "))
	isLatin := func(r rune) bool { return unicode.Is(unicode.Latin, r) }
	n := 0
	for i := 0; i < len(runes); {
		if !unicode.IsDigit(runes[i]) {
			i++
			continue
		}
		start := i
		for i < len(runes) && unicode.IsDigit(runes[i]) {
			i++
		}
		if i+1 < len(runes) && runes[i] == '.' && unicode.IsDigit(runes[i+1]) {
			i++
			for i < len(runes) && unicode.IsDigit(runes[i]) {
				i++
			}
		}
		before := start == 0 || !(isLatin(runes[start-1]) || unicode.IsDigit(runes[start-1]) || strings.ContainsRune("#_.", runes[start-1]))
		rest := string(runes[i:])
		for _, u := range units {
			if strings.HasPrefix(rest, u) {
				i += len([]rune(u))
				break
			}
		}
		after := i >= len(runes) || !(isLatin(runes[i]) || unicode.IsDigit(runes[i]) || runes[i] == '_')
		if before && after {
			n++
		}
	}
	return n
}

func statusWordIn(content string) string {
	for _, w := range statusWords {
		if strings.Contains(content, w) {
			return w
		}
	}
	return ""
}
