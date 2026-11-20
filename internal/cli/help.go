package cli

import (
	"fmt"
	"slices"
	"strings"
)

// The command line is used by agents, which read its help more than anyone: so
// what a command takes, what it prints and what its answers mean are written
// once, here, beside the flags themselves. The usage line, the flag list in the
// help and the check that refuses a flag a command does not take all come from
// one definition of the flags, so none of them can say what another does not.

// flagSpec is one flag of a command: --name VALUE, or --name alone when value is "".
type flagSpec struct{ name, value, desc string }

// spec is one memory command: how to use it, which flags it takes, what it does.
type spec struct {
	summary string     // one line, in the list of commands
	args    string     // the positional arguments, as written in the usage line
	flags   []flagSpec // every flag it takes, besides --project and --json
	aliases []string   // flags it takes without listing: spellings an agent may use
	help    string     // everything else: what it does, what it prints, tips, examples
	run     func(command) error
}

// everyCommand are the flags every command takes.
var everyCommand = []flagSpec{
	{"project", "DIR", "the working directory the memory belongs to (default: the current directory; its parent and subdirectories are the same project); it only affects ranking, and in `list` it filters"},
	{"json", "", "print the answer as JSON"},
}

var commandOrder = []string{"search", "add", "confirm", "show", "list", "archive", "restore", "delete"}

// usage is the command's usage line, made from its flags.
func (sp spec) usage(name string) string {
	parts := []string{"memory", name}
	if sp.args != "" {
		parts = append(parts, sp.args)
	}
	for _, f := range sp.flags {
		if f.value == "" {
			parts = append(parts, "[--"+f.name+"]")
		} else {
			parts = append(parts, "[--"+f.name+" "+f.value+"]")
		}
	}
	return strings.Join(parts, " ")
}

// allowed is every flag name the command takes.
func (sp spec) allowed() []string {
	var names []string
	for _, f := range sp.flags {
		names = append(names, f.name)
	}
	for _, f := range everyCommand {
		names = append(names, f.name)
	}
	return append(names, sp.aliases...)
}

// check refuses a flag the command does not take, naming what it does take: a
// mistyped flag that was ignored would look to an agent as if it had worked.
func (sp spec) check(name string, f flags) error {
	allowed := sp.allowed()
	for flag := range f {
		if slices.Contains(allowed, flag) {
			continue
		}
		var takes []string
		for _, n := range allowed {
			if !slices.Contains(sp.aliases, n) {
				takes = append(takes, "--"+n)
			}
		}
		return usageErr("memory %s has no --%s flag (it takes: %s)", name, flag, strings.Join(takes, ", "))
	}
	return nil
}

// boolFlag is whether --name takes no value, in any command.
func boolFlag(name string) bool {
	for _, f := range everyCommand {
		if f.name == name {
			return f.value == ""
		}
	}
	for _, sp := range commands {
		for _, f := range sp.flags {
			if f.name == name {
				return f.value == ""
			}
		}
	}
	return false
}

// helpText is the whole help of a command: usage, flags, then its own text.
func (sp spec) helpText(name string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "memory %s — %s\n\nUSAGE\n  %s\n", name, sp.summary, sp.usage(name))
	if len(sp.flags) > 0 {
		b.WriteString("\nFLAGS\n")
	}
	width := 0
	all := append(slices.Clone(sp.flags), everyCommand...)
	label := func(f flagSpec) string {
		if f.value == "" {
			return "--" + f.name
		}
		return "--" + f.name + " " + f.value
	}
	for _, f := range all {
		width = max(width, len(label(f)))
	}
	for i, f := range all {
		if i == len(sp.flags) {
			b.WriteString("\nFLAGS EVERY COMMAND TAKES\n")
		}
		fmt.Fprintf(&b, "  %-*s  %s\n", width, label(f), f.desc)
	}
	b.WriteString("\n" + strings.TrimSpace(sp.help) + "\n")
	return b.String()
}

// moreAbout is the line that points from a mistake to the command's full help.
func moreAbout(name string) string { return "more: memory " + name + " --help" }

