// Package cli is the `memory` command line: how an agent (taught by the
// memory skill) uses the local memory, and the entry the agents' hook calls.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"memory/internal/hooks"
	"memory/internal/install"
	"memory/internal/store"
	"memory/internal/update"
)

// Env is what the command reads from its surroundings; a seam for tests.
type Env struct {
	Getenv func(string) string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Cwd    string
}

// ErrReported is an error whose explanation is already on stdout.
var ErrReported = errors.New("reported")

// ExitUnavailable is the exit code of a command that could not use the memory
// database at all. It is not 1, which says "not done, here is why": an agent
// that reads an unusable database as an empty one goes on as if nothing had
// ever been remembered.
const ExitUnavailable = 2

// Main runs one command and returns the process's exit code. Any command but
// those that set memory up starts the daily background update check, if due.
func Main(args []string, env Env) int {
	if len(args) > 0 && !slices.Contains([]string{"update", "install", "uninstall"}, args[0]) {
		if path, err := DBPath(env.Getenv); err == nil {
			update.Due(filepath.Dir(path), env.Getenv)
		}
	}
	if err := dispatch(args, env); err != nil {
		if !errors.Is(err, ErrReported) {
			fmt.Fprintln(env.Stderr, "memory:", err)
		}
		var unavailable *store.UnavailableError
		if errors.As(err, &unavailable) {
			return ExitUnavailable
		}
		return 1
	}
	return 0
}

// DBPath is where the memory lives: $MEMORY_HOME/memory.db, by default
// ~/.memory/memory.db.
func DBPath(getenv func(string) string) (string, error) {
	if home := getenv("MEMORY_HOME"); home != "" {
		return filepath.Join(home, "memory.db"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".memory", "memory.db"), nil
}

func dispatch(args []string, env Env) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(env.Stdout, help())
		return nil
	}
	if args[0] == "help" {
		text, err := helpFor(args[1:])
		if err != nil {
			return err
		}
		fmt.Fprint(env.Stdout, text)
		return nil
	}
	// `memory <command> --help`: the command's own help, before anything is
	// opened or parsed.
	if wantsHelp(args[1:]) {
		if text, err := helpFor(args[:1]); err == nil {
			fmt.Fprint(env.Stdout, text)
			return nil
		}
	}
	// open is the database for a command: one that only reads (reads) is given
	// a read-only one where the file cannot be written, and says so on stderr,
	// since a search that notes nothing is not quite the search it was.
	open := func(reads bool) (*store.Store, error) {
		path, err := DBPath(env.Getenv)
		if err != nil {
			return nil, err
		}
		if !reads {
			return store.Open(path)
		}
		s, err := store.OpenForRead(path)
		if err == nil && s.ReadOnly() != nil {
			why := s.ReadOnly()
			var unavailable *store.UnavailableError
			if errors.As(why, &unavailable) {
				why = unavailable.Err
			}
			fmt.Fprintf(env.Stderr, "memory: note: %s cannot be written here (%v), so it was opened read-only: results are complete, but nothing is noted about them.\n", path, why)
		}
		return s, err
	}
	switch args[0] {
	case "version", "--version":
		fmt.Fprintln(env.Stdout, "memory "+update.Version)
		return nil
	case "update":
		return selfUpdate(args[1:], env)
	case "hook":
		if len(args) != 3 {
			return errors.New("usage: memory hook <agent> <event> (the agents' hook configs call this)")
		}
		hooks.Run(args[1], args[2], env.Stdin, env.Stdout, env.Stderr, env.Getenv)
		return nil
	case "install", "uninstall":
		if len(args) < 2 {
			return fmt.Errorf("usage: memory %s <agent...> (agents: %s)", args[0], strings.Join(hooks.Agents, ", "))
		}
		for _, agent := range args[1:] {
			var err error
			var changed []string
			if args[0] == "install" {
				changed, err = install.Install(agent, env.Getenv)
			} else {
				changed, err = install.Uninstall(agent, env.Getenv)
			}
			if err != nil {
				return fmt.Errorf("%s: %w", agent, err)
			}
			for _, c := range changed {
				fmt.Fprintf(env.Stdout, "%s: %s\n", agent, c)
			}
		}
		return nil
	}
	cmd, ok := commands[args[0]]
	if !ok {
		return fmt.Errorf("unknown command %q. Commands: %s. Run `memory --help`", args[0], strings.Join(commandOrder, ", "))
	}
	err := func() error {
		if args[0] == "add" || args[0] == "edit" {
			// The command's own name is not a positional argument: folding it
			// in would look like the text was already given, and the words
			// after --keywords of a command with the flags first would be
			// swallowed as keywords instead of being the text.
			args = append([]string{args[0]}, foldKeywordSpill(args[1:])...)
		}
		flags, positional, err := parseFlags(args[1:])
		if err != nil {
			return err
		}
		if err := cmd.check(args[0], flags); err != nil {
			return err
		}
		s, err := open(cmd.reads)
		if err != nil {
			return err
		}
		defer s.Close()
		c := command{s: s, env: env, flags: flags, args: positional}
		runErr := cmd.run(c)
		// The one place every command passes: a row in the command log, however
		// it ended. A command refused before the database is opened (a mistyped
		// flag) never gets here, and is not logged.
		c.logRun(args[0], runErr)
		return runErr
	}()
	// A misuse is answered with how to use the command right.
	var misuse usageError
	if errors.As(err, &misuse) {
		return fmt.Errorf("%s\nusage: %s\n%s", misuse.msg, cmd.usage(args[0]), moreAbout(args[0]))
	}
	return err
}

