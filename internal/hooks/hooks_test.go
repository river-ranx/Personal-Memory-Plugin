package hooks

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// codexHome is CODEX_HOME in these tests.
const codexHome = "/home/x/.codex"

// call runs one hook and returns what it printed and what it reported.
func call(agent, event, payload string) (out, errw string) {
	var o, e bytes.Buffer
	getenv := func(k string) string {
		if k == "CODEX_HOME" {
			return codexHome
		}
		return ""
	}
	Run(agent, event, strings.NewReader(payload), &o, &e, getenv)
	return strings.TrimSpace(o.String()), strings.TrimSpace(e.String())
}

// context is the additionalContext of a Claude Code / Codex answer.
func context(t *testing.T, out string) string {
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
	if a.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Fatalf("answers as %q", a.HookSpecificOutput.HookEventName)
	}
	return a.HookSpecificOutput.AdditionalContext
}

// A session starting is the one moment the hook speaks, with the one prompt.
func TestASessionStartsWithThePrompt(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		out, errw := call(agent, "SessionStart", `{"session_id":"s1","cwd":"/work/x","source":"startup"}`)
		if errw != "" || context(t, out) != Note {
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
	if n := len([]rune(Note)); n > 520 {
		t.Fatalf("the prompt is %d characters: it goes in front of every session", n)
	}
}

// The hook decides nothing about what the person typed: every moment but a
// session start and a turn end — a message, a tool call — and any agent it does
// not know is not answered, only reported to the agent's error log.
func TestNothingElseIsAnswered(t *testing.T) {
	for _, event := range []string{"UserPromptSubmit", "PreInvocation", "PostToolUse", "PreCompact", "Nope"} {
		for _, agent := range Agents {
			out, errw := call(agent, event, `{"prompt":"记住：发版只走 release 分支","lastUserInput":"记住：用 pnpm"}`)
			if out != "" || !strings.Contains(errw, "not an event memory answers") {
				t.Fatalf("%s %s: %q %q", agent, event, out, errw)
			}
		}
	}
	for _, event := range Events {
		if out, errw := call("vim", event, "{}"); out != "" || !strings.Contains(errw, "not an event memory answers") {
			t.Fatalf("an unknown agent's %s: %q %q", event, out, errw)
		}
	}
}

// decision is a Stop answer: its decision and its reason.
func decision(t *testing.T, out string) (string, string) {
	t.Helper()
	if out == "" {
		return "", ""
	}
	var a struct{ Decision, Reason string }
	if err := json.Unmarshal([]byte(out), &a); err != nil {
		t.Fatalf("not JSON: %q", out)
	}
	return a.Decision, a.Reason
}

// A turn ending holds the agent back once, with the reminder; the stop that
// reminder asked for goes through, or the agent would never stop.
func TestATurnEndsWithTheReminderOnce(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		out, errw := call(agent, "Stop", `{"session_id":"s","cwd":"/work/x","permission_mode":"default","stop_hook_active":false,"last_assistant_message":"好了"}`)
		if d, r := decision(t, out); d != "block" || r != TurnEnd || errw != "" {
			t.Fatalf("%s: %q %q", agent, out, errw)
		}
		if out, errw := call(agent, "Stop", `{"session_id":"s","cwd":"/work/x","stop_hook_active":true}`); out != "" || errw != "" {
			t.Fatalf("%s, the second stop: %q %q", agent, out, errw)
		}
	}
	out, _ := call("antigravity", "Stop", `{"conversationId":"g","terminationReason":"NO_TOOL_CALL","continuationActive":false}`)
	if d, r := decision(t, out); d != "continue" || r != TurnEnd {
		t.Fatalf("antigravity: %q", out)
	}
	out, _ = call("antigravity", "Stop", `{"conversationId":"g","terminationReason":"NO_TOOL_CALL","continuationActive":true}`)
	if d, _ := decision(t, out); d != "stop" {
		t.Fatalf("antigravity, the second stop: %q", out)
	}
}

// No reminder where it would go wrong: a turn that did not end normally, an
// agent that may not run commands, Codex's own memory consolidation, a payload
// that cannot be read (a loop is worse than a missed reminder).
func TestNoReminderWhereItWouldGoWrong(t *testing.T) {
	for _, c := range []struct{ agent, payload string }{
		{"claude", `{"cwd":"/work/x","permission_mode":"plan","stop_hook_active":false}`},
		{"codex", `{"cwd":"/work/x","permission_mode":"plan","stop_hook_active":false}`},
		{"codex", `{"cwd":"` + codexHome + `/memories","stop_hook_active":false}`},
		{"codex", `{"cwd":"` + codexHome + `/memories/extensions","stop_hook_active":false}`},
		{"claude", `not json`},
		{"codex", ``},
	} {
		if out, errw := call(c.agent, "Stop", c.payload); out != "" || errw != "" {
			t.Fatalf("%s %s: %q %q", c.agent, c.payload, out, errw)
		}
	}
	// A directory that only starts like the memories directory is someone's work.
	if d, _ := decision(t, first(call("codex", "Stop", `{"cwd":"`+codexHome+`/memories-app","stop_hook_active":false}`))); d != "block" {
		t.Fatal("a look-alike directory got no reminder")
	}
	for _, reason := range []string{"ERROR", "USER_CANCELED", "MAX_INVOCATIONS", "MAX_FORCED_INVOCATIONS"} {
		out, _ := call("antigravity", "Stop", `{"terminationReason":"`+reason+`","continuationActive":false}`)
		if d, _ := decision(t, out); d != "stop" {
			t.Fatalf("antigravity %s: %q", reason, out)
		}
	}
	if d, _ := decision(t, first(call("antigravity", "Stop", `{`))); d != "stop" {
		t.Fatal("antigravity, an unreadable payload: not let stop")
	}
}

