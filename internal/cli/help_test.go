package cli

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"memory/internal/store"
)

// Every command answers --help, -h and `memory help <command>` with the same
// text, which is its own; and asking opens nothing and runs nothing.
func TestEveryCommandHasItsOwnHelp(t *testing.T) {
	r := newRun(t)
	for _, name := range append(slices.Clone(commandOrder), "install", "uninstall", "hook") {
		var texts []string
		for _, args := range [][]string{{name, "--help"}, {name, "-h"}, {"help", name}} {
			code, out := r.memory("", args...)
			if code != 0 || !strings.HasPrefix(out, "memory "+name+" — ") || !strings.Contains(out, "USAGE") {
				t.Fatalf("%v: %d %q", args, code, out)
			}
			texts = append(texts, out)
		}
		if texts[0] != texts[1] || texts[1] != texts[2] {
			t.Fatalf("%s: the three ways to ask give different help", name)
		}
	}
	// `memory install --help` is help, not an install.
	if code, out := r.memory("", "install", "--help"); code != 0 || strings.Contains(out, "skill →") {
		t.Fatalf("%d %q", code, out)
	}
	if _, err := os.Stat(r.home + "/memory.db"); err == nil {
		t.Fatal("asking for help opened the database")
	}
	code, out := r.memory("", "help", "nope")
	if code != 1 || !strings.Contains(out, `no help for "nope"`) || !strings.Contains(out, "search, add") {
		t.Fatalf("an unknown topic: %d %q", code, out)
	}
	if code, out := r.memory("", "help"); code != 0 || !strings.HasPrefix(out, "memory — long-term memory") {
		t.Fatalf("%d %q", code, out)
	}
}

// The usage line and the flag list are made from the flags, and the check on a
// flag is made from them too: they cannot disagree.
func TestUsageHelpAndCheckAreMadeFromTheSameFlags(t *testing.T) {
	for _, name := range commandOrder {
		sp := commands[name]
		usage, helpText := sp.usage(name), sp.helpText(name)
		if !strings.HasPrefix(usage, "memory "+name) || !strings.Contains(helpText, "\n  "+usage+"\n") {
			t.Fatalf("%s: usage %q", name, usage)
		}
		for _, f := range sp.flags {
			if !strings.Contains(usage, "--"+f.name) || !strings.Contains(helpText, "  --"+f.name) || f.desc == "" {
				t.Fatalf("%s: --%s is not in the usage and the help, or has no description", name, f.name)
			}
			if err := sp.check(name, flags{f.name: {"x"}}); err != nil {
				t.Fatalf("%s: its own flag --%s is refused: %v", name, f.name, err)
			}
			if f.value == "" != boolFlag(f.name) {
				t.Fatalf("--%s: a flag without a value is parsed as one with a value, or the reverse", f.name)
			}
		}
		for _, f := range everyCommand {
			if !strings.Contains(helpText, "--"+f.name) {
				t.Fatalf("%s: help lacks --%s", name, f.name)
			}
		}
		if err := sp.check(name, flags{"nonsense": {"x"}}); err == nil {
			t.Fatalf("%s accepts a flag it does not have", name)
		}
	}
}

var (
	exampleCommand = regexp.MustCompile(`memory (search|add|confirm|show|list|archive|restore|delete)\b([^\n#]*)`)
	flagInText     = regexp.MustCompile(`--([a-z][a-z-]*)`)
	quoted         = regexp.MustCompile(`"[^"\n]*"`)
)

// Every command written out in any help — an example, a typical use, the
// command in an answer — uses only flags that command takes.
func TestEveryCommandInTheHelpUsesFlagsItTakes(t *testing.T) {
	texts := map[string]string{"memory --help": help()}
	for _, name := range commandOrder {
		texts["memory "+name+" --help"] = commands[name].helpText(name)
	}
	seen := 0
	for from, text := range texts {
		for _, m := range exampleCommand.FindAllStringSubmatch(text, -1) {
			name := m[1]
			// What is in quotes is the text of a memory, not flags.
			for _, f := range flagInText.FindAllStringSubmatch(quoted.ReplaceAllString(m[2], `""`), -1) {
				seen++
				if !slices.Contains(commands[name].allowed(), f[1]) {
					t.Errorf("%s writes `memory %s … --%s`, which memory %s does not take", from, name, f[1], name)
				}
			}
		}
	}
	if seen < 20 {
		t.Fatalf("only %d flags were checked: the pattern no longer finds the examples", seen)
	}
}

