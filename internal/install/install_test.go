package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNestedHooksKeepOthersAndReplaceOnlyOurs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	// memory's entry from a binary at another path is replaced; the person's
	// own hooks stay.
	// An install replaces every entry of memory's, whatever its event — here a
	// Stop entry memory no longer hooks for Claude Code.
	other := `{"model":"opus","hooks":{"Stop":[{"hooks":[{"type":"command","command":"say done"},{"type":"command","command":"'/old/memory' hook claude Stop"}]}],
	  "SessionStart":[{"hooks":[{"type":"command","command":"'/old/memory' hook claude SessionStart"}]}]}}`
	if err := os.WriteFile(path, []byte(other), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 { // installing twice changes nothing more
		if err := setNestedHooks(path, "claude", "/new/memory"); err != nil {
			t.Fatal(err)
		}
	}
	root := read(t, path)
	if root["model"] != "opus" {
		t.Fatal("an unrelated setting was lost")
	}
	hooks := root["hooks"].(map[string]any)
	// memory's two events, and the person's own Stop hook, untouched.
	if len(hooks) != 3 || hooks["SessionStart"] == nil || hooks["UserPromptSubmit"] == nil || hooks["Stop"] == nil {
		t.Fatalf("events %v", keys(hooks))
	}
	if stop := dump(hooks["Stop"]); len(hooks["Stop"].([]any)) != 1 || !strings.Contains(stop, "say done") || strings.Contains(stop, "memory") {
		t.Fatalf("Stop: %s", stop)
	}
	if prompt := dump(hooks["UserPromptSubmit"]); strings.Count(prompt, "'/new/memory' hook claude UserPromptSubmit") != 1 {
		t.Fatalf("UserPromptSubmit: %s", prompt)
	}
	start := dump(hooks["SessionStart"])
	if strings.Contains(start, "/old/") || strings.Count(start, "/new/memory") != 1 {
		t.Fatalf("SessionStart: %s", start)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode())
	}
	if b, _ := os.ReadFile(path + ".memory-backup"); string(b) != other {
		t.Fatal("no backup of the original")
	}

	if err := setNestedHooks(path, "claude", ""); err != nil {
		t.Fatal(err)
	}
	root = read(t, path)
	hooks = root["hooks"].(map[string]any)
	if len(hooks) != 1 || len(hooks["Stop"].([]any)) != 1 {
		t.Fatalf("after uninstall: %s", dump(hooks))
	}
}

func TestAntigravityHookIsOneNamedEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	os.WriteFile(path, []byte(`{"lint":{"Stop":[{"command":"./lint.sh"}]}}`), 0o644)
	if err := setNamedHook(path, "/bin/memory"); err != nil {
		t.Fatal(err)
	}
	root := read(t, path)
	entry := dump(root[SkillName])
	for _, want := range []string{`"SessionStart"`, `'/bin/memory' hook antigravity SessionStart`, `"Stop"`, `'/bin/memory' hook antigravity Stop`} {
		if !strings.Contains(entry, want) {
			t.Fatalf("lacks %s: %s", want, entry)
		}
	}
	for _, gone := range []string{`"PreInvocation"`, `"PostToolUse"`} {
		if strings.Contains(entry, gone) {
			t.Fatalf("still hooks %s: %s", gone, entry)
		}
	}
	if err := setNamedHook(path, ""); err != nil {
		t.Fatal(err)
	}
	if root = read(t, path); len(root) != 1 || root["lint"] == nil {
		t.Fatalf("after uninstall: %s", dump(root))
	}
}

func TestAPathWithAQuoteIsQuoted(t *testing.T) {
	if got := command("/a b/it's/memory", "codex", "Stop"); got != `'/a b/it'\''s/memory' hook codex Stop` {
		t.Fatal(got)
	}
	if !ours.MatchString(command("/x/memory", "codex", "Stop") + " ") {
		t.Fatal("our own command is not recognised")
	}
}

func read(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	root := map[string]any{}
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatal(err)
	}
	return root
}

func dump(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func keys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
