package hooks

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// callWith runs one hook with env as its environment and returns what it
// printed and what it reported.
func callWith(env map[string]string, agent, event, payload string) (out, errw string) {
	var o, e bytes.Buffer
	Run(agent, event, strings.NewReader(payload), &o, &e, func(k string) string { return env[k] })
	return strings.TrimSpace(o.String()), strings.TrimSpace(e.String())
}

func call(agent, event, payload string) (out, errw string) {
	return callWith(nil, agent, event, payload)
}

// added is the additionalContext of a Claude Code / Codex answer to event.
func added(t *testing.T, event, out string) string {
	t.Helper()
	var a struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &a); err != nil {
		t.Fatalf("not JSON: %q", out)
	}
	if a.HookSpecificOutput.HookEventName != event {
		t.Fatalf("answers %s as %q", event, a.HookSpecificOutput.HookEventName)
	}
	return a.HookSpecificOutput.AdditionalContext
}

// decision is an Antigravity Stop answer: its decision and its reason.
func decision(t *testing.T, out string) (string, string) {
	t.Helper()
	var a struct{ Decision, Reason string }
	if err := json.Unmarshal([]byte(out), &a); err != nil {
		t.Fatalf("not JSON: %q", out)
	}
	return a.Decision, a.Reason
}