// selfUpdate is `memory update`: upgrade to the latest release now, then set
// up again, with the new binary, every agent memory is installed for. With
// --background (how the daily check runs it) it says nothing.
func selfUpdate(args []string, env Env) error {
	out := env.Stdout
	if slices.Contains(args, "--background") {
		out = io.Discard
	}
	path, err := DBPath(env.Getenv)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return err
	}
	return update.Run(filepath.Dir(path), exe, out, func(newExe string) error {
		agents := install.Installed(env.Getenv)
		if len(agents) == 0 {
			return nil
		}
		cmd := exec.Command(newExe, append([]string{"install"}, agents...)...)
		cmd.Stdout, cmd.Stderr = out, out
		return cmd.Run()
	})
}

// usageError is a command used wrongly; the answer to it carries the
// command's usage.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usageErr(format string, args ...any) error {
	return usageError{fmt.Sprintf(format, args...)}
}

type command struct {
	s     *store.Store
	env   Env
	flags flags
	args  []string
}

// project is the one asked about: --project, else the working directory the
// command runs in.
func (c command) project() *string {
	p := c.flags.one("project")
	if p == "" {
		p = c.env.Cwd
	}
	if p = store.NormalizeProject(p); p == "" {
		return nil
	}
	return &p
}

// logRun writes the command's row in the command log. The log is bookkeeping,
// so a failure to write it is not the command's failure.
func (c command) logRun(name string, runErr error) {
	query := ""
	if name == "search" {
		query = strings.Join(c.args, " ")
	}
	_ = c.s.LogCommand(store.CommandRun{Command: name, Project: c.project(), Query: query, OK: runErr == nil})
}

func (c command) search() error {
	query := strings.Join(c.args, " ")
	if strings.TrimSpace(query) == "" {
		return usageErr("search needs the words to look for")
	}
	limit, err := c.flags.int("limit", 5)
	if err != nil {
		return err
	}
	kind, err := c.kind()
	if err != nil {
		return err
	}
	project := c.project()
	found, err := c.s.Find(query, project, kind, limit)
	if err != nil {
		return err
	}
	hits := found.Hits
	shown := min(limit, store.MaxSearchLimit)
	if c.flags.has("json") {
		return c.json(map[string]any{"memories": views(hits), "matched": found.Matched, "limit": shown})
	}
	if len(hits) == 0 {
		fmt.Fprintln(c.env.Stdout, c.noMatch(query, kind))
	}
	c.printAll(hits, project)
	if found.Matched > len(hits) {
		fmt.Fprintf(c.env.Stdout, "\n%d matched; showing %d (--limit goes up to %d).\n", found.Matched, len(hits), store.MaxSearchLimit)
	}
	return nil
}

