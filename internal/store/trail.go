package store

import (
	"encoding/json"
	"slices"
)

// The command log: one row for every `memory` command that ran, whatever it
// was — a search, a write, an edit, a confirm, a show or list, an archive, a
// delete. It keeps no text of any memory, only their ids, so the log says
// which memories a command was about or returned and nothing about what they
// say; a search that found nothing is a row with no ids, which is how a
// question the memory could not answer is told from one it could.
//
// Every command passes through one place (the dispatcher) and every store
// operation notes the ids it was given or returned in [Store.touch], so a
// command cannot run without a row and no command has its own logging to forget.

// CommandRun is one run of a command, as the log keeps it.
type CommandRun struct {
	// Command is the command's name: search, add, edit, confirm, show, list,
	// archive, restore or delete.
	Command string
	// Project is the project the command ran for; nil if it had none.
	Project *string
	// Query is the words a search was given; empty for every other command.
	Query string
	// OK is whether the command did what it was asked. A refusal, a repeat, a
	// misuse and an error are not.
	OK bool
}

// touch notes ids as what the command now running was about: the ids it was
// given, the ones it returned, the one it wrote. Each is noted once, in the
// order first met.
func (s *Store) touch(ids ...int64) {
	for _, id := range ids {
		if !slices.Contains(s.touched, id) {
			s.touched = append(s.touched, id)
		}
	}
}

// LogCommand writes the row for the command that has just run, with the ids
// noted since the last one. Like noting a search's hits it is bookkeeping: a
// read-only store writes nothing, and a failure to write is the caller's to
// ignore — the command's own answer stands.
func (s *Store) LogCommand(run CommandRun) error {
	ids := s.touched
	s.touched = nil
	if s.readOnly != nil {
		return nil
	}
	if ids == nil {
		ids = []int64{}
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	ok := 0
	if run.OK {
		ok = 1
	}
	_, err = s.db.Exec(`INSERT INTO command_log (at, command, project, query, ids, ok)
                             VALUES (?1, ?2, ?3, ?4, ?5, ?6)`,
		clampI64(s.now()), run.Command, run.Project, nullIfEmpty(run.Query), string(encoded), ok)
	return err
}
