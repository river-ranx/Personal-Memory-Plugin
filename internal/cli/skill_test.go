package cli

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	root "memory"
	"memory/internal/hooks"
	"memory/internal/store"
)

// The skill is what an agent has in its context about this tool. It tells
// it how to think; the help tells it how to type. Where the skill does write
// a command or a number, it must be the one the tool has.

var (
	skillCommand = regexp.MustCompile("memory (search|add|confirm|show|list|archive|restore|delete)\\b([^\\n#|`]*)")
	fenced       = regexp.MustCompile("(?s)```.*?```")
	inlineFlag   = regexp.MustCompile("`(?:[^`]* )?--([a-z][a-z-]*)[^`]*`")
)

func TestTheSkillUsesOnlyFlagsTheCommandsTake(t *testing.T) {
	skill := root.Skill
	seen := 0
	for _, m := range skillCommand.FindAllStringSubmatch(skill, -1) {
		name := m[1]
		for _, f := range flagInText.FindAllStringSubmatch(quoted.ReplaceAllString(m[2], `""`), -1) {
			if f[1] == "help" { // `memory add --help` asks for help, it is no flag of add
				continue
			}
			seen++
			if !slices.Contains(commands[name].allowed(), f[1]) {
				t.Errorf("the skill writes `memory %s … --%s`, which memory %s does not take", name, f[1], name)
			}
		}
	}
	if seen < 6 {
		t.Fatalf("only %d flags were checked: the pattern no longer finds the commands", seen)
	}
	// A flag named in prose, in backticks, is a flag some command takes.
	known := []string{"help"}
	for _, sp := range commands {
		known = append(known, sp.allowed()...)
	}
	for _, m := range inlineFlag.FindAllStringSubmatch(fenced.ReplaceAllString(skill, ""), -1) {
		if !slices.Contains(known, m[1]) {
			t.Errorf("the skill names a flag, --%s, that no command takes", m[1])
		}
	}
}

func TestTheSkillStatesWhatTheToolDoes(t *testing.T) {
	skill := root.Skill
	if !strings.HasPrefix(skill, "---\nname: memory\n") || strings.Count(skill, "{{MEMORY}}") != 1 {
		t.Fatal("the skill is not named memory, or does not carry the binary's path once")
	}
	for _, want := range []string{
		fmt.Sprint(store.ContentLimit) + " 字", // the longest memory
		"memory --help", "memory add --help",  // where the rest is
		"`--keywords", "`--supersedes", "--outcome verified", "--outcome contradicted",
	} {
		if !strings.Contains(skill, want) {
			t.Errorf("the skill lacks %q", want)
		}
	}
	for _, kind := range store.Kinds {
		if !strings.Contains(skill, "`"+kind+"`") {
			t.Errorf("the skill does not name the kind %s", kind)
		}
	}
	for _, name := range []string{"search", "add", "confirm", "show"} {
		if !strings.Contains(skill, "memory "+name) {
			t.Errorf("the skill never shows memory %s", name)
		}
	}
	// It is loaded into the context of every session that uses it.
	// It tells the agent to weigh what it finds, not to take it as given.
	for _, want := range []string{"线索，不是事实", "信任顺序", "难撤销", "may be out of date", "修正那条记忆"} {
		if !strings.Contains(skill, want) {
			t.Errorf("the skill does not tell the agent how to weigh a memory: lacks %q", want)
		}
	}
	if n := strings.Count(skill, "\n"); n > 150 {
		t.Errorf("the skill is %d lines: what it says must earn its place in the context", n)
	}
}

// The part of the skill that says when to write was written once, and approved
// as it is: it is the judgment the whole tool leaves to the agent. It does not
// change by accident.
func TestWhenToWriteIsAsApproved(t *testing.T) {
	for _, line := range []string{
		"## 什么时候记",
		"**由你判断，没有任何程序替你决定**：每一轮的回顾提醒都一样，不看用户说了什么，也不看关键词。",
		"边做事边问自己：**换一个新会话，我会希望自己一开始就知道这件事吗？** 会，就记。",
		"- **用户明确要你记住的，一定记**：「记住」「以后都这样」「从现在起」「我决定」「别再…」。",
		"- **用户没说也该记**：被用户纠正过的做法、用户反复强调的偏好、定下的结论和决策（连同理由）、",
		"  概念和口径的界定、资料来源可不可靠及出处、试过不行的思路及原因、人和组织的背景与约定。",
		"- 做完一段较多的工作、任务结束时，回顾一遍有没有值得记的；没有就不记，不用凑数。",
	} {
		if !strings.Contains(root.Skill, line+"\n") {
			t.Errorf("the approved text of \"when to write\" changed; missing:\n%s", line)
		}
	}
}

// What is worth writing down is said in the skill and in the session's prompt
// (the turn-end reminder points back to that); they say it in the same words,
// so one cannot change without the other.
func TestTheSkillAndThePromptsAgreeOnWhatIsWorthKeeping(t *testing.T) {
	for _, item := range []string{"定下的结论和决策（连同理由）", "概念和口径的界定", "资料来源可不可靠及出处", "试过不行的思路及原因", "人和组织的背景与约定"} {
		for name, text := range map[string]string{"the skill": root.Skill, "the session prompt": hooks.Note} {
			if !strings.Contains(text, item) {
				t.Errorf("%s does not name %q", name, item)
			}
		}
	}
}