// noMatch is what to try when a search found nothing. A one-character Chinese
// query is not a word the search has: it cuts Chinese into two-character
// windows, so 图 matches nothing however much 图 is in the store. Saying that
// is better than sending the agent to try a synonym of a query that cannot
// work.
func (c command) noMatch(query, kind string) string {
	if len([]rune(strings.TrimSpace(query))) == 1 {
		return "No memories match. A single character is too short to search with: give a two-character word (图片, 图表), or a word or two more."
	}
	if kind != "" {
		return fmt.Sprintf("No memories of kind %s match. Try other words, or leave out --kind: a synonym, the English name, another name for the thing — or fewer words.", kind)
	}
	return "No memories match. Try other words: a synonym, the English name, another name for the thing — or fewer words."
}

func (c command) add() error {
	content := strings.Join(c.args, " ")
	if content == "-" {
		b, err := io.ReadAll(c.env.Stdin)
		if err != nil {
			return err
		}
		content = string(b)
	}
	if strings.TrimSpace(content) == "" {
		return usageErr("add needs the text to remember")
	}
	d := store.Draft{
		Content: content, Kind: c.flags.one("kind"),
		Keywords: splitWords(append(c.flags.all("keywords"), c.flags.all("keyword")...)), Project: c.project(),
	}
	if v := c.flags.one("supersedes"); v != "" {
		id, err := parseID(v)
		if err != nil {
			return err
		}
		d.Supersedes = &id
		// A rewrite keeps the memory's project unless --project says otherwise:
		// the directory this happens to run in does not decide where a memory
		// belonged, and filling it in here would silently move the memory.
		if !c.flags.has("project") {
			d.Project = nil
		}
	}
	// The kind says how a later session treats the memory, and left to a
	// default it was left to chance: an agent's own reading of "the person
	// prefers…" stored as a fact, flagged out of date after a quarter. So a new
	// memory names it; one that supersedes another keeps that one's.
	var m store.Record
	var err error
	// How long ago the memory being replaced was written, before it is replaced.
	replacedAge := time.Duration(-1)
	if d.Supersedes != nil {
		if old, err := c.s.Entry(*d.Supersedes); err == nil {
			replacedAge = time.Since(time.Unix(int64(old.CreatedAt), 0))
		}
	}
	if strings.TrimSpace(d.Kind) == "" && d.Supersedes == nil {
		err = fmt.Errorf("%w: say which kind of memory this is with --kind:\n%s", store.ErrInvalid, kindGuide)
	} else {
		m, err = c.s.Write(d)
	}
	var duplicate *store.Duplicate
	switch {
	case errors.As(err, &duplicate):
		existing := make([]store.Hit, len(duplicate.Existing))
		for i, r := range duplicate.Existing {
			existing[i] = store.Hit{Record: r, Decay: 1}
		}
		if c.flags.has("json") {
			_ = c.json(map[string]any{"error": "duplicate", "message": duplicate.Error(), "existing": views(existing)})
			return ErrReported
		}
		fmt.Fprintln(c.env.Stdout, "Not stored: "+duplicate.Error()+".")
		c.printAll(existing, d.Project)
		return ErrReported
	case errors.Is(err, store.ErrRefused), errors.Is(err, store.ErrInvalid), errors.Is(err, store.ErrNotFound):
		code := "invalid"
		if errors.Is(err, store.ErrRefused) {
			code = "refused"
		}
		if c.flags.has("json") {
			_ = c.json(map[string]any{"error": code, "message": err.Error()})
			return ErrReported
		}
		fmt.Fprintln(c.env.Stdout, "Not stored: "+err.Error())
		return ErrReported
	case err != nil:
		return err
	}
	// Housekeeping rides on a write, which is rare; a failure is retried at the next.
	_ = c.s.Prune()
	// Keywords that only repeat the text are stored all the same; saying so
	// teaches the next write what a keyword is for.
	inText := store.KeywordsInText(m.Content, d.Keywords)
	_, dropped := store.CleanKeywords(d.Keywords)
	length := utf8.RuneCountInString(m.Content)
	hints := store.WriteHints(m.Content)
	// A memory replaced soon after it was written was most likely written wrong,
	// or too early; a fix to that is an edit, which keeps one memory and its id.
	// Said, not refused: what it says may really have changed since.
	recentlyReplaced := d.Supersedes != nil && replacedAge >= 0 && replacedAge < recentWrite
	if c.flags.has("json") {
		answer := map[string]any{"memory": view(store.Hit{Record: m, Decay: 1})}
		if len(hints) > 0 {
			answer["hints"] = hintViews(hints, m.ID)
		}
		if recentlyReplaced {
			answer["replacedRecently"] = map[string]any{"id": *d.Supersedes, "minutes": int(replacedAge / time.Minute)}
		}
		if len(inText) > 0 {
			answer["keywordsInText"] = inText
		}
		if len(dropped) > 0 {
			answer["keywordsDropped"] = dropped
		}
		if length > store.LongContent {
			answer["tooLong"] = length
		}
		return c.json(answer)
	}
	fmt.Fprintf(c.env.Stdout, "Stored %s.\n", store.FormatID(m.ID))
	if d.Supersedes != nil {
		// A rewrite keeps the project the memory belonged to, whatever
		// directory it runs in; say so when that is not where it runs, since
		// the answer would otherwise leave it to be assumed.
		if m.Project != nil && !store.SameProject(*m.Project, c.env.Cwd) {
			fmt.Fprintf(c.env.Stdout, "It replaces %s and keeps its project: %s.\n", store.FormatID(*d.Supersedes), *m.Project)
		}
	}
	if len(dropped) > 0 {
		fmt.Fprintf(c.env.Stdout, "Keywords past the limit of %d were not stored: %s.\n", store.KeywordLimit, strings.Join(dropped, ", "))
	}
	if len(inText) > 0 {
		fmt.Fprintf(c.env.Stdout, "Keywords already in the text add nothing to finding it: %s. Next time give words the text lacks — a synonym, the name in another language, an abbreviation.\n", strings.Join(inText, ", "))
	}
	if length > store.LongContent {
		fmt.Fprintf(c.env.Stdout, "This memory is %d characters, long for one thing. If it says more than one thing, split it: cut this one down with `memory edit %d --content …` and add the rest as its own memory. Keep the conclusion, the way to do it and the reason; leave out the process.\n", length, m.ID)
	}
	for _, h := range hintViews(hints, m.ID) {
		fmt.Fprintln(c.env.Stdout, h.Text)
	}
	if recentlyReplaced {
		fmt.Fprintf(c.env.Stdout, "%s was written only %d min ago. If it was written wrong or too early, `memory edit %d --content …` corrects it in place and keeps one memory; supersede when what it says has changed since.\n",
			store.FormatID(*d.Supersedes), int(replacedAge/time.Minute), *d.Supersedes)
	}
	return nil
}

