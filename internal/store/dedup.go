package store

import (
	"cmp"
	"database/sql"
	"fmt"
	"slices"
	"strings"
)

// duplicateThreshold is how alike two memories' words have to be — the
// Jaccard overlap of their token sets — for the second to be a repeat.
// Rewordings of one memory measured 0.46–0.83 and memories that merely share
// a topic 0.10–0.20 (dedup_test.go), so the line sits between them. Jaccard
// rather than the overlap of the smaller set: a two-word memory inside a long
// different one would otherwise be a "repeat".
const duplicateThreshold = 0.4

// duplicateCandidates is how many of the closest full-text matches are
// compared.
const duplicateCandidates = 5

// Duplicate is the memories a draft repeats, closest first; there is at least
// one. An agent either supersedes one of them with what it has learned or
// drops the draft.
type Duplicate struct {
	Existing []Record
}

// Error is what the agent is told, with --json or without: which memories it
// repeats, and the command to replace the closest.
func (d *Duplicate) Error() string {
	ids := make([]string, len(d.Existing))
	for i, r := range d.Existing {
		ids[i] = FormatID(r.ID)
	}
	return fmt.Sprintf("already remembered as %s. If yours is newer or more precise, add it again with --supersedes %s; otherwise leave it",
		strings.Join(ids, ", "), ids[0])
}

// duplicates is the active, unsuperseded memories whose words overlap the
// draft's by at least duplicateThreshold, closest first.
func duplicates(tx *sql.Tx, d Draft) ([]Record, error) {
	draftWords := wordSet(d.Title + " " + d.Content)
	terms := searchTerms(d.Title + " " + d.Content)
	if len(terms) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(`SELECT `+recordColumns+`
               FROM (SELECT memory_id, rank FROM memories_fts
                      WHERE memories_fts MATCH ?1 AND rank MATCH ?2
                      ORDER BY rank LIMIT ?3) AS best
               JOIN memories m ON m.id = best.memory_id
              WHERE m.state = 'active' AND NOT `+replaced+`
              ORDER BY best.rank`, matchQuery(terms), bm25Rank, duplicateCandidates)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type scored struct {
		Record
		overlap float64
	}
	var found []scored
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		if overlap := jaccard(draftWords, wordSet(r.Title+" "+r.Content)); overlap >= duplicateThreshold {
			found = append(found, scored{r, overlap})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.SortStableFunc(found, func(a, b scored) int { return cmp.Compare(b.overlap, a.overlap) })
	out := make([]Record, len(found))
	for i, f := range found {
		out[i] = f.Record
	}
	return out, nil
}

func wordSet(text string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, t := range tokens(text) {
		set[t] = struct{}{}
	}
	return set
}

func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	both := 0
	for t := range a {
		if _, ok := b[t]; ok {
			both++
		}
	}
	return float64(both) / float64(len(a)+len(b)-both)
}