// What the help says in numbers is what the store does.
func TestTheHelpStatesTheLimitsTheStoreHas(t *testing.T) {
	add, search, list, confirm := commands["add"].helpText("add"), commands["search"].helpText("search"), commands["list"].helpText("list"), commands["confirm"].helpText("confirm")
	for text, wants := range map[string][]string{
		add:     {fmt.Sprint(store.ContentLimit) + " characters", fmt.Sprint(store.TitleLimit) + " characters", "at most " + fmt.Sprint(store.KeywordLimit)},
		search:  {"at most " + fmt.Sprint(store.MaxSearchLimit)},
		list:    {"at most " + fmt.Sprint(store.MaxListLimit)},
		confirm: {string(store.OutcomeUsed), string(store.OutcomeVerified), string(store.OutcomeContradicted)},
	} {
		for _, want := range wants {
			if !strings.Contains(text, want) {
				t.Errorf("a help lacks %q:\n%s", want, text)
			}
		}
	}
	for _, kind := range store.Kinds {
		if !strings.Contains(add, "\n  "+kind+" ") {
			t.Errorf("the help of add does not explain the kind %s", kind)
		}
	}
	if !strings.Contains(add, "At least 3 words") {
		t.Error("the help of add does not state the shortest text")
	}
}

// The header in the help is the header a result has.
func TestTheResultHeaderInTheHelpIsTheRealOne(t *testing.T) {
	header := regexp.MustCompile(`^#\d+ · (preference|fact|rule|decision|practice) · (.+ · )?written (today|\d+(d|mo|y) ago) \(\d{4}-\d{2}-\d{2}\)( · .+)?$`)
	var sample []string
	for _, line := range strings.Split(help(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#12 · ") {
			sample = append(sample, strings.TrimSpace(line))
		}
	}
	if len(sample) != 1 || !header.MatchString(sample[0]) {
		t.Fatalf("the sample header: %q", sample)
	}
	r := newRun(t)
	r.add("提交信息用中文，前缀照 git log 的写法", "--kind", "rule", "--title", "提交规范")
	_, out := r.memory("", "search", "提交信息")
	if got := strings.SplitN(out, "\n", 2)[0]; !header.MatchString(got) {
		t.Fatalf("a real header does not read as the help says: %q", got)
	}
	// Every unusual thing the header can say is explained.
	for _, word := range []string{"verified", "contradicted", "superseded by", "archived", "unused for 90+ days", "another working directory", "may be out of date"} {
		if !strings.Contains(help(), word) {
			t.Errorf("the help does not explain %q", word)
		}
	}
}

// The overview names every command with what it is for, and says how to get
// the rest.
func TestTheOverviewListsEveryCommandAndSaysWhereTheRestIs(t *testing.T) {
	text := help()
	for _, name := range commandOrder {
		if !strings.Contains(text, "  "+name+" ") || !strings.Contains(text, commands[name].summary) {
			t.Errorf("the overview lacks %s", name)
		}
	}
	for _, want := range []string{"memory <command> --help", "READING RESULTS", "EXIT STATUS", "TYPICAL USE", "IDS AND FLAGS", "STORAGE", "--json", "--project"} {
		if !strings.Contains(text, want) {
			t.Errorf("the overview lacks %q", want)
		}
	}
}

// A mistake is answered with the usage and with where the full help is.
func TestAMistakeSaysWhereTheHelpIs(t *testing.T) {
	r := newRun(t)
	for _, args := range [][]string{{"add"}, {"search"}, {"show", "x"}, {"add", "text", "--kinds", "rule"}, {"confirm"}} {
		code, out := r.memory("", args...)
		name := args[0]
		if code != 1 || !strings.Contains(out, "usage: memory "+name) || !strings.Contains(out, "more: memory "+name+" --help") {
			t.Fatalf("%v: %d %q", args, code, out)
		}
	}
	if code, out := r.memory("", "serach", "x"); code != 1 || !strings.Contains(out, "memory --help") {
		t.Fatalf("an unknown command: %d %q", code, out)
	}
}

// Each heading of a help appears once, whether or not the command has flags of
// its own.
func TestNoHeadingOfAHelpIsRepeated(t *testing.T) {
	for _, name := range commandOrder {
		text := commands[name].helpText(name)
		for _, line := range strings.Split(text, "\n") {
			if line != "" && line == strings.ToUpper(line) && !strings.HasPrefix(line, " ") && strings.Count(text, "\n"+line+"\n") > 1 {
				t.Errorf("memory %s --help has the heading %q more than once", name, line)
			}
		}
		if strings.Count(text, "FLAGS EVERY COMMAND TAKES") != 1 {
			t.Errorf("memory %s --help: the heading of the flags every command takes is not there exactly once", name)
		}
		hasOwn := len(commands[name].flags) > 0
		if hasOwn != strings.Contains(text, "\nFLAGS\n") {
			t.Errorf("memory %s --help: the FLAGS heading is %v but the command has own flags: %v", name, !hasOwn, hasOwn)
		}
	}
}
