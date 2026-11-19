package store

import (
	"database/sql"
	"fmt"
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

// usedKinds are the events that count as a use, for the use count and for
// when decay starts.
var usedKinds = []string{HistoryHit, HistoryUsed, HistoryVerified}

// Three verifications and no contradiction promote an observed memory; a
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
		if old.State != StateActive || old.SupersededBy != nil {
			return 0, invalid("the memory it supersedes is archived or already superseded")
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
                 (title, content, kind, keywords, confidence, project, supersedes, state, created_at)
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, 'active', ?8)`,
		nullIfEmpty(d.Title), d.Content, d.Kind, strings.Join(d.Keywords, " "),
		ConfidenceObserved, d.Project, intArg(d.Supersedes), clampI64(now),
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, index(tx, id, d.Title, d.Keywords, d.Content)
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
// three different days — with no contradiction promote an observed one.
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
				counts, err := countsOf(tx, []int64{id}, now)
				if err != nil {
					return err
				}
				c := counts[id]
				if confidence == ConfidenceObserved && c.Verified >= promoteVerifiedN && c.Contradicted == 0 {
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
func index(tx *sql.Tx, id int64, title string, keywords []string, content string) error {
	_, err := tx.Exec("INSERT INTO memories_fts (memory_id, title, keywords, body) VALUES (?1, ?2, ?3, ?4)",
		id, indexText(title), indexText(strings.Join(keywords, " ")), indexText(content))
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
