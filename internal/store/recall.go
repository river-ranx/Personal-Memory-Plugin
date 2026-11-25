package store

import (
	"database/sql"
	"slices"
	"strconv"
	"strings"
)

// candidateLimit is how many full-text matches are scored; the best few of
// them are returned.
const candidateLimit = 30

// MaxSearchLimit bounds one search.
const MaxSearchLimit = 20

// weakMatch is how little of the best match's strength a candidate may have
// and still be returned. A query is cut into words (two-character windows
// for Chinese), and any one word matches: a memory sharing only 怎么 with a
// question about 部署 is not an answer, and would take one of the few places
// an agent reads. The best match always stays.
//
// bm25 only tells a telling word from a common one with a corpus behind it:
// in a handful of memories a word found in half of them already scores next
// to nothing, though the memory is exactly what was asked for (a query about
// two topics would lose the one that is not the stronger). So the cut applies
// from weakMatchCorpus memories up, and below that every match is returned —
// there are few, and the search returns at most a few anyway.
const (
	weakMatch       = 0.1
	weakMatchCorpus = 30
)

// bm25 weights per column of memories_fts: memory_id, keywords, body. A
// keyword was given as a way to find the memory, so it says more about what
// the memory is than a word of the text in passing.
const bm25Rank = "bm25(0.0, 2.0, 1.0)"

// Hit is a memory and how it stands: its score when it was retrieved, how
// much of its standing it keeps after going unused, and its counts.
type Hit struct {
	Record
	Score float64
	Decay float64
	Counts
}

// Counts are a memory's events over the stats window, and when it was last
// seen in use.
type Counts struct {
	// Uses are the agents' reports that it was used or verified; a return
	// from a search is not one.
	Uses, Verified, Contradicted int
	// LastUsed is the last time it was returned, used or verified, Unix
	// seconds; nil if never. Decay runs from it.
	LastUsed *uint64
}

// Search is the active memories that match query, best first, at most
// limit; each one returned is noted as a hit, which keeps it from decaying.
// A memory a newer one replaced (while that one is in use) is not returned,
// and in a store of any size
// neither is one that matches only on a stray word of the query (see
// [weakMatch]).
// project only reorders: a memory from another project still shows, ranked
// lower, because filtering would hide exactly the lesson that generalises.
func (s *Store) Search(query string, project *string, limit int) ([]Hit, error) {
	found, err := s.Find(query, project, "", limit)
	return found.Hits, err
}

// SearchKind is [Store.Search] with memories of one kind only, when kind is
// named: the answer to "what rules do I have about this?".
func (s *Store) SearchKind(query string, project *string, kind string, limit int) ([]Hit, error) {
	found, err := s.Find(query, project, kind, limit)
	return found.Hits, err
}

// Found is a search's answer: the memories returned, and how many the query
// matched in all. A caller that shows fewer than matched says so: a limit
// that silently drops results looks like a memory that is not there.
type Found struct {
	Hits    []Hit
	Matched int
}

// Find is [Store.Search] with the count of everything the query matched,
// before the limit.
func (s *Store) Find(query string, project *string, kind string, limit int) (Found, error) {
	now := s.now()
	hits, matched, err := s.recall(query, project, kind, min(max(limit, 1), MaxSearchLimit), now)
	if err != nil || len(hits) == 0 {
		return Found{Hits: hits, Matched: matched}, err
	}
	ids := make([]int64, len(hits))
	for i, h := range hits {
		ids[i] = h.ID
	}
	// Noting what was returned is bookkeeping, which only keeps a memory from
	// fading. When it cannot be written — a read-only store, a writer holding the
	// lock past the busy timeout — the search has still found its memories, and
	// answers with them: failing here would hand an agent an error in place of
	// what it asked for, and it would read that as there being nothing.
	if s.readOnly == nil {
		_ = s.recordHits(ids, now)
	}
	return Found{Hits: hits, Matched: matched}, nil
}

