package store

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Event kinds: what memory_events records, which is how each memory was
// used. Counts, the last use and decay are all read from these, never kept
// beside them.
const (
	HistoryHit          = "hit"      // returned to an agent's search
	HistoryVerified     = "verified" // an agent checked it against the source
	HistoryUsed         = "used"     // an agent relied on it and saw no conflict
	HistoryContradicted = "contradicted"
)

// liveKinds keep a memory from fading: returned to a search, used, verified.
// Decay runs from the latest of them.
var liveKinds = []string{HistoryHit, HistoryUsed, HistoryVerified}

// useKinds count as a use, which raises a memory's rank: only what an agent
// reports with confirm. Being returned to a search is not a use, or a memory
// that matches often would rank itself up for matching.
var useKinds = []string{HistoryUsed, HistoryVerified}

// Three verifications since the last contradiction (or ever, if none), on
// three different days however far apart, promote an observed memory; a
// single contradiction demotes a verified one at once.
const promoteVerifiedN = 3

// Write stores d. Every write passes the gate ([screen]) and, unless it
// supersedes a memory, the duplicate check — both inside [insert], so no
// path skips them. A draft too short to say anything is an [ErrRefused], a
// repeat a *[Duplicate]; either way nothing is stored.
func (s *Store) Write(d Draft) (Record, error) {
	var id int64
	err := s.inTx(func(tx *sql.Tx) error {
		var err error
		id, err = insert(tx, d, s.now())
		return err
	})
	if err != nil {
		return Record{}, err
	}
	return load(s.db, id)
}

// insert is [Store.Write] inside a transaction the caller owns.
func insert(tx *sql.Tx, d Draft, now uint64) (int64, error) {
	if d.Supersedes != nil {
		old, err := load(tx, *d.Supersedes)
		if err != nil {
			return 0, err
		}
		if old.State != StateActive {
			return 0, invalid("%s is archived: restore it first, or write a new memory without --supersedes", FormatID(old.ID))
		}
		if old.SupersededBy != nil {
			return 0, invalid("%s was replaced by %s: supersede %s instead", FormatID(old.ID), FormatID(*old.SupersededBy), FormatID(*old.SupersededBy))
		}
		// An agent rewording a memory says the new text; what it was, and
		// the words it was found by, carry over.
		d = d.inherit(old)
	}
	d, err := d.normalize()
	if err != nil {
		return 0, err
	}
	if err := screen(d); err != nil {
		return 0, err
	}
	if d.Supersedes == nil {
		existing, err := duplicates(tx, d)
		if err != nil {
			return 0, err
		}
		if len(existing) > 0 {
			return 0, &Duplicate{Existing: existing}
		}
	}
	res, err := tx.Exec(`INSERT INTO memories
                 (content, kind, keywords, confidence, project, supersedes, state, created_at)
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, 'active', ?7)`,
		d.Content, d.Kind, strings.Join(d.Keywords, " "),
		ConfidenceObserved, d.Project, intArg(d.Supersedes), clampI64(now),
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, index(tx, id, d.Keywords, d.Content)
}

// SetArchived archives or restores a memory. Archived, it is never retrieved.
func (s *Store) SetArchived(id int64, archived bool) error {
	state := StateActive
	if archived {
		state = StateArchived
	}
	res, err := s.db.Exec("UPDATE memories SET state = ?2 WHERE id = ?1", id, state)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, FormatID(id))
	}
	return nil
}

// Replaces is the memory id was written to replace, if any.
func (s *Store) Replaces(id int64) (int64, bool, error) {
	var old sql.NullInt64
	err := s.db.QueryRow("SELECT supersedes FROM memories WHERE id = ?1", id).Scan(&old)
	if err != nil || !old.Valid {
		return 0, false, nil
	}
	return old.Int64, true, nil
}