func first(out, _ string) string { return out }

// The reminder asks the two questions, names the commands for each, and
// spells out the way out, which most turns take.
func TestTheReminderAsksAndLetsGo(t *testing.T) {
	for _, want := range []string{"memory add", "memory confirm", "--outcome verified", "--supersedes", "contradicted",
		"用户要你记住的，一定记", "不要为此再去查", "直接结束，不要输出任何文字", "不是用户的新消息"} {
		if !strings.Contains(TurnEnd, want) {
			t.Fatalf("the reminder lacks %q:\n%s", want, TurnEnd)
		}
	}
	if n := len([]rune(TurnEnd)); n > 420 {
		t.Fatalf("the reminder is %d characters: it comes at the end of every turn", n)
	}
}

// Memory is not only for code: the agent may be doing research, writing,
// analysis or management. What is worth keeping is said once, in the session's
// prompt; the turn-end reminder points back to it instead of repeating it. And
// neither speaks the words of one trade.
func TestThePromptsAreForAnyKindOfWork(t *testing.T) {
	if !strings.Contains(Note, worth) || !strings.Contains(Note, notWorth) || !strings.HasPrefix(Note, "[Memory]") {
		t.Fatal("the session's prompt does not say what is worth keeping")
	}
	if strings.Contains(TurnEnd, worth) || !strings.Contains(TurnEnd, "开头 [Memory] 说的标准") || !strings.Contains(TurnEnd, "记学到了什么，不记发生了什么") {
		t.Fatal("the reminder does not point back to the session's prompt, in short")
	}
	for name, prompt := range map[string]string{"Note": Note, "TurnEnd": TurnEnd} {
		for _, trade := range []string{"git", "代码", "部署", "合并", "测试", "环境", "踩坑", "项目"} {
			if strings.Contains(prompt, trade) {
				t.Fatalf("%s speaks of %q, which belongs to one trade:\n%s", name, trade, prompt)
			}
		}
	}
}

// What a session start's payload holds, or whether it is readable at all,
// changes nothing: the prompt is the same.
func TestASessionStartIgnoresItsPayload(t *testing.T) {
	for _, payload := range []string{"", "not json", `{"prompt":"记住这个"}`, `{"session_id":`} {
		out, errw := call("claude", "SessionStart", payload)
		if errw != "" || context(t, out) != Note {
			t.Fatalf("payload %q: %q %q", payload, out, errw)
		}
	}
}

// Whoever starts the agent can switch the turn-end reminder off — a script
// running `claude -p` looks no different from a person — and nothing else.
func TestTheTurnEndReminderCanBeSwitchedOff(t *testing.T) {
	stopWith := func(agent, payload, value string) string {
		var o, e bytes.Buffer
		Run(agent, "Stop", strings.NewReader(payload), &o, &e, func(k string) string {
			if k == "MEMORY_TURN_END" {
				return value
			}
			return ""
		})
		return strings.TrimSpace(o.String())
	}
	claude := `{"cwd":"/work/x","stop_hook_active":false}`
	antigravity := `{"terminationReason":"NO_TOOL_CALL","continuationActive":false}`
	for _, off := range []string{"off", "OFF", "0", "false", " no "} {
		if out := stopWith("claude", claude, off); out != "" {
			t.Fatalf("MEMORY_TURN_END=%q: %q", off, out)
		}
		if d, _ := decision(t, stopWith("antigravity", antigravity, off)); d != "stop" {
			t.Fatalf("antigravity, MEMORY_TURN_END=%q: not let stop", off)
		}
	}
	for _, on := range []string{"", "on", "1"} {
		if d, _ := decision(t, stopWith("codex", claude, on)); d != "block" {
			t.Fatalf("MEMORY_TURN_END=%q: no reminder", on)
		}
	}
	// The session's prompt is not switched off with it.
	var o, e bytes.Buffer
	Run("claude", "SessionStart", strings.NewReader("{}"), &o, &e, func(string) string { return "off" })
	if context(t, strings.TrimSpace(o.String())) != Note {
		t.Fatal("switching the reminder off took the session's prompt with it")
	}
}