// recentWrite is how new a memory is that a replacement of it is noted: what is
// replaced within the hour was most likely never settled.
const recentWrite = time.Hour

// hintView is a write hint as --json gives it.
type hintView struct {
	Code string `json:"code"`
	Text string `json:"text"`
}

// hintViews are the hints with the memory's own id where they name the command
// that fixes it.
func hintViews(hints []store.Hint, id int64) []hintView {
	out := make([]hintView, len(hints))
	for i, h := range hints {
		out[i] = hintView{h.Code, strings.ReplaceAll(h.Text, "<id>", strconv.FormatInt(id, 10))}
	}
	return out
}

func (c command) confirm() error {
	ids, err := c.ids("confirm needs the ids of the memories")
	if err != nil {
		return err
	}
	// Relying on a memory and seeing no conflict is what an agent usually
	// has to report.
	outcome := c.flags.one("outcome")
	if outcome == "" {
		outcome = string(store.OutcomeUsed)
	}
	done, err := c.s.Confirm(ids, store.Outcome(outcome), c.flags.one("note"))
	if err != nil {
		return err
	}
	if c.flags.has("json") {
		return c.json(map[string]any{"missing": orEmpty(done.Missing), "repeated": orEmpty(done.Repeated)})
	}
	// A memory counts once a day for each outcome: say so, rather than let an
	// agent think a repeat did something.
	var said []string
	if len(done.Missing)+len(done.Repeated) < len(ids) {
		said = append(said, "Recorded.")
	}
	if len(done.Repeated) > 0 {
		said = append(said, "Already reported today, counted once: "+idList(done.Repeated)+".")
	}
	if len(done.Missing) > 0 {
		said = append(said, "No memory has these ids: "+idList(done.Missing)+".")
	}
	fmt.Fprintln(c.env.Stdout, strings.Join(said, " "))
	return nil
}