// IsCurrent is whether memory id is one a search returns: active, and not
// replaced by a memory in use. A memory that does not exist is not.
func (s *Store) IsCurrent(id int64) (bool, error) {
	r, err := load(s.db, id)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil && r.State == StateActive && r.SupersededBy == nil, err
}

// Delete removes a memory, its index entry and its history: a deleted
// memory leaves nothing behind.
func (s *Store) Delete(id int64) error {
	return s.inTx(func(tx *sql.Tx) error {
		if _, err := load(tx, id); err != nil {
			return err
		}
		if err := unindex(tx, id); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM memory_events WHERE memory_id = ?1", id); err != nil {
			return err
		}
		_, err := tx.Exec("DELETE FROM memories WHERE id = ?1", id)
		return err
	})
}

// Edit is what [Store.Update] changes about a memory. A field left out keeps
// what the memory has: Content nil, Kind empty, Keywords and AddKeywords nil.
// Keywords that are not nil replace the list, so an empty one clears it;
// AddKeywords are put after the ones it has, a word it already has once only,
// and what does not fit the limit is left out (see [CleanKeywords]). Giving
// both is for the caller to refuse: Keywords would win.
type Edit struct {
	Content     *string
	Kind        string
	Keywords    []string
	AddKeywords []string
}

// Update corrects a memory in place: same id, same history, same age. It is
// for a memory written wrong — another kind, a missing keyword, a typo — not
// for what has changed since, which is a new memory superseding this one (the
// old one stays as it was, and the new one is dated today).
//
// Only new text passes the gate and the duplicate check: a memory that is
// short or repeats another does not stop its kind being put right. An
// archived memory stays archived, and a replaced one stays replaced.
func (s *Store) Update(id int64, e Edit) (Record, error) {
	err := s.inTx(func(tx *sql.Tx) error {
		old, err := load(tx, id)
		if err != nil {
			return err
		}
		d := Draft{Content: old.Content, Kind: old.Kind, Keywords: old.Keywords, Project: old.Project}
		if e.Content != nil {
			d.Content = *e.Content
		}
		if e.Kind != "" {
			d.Kind = e.Kind
		}
		if e.Keywords != nil {
			d.Keywords = e.Keywords
		} else if len(e.AddKeywords) > 0 {
			d.Keywords = append(slices.Clone(old.Keywords), e.AddKeywords...)
		}
		d, err = d.normalize()
		if err != nil {
			return err
		}
		if d.Content != old.Content {
			if err := screen(d); err != nil {
				return err
			}
			existing, err := duplicates(tx, d)
			if err != nil {
				return err
			}
			same := existing[:0]
			for _, r := range existing {
				if r.ID != id {
					same = append(same, r)
				}
			}
			if len(same) > 0 {
				return &Duplicate{Existing: same}
			}
		}
		_, err = tx.Exec(`UPDATE memories SET content = ?2, kind = ?3, keywords = ?4 WHERE id = ?1`,
			id, d.Content, d.Kind, strings.Join(d.Keywords, " "))
		if err != nil {
			return err
		}
		if err := unindex(tx, id); err != nil {
			return err
		}
		return index(tx, id, d.Keywords, d.Content)
	})
	if err != nil {
		return Record{}, err
	}
	return load(s.db, id)
}

// Outcome is what an agent found when it relied on or checked a memory.
type Outcome string

const (
	OutcomeVerified     Outcome = HistoryVerified
	OutcomeUsed         Outcome = HistoryUsed
	OutcomeContradicted Outcome = HistoryContradicted
)

// Confirmation is what a [Store.Confirm] did with the ids it was given.
type Confirmation struct {
	// Missing are ids no memory has.
	Missing []int64
	// Repeated are memories already reported the same way today: counted once.
	Repeated []int64
}

