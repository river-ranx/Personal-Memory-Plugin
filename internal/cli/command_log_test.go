package cli

import (
	"database/sql"
	"slices"
	"testing"
)

type logRow struct {
	Command, Query, IDs string
	OK                  bool
}

// logRows is the command log as it stands, oldest first.
func (r run) logRows() []logRow {
	r.t.Helper()
	db, err := sql.Open("sqlite", r.home+"/memory.db")
	if err != nil {
		r.t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT command, COALESCE(query, ''), ids, ok FROM command_log ORDER BY id`)
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	var out []logRow
	for rows.Next() {
		var l logRow
		var ok int
		if err := rows.Scan(&l.Command, &l.Query, &l.IDs, &ok); err != nil {
			r.t.Fatal(err)
		}
		l.OK = ok == 1
		out = append(out, l)
	}
	return out
}

// Every command that runs leaves one row, whatever it was and however it
// ended; a row holds the ids the command was given or returned, and a search
// also the words it was asked.
func TestEveryCommandLeavesARowWithTheIdsItTouched(t *testing.T) {
	r := newRun(t)
	r.add("提交信息用中文，前缀照 git log 的写法")   // #1
	r.add("发版只走 release 分支，不直接推 main") // #2

	r.memory("", "search", "提交信息")                                   // returns #1
	r.memory("", "search", "zzzz", "qqqq")                           // finds nothing
	r.memory("", "show", "2")                                        // #2
	r.memory("", "list")                                             // both, newest first
	r.memory("", "edit", "1", "--kind", "rule")                      // #1
	r.memory("", "confirm", "1", "2")                                // both
	r.memory("", "add", "提交信息用中文，前缀照 git log 的写法", "--kind", "fact") // a repeat of #1
	r.memory("", "archive", "2")                                     // #2
	r.memory("", "restore", "2")                                     // #2
	r.memory("", "delete", "2")                                      // #2
	r.memory("", "show", "99")                                       // no such memory

	want := []logRow{
		{"add", "", "[1]", true},
		{"add", "", "[2]", true},
		{"search", "提交信息", "[1]", true},
		{"search", "zzzz qqqq", "[]", true},
		{"show", "", "[2]", true},
		{"list", "", "[2,1]", true},
		{"edit", "", "[1]", true},
		{"confirm", "", "[1,2]", true},
		{"add", "", "[1]", false},
		{"archive", "", "[2]", true},
		{"restore", "", "[2]", true},
		{"delete", "", "[2]", true},
		{"show", "", "[99]", false},
	}
	if got := r.logRows(); !slices.Equal(got, want) {
		t.Fatalf("command log:\n got %v\nwant %v", got, want)
	}
}

// A command refused before the database is opened leaves no row, and the log
// does not change what a command answers.
func TestAMistypedFlagIsNotLogged(t *testing.T) {
	r := newRun(t)
	r.add("发版只走 release 分支，不直接推 main")
	before := len(r.logRows())
	if code, _ := r.memory("", "search", "发版", "--limt", "3"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if after := len(r.logRows()); after != before {
		t.Fatalf("%d rows before, %d after", before, after)
	}
}