func orEmpty(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}

func idList(ids []int64) string {
	text := make([]string, len(ids))
	for i, id := range ids {
		text[i] = store.FormatID(id)
	}
	return strings.Join(text, ", ")
}

func (c command) show() error {
	if len(c.args) != 1 {
		return usageErr("show takes one memory id")
	}
	id, err := parseID(c.args[0])
	if err != nil {
		return err
	}
	h, err := c.s.Entry(id)
	if err != nil {
		return err
	}
	if c.flags.has("json") {
		return c.json(map[string]any{"memory": detailed(h)})
	}
	c.printAll([]store.Hit{h}, c.project())
	fmt.Fprintf(c.env.Stdout, "keywords: %s · last 30 days: used %d, verified %d, contradicted %d\n",
		orDash(strings.Join(h.Keywords, ", ")), h.Uses, h.Verified, h.Contradicted)
	return nil
}

func (c command) list() error {
	limit, err := c.flags.int("limit", 50)
	if err != nil {
		return err
	}
	kind, err := c.kind()
	if err != nil {
		return err
	}
	hits, err := c.s.List(store.ListQuery{
		Archived: c.flags.has("archived"), All: c.flags.has("all"), Kind: kind, Project: c.flags.one("project"), Limit: limit,
	})
	if err != nil {
		return err
	}
	if c.flags.has("json") {
		detailedHits := make([]detailedView, len(hits))
		for i, h := range hits {
			detailedHits[i] = detailed(h)
		}
		return c.json(map[string]any{"memories": detailedHits})
	}
	if len(hits) == 0 {
		if kind != "" {
			fmt.Fprintf(c.env.Stdout, "No memories of kind %s.\n", kind)
		} else {
			fmt.Fprintln(c.env.Stdout, "No memories.")
		}
	}
	c.printAll(hits, c.project())
	return nil
}

// edit corrects a memory in place: same id, same history, same project. It is
// for what was written wrong, not for what has changed.
func (c command) edit() error {
	if len(c.args) != 1 {
		return usageErr("edit takes one memory id")
	}
	id, err := parseID(c.args[0])
	if err != nil {
		return err
	}
	e := store.Edit{Kind: c.flags.one("kind")}
	if e.Kind != "" {
		if e.Kind, err = c.kind(); err != nil {
			return err
		}
	}
	if text := c.flags.one("content"); text != "" {
		if text == "-" {
			b, err := io.ReadAll(c.env.Stdin)
			if err != nil {
				return err
			}
			text = string(b)
		}
		e.Content = &text
	}
	keywords := append(c.flags.all("keywords"), c.flags.all("keyword")...)
	adding := append(c.flags.all("add-keywords"), c.flags.all("add-keyword")...)
	if len(keywords) > 0 && len(adding) > 0 {
		return usageErr("give --keywords (it replaces the list) or --add-keywords (it adds to the list), not both")
	}
	if len(keywords) > 0 {
		words := splitWords(keywords)
		if len(words) == 1 && words[0] == "-" {
			words = []string{}
		}
		e.Keywords = words
	}
	if len(adding) > 0 {
		if e.AddKeywords = splitWords(adding); len(e.AddKeywords) == 0 {
			return usageErr("--add-keywords needs the words to add")
		}
	}
	if e.Content == nil && e.Kind == "" && e.Keywords == nil && e.AddKeywords == nil {
		return usageErr("edit needs something to change: --kind, --keywords, --add-keywords or --content")
	}
	m, err := c.s.Update(id, e)
	var duplicate *store.Duplicate
	switch {
	case errors.As(err, &duplicate):
		if c.flags.has("json") {
			_ = c.json(map[string]any{"error": "duplicate", "message": duplicate.Error()})
			return ErrReported
		}
		fmt.Fprintln(c.env.Stdout, "Not stored: "+duplicate.Error()+".")
		return ErrReported
	case errors.Is(err, store.ErrRefused), errors.Is(err, store.ErrInvalid), errors.Is(err, store.ErrNotFound):
		code := "invalid"
		if errors.Is(err, store.ErrRefused) {
			code = "refused"
		}
		if c.flags.has("json") {
			_ = c.json(map[string]any{"error": code, "message": err.Error()})
			return ErrReported
		}
		fmt.Fprintln(c.env.Stdout, "Not stored: "+err.Error())
		return ErrReported
	case err != nil:
		return err
	}
	// Words asked for and not kept were past the limit: said, since a keyword
	// silently lost is a way to find the memory that the writer thinks is there.
	var notKept []string
	for _, w := range append(slices.Clone(e.Keywords), e.AddKeywords...) {
		if !slices.ContainsFunc(m.Keywords, func(k string) bool { return strings.EqualFold(k, w) }) && !slices.Contains(notKept, w) {
			notKept = append(notKept, w)
		}
	}
	if c.flags.has("json") {
		answer := map[string]any{"memory": detailed(store.Hit{Record: m, Decay: 1})}
		if len(notKept) > 0 {
			answer["keywordsDropped"] = notKept
		}
		return c.json(answer)
	}
	fmt.Fprintf(c.env.Stdout, "Updated %s.\n", store.FormatID(m.ID))
	c.printAll([]store.Hit{{Record: m, Decay: 1}}, c.project())
	if len(notKept) > 0 {
		fmt.Fprintf(c.env.Stdout, "Keywords past the limit of %d were not stored: %s.\n", store.KeywordLimit, strings.Join(notKept, ", "))
	}
	return nil
}

