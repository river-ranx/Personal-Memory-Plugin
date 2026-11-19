// Package cli is the `memory` command line: how an agent (taught by the
// memory skill) uses the local memory, and the entry the agents' hook calls.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"memory/internal/hooks"
	"memory/internal/install"
	"memory/internal/store"
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

// Main runs one command and returns the process's exit code.
func Main(args []string, env Env) int {
	if err := dispatch(args, env); err != nil {
		if !errors.Is(err, ErrReported) {
			fmt.Fprintln(env.Stderr, "memory:", err)
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
	open := func() (*store.Store, error) {
		path, err := DBPath(env.Getenv)
		if err != nil {
			return nil, err
		}
		return store.Open(path)
	}
	switch args[0] {
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
		flags, positional, err := parseFlags(args[1:])
		if err != nil {
			return err
		}
		if err := cmd.check(args[0], flags); err != nil {
			return err
		}
		s, err := open()
		if err != nil {
			return err
		}
		defer s.Close()
		return cmd.run(command{s: s, env: env, flags: flags, args: positional})
	}()
	// A misuse is answered with how to use the command right.
	var misuse usageError
	if errors.As(err, &misuse) {
		return fmt.Errorf("%s\nusage: %s\n%s", misuse.msg, cmd.usage(args[0]), moreAbout(args[0]))
	}
	return err
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

func (c command) search() error {
	query := strings.Join(c.args, " ")
	if strings.TrimSpace(query) == "" {
		return usageErr("search needs the words to look for")
	}
	limit, err := c.flags.int("limit", 5)
	if err != nil {
		return err
	}
	project := c.project()
	hits, err := c.s.Search(query, project, limit)
	if err != nil {
		return err
	}
	if c.flags.has("json") {
		return c.json(map[string]any{"memories": views(hits)})
	}
	if len(hits) == 0 {
		fmt.Fprintln(c.env.Stdout, "No memories match. Try other words: a synonym, the English name, another name for the thing — or fewer words.")
	}
	c.printAll(hits, project)
	return nil
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
		Title: c.flags.one("title"), Content: content, Kind: c.flags.one("kind"),
		Keywords: splitWords(append(c.flags.all("keywords"), c.flags.all("keyword")...)), Project: c.project(),
	}
	if v := c.flags.one("supersedes"); v != "" {
		id, err := parseID(v)
		if err != nil {
			return err
		}
		d.Supersedes = &id
	}
	m, err := c.s.Write(d)
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
	if c.flags.has("json") {
		return c.json(map[string]any{"memory": view(store.Hit{Record: m, Decay: 1})})
	}
	fmt.Fprintf(c.env.Stdout, "Stored %s.\n", store.FormatID(m.ID))
	return nil
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
		return c.json(map[string]any{"memory": view(h)})
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
	hits, err := c.s.List(store.ListQuery{
		Archived: c.flags.has("archived"), Kind: c.flags.one("kind"), Project: c.flags.one("project"), Limit: limit,
	})
	if err != nil {
		return err
	}
	if c.flags.has("json") {
		return c.json(map[string]any{"memories": views(hits)})
	}
	if len(hits) == 0 {
		fmt.Fprintln(c.env.Stdout, "No memories.")
	}
	c.printAll(hits, c.project())
	return nil
}

func (c command) setArchived(archived bool) error {
	ids, err := c.ids("give the ids of the memories")
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := c.s.SetArchived(id, archived); err != nil {
			return err
		}
	}
	fmt.Fprintln(c.env.Stdout, "Done.")
	return nil
}

func (c command) delete() error {
	ids, err := c.ids("give the ids of the memories")
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := c.s.Delete(id); err != nil {
			return err
		}
	}
	fmt.Fprintln(c.env.Stdout, "Deleted.")
	return nil
}

// parseID reads an id an agent typed; one that is not a number is a misuse.
func parseID(text string) (int64, error) {
	id, err := store.ParseID(text)
	if err != nil {
		return 0, usageErr("%s", strings.TrimPrefix(err.Error(), store.ErrInvalid.Error()+": "))
	}
	return id, nil
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
	ID           int64   `json:"id"`
	Kind         string  `json:"kind"`
	Title        string  `json:"title,omitempty"`
	Content      string  `json:"content"`
	Written      string  `json:"written"`
	Confidence   string  `json:"confidence"`
	Verified     int     `json:"verified,omitempty"`
	Contradicted int     `json:"contradicted,omitempty"`
	Project      string  `json:"project,omitempty"`
	SupersededBy int64   `json:"supersededBy,omitempty"`
	Archived     bool    `json:"archived,omitempty"`
	Decay        float64 `json:"decay"`
}

func view(h store.Hit) memoryView {
	v := memoryView{
		ID: h.ID, Kind: h.Kind, Title: h.Title, Content: h.Content, Written: day(h.CreatedAt),
		Confidence: h.Confidence, Verified: h.Verified, Contradicted: h.Contradicted,
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
//	#12 · preference · 引用格式 · written 3d ago (2026-11-19) · verified
//	论文引用一律用 GB/T 7714 著者-出版年制；导师说过不要脚注。
//
// Besides the id, kind and age, the header names only what is out of the
// ordinary: a title; a confidence above observed; a contradiction; another
// project's directory; a replacement; an archive; no use for a quarter.
func (c command) printAll(hits []store.Hit, asked *string) {
	now := time.Now()
	for i, h := range hits {
		if i > 0 {
			fmt.Fprintln(c.env.Stdout)
		}
		parts := []string{store.FormatID(h.ID), h.Kind}
		if h.Title != "" {
			parts = append(parts, h.Title)
		}
		parts = append(parts, fmt.Sprintf("written %s (%s)", ago(h.CreatedAt, now), day(h.CreatedAt)))
		if h.Confidence != store.ConfidenceObserved {
			parts = append(parts, h.Confidence)
		}
		if h.Contradicted > 0 {
			parts = append(parts, fmt.Sprintf("contradicted %dx", h.Contradicted))
		}
		if h.Project != nil && (asked == nil || store.NormalizeProject(*h.Project) != *asked) {
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
