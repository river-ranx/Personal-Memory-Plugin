package hooks

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Agents is every agent the hook speaks to.
var Agents = []string{"claude", "codex", "antigravity"}

// The two moments the hook answers. All three agents name them the same.
const (
	startEvent = "SessionStart"
	endEvent   = "Stop"
)

// Events is every event the hook answers, in the order it is installed.
var Events = []string{startEvent, endEvent}

// stop is what of a Stop payload the hook reads: Claude Code and Codex write
// snake_case, Antigravity camelCase.
type stop struct {
	StopHookActive     bool   `json:"stop_hook_active"`
	PermissionMode     string `json:"permission_mode"`
	Cwd                string `json:"cwd"`
	ContinuationActive bool   `json:"continuationActive"`
	TerminationReason  string `json:"terminationReason"`
}

// Run answers one hook call: agent's event, its payload on in, its answer on
// out. It never fails the agent — memory is a help, not a gate — so an event
// that is not understood is reported on errw and answered with nothing.
func Run(agent, event string, in io.Reader, out, errw io.Writer, getenv func(string) string) {
	payload, _ := io.ReadAll(in)
	known := slices.Contains(Agents, agent)
	var answer any
	switch {
	case known && event == startEvent:
		answer = sessionStart(agent)
	case known && event == endEvent:
		answer = turnEnd(agent, payload, getenv)
	default:
		fmt.Fprintf(errw, "memory hook %s %s: not an event memory answers (agents: %v, events: %v)\n", agent, event, Agents, Events)
	}
	if answer != nil {
		b, _ := json.Marshal(answer)
		fmt.Fprintln(out, string(b))
	}
}

// sessionStart is the prompt, in the shape agent reads.
func sessionStart(agent string) any {
	if agent == "antigravity" {
		// A system message stays in the conversation across turns. It takes
		// an object, not a string: {"systemMessage": {"systemMessage": text}}
		// (Antigravity's hooks documentation says so in as many words).
		return map[string]any{"injectSteps": []map[string]any{{"systemMessage": map[string]string{"systemMessage": Note}}}}
	}
	return map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": startEvent, "additionalContext": Note}}
}

// turnEnd keeps the agent from stopping once, with [TurnEnd] — unless it was
// switched off ([switchedOff]), or reminding would go wrong: the stop is the
// one a reminder already asked for (asking again would never end), the agent
// may not run commands, or there is nothing to look back on. A payload it
// cannot read gets no reminder, for the same reason: a loop is worse than a
// missed one.
func turnEnd(agent string, payload []byte, getenv func(string) string) any {
	var p stop
	remind := json.Unmarshal(payload, &p) == nil && !switchedOff(getenv)
	if agent == "antigravity" {
		// Only a turn that ended normally; continuationActive is Antigravity's
		// stop_hook_active. Any decision but "continue" lets it stop.
		if !remind || p.ContinuationActive || p.TerminationReason != "NO_TOOL_CALL" {
			return map[string]any{"decision": "stop"}
		}
		return map[string]any{"decision": "continue", "reason": TurnEnd}
	}
	if !remind || p.StopHookActive || p.PermissionMode == "plan" ||
		agent == "codex" && consolidating(p.Cwd, getenv) {
		return nil
	}
	return map[string]any{"decision": "block", "reason": TurnEnd}
}

// switchedOff is whether whoever started the agent asked for no turn-end
// reminder: MEMORY_TURN_END=off (or 0, false). A script running `claude -p`
// sets it, since nothing in the payload tells a script from a person — an
// app driving the agent through its SDK looks the same as a script does.
func switchedOff(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv("MEMORY_TURN_END"))) {
	case "off", "0", "false", "no":
		return true
	}
	return false
}

// consolidating is whether a Codex turn is Codex's own memory consolidation,
// an agent it runs in its memories directory: Codex runs Stop hooks for it too,
// and gives up consolidating when one blocks.
func consolidating(cwd string, getenv func(string) string) bool {
	if cwd == "" {
		return false
	}
	home := getenv("CODEX_HOME")
	if home == "" {
		dir, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		home = filepath.Join(dir, ".codex")
	}
	memories := filepath.Join(home, "memories")
	return cwd == memories || strings.HasPrefix(cwd, memories+string(filepath.Separator))
}