func (c command) setArchived(archived bool) error {
	ids, err := c.ids("give the ids of the memories")
	if err != nil {
		return err
	}
	watched, err := c.watchReplaced(ids)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := c.s.SetArchived(id, archived); err != nil {
			return err
		}
	}
	fmt.Fprintln(c.env.Stdout, "Done.")
	return c.sayReplaced(watched)
}

// replacedWatch is a memory that one being changed had replaced, and whether it
// was current before: archiving or deleting the newer one makes it current
// again, restoring it makes it replaced again, and that is something an agent
// would otherwise learn by being surprised by a search.
type replacedWatch struct {
	id      int64
	current bool
}

func (c command) watchReplaced(ids []int64) ([]replacedWatch, error) {
	var out []replacedWatch
	for _, id := range ids {
		old, ok, err := c.s.Replaces(id)
		if err != nil {
			return nil, err
		}
		if !ok || slices.ContainsFunc(out, func(w replacedWatch) bool { return w.id == old }) {
			continue
		}
		current, err := c.s.IsCurrent(old)
		if err != nil {
			return nil, err
		}
		out = append(out, replacedWatch{old, current})
	}
	return out, nil
}

func (c command) sayReplaced(watched []replacedWatch) error {
	for _, w := range watched {
		current, err := c.s.IsCurrent(w.id)
		if err != nil {
			return err
		}
		switch {
		case current && !w.current:
			fmt.Fprintf(c.env.Stdout, "%s, which it replaced, is current again and searches return it. Archive or delete %s too if it should go as well.\n", store.FormatID(w.id), store.FormatID(w.id))
		case !current && w.current:
			fmt.Fprintf(c.env.Stdout, "%s is replaced again, so searches no longer return it.\n", store.FormatID(w.id))
		}
	}
	return nil
}

func (c command) delete() error {
	ids, err := c.ids("give the ids of the memories")
	if err != nil {
		return err
	}
	watched, err := c.watchReplaced(ids)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := c.s.Delete(id); err != nil {
			return err
		}
	}
	fmt.Fprintln(c.env.Stdout, "Deleted.")
	return c.sayReplaced(watched)
}

// parseID reads an id an agent typed; one that is not a number is a misuse.
func parseID(text string) (int64, error) {
	id, err := store.ParseID(text)
	if err != nil {
		return 0, usageErr("%s", strings.TrimPrefix(err.Error(), store.ErrInvalid.Error()+": "))
	}
	return id, nil
}