var commands = map[string]spec{
	"search": {
		summary: "find memories relevant to some words",
		args:    "<words...>",
		flags:   []flagSpec{{"limit", "N", "how many to return (default 5, at most 20)"}},
		help: `
WHAT IT DOES
  Cuts the words into terms and returns the memories that contain any of them,
  best first. Chinese is cut into two-character windows (报销 is found inside
  这次报销怎么走); English words count, and so do abbreviations such as ci, db, oa;
  single letters and function words (to, is, the, with) do not.
  Ranking: a match in a memory's title or keywords outweighs one in its text.
  Memories from this project — this directory, one inside it or one containing
  it — rank above other projects' (which are still returned). Memories
  confirmed as used or verified often rank higher; contradicted ones, and ones
  unused for months, rank lower.
  Not returned: archived memories, and memories a newer one replaced.
  Each memory returned is noted (once a day at most), which keeps it from
  fading; it counts as used only when you say so with memory confirm.

OUTPUT
  At most --limit results, best first; each is a header line and then the text
  as written, with a blank line between results (see READING RESULTS in
  memory --help). Nothing found prints "No memories match." and what to try.

TIPS
  - A few specific words beat a sentence.
  - One query may carry two topics; a memory matching either is found.
  - Nothing found: try a synonym, the English name, another name for the thing
    (a module, a person's role, an institution), or fewer words. If that finds nothing either, there is nothing to find.
  - A result is a lead, not evidence: check what you can before acting on it
    (see WEIGHING WHAT YOU FIND in memory --help), and report how it held up:
    memory confirm <id>.

EXAMPLES
  memory search 报销 流程 OA
  memory search 城镇化率 口径 --limit 8
  memory search 引用格式 导师      # two topics in one query`,
		run: command.search,
	},
	"add": {
		summary: "remember something",
		args:    `"<text>"`,
		flags: []flagSpec{
			{"kind", "KIND", "preference | fact | rule | decision | practice (default fact)"},
			{"title", "T", "a short name, one line, at most 60 characters"},
			{"keywords", "a,b,c", "other words to find it by, at most 10 (comma or space separated; --keyword is the same and may repeat)"},
			{"supersedes", "ID", "the memory this one replaces; see SUPERSEDING"},
		},
		aliases: []string{"keyword"},
		help: `
TEXT
  One memory is one thing, written so that a later session can act on it: what
  to do, where, and why. At least 3 words (in Chinese, about 4 characters in a
  row or more; "提交用中文" is enough, "报销单" is not), at most 1000 characters. Give "-" as
  the text to read it from stdin, which is the way to pass quotes, backticks or
  several lines:
      memory add - --kind practice <<'TEXT'
      ...the text...
      TEXT

KINDS (--kind; fact when left out)
  rule        must be followed; not following it is a mistake
  decision    a choice that was made, and why; learn the reason before overturning it
  preference  how the person likes things done
  practice    a reusable way of doing something
  fact        how things are now; it can go stale, so a result shows its age

KEYWORDS
  Words someone might search with: synonyms, the English name, abbreviations.
  They decide whether the memory is found, and count double in ranking. Pass
  them with --keywords a,b,c.

WHAT IS NOT STORED (each answer says why and what to do)
  - Too short: fewer than 3 words. A label ("报销", "release") says nothing; the
    title and keywords do not count towards it.
  - A repeat: a memory with nearly the same words exists. The answer shows it and
    the command to replace it.
  Secrets, phone numbers and the like are not checked or changed: this is a
  local store.

WHAT IS NOT WORTH STORING (not checked: your judgment)
  Ask whether the text says what happened or what was learned. A record of what
  happened — "draft sent to my supervisor", "chapter 3 written", "meeting held",
  "merged MR", "today I finished…" — is kept already by the mail, the document,
  git or the pipeline: do not store it. If it holds a lesson, store only the
  lesson. Nor what the source itself says and can be looked up again (where it
  is, and why it matters, can be stored).

SUPERSEDING
  --supersedes N replaces memory #N with this one. Give only the new text: the
  kind, title and keywords you leave out are carried over from #N. #N is not
  returned by search while this memory is in use; delete or archive this one and
  #N is current again. #N must be in use itself (not archived or already replaced).

ANSWERS
  Stored #12.                      stored; 12 is its id.
  Not stored: already remembered   names the existing memory (#5), says "add it
                                   again with --supersedes #5" if yours is newer
                                   or more precise, otherwise leave it; then shows it.
  Not stored: refused: …           too short; say it in a sentence, or drop it.
  Not stored: invalid memory: …    text too long, or an unknown --kind.
  With --json the same, as {"memory": …} or {"error": …, "message": …}.

EXAMPLES
  memory add "论文引用一律用 GB/T 7714 著者-出版年制；导师说过不要脚注" --kind preference --title 引用格式
  memory add "城镇化率一律用常住人口口径，因为统计年鉴 2018 年以前的户籍口径和之后不可比" --kind decision --keywords 城镇化率,口径,统计年鉴
  memory add "发版只走 release 分支，因为 main 不触发签名流水线" --kind rule --keywords 发版,release,签名
  memory add "访谈稿统一由课题组自己转录，不再外包" --supersedes 12`,
		run: command.add,
	},
	"confirm": {
		summary: "report that a memory was used, checked, or found wrong",
		args:    "<id...>",
		flags: []flagSpec{
			{"outcome", "OUTCOME", "used | verified | contradicted (default used)"},
			{"note", "N", "what you found (useful for contradicted)"},
		},
		help: `
WHAT IT DOES
  Reports how a memory held up when you relied on it. These reports decide which
  memories rank first, so make one whenever you used a result.

OUTCOMES (--outcome; used when left out)
  used          you relied on it and saw no conflict
  verified      you checked it against the source and it is right; three
                verifications on three different days since it was last
                contradicted (however far apart) make a memory "verified"
  contradicted  it is wrong or out of date (say how with --note); it ranks lower
                from then on, and a verified memory falls back to unverified

ONCE A DAY
  A memory counts once a day for each outcome. Reporting it again the same day
  is not an error: the answer says "Already reported today, counted once" and
  nothing is added. There is no need to repeat a report.

IF A MEMORY IS WRONG
  You know what is true now:  memory add "<what is true>" --supersedes <id>
  You only know it is wrong:  memory confirm <id> --outcome contradicted --note "…"

OUTPUT
  Recorded.                                      something was recorded
  Already reported today, counted once: #3.      a repeat
  No memory has these ids: #999.                 an id that does not exist
  One call may print several. With --json: {"missing": […], "repeated": […]}.

EXAMPLES
  memory confirm 12
  memory confirm 12 13 --outcome verified
  memory confirm 12 --outcome contradicted --note "统计年鉴 2020 年起改用常住口径"`,
		run: command.confirm,
	},
	"show": {
		summary: "one memory in full, with its counts",
		args:    "<id>",
		help: `
OUTPUT
  The memory as a search shows it (a header line, then the text), then a line
  with its keywords and, for the last 30 days, how many times it was used,
  verified and contradicted. Unlike search, show works for any memory,
  including archived ones and ones a newer memory replaced; it does not count
  as a use.

EXAMPLES
  memory show 12
  memory show #12 --json`,
		run: command.show,
	},
	"list": {
		summary: "browse memories, newest first",
		flags: []flagSpec{
			{"kind", "KIND", "only this kind"},
			{"archived", "", "list the archived ones instead of the active ones"},
			{"limit", "N", "how many to list (default 50, at most 200)"},
		},
		help: `
WHAT IT DOES
  Lists memories newest first, in the same form as search. It is for looking
  through the store, so it does not rank, and it does not count as a use.
  Unlike search it also lists memories a newer one replaced (marked
  "superseded by #N"). With --project DIR it lists only that project's memories:
  written from DIR, a directory inside it, or one containing it.

EXAMPLES
  memory list
  memory list --kind rule --limit 20
  memory list --archived`,
		run: command.list,
	},
	"archive": {
		summary: "stop a memory from being returned (memory restore undoes it)",
		args:    "<id...>",
		help: `
WHAT IT DOES
  Keeps a memory but never returns it from search. Use it for a memory that is
  not wrong but no longer useful. If the archived memory had replaced an older
  one, the older one is current again until this one is restored.

EXAMPLES
  memory archive 12 13`,
		run: func(c command) error { return c.setArchived(true) },
	},
	"restore": {
		summary: "bring an archived memory back",
		args:    "<id...>",
		help: `
EXAMPLES
  memory restore 12`,
		run: func(c command) error { return c.setArchived(false) },
	},
	"delete": {
		summary: "remove a memory for good",
		args:    "<id...>",
		help: `
WHAT IT DOES
  Removes the memory, its search index entry and its history; nothing is left
  and it cannot be restored. Its id is never given to another memory. If it had
  replaced an older memory, the older one is current again. To keep a memory but
  stop it being returned, use archive.

EXAMPLES
  memory delete 12`,
		run: command.delete,
	},
}

