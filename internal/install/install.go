// Package install puts the memory skill and the hooks where each
// agent reads them, pointing at this binary, and takes them out again.
//
// It only ever touches what is its own: the skill's directory, the hook
// entries whose command runs `memory hook`, and Antigravity's `memory`
// hook entry. Everything else in a config file is kept; the file as it was
// before memory first changed it is saved beside it as `<file>.memory-backup`.
package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"memory"
	"memory/internal/hooks"
)

// SkillName is the skill's directory and frontmatter name.
const SkillName = "memory"

// Install installs the skill and the hooks for agent and says what it
// changed.
func Install(agent string, getenv func(string) string) ([]string, error) {
	t, err := targetOf(agent, getenv)
	if err != nil {
		return nil, err
	}
	bin, err := binary()
	if err != nil {
		return nil, err
	}
	skill := strings.ReplaceAll(memory.Skill, "{{MEMORY}}", bin)
	skillPath := filepath.Join(t.skills, SkillName, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(skillPath, []byte(skill), 0o644); err != nil {
		return nil, err
	}
	if err := t.setHooks(bin); err != nil {
		return nil, err
	}
	changed := []string{"skill → " + skillPath, "hooks → " + t.hooks}
	if t.note != "" {
		changed = append(changed, t.note)
	}
	return changed, nil
}

// Uninstall removes what [Install] put in place.
func Uninstall(agent string, getenv func(string) string) ([]string, error) {
	t, err := targetOf(agent, getenv)
	if err != nil {
		return nil, err
	}
	skillDir := filepath.Join(t.skills, SkillName)
	if err := os.RemoveAll(skillDir); err != nil {
		return nil, err
	}
	if err := t.setHooks(""); err != nil {
		return nil, err
	}
	return []string{"removed skill " + skillDir, "removed hooks from " + t.hooks}, nil
}

type target struct {
	skills string // the directory skills live in
	hooks  string // the file hooks are configured in
	// setHooks writes memory's hooks running bin, or removes them for "".
	setHooks func(bin string) error
	note     string
}

func targetOf(agent string, getenv func(string) string) (target, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return target{}, err
	}
	dirOr := func(env string, fallback ...string) string {
		if v := getenv(env); v != "" {
			return v
		}
		return filepath.Join(append([]string{home}, fallback...)...)
	}
	switch agent {
	case "claude":
		dir := dirOr("CLAUDE_CONFIG_DIR", ".claude")
		t := target{skills: filepath.Join(dir, "skills"), hooks: filepath.Join(dir, "settings.json")}
		t.setHooks = func(bin string) error { return setNestedHooks(t.hooks, "claude", bin) }
		return t, nil
	case "codex":
		dir := dirOr("CODEX_HOME", ".codex")
		t := target{skills: filepath.Join(dir, "skills"), hooks: filepath.Join(dir, "hooks.json"),
			note: "Codex asks you to trust new hooks: run /hooks in Codex once."}
		t.setHooks = func(bin string) error { return setNestedHooks(t.hooks, "codex", bin) }
		return t, nil
	case "antigravity":
		dir := filepath.Join(home, ".gemini", "config")
		t := target{skills: filepath.Join(dir, "skills"), hooks: filepath.Join(dir, "hooks.json")}
		t.setHooks = func(bin string) error { return setNamedHook(t.hooks, bin) }
		return t, nil
	}
	return target{}, fmt.Errorf("unknown agent %q (claude, codex, antigravity)", agent)
}

// binary is this executable's real path, which the hooks and the skill
// call. A `go run` build lives in a temporary directory and would vanish.
func binary() (string, error) {
	bin, err := os.Executable()
	if err != nil {
		return "", err
	}
	if bin, err = filepath.EvalSymlinks(bin); err != nil {
		return "", err
	}
	if strings.Contains(bin, "go-build") {
		return "", errors.New("run install from a built memory (make install), not go run")
	}
	return bin, nil
}

// timeout is each hook's limit, in seconds. The hook is one short process.
//
// Claude Code and Codex: settings `hooks` → event → [{matcher, hooks: [...]}];
// Antigravity: hooks.json → hook name → event → handlers. Either way memory
// hooks exactly the events it answers for that agent, [hooks.Events]; an
// install replaces every entry of memory's, whatever its event.
const timeout = 10

// ours is a hook command memory installed.
var ours = regexp.MustCompile(`memory'?\s+hook\s+(?:claude|codex|antigravity)\s`)

func command(bin, agent, event string) string {
	return "'" + strings.ReplaceAll(bin, "'", `'\''`) + "' hook " + agent + " " + event
}

// setNestedHooks replaces memory's entries in a Claude Code or Codex hooks
// file, keeping every other hook as it is.
func setNestedHooks(path, agent, bin string) error {
	root, err := readJSON(path)
	if err != nil {
		return err
	}
	all, _ := root["hooks"].(map[string]any)
	if all == nil {
		all = map[string]any{}
	}
	for event, value := range all {
		groups, _ := value.([]any)
		var kept []any
		for _, g := range groups {
			group, ok := g.(map[string]any)
			if !ok {
				kept = append(kept, g)
				continue
			}
			handlers, _ := group["hooks"].([]any)
			var left []any
			for _, h := range handlers {
				if handler, ok := h.(map[string]any); ok {
					if c, _ := handler["command"].(string); ours.MatchString(c + " ") {
						continue
					}
				}
				left = append(left, h)
			}
			if len(left) > 0 {
				group["hooks"] = left
				kept = append(kept, group)
			}
		}
		if len(kept) == 0 {
			delete(all, event)
		} else {
			all[event] = kept
		}
	}
	if bin != "" {
		for _, event := range hooks.Events[agent] {
			group := map[string]any{"hooks": []any{map[string]any{
				"type": "command", "command": command(bin, agent, event), "timeout": timeout,
			}}}
			groups, _ := all[event].([]any)
			all[event] = append(groups, group)
		}
	}
	if len(all) == 0 {
		delete(root, "hooks")
	} else {
		root["hooks"] = all
	}
	return writeJSON(path, root)
}

// setNamedHook sets or removes the `memory` entry of Antigravity's
// hooks file.
func setNamedHook(path, bin string) error {
	root, err := readJSON(path)
	if err != nil {
		return err
	}
	delete(root, SkillName)
	if bin != "" {
		entry := map[string]any{}
		for _, event := range hooks.Events["antigravity"] {
			handler := map[string]any{"type": "command", "command": command(bin, "antigravity", event), "timeout": timeout}
			entry[event] = []any{handler}
		}
		root[SkillName] = entry
	}
	return writeJSON(path, root)
}

func readJSON(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	root := map[string]any{}
	if len(bytes.TrimSpace(raw)) == 0 {
		return root, nil
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("%s is not JSON memory can edit: %w", path, err)
	}
	return root, nil
}

// writeJSON replaces path with root, atomically, keeping the first original
// as a backup.
func writeJSON(path string, root map[string]any) error {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(root); err != nil {
		return err
	}
	mode := fs.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
		backup := path + ".memory-backup"
		if _, err := os.Stat(backup); errors.Is(err, fs.ErrNotExist) {
			original, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if err := os.WriteFile(backup, original, mode); err != nil {
				return err
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".memory-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