// foldKeywordSpill keeps stray words after --keywords out of the memory's text.
// A shell splits `--keywords 别名 word 视图` on the space, and every word after
// the flag's one value lands among the positional arguments, where it would be
// appended to the text. When the text (or, for edit, the id) is already given,
// the words that follow --keywords are taken as more keywords; when it is not,
// the first one may be the text itself, and the arguments are left alone.
func foldKeywordSpill(args []string) []string {
	var out []string
	given := false // a positional argument before the flag: text, or the id
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--keywords" || arg == "--keyword" || arg == "--add-keywords" || arg == "--add-keyword" {
			value := ""
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				value = args[i+1]
				i++
			}
			words := []string{}
			if given {
				for i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
					words = append(words, args[i+1])
					i++
				}
			}
			out = append(out, arg)
			all := words
			if value != "" {
				all = append([]string{value}, words...)
			}
			if len(all) > 0 {
				out = append(out, strings.Join(all, ","))
			}
			continue
		}
		if !strings.HasPrefix(arg, "--") {
			given = true
		}
		out = append(out, arg)
	}
	return out
}

// kind reads --kind where a typo would otherwise look like an empty answer:
// a list of no memories of kind "prefer" is an answer, and a wrong one. It is
// a misuse, answered with the kinds there are.
func (c command) kind() (string, error) {
	k := strings.ToLower(strings.TrimSpace(c.flags.one("kind")))
	if k == "" {
		return "", nil
	}
	if !slices.Contains(store.Kinds, k) {
		return "", usageErr("--kind must be one of %s", strings.Join(store.Kinds, ", "))
	}
	return k, nil
}

func (c command) ids(missing string) ([]int64, error) {
	if len(c.args) == 0 {
		return nil, usageErr("%s", missing)
	}
	ids := make([]int64, len(c.args))
	for i, a := range c.args {
		id, err := parseID(a)
		if err != nil {
			return nil, err
		}
		ids[i] = id
	}
	return ids, nil
}

func (c command) json(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	fmt.Fprintln(c.env.Stdout, string(b))
	return nil
}

// memoryView is a memory as `--json` shows it.
type memoryView struct {
	ID         int64    `json:"id"`
	Kind       string   `json:"kind"`
	Content    string   `json:"content"`
	Keywords   []string `json:"keywords"`
	Written    string   `json:"written"`
	Confidence string   `json:"confidence"`
	// Counts are always present: a zero is a fact an agent reads, and a key
	// left out looks like a field the store does not have.
	Uses         int     `json:"uses"`
	Verified     int     `json:"verified"`
	Contradicted int     `json:"contradicted"`
	LastUsed     string  `json:"lastUsed,omitempty"`
	Project      string  `json:"project,omitempty"`
	SupersededBy int64   `json:"supersededBy,omitempty"`
	Archived     bool    `json:"archived,omitempty"`
	Decay        float64 `json:"decay"`
}

func view(h store.Hit) memoryView {
	v := memoryView{
		ID: h.ID, Kind: h.Kind, Content: h.Content, Keywords: orEmptyStrings(h.Keywords), Written: day(h.CreatedAt),
		Confidence: h.Confidence, Verified: h.Verified, Contradicted: h.Contradicted,
		Uses: h.Uses, LastUsed: lastUsed(h.LastUsed),
		Archived: h.State == store.StateArchived, Decay: h.Decay,
	}
	if h.Project != nil {
		v.Project = *h.Project
	}
	if h.SupersededBy != nil {
		v.SupersededBy = *h.SupersededBy
	}
	return v
}

// detailedView is a memory as show and list --json give it: everything a
// search result carries, and that it is the current one or what replaced it.
type detailedView struct {
	memoryView
	Current     bool `json:"current"`
	Superseding bool `json:"superseding"`
}

func detailed(h store.Hit) detailedView {
	return detailedView{
		memoryView:  view(h),
		Current:     h.State == store.StateActive && h.SupersededBy == nil,
		Superseding: h.SupersededBy != nil,
	}
}

// orEmptyStrings is keywords as JSON lists them: [] rather than null.
func orEmptyStrings(words []string) []string {
	if words == nil {
		return []string{}
	}
	return words
}

// lastUsed is when a memory was last returned, used or verified; empty when
// it never was.
func lastUsed(unix *uint64) string {
	if unix == nil {
		return ""
	}
	return day(*unix)
}

func views(hits []store.Hit) []memoryView {
	out := make([]memoryView, len(hits))
	for i, h := range hits {
		out[i] = view(h)
	}
	return out
}