// other commands, which have help but are not memory commands: a person runs
// them, not an agent.
var otherHelp = map[string]string{
	"install": `memory install — set up the skill and the hooks

USAGE
  memory install <agent...>        agents: claude, codex, antigravity

Puts the memory skill and two hooks where each agent reads them: one on session
start, which gives the agent the prompt about its memory; one that has it look
back over each turn, to write down and confirm (with the next message for
Claude Code and Codex, at the turn's end for Antigravity). It only touches its own
entries and keeps a copy of a config file it changes as <file>.memory-backup.
Codex asks you to trust new hooks: run /hooks in Codex once. A person runs this,
once; an agent has no need to.`,
	"uninstall": `memory uninstall — remove the skill and the hooks

USAGE
  memory uninstall <agent...>      agents: claude, codex, antigravity

Takes out what memory install put in place, and nothing else. A person runs this.`,
	"hook": `memory hook — what an agent's hook configuration calls

USAGE
  memory hook <agent> <event>

Called by an agent, not run by hand. On SessionStart it prints the prompt that
tells the agent about its memory. On UserPromptSubmit (Claude Code, Codex) it
adds a review of the last turn to the message; on Stop (Antigravity) it holds
the agent back once with a review of the turn: write down what is worth
keeping, confirm the memories relied on. Both in the agent's own format.
MEMORY_REVIEW=off turns the review off. It keeps nothing and opens no
database; any other event gets no answer.`,
}