// Confirm records outcome for each id and adjusts confidence: a
// contradiction demotes a verified memory at once; three verifications — on
// three different days — since the last contradiction promote an observed one.
// A memory is reported once a day per outcome (see [reportInterval]); a repeat
// is noted in the result, not counted.
func (s *Store) Confirm(ids []int64, outcome Outcome, note string) (Confirmation, error) {
	var done Confirmation
	switch outcome {
	case OutcomeVerified, OutcomeUsed, OutcomeContradicted:
	default:
		return done, invalid("outcome must be verified, used or contradicted")
	}
	now := s.now()
	err := s.inTx(func(tx *sql.Tx) error {
		for _, id := range ids {
			current, err := load(tx, id)
			if err != nil {
				done.Missing = append(done.Missing, id)
				continue
			}
			again, err := reported(tx, id, string(outcome), now)
			if err != nil {
				return err
			}
			if again {
				done.Repeated = append(done.Repeated, id)
				continue
			}
			if err := event(tx, id, string(outcome), now, note); err != nil {
				return err
			}
			confidence := current.Confidence
			switch outcome {
			case OutcomeContradicted:
				if confidence == ConfidenceVerified {
					confidence = ConfidenceObserved
				}
			case OutcomeVerified:
				// Every verification since the last contradiction, however long
				// ago; one a day at most, so this many are this many days.
				var since int
				if err := tx.QueryRow(`SELECT COUNT(*) FROM memory_events
				                        WHERE memory_id = ?1 AND kind = ?2
				                          AND at > COALESCE((SELECT MAX(at) FROM memory_events
				                                              WHERE memory_id = ?1 AND kind = ?3), -1)`,
					id, HistoryVerified, HistoryContradicted).Scan(&since); err != nil {
					return err
				}
				if confidence == ConfidenceObserved && since >= promoteVerifiedN {
					confidence = ConfidenceVerified
				}
			}
			if confidence != current.Confidence {
				if _, err := tx.Exec("UPDATE memories SET confidence = ?2 WHERE id = ?1", id, confidence); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return done, err
}

// reportInterval is how often one thing is noted about a memory: once a day at
// most, for each kind of event that counts toward its standing — a return from
// a search, a use, a verification. A search is a read and should not write a
// row per result every time; and a memory must not rank itself ever higher, or
// be made verified, by being asked about or confirmed over and over in one
// sitting: the counts weigh days of use, not calls.
const reportInterval = 86400

// reported is whether memory id already has an event of kind in the last day.
func reported(tx *sql.Tx, id int64, kind string, now uint64) (bool, error) {
	var recent int
	err := tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM memory_events
                                        WHERE memory_id = ?1 AND kind = ?2 AND at > ?3)`,
		id, kind, int64(now)-reportInterval).Scan(&recent)
	return recent == 1, err
}

// recordHits notes that ids were returned to a search, which is what keeps
// them from decaying.
func (s *Store) recordHits(ids []int64, now uint64) error {
	if len(ids) == 0 {
		return nil
	}
	return s.inTx(func(tx *sql.Tx) error {
		for _, id := range ids {
			again, err := reported(tx, id, HistoryHit, now)
			if err != nil {
				return err
			}
			if !again {
				if err := event(tx, id, HistoryHit, now, ""); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// index adds a memory's tokens to the full-text index.
func index(tx *sql.Tx, id int64, keywords []string, content string) error {
	_, err := tx.Exec("INSERT INTO memories_fts (memory_id, keywords, body) VALUES (?1, ?2, ?3)",
		id, indexText(strings.Join(keywords, " ")), indexText(content))
	return err
}

func unindex(tx *sql.Tx, id int64) error {
	_, err := tx.Exec("DELETE FROM memories_fts WHERE memory_id = ?1", id)
	return err
}

func event(tx *sql.Tx, id int64, kind string, now uint64, note string) error {
	_, err := tx.Exec(`INSERT INTO memory_events (memory_id, at, kind, note) VALUES (?1, ?2, ?3, ?4)`,
		id, clampI64(now), kind, nullIfEmpty(note))
	return err
}