// A session starting is answered with the one prompt, in each agent's shape.
func TestASessionStartsWithThePrompt(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		out, errw := call(agent, "SessionStart", `{"session_id":"s1","cwd":"/work/x","source":"startup"}`)
		if errw != "" || added(t, "SessionStart", out) != Note {
			t.Fatalf("%s: %q %q", agent, out, errw)
		}
	}
	// Antigravity: {"injectSteps":[{"systemMessage":{"systemMessage":"…"}}]} — the
	// message is an object. A plain string there is not what it reads, and would
	// not decode into this shape.
	out, errw := call("antigravity", "SessionStart", `{"conversationId":"g1"}`)
	var got struct {
		InjectSteps []struct {
			SystemMessage struct {
				SystemMessage string `json:"systemMessage"`
			} `json:"systemMessage"`
		} `json:"injectSteps"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || errw != "" ||
		len(got.InjectSteps) != 1 || got.InjectSteps[0].SystemMessage.SystemMessage != Note {
		t.Fatalf("antigravity: %q %q", out, errw)
	}
}

// What a session start's payload holds, or whether it is readable at all,
// changes nothing: the prompt is the same.
func TestASessionStartIgnoresItsPayload(t *testing.T) {
	for _, payload := range []string{"", "not json", `{"prompt":"记住这个"}`, `{"session_id":`} {
		out, errw := call("claude", "SessionStart", payload)
		if errw != "" || added(t, "SessionStart", out) != Note {
			t.Fatalf("payload %q: %q %q", payload, out, errw)
		}
	}
}

// The prompt leaves the judgment to the agent: it says when to look, what is
// worth writing, what is not; it does not say a message called for either.
func TestThePromptLeavesTheJudgmentToTheAgent(t *testing.T) {
	for _, want := range []string{"memory search", "memory add", "由你判断", "用户明确要你记住的，一定记", "流水账", "没说学到了什么", "只记教训", "memory skill", "memory --help"} {
		if !strings.Contains(Note, want) {
			t.Fatalf("the prompt lacks %q:\n%s", want, Note)
		}
	}
	for _, not := range []string{"刚说了", "just stated", "pending"} {
		if strings.Contains(Note, not) {
			t.Fatalf("the prompt claims something about a message (%q)", not)
		}
	}
	for _, want := range []string{"线索，不是事实", "以后者为准", "核实"} {
		if !strings.Contains(Note, want) {
			t.Fatalf("the prompt does not tell the agent to weigh what it finds: lacks %q", want)
		}
	}
	// The prompt shows a write with both flags and says how to fill them: what
	// the prompt does not show, agents leave out.
	for _, want := range []string{write, kinds, keywords, fix, example} {
		if !strings.Contains(Note, want) {
			t.Fatalf("the prompt does not say how to fill a write's flags: lacks %q", want)
		}
	}
	// Once a session, so it may take the room to set the write out on lines of
	// its own, with an example, and the two ways to fix one; it may not grow
	// past that.
	if n := len([]rune(Note)); n > 830 {
		t.Fatalf("the prompt is %d characters: it goes in front of every session", n)
	}
}

// Claude Code and Codex look back over a turn when the person's next message
// comes: the review rides along as context and holds nothing up, so a turn
// with nothing to record costs no reply of its own. What the person typed is
// not read: every message gets the same review.
func TestTheNextMessageBringsTheReviewOfTheLastTurn(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		for _, payload := range []string{
			`{"session_id":"s","cwd":"/work/x","permission_mode":"default","prompt":"记住：发版只走 release 分支"}`,
			`{"prompt":"今天天气怎么样"}`,
			"not json",
		} {
			out, errw := call(agent, "UserPromptSubmit", payload)
			if errw != "" || added(t, "UserPromptSubmit", out) != LastTurn {
				t.Fatalf("%s %s: %q %q", agent, payload, out, errw)
			}
		}
	}
}

// No review where it does not belong: plan mode, where the agent may not run
// commands; or a Codex subagent's task, which is no person's message.
func TestNoReviewWhereItDoesNotBelong(t *testing.T) {
	for _, c := range []struct{ agent, payload string }{
		{"claude", `{"permission_mode":"plan","prompt":"x"}`},
		{"codex", `{"permission_mode":"plan","prompt":"x"}`},
		{"codex", `{"agent_id":"a1","agent_type":"explorer","prompt":"look at the logs"}`},
	} {
		if out, errw := call(c.agent, "UserPromptSubmit", c.payload); out != "" || errw != "" {
			t.Fatalf("%s %s: %q %q", c.agent, c.payload, out, errw)
		}
	}
}

// Antigravity looks back when a turn ends: it is kept from stopping once; the
// stop that reminder asked for goes through, or it would never stop.
func TestAnAntigravityTurnEndsWithTheReminderOnce(t *testing.T) {
	out, _ := call("antigravity", "Stop", `{"conversationId":"g","terminationReason":"NO_TOOL_CALL","continuationActive":false}`)
	if d, r := decision(t, out); d != "continue" || r != TurnEnd {
		t.Fatalf("%q", out)
	}
	for _, payload := range []string{
		`{"terminationReason":"NO_TOOL_CALL","continuationActive":true}`, // the stop the reminder asked for
		`{"terminationReason":"ERROR","continuationActive":false}`,
		`{"terminationReason":"USER_CANCELED","continuationActive":false}`,
		`{"terminationReason":"MAX_INVOCATIONS","continuationActive":false}`,
		`{"terminationReason":"MAX_FORCED_INVOCATIONS","continuationActive":false}`,
		`{`, // unreadable: a loop is worse than a missed reminder
	} {
		if d, _ := decision(t, first(call("antigravity", "Stop", payload))); d != "stop" {
			t.Fatalf("%s: not let stop", payload)
		}
	}
}

func first(out, _ string) string { return out }

// Whoever starts the agent can switch the review off — a script running
// `claude -p` looks no different from a person — and nothing else with it.
func TestTheReviewCanBeSwitchedOff(t *testing.T) {
	for _, off := range []string{"off", "OFF", "0", "false", " no "} {
		env := map[string]string{"MEMORY_REVIEW": off}
		if out, _ := callWith(env, "claude", "UserPromptSubmit", `{"prompt":"x"}`); out != "" {
			t.Fatalf("MEMORY_REVIEW=%q: %q", off, out)
		}
		out, _ := callWith(env, "antigravity", "Stop", `{"terminationReason":"NO_TOOL_CALL"}`)
		if d, _ := decision(t, out); d != "stop" {
			t.Fatalf("antigravity, MEMORY_REVIEW=%q: not let stop", off)
		}
		if out, _ := callWith(env, "codex", "SessionStart", "{}"); added(t, "SessionStart", out) != Note {
			t.Fatal("switching the review off took the session's prompt with it")
		}
	}
	for _, on := range []string{"", "on", "1"} {
		if out, _ := callWith(map[string]string{"MEMORY_REVIEW": on}, "codex", "UserPromptSubmit", "{}"); out == "" {
			t.Fatalf("MEMORY_REVIEW=%q: no review", on)
		}
	}
}

// The hook decides nothing about what the person typed: every moment but the
// ones an agent's review is tied to — a tool call, a compaction, another
// agent's moment — and any agent it does not know is not answered, only
// reported to the agent's error log.
func TestNothingElseIsAnswered(t *testing.T) {
	for agent, events := range map[string][]string{
		"claude":      {"Stop", "PostToolUse", "PreCompact", "Nope"},
		"codex":       {"Stop", "PostToolUse", "Nope"},
		"antigravity": {"UserPromptSubmit", "PreInvocation", "PostToolUse", "Nope"},
	} {
		for _, event := range events {
			out, errw := call(agent, event, `{"prompt":"记住：发版只走 release 分支","lastUserInput":"记住：用 pnpm"}`)
			if out != "" || !strings.Contains(errw, "not an event memory answers") {
				t.Fatalf("%s %s: %q %q", agent, event, out, errw)
			}
		}
	}
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "Stop"} {
		if out, errw := call("vim", event, "{}"); out != "" || !strings.Contains(errw, "not an event memory answers") {
			t.Fatalf("an unknown agent's %s: %q %q", event, out, errw)
		}
	}
}

// Both reviews ask the two questions, name the command for each, and spell
// out the way through when there is nothing to do: say nothing of it. They
// are short, since one comes with every turn.
func TestTheReviewsAskAndLetGo(t *testing.T) {
	for name, text := range map[string]string{"LastTurn": LastTurn, "TurnEnd": TurnEnd} {
		for _, want := range []string{write, "memory confirm", "--outcome verified", "--supersedes", "contradicted",
			"用户要你记住的，一定记", "不要为此再去查", "不是用户"} {
			if !strings.Contains(text, want) {
				t.Fatalf("%s lacks %q:\n%s", name, want, text)
			}
		}
		// A review comes with every turn, so it stays one sentence per question;
		// 500 characters is that with the three ways a memory can be wrong.
		if n := len([]rune(text)); n > 500 {
			t.Fatalf("%s is %d characters: it comes with every turn", name, n)
		}
	}
	if !strings.Contains(LastTurn, "上一轮") || !strings.Contains(LastTurn, "悄悄做，回复里一个字也别提，直接回应用户这条消息") || !strings.Contains(LastTurn, "会话刚开始就跳过") {
		t.Fatalf("LastTurn does not say to answer the message and keep quiet about an empty review:\n%s", LastTurn)
	}
	if !strings.Contains(TurnEnd, "这一轮") || !strings.Contains(TurnEnd, "悄悄做，做完（或两件都没有）就直接结束，不要输出任何文字") {
		t.Fatalf("TurnEnd does not say to stop quietly:\n%s", TurnEnd)
	}
}

// Memory is not only for code: the agent may be doing research, writing,
// analysis or management. What is worth keeping is said once, in the session's
// prompt; the reviews point back to it instead of repeating it. And none of
// them speaks the words of one trade.
func TestThePromptsAreForAnyKindOfWork(t *testing.T) {
	if !strings.Contains(Note, worth) || !strings.Contains(Note, notWorth) || !strings.HasPrefix(Note, "[Memory]") {
		t.Fatal("the session's prompt does not say what is worth keeping")
	}
	for name, prompt := range map[string]string{"Note": Note, "LastTurn": LastTurn, "TurnEnd": TurnEnd} {
		if name != "Note" && (strings.Contains(prompt, worth) || !strings.Contains(prompt, "开头 [Memory] 说的标准") || !strings.Contains(prompt, "记学到了什么，不记发生了什么")) {
			t.Fatalf("%s does not point back to the session's prompt, in short", name)
		}
		for _, trade := range []string{"git", "代码", "部署", "合并", "测试", "环境", "踩坑", "项目"} {
			if strings.Contains(prompt, trade) {
				t.Fatalf("%s speaks of %q, which belongs to one trade:\n%s", name, trade, prompt)
			}
		}
	}
}