// help is the whole command line, what an agent reads first.
func help() string {
	var b strings.Builder
	b.WriteString(`memory — long-term memory on this machine, for coding agents.

A memory is a short note — a fact, rule, decision, preference or practice — that
outlives a session. Search before you start work; write down what a later session
will need; report whether what you found held up.

COMMANDS
`)
	for _, name := range commandOrder {
		fmt.Fprintf(&b, "  %-8s %s\n           %s\n", name, commands[name].summary, commands[name].usage(name))
	}
	b.WriteString(`  install, uninstall, hook   set up or remove the skill and hooks (for a person, not an agent)

Everything about one command — flags, what it prints, what its answers mean,
examples:   memory <command> --help      (or: memory help <command>)

TYPICAL USE
  memory search 报销 流程 OA          before a task, or when something may have come up before
  memory add "季度复盘材料先给王总过目再发全员" --kind preference
  memory confirm 12                   you used #12 and it held up
  memory add "<new text>" --supersedes 12   what #12 says has changed

READING RESULTS
  A result is a header line, then the text as written; results are separated by a
  blank line:
      #12 · preference · 引用格式 · written 3d ago (2026-11-19) · verified
      论文引用一律用 GB/T 7714 著者-出版年制；导师说过不要脚注。
  The header reads #id · kind · [title] · when it was written, and then only what
  is unusual:
      verified             checked on several days: more likely right, still no guarantee
      contradicted 2x      reported wrong twice: check before relying on it
      may be out of date   a fact written 90+ days ago: how things were, maybe not now
      /some/directory      written from another working directory: does it apply here?
      superseded by #13    replaced by a newer memory (list and show only)
      archived             not returned by search (list and show only)
      unused for 90+ days  nobody has used it for a quarter: check it
  Kinds: rule = follow it · decision = learn the reason before overturning it ·
  preference = adapt to it · practice = a way to do it · fact = how things were
  when written (it can be stale: look at its age).

WEIGHING WHAT YOU FIND
  A memory is a note somebody wrote at some point. It can have been wrong when
  written, be out of date now, apply to another project, or be misread by you.
  It is a lead, not evidence. So:
    1. Read the header first: age, kind, verified or contradicted, other directory.
    2. Ask whether it applies to this task and this project. If not, leave it.
    3. Check what is cheap to check (the document, the source, the file or
       setting it names) before acting on it. For anything hard to undo —
       deleting, publishing, sending something out, changing what others share —
       do not act on a memory alone: check, or ask.
    4. Order of trust: what the person says now > what you see now (documents,
       data, files, command output) > a memory. A rule or decision binds only while the
       situation it was written for still holds.
    5. If a memory disagrees with what the person says or what you see, go with
       the latter and say so. Then fix the memory: memory add "<what is true>"
       --supersedes <id>, or memory confirm <id> --outcome contradicted.
    6. When you tell the person something that comes from a memory, say it is a
       past note ("my notes say…"), not a fact you verified.
    7. Report how it held up: memory confirm <id> (used or verified).

IDS AND FLAGS
  An id is the number a result shows: 12 or #12. Ids are never reused.
  Every command takes --json (the answer as JSON) and --project DIR (default: the
  current directory, which is a memory's project; it only affects ranking).
  A flag a command does not take is an error that lists the flags it does take;
  it is never ignored. A misuse prints the command's usage line.

EXIT STATUS
  0 = done. 1 = not done: the output says why and what to try next.

STORAGE
  $MEMORY_HOME/memory.db, by default ~/.memory/memory.db. Nothing leaves this machine.
`)
	return b.String()
}

// helpFor is the help asked for with `memory help <topic>`.
func helpFor(topic []string) (string, error) {
	if len(topic) == 0 || topic[0] == "help" {
		return help(), nil
	}
	if sp, ok := commands[topic[0]]; ok {
		return sp.helpText(topic[0]), nil
	}
	if text, ok := otherHelp[topic[0]]; ok {
		return text + "\n", nil
	}
	return "", fmt.Errorf("no help for %q. Commands: %s, install, uninstall, hook", topic[0], strings.Join(commandOrder, ", "))
}

// wantsHelp is whether the arguments ask for help.
func wantsHelp(args []string) bool {
	return slices.Contains(args, "--help") || slices.Contains(args, "-h")
}
