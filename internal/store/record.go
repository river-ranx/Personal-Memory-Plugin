package store

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The five kinds. What decides the kind is how an agent should use the
// memory when it gets it back. A draft that names none is a fact.
const (
	KindPreference = "preference"
	KindFact       = "fact"
	KindRule       = "rule"
	KindDecision   = "decision"
	KindPractice   = "practice"
)

// Kinds is every kind, in the order a client lists them.
var Kinds = []string{KindPreference, KindFact, KindRule, KindDecision, KindPractice}

// Confidence levels. A memory starts observed; three verifications make it
// verified, and a contradiction takes that back.
const (
	ConfidenceVerified = "verified"
	ConfidenceObserved = "observed"
)

// States: an archived memory is kept but never retrieved.
const (
	StateActive   = "active"
	StateArchived = "archived"
)

// KeywordLimit is how many keywords a memory keeps: the words it is found by,
// which weigh twice a word of the text, so they are not a place to pile words.
const KeywordLimit = 10

// ContentLimit is how long a memory's text may be, in characters. A memory is
// one thing worth remembering, in a sentence or a few: a longer text is a
// document or a log, and every search that returns it would carry all of it.
const ContentLimit = 1000

// LongContent is where a memory starts to read as long for one thing, in
// characters. Past it the text is stored all the same, but the answer says so:
// a text this long is usually two memories, or a record of a process with the
// conclusion buried in it, and the agent that wrote it can still split or cut it.
const LongContent = 250

// Record is one memory with everything the store keeps about it.
type Record struct {
	ID       int64
	Content  string
	Kind     string
	Keywords []string
	// Confidence is observed or verified.
	Confidence string
	Project    *string
	// SupersededBy is the memory that replaced this one, while that memory is
	// in use (active): see [replaced].
	SupersededBy *int64
	State        string
	CreatedAt    uint64
}

// Draft is a memory on its way in. The store owns the id and the clock.
//
// Only Content is required. Kind defaults to a fact (the command line asks an
// agent to name it); Keywords are extra handles for search, and Project only
// changes how a memory ranks.
type Draft struct {
	Content  string
	Kind     string
	Keywords []string
	Project  *string
	// Supersedes is the active memory this one replaces.
	Supersedes *int64
}

// ErrInvalid is a draft the store refuses; its text says why, in words
// meant for the agent that sent it.
var ErrInvalid = errors.New("invalid memory")

// ErrNotFound is an id the store has no memory for.
var ErrNotFound = errors.New("no such memory")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// inherit fills what d leaves out from the memory it replaces: a rule that is
// reworded stays a rule, found by the words it was found by.
func (d Draft) inherit(old Record) Draft {
	if d.Kind == "" {
		d.Kind = old.Kind
	}
	if len(cleanKeywords(d.Keywords)) == 0 {
		d.Keywords = old.Keywords
	}
	// A memory rewritten from elsewhere keeps the project it belonged to: the
	// working directory a command happens to run in says nothing about where
	// the memory applies.
	if d.Project == nil {
		d.Project = old.Project
	}
	return d
}

// normalize checks d and fills its defaults.
func (d Draft) normalize() (Draft, error) {
	d.Content = strings.TrimFunc(d.Content, unicode.IsSpace)
	if d.Content == "" {
		return d, invalid("content is empty")
	}
	if n := utf8.RuneCountInString(d.Content); n > ContentLimit {
		return d, invalid("content is %d characters, at most %d: one memory is one thing — split it, or keep only what will matter later", n, ContentLimit)
	}
	d.Kind = strings.ToLower(strings.TrimSpace(d.Kind))
	if d.Kind == "" {
		d.Kind = KindFact
	}
	if !slices.Contains(Kinds, d.Kind) {
		return d, invalid("kind must be one of %s", strings.Join(Kinds, ", "))
	}
	d.Keywords = cleanKeywords(d.Keywords)
	return d, nil
}

// cleanKeywords is what a memory keeps of keywords: see [CleanKeywords].
func cleanKeywords(keywords []string) []string {
	kept, _ := CleanKeywords(keywords)
	return kept
}

// CleanKeywords splits keywords into the words a memory keeps — once each,
// case aside, at most [KeywordLimit] — and the words beyond the limit, in the
// order written. A phrase is its words, which is how the index holds it. The
// command line says what was dropped: a keyword silently lost is a way to find
// a memory that the writer thinks is there.
func CleanKeywords(keywords []string) (kept, dropped []string) {
	kept, dropped = []string{}, []string{}
	for _, k := range keywords {
		for _, word := range strings.Fields(k) {
			if slices.ContainsFunc(kept, func(o string) bool { return strings.EqualFold(o, word) }) {
				continue
			}
			if len(kept) < KeywordLimit {
				kept = append(kept, word)
			} else {
				dropped = append(dropped, word)
			}
		}
	}
	return kept, dropped
}

// supersededBy is SQL for the newest memory in use that replaced this one, on
// a row of memories aliased m; NULL if none. Only the new memory says what it
// replaced (its supersedes, set once), and it only counts while it is active:
// delete it, or archive it, and the old one is current again; restore it, and
// the old one is replaced again. Nothing is kept on the old row, so nothing has
// to be kept in step.
const supersededBy = `(SELECT MAX(next.id) FROM memories next WHERE next.supersedes = m.id AND next.state = 'active')`

// replaced is SQL for "a memory in use replaced this one", on a row aliased m.
const replaced = supersededBy + ` IS NOT NULL`

// recordColumns is what scanRecord reads, prefixed for a join.
const recordColumns = `m.id, m.content, m.kind, m.keywords, m.confidence, m.project,
       ` + supersededBy + `, m.state, m.created_at`

type scanner interface{ Scan(...any) error }

// scanRecord reads recordColumns, plus any extra destinations after them.
func scanRecord(row scanner, extra ...any) (Record, error) {
	var (
		id, createdAt                              int64
		content, kind, keywords, confidence, state string
		project                                    sql.NullString
		supersededBy                               sql.NullInt64
	)
	dest := append([]any{&id, &content, &kind, &keywords, &confidence, &project,
		&supersededBy, &state, &createdAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		return Record{}, err
	}
	r := Record{
		ID:           id,
		Content:      content,
		Kind:         kind,
		Keywords:     strings.Fields(keywords),
		Confidence:   confidence,
		Project:      nullString(project),
		SupersededBy: optionalInt(supersededBy),
		State:        state,
		CreatedAt:    nonNegative(createdAt),
	}
	if r.Keywords == nil {
		r.Keywords = []string{}
	}
	return r, nil
}

func optionalInt(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	return &n.Int64
}

func intArg(id *int64) any {
	if id == nil {
		return nil
	}
	return *id
}

// load reads one memory.
func load(q queryer, id int64) (Record, error) {
	r, err := scanRecord(q.QueryRow("SELECT "+recordColumns+" FROM memories m WHERE m.id = ?1", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, fmt.Errorf("%w: %s", ErrNotFound, FormatID(id))
	}
	return r, err
}

// FormatID is how an id is shown: #12.
func FormatID(id int64) string { return "#" + strconv.FormatInt(id, 10) }

// ParseID reads an id as an agent typed it: 12 or #12.
func ParseID(text string) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimPrefix(strings.TrimSpace(text), "#"), 10, 64)
	if err != nil || n <= 0 {
		return 0, invalid("%q is not an id (a number such as 12 or #12)", text)
	}
	return n, nil
}
