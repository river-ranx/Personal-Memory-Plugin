package hooks

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
)

// Agents is every agent the hook speaks to.
var Agents = []string{"claude", "codex", "antigravity"}

// The moments the hook answers, by each agent's name for them.
const (
	sessionStartEvent = "SessionStart"
	promptEvent       = "UserPromptSubmit"
	stopEvent         = "Stop"
)

// Events is, for each agent, the events the hook answers, in the order they
// are installed: the session's start, then the moment a turn is looked back
// over (see the package comment for why it differs).
var Events = map[string][]string{
	"claude":      {sessionStartEvent, promptEvent},
	"codex":       {sessionStartEvent, promptEvent},
	"antigravity": {sessionStartEvent, stopEvent},
}

// payload is what of a hook's input the hook reads. Claude Code and Codex
// write snake_case, Antigravity camelCase.
type payload struct {
	Cwd               string `json:"cwd"`
	PermissionMode    string `json:"permission_mode"`
	AgentID           string `json:"agent_id"`
	TerminationReason string `json:"terminationReason"`
	// ExecutionNum is, on Antigravity's Stop, how many times the agent has
	// been kept running since the person's message: 0 at the first stop, 1
	// at the stop after one continue. A pointer, to tell 0 from not sent.
	ExecutionNum *int `json:"executionNum"`
}

// Run answers one hook call: agent's event, its payload on in, its answer on
// out. It never fails the agent — memory is a help, not a gate — so an event
// that is not understood is reported on errw and answered with nothing.
func Run(agent, event string, in io.Reader, out, errw io.Writer, getenv func(string) string) {
	raw, _ := io.ReadAll(in)
	var p payload
	readable := json.Unmarshal(raw, &p) == nil
	var answer any
	switch {
	case !slices.Contains(Events[agent], event):
		fmt.Fprintf(errw, "memory hook %s %s: not an event memory answers (agents and events: %v)\n", agent, event, Events)
	case event == sessionStartEvent:
		cwd := ""
		if readable {
			cwd = p.Cwd
		}
		answer = sessionStart(agent, cwd)
	case event == promptEvent:
		answer = lastTurn(p, getenv)
	case event == stopEvent:
		answer = turnEnd(p, readable, getenv)
	}
	if answer != nil {
		b, _ := json.Marshal(answer)
		fmt.Fprintln(out, string(b))
	}
}

// sessionStart is the prompt, in the shape agent reads. A session that names
// an absolute cwd gets one more line, so a memory written after the shell has
// moved keeps the directory the session began in.
func sessionStart(agent, cwd string) any {
	text := Note
	if line := workingDirectoryLine(cwd); line != "" {
		text += "\n" + line
	}
	if agent == "antigravity" {
		// A system message stays in the conversation across turns. It takes
		// an object, not a string: {"systemMessage": {"systemMessage": text}}
		// (Antigravity's hooks documentation says so in as many words).
		return map[string]any{"injectSteps": []map[string]any{{"systemMessage": map[string]string{"systemMessage": text}}}}
	}
	return context(sessionStartEvent, text)
}

// workingDirectoryLine is [workingDirectoryNote] for cwd, or empty when cwd is
// missing or not absolute: then a session start carries [Note] alone.
func workingDirectoryLine(cwd string) string {
	if cwd == "" || !filepath.IsAbs(cwd) {
		return ""
	}
	return fmt.Sprintf(workingDirectoryNote, cwd, cwd)
}

// lastTurn adds [LastTurn] to the person's message (Claude Code, Codex) —
// unless the review was switched off, the agent may not run commands (plan
// mode), or the message is not a person's but a subagent's task (Codex sends
// those with an agent_id). It holds nothing up, so a payload it cannot read
// is no risk: the review comes all the same.
func lastTurn(p payload, getenv func(string) string) any {
	if switchedOff(getenv) || p.PermissionMode == "plan" || p.AgentID != "" {
		return nil
	}
	return context(promptEvent, LastTurn)
}

// turnEnd keeps Antigravity from stopping once, with [TurnEnd] — only at the
// first stop of a turn (executionNum 0): every stop after it is one a continue
// brought about, and asking again would never end. Not when it was switched
// off or the turn did not end normally either. A payload it cannot read, or
// that does not say which stop this is, gets no reminder for that reason: a
// loop is worse than a missed one. Any decision but "continue" lets it stop.
//
// executionNum, not the payload's continuationActive: measured on Antigravity
// CLI (2026-11-25), continuationActive is false at every stop, the ones a
// continue brought about included, so a guard on it never held and the agent
// was kept from stopping again and again.
func turnEnd(p payload, readable bool, getenv func(string) string) any {
	if !readable || switchedOff(getenv) || p.ExecutionNum == nil || *p.ExecutionNum != 0 || p.TerminationReason != "NO_TOOL_CALL" {
		return map[string]any{"decision": "stop"}
	}
	return map[string]any{"decision": "continue", "reason": TurnEnd}
}

// context is a Claude Code / Codex answer that adds text to the context.
func context(event, text string) any {
	return map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": event, "additionalContext": text}}
}

// switchedOff is whether whoever started the agent asked for no review of
// each turn: MEMORY_REVIEW=off (or 0, false, no). A script running `claude -p`
// sets it, since nothing in the payload tells a script from a person — an app
// driving the agent through its SDK looks the same as a script does.
func switchedOff(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv("MEMORY_REVIEW"))) {
	case "off", "0", "false", "no":
		return true
	}
	return false
}