// printAll writes the memories, a blank line between them. Each is a header
// line, then its text as written. The header comes first because it says how
// to treat what follows: what kind it is, how old, how far to trust it.
//
//	#12 · preference · written 3d ago (2026-11-19) · verified
//	论文引用一律用 GB/T 7714 著者-出版年制；导师说过不要脚注。
//
// Besides the id, kind and age, the header names only what is out of the
// ordinary: a confidence above observed; a contradiction; another
// project's directory; a replacement; an archive; no use for a quarter.
func (c command) printAll(hits []store.Hit, asked *string) {
	now := time.Now()
	for i, h := range hits {
		if i > 0 {
			fmt.Fprintln(c.env.Stdout)
		}
		parts := []string{store.FormatID(h.ID), h.Kind}
		parts = append(parts, fmt.Sprintf("written %s (%s)", ago(h.CreatedAt, now), day(h.CreatedAt)))
		if h.Confidence != store.ConfidenceObserved {
			parts = append(parts, h.Confidence)
		}
		if h.Contradicted > 0 {
			parts = append(parts, fmt.Sprintf("contradicted %dx", h.Contradicted))
		}
		if h.Project != nil && (asked == nil || !store.SameProject(*h.Project, *asked)) {
			parts = append(parts, *h.Project)
		}
		if h.SupersededBy != nil {
			parts = append(parts, "superseded by "+store.FormatID(*h.SupersededBy))
		}
		if h.State == store.StateArchived {
			parts = append(parts, "archived")
		}
		// A fact is how things were when it was written; after a quarter it is
		// likelier than not to have moved on.
		if h.Kind == store.KindFact && now.Sub(time.Unix(int64(h.CreatedAt), 0)) >= 90*24*time.Hour {
			parts = append(parts, "may be out of date")
		}
		if h.Decay <= store.UnusedDecay {
			parts = append(parts, "unused for 90+ days")
		}
		fmt.Fprintf(c.env.Stdout, "%s\n%s\n", strings.Join(parts, " · "), strings.TrimSpace(h.Content))
	}
}

// ago is how long ago something happened, in the unit an age reads in.
func ago(unix uint64, now time.Time) string {
	days := int(now.Sub(time.Unix(int64(unix), 0)).Hours() / 24)
	switch {
	case days <= 0:
		return "today"
	case days < 60:
		return fmt.Sprintf("%dd ago", days)
	case days < 730:
		return fmt.Sprintf("%dmo ago", days/30)
	}
	return fmt.Sprintf("%dy ago", days/365)
}

// splitWords is the words in values, which an agent writes as it likes: one
// flag with a, b, c; or several; commas of either width, or spaces.
func splitWords(values []string) []string {
	var out []string
	for _, v := range values {
		out = append(out, strings.FieldsFunc(v, func(r rune) bool {
			return r == ',' || r == '，' || r == '、' || unicode.IsSpace(r)
		})...)
	}
	return out
}

func day(unix uint64) string { return time.Unix(int64(unix), 0).Format("2006-01-02") }

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// flags is --name value, --name=value and bare --name, anywhere in the
// arguments: agents put flags after the text as often as before it.
type flags map[string][]string

func parseFlags(args []string) (flags, []string, error) {
	f := flags{}
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		name, ok := strings.CutPrefix(arg, "--")
		if !ok || name == "" {
			positional = append(positional, arg)
			continue
		}
		if key, value, has := strings.Cut(name, "="); has {
			f[key] = append(f[key], value)
			continue
		}
		if boolFlag(name) {
			f[name] = append(f[name], "true")
			continue
		}
		if i+1 >= len(args) {
			return nil, nil, usageErr("--%s needs a value", name)
		}
		f[name] = append(f[name], args[i+1])
		i++
	}
	return f, positional, nil
}

func (f flags) has(name string) bool { return len(f[name]) > 0 }

func (f flags) one(name string) string {
	if v := f[name]; len(v) > 0 {
		return v[len(v)-1]
	}
	return ""
}

func (f flags) all(name string) []string { return f[name] }

func (f flags) int(name string, fallback int) (int, error) {
	v := f.one(name)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, usageErr("--%s takes a positive number", name)
	}
	return n, nil
}