func (s *Store) recall(query string, project *string, kind string, limit int, now uint64) ([]Hit, int, error) {
	terms := searchTerms(query)
	if len(terms) == 0 || limit <= 0 {
		return []Hit{}, 0, nil
	}
	// The best matches are picked inside FTS5 first — its `rank` column with
	// the weights set lets it keep only the top ones while it scans — and
	// joined to their rows after. Archived and superseded rows are dropped
	// after the cut, so the cut takes twice what is scored.
	rows, err := s.db.Query(`SELECT `+recordColumns+`, best.score
               FROM (SELECT memory_id, rank AS score FROM memories_fts
                      WHERE memories_fts MATCH ?1 AND rank MATCH ?2
                      ORDER BY rank LIMIT ?3) AS best
               JOIN memories m ON m.id = best.memory_id
              WHERE m.state = 'active' AND NOT `+replaced+` AND (?5 = '' OR m.kind = ?5)
              ORDER BY best.score
              LIMIT ?4`, matchQuery(terms), bm25Rank, 2*candidateLimit, candidateLimit, kind)
	if err != nil {
		return nil, 0, err
	}
	type candidate struct {
		Record
		bm25 float64
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		r, err := scanRecord(rows, &c.bm25)
		if err != nil {
			rows.Close()
			return nil, 0, err
		}
		c.Record = r
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	// How many the query matched in all, before the limit — asked after the
	// rows are closed, since the store has one connection.
	var matched int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM memories_fts
                              JOIN memories m ON m.id = memories_fts.memory_id
                             WHERE memories_fts MATCH ?1 AND m.state = 'active'
                               AND NOT `+replaced+` AND (?2 = '' OR m.kind = ?2)`,
		matchQuery(terms), kind).Scan(&matched); err != nil {
		return nil, 0, err
	}
	best := 0.0
	for _, c := range candidates {
		best = min(best, c.bm25)
	}
	var corpus int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM memories m WHERE m.state = 'active' AND NOT " + replaced).Scan(&corpus); err != nil {
		return nil, 0, err
	}
	if corpus >= weakMatchCorpus {
		// bm25 is negative, stronger is lower: keep what is at least weakMatch
		// of the best.
		candidates = slices.DeleteFunc(candidates, func(c candidate) bool { return c.bm25 > best*weakMatch })
	}
	ids := make([]int64, len(candidates))
	for i, c := range candidates {
		ids[i] = c.ID
	}
	allCounts, err := countsOf(s.db, ids, now)
	if err != nil {
		return nil, 0, err
	}
	hits := make([]Hit, 0, len(candidates))
	for _, c := range candidates {
		counts := allCounts[c.ID]
		days := daysUnused(c.Record, counts, now)
		score := Score(Signals{
			Relevance:    Relevance(c.bm25, best),
			Project:      relateProjects(c.Project, project),
			Uses:         counts.Uses,
			Verified:     counts.Verified,
			Contradicted: counts.Contradicted,
			DaysSinceUse: days,
		})
		hits = append(hits, Hit{Record: c.Record, Score: score, Decay: Decay(days), Counts: counts})
	}
	slices.SortStableFunc(hits, func(a, b Hit) int {
		if a.Score != b.Score {
			if a.Score > b.Score {
				return -1
			}
			return 1
		}
		return int(int64(b.CreatedAt) - int64(a.CreatedAt))
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, matched, nil
}

// Entry is one memory with its counts and decay.
func (s *Store) Entry(id int64) (Hit, error) {
	r, err := load(s.db, id)
	if err != nil {
		return Hit{}, err
	}
	hits, err := withCounts(s.db, []Record{r}, s.now())
	if err != nil {
		return Hit{}, err
	}
	return hits[0], nil
}

// MaxListLimit bounds one list.
const MaxListLimit = 200

// ListQuery is what a list asks for; empty fields are any.
type ListQuery struct {
	Archived bool
	// All lists the memories a newer one replaced too, marked as such; by
	// default a list shows what is current, as a search does.
	All     bool
	Kind    string
	Project string
	Limit   int
}

// List is the memories q asks for, newest first, each with its counts and
// decay. A list is for a person looking through the store, so it records no
// hits.
func (s *Store) List(q ListQuery) ([]Hit, error) {
	limit := q.Limit
	if limit <= 0 || limit > MaxListLimit {
		limit = MaxListLimit
	}
	state := StateActive
	if q.Archived {
		state = StateArchived
	}
	rows, err := s.db.Query(`SELECT `+recordColumns+` FROM memories m
                              WHERE m.state = ?1 AND (?2 = '' OR m.kind = ?2)
                                AND (?3 OR NOT `+replaced+`)
                              ORDER BY m.created_at DESC, m.id DESC`, state, q.Kind, q.All)
	if err != nil {
		return nil, err
	}
	var records []Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		// Projects are compared as paths, which SQL cannot do.
		if q.Project != "" && (r.Project == nil || !SameProject(*r.Project, q.Project)) {
			continue
		}
		records = append(records, r)
		if len(records) == limit {
			break
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return withCounts(s.db, records, s.now())
}

func withCounts(q queryer, records []Record, now uint64) ([]Hit, error) {
	ids := make([]int64, len(records))
	for i, r := range records {
		ids[i] = r.ID
	}
	all, err := countsOf(q, ids, now)
	if err != nil {
		return nil, err
	}
	hits := make([]Hit, len(records))
	for i, r := range records {
		counts := all[r.ID]
		hits[i] = Hit{Record: r, Decay: Decay(daysUnused(r, counts, now)), Counts: counts}
	}
	return hits, nil
}

// countsOf is each memory's event counts over the stats window and the last
// time it was returned, used or verified, in one query; a memory with no
// events is absent (zero Counts).
func countsOf(q queryer, ids []int64, now uint64) (map[int64]Counts, error) {
	out := make(map[int64]Counts, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	window := int64(now) - statsWindowDays*86400
	args := []any{window, HistoryVerified, HistoryContradicted}
	// marks adds values to the arguments and returns their placeholders.
	marks := func(n int, value func(int) any) string {
		out := make([]string, n)
		for i := range out {
			args = append(args, value(i))
			out[i] = "?" + strconv.Itoa(len(args))
		}
		return strings.Join(out, ", ")
	}
	isUse := "kind IN (" + marks(len(useKinds), func(i int) any { return useKinds[i] }) + ")"
	isLive := "kind IN (" + marks(len(liveKinds), func(i int) any { return liveKinds[i] }) + ")"
	inIDs := marks(len(ids), func(i int) any { return ids[i] })
	rows, err := q.Query(`SELECT memory_id,
              COALESCE(SUM(CASE WHEN at >= ?1 AND `+isUse+` THEN 1 ELSE 0 END), 0),
              COALESCE(SUM(CASE WHEN at >= ?1 AND kind = ?2 THEN 1 ELSE 0 END), 0),
              COALESCE(SUM(CASE WHEN at >= ?1 AND kind = ?3 THEN 1 ELSE 0 END), 0),
              MAX(CASE WHEN `+isLive+` THEN at END)
            FROM memory_events WHERE memory_id IN (`+inIDs+`)
            GROUP BY memory_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id, uses, verified, contradicted int
			last                             sql.NullInt64
		)
		if err := rows.Scan(&id, &uses, &verified, &contradicted, &last); err != nil {
			return nil, err
		}
		out[int64(id)] = Counts{Uses: uses, Verified: verified, Contradicted: contradicted, LastUsed: optionalU64(last)}
	}
	return out, rows.Err()
}

// daysUnused is how long a memory has gone without a use: since its last
// use, or since it was written when it never was.
func daysUnused(r Record, counts Counts, now uint64) float64 {
	since := r.CreatedAt
	if counts.LastUsed != nil {
		since = max(since, *counts.LastUsed)
	}
	if now <= since {
		return 0
	}
	return float64(now-since) / 86400
}

// eventRetentionDays is how long a return from a search is kept; a memory's
// latest use is kept regardless, since decay runs from it.
const eventRetentionDays = 180

// Prune drops returns from a search older than eventRetentionDays, but never a
// memory's latest use. Cheap; a write runs it, which is rare, and nothing has
// to ask for it.
func (s *Store) Prune() error {
	cut := int64(s.now()) - eventRetentionDays*86400
	return s.inTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM memory_events
                            WHERE at < ?1 AND kind = ?2
                              AND id NOT IN (SELECT id FROM (
                                  SELECT id, ROW_NUMBER() OVER (PARTITION BY memory_id ORDER BY at DESC) AS n
                                    FROM memory_events WHERE kind IN (?3, ?4, ?5)) WHERE n = 1)`,
			cut, HistoryHit, liveKinds[0], liveKinds[1], liveKinds[2])
		return err
	})
}
