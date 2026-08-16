package spec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"flow/internal/harness"
)

// Hooks reports the hook-wiring capability, or nil when the manifest
// declares no [hooks] table.
func (a *Adapter) Hooks() harness.HookWirer {
	if a.spec.Hooks == nil {
		return nil
	}
	return a
}

// Strategies lists how this harness receives flow's hook context.
func (a *Adapter) Strategies() []string { return a.spec.Hooks.Strategies }

// PreparePrompt delivers SessionStart context for harnesses that declare
// prompt-prelude. Other strategies act through config or instructions and
// therefore leave the prompt untouched.
func (a *Adapter) PreparePrompt(prompt, sessionStartContext string) string {
	if !a.spec.Hooks.Has(StrategyPromptPrelude) || sessionStartContext == "" {
		return prompt
	}
	if prompt == "" {
		return sessionStartContext
	}
	return sessionStartContext + "\n\n" + prompt
}

func (a *Adapter) InstallSessionStartHook(command string) (bool, error) {
	return a.installHook(EventSessionStart, command)
}

func (a *Adapter) UninstallSessionStartHook(command string) (bool, error) {
	return a.uninstallHook(EventSessionStart, command)
}

func (a *Adapter) InstallUserPromptSubmitHook(command string) (bool, error) {
	return a.installHook(EventUserPromptSubmit, command)
}

func (a *Adapter) UninstallUserPromptSubmitHook(command string) (bool, error) {
	return a.uninstallHook(EventUserPromptSubmit, command)
}

// installHook performs whichever strategies apply to this event.
//
// Only config-patch writes anything here. prompt-prelude is applied at
// launch, and instruction-directive rides in the skills pointer block —
// both are already "installed" by the time this runs, so reporting
// added=false for them is accurate rather than a silent skip.
func (a *Adapter) installHook(event, command string) (bool, error) {
	if !a.spec.Hooks.Has(StrategyConfigPatch) {
		return false, nil
	}
	ev, ok := a.spec.Hooks.ConfigPatch.Events[event]
	if !ok {
		// The harness supports config-patch but not this event. Not an
		// error: a harness may have SessionStart and no per-prompt hook.
		return false, nil
	}
	path, err := a.hookConfigPath()
	if err != nil {
		return false, err
	}
	entry, err := a.renderEntry(event, ev.Entry, command)
	if err != nil {
		return false, err
	}
	return patchJSONArray(path, ev.Pointer, entry, command)
}

func (a *Adapter) uninstallHook(event, command string) (bool, error) {
	if !a.spec.Hooks.Has(StrategyConfigPatch) {
		return false, nil
	}
	ev, ok := a.spec.Hooks.ConfigPatch.Events[event]
	if !ok {
		return false, nil
	}
	path, err := a.hookConfigPath()
	if err != nil {
		return false, err
	}
	return unpatchJSONArray(path, ev.Pointer, command)
}

func (a *Adapter) hookConfigPath() (string, error) {
	return ExpandPath(a.spec.Hooks.ConfigPatch.File, a.vars())
}

// entryVars is the template environment for a hook entry.
type entryVars struct {
	Vars
	Command string
	Event   string
}

// renderEntry expands an entry template and parses it, so a manifest
// that produces malformed JSON fails before the user's config file is
// opened for writing rather than after it is corrupted.
func (a *Adapter) renderEntry(event, tmpl, command string) (any, error) {
	text, err := ExpandText(tmpl, entryVars{Vars: a.vars(), Command: command, Event: event})
	if err != nil {
		return nil, fmt.Errorf("hooks.config_patch.events.%s.entry: %w", event, err)
	}
	var entry any
	if err := json.Unmarshal([]byte(text), &entry); err != nil {
		return nil, fmt.Errorf(
			"hooks.config_patch.events.%s.entry did not render to valid JSON: %w\n  rendered: %s",
			event, err, text)
	}
	return entry, nil
}

// patchJSONArray appends entry to the array at pointer, creating the
// file and any missing objects along the way.
//
// Idempotency is decided by searching the existing elements for the
// command string in their serialized form. That works across wildly
// different entry shapes — claude nests the command two levels deep,
// praxis accepts a bare string — without the manifest having to say
// where the command lives.
func patchJSONArray(path, pointer string, entry any, command string) (bool, error) {
	root, err := readJSONObject(path)
	if err != nil {
		return false, err
	}
	parent, key, err := walkToParent(root, pointer)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}

	arr, _ := parent[key].([]any)
	for _, existing := range arr {
		if containsCommand(existing, command) {
			return false, nil
		}
	}
	parent[key] = append(arr, entry)

	return true, writeJSONObject(path, root)
}

// unpatchJSONArray removes every element mentioning command.
//
// Empty containers are pruned on the way out so uninstall leaves the
// user's config as it found it, not littered with `"hooks": {}`.
func unpatchJSONArray(path, pointer, command string) (bool, error) {
	root, err := readJSONObject(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	parent, key, err := walkToParent(root, pointer)
	if err != nil {
		// A missing path means nothing to remove, not a failure.
		return false, nil
	}
	arr, ok := parent[key].([]any)
	if !ok {
		return false, nil
	}
	kept := make([]any, 0, len(arr))
	for _, existing := range arr {
		if containsCommand(existing, command) {
			continue
		}
		kept = append(kept, existing)
	}
	if len(kept) == len(arr) {
		return false, nil
	}
	if len(kept) == 0 {
		delete(parent, key)
	} else {
		parent[key] = kept
	}
	pruneEmpty(root)
	return true, writeJSONObject(path, root)
}

// containsCommand reports whether a hook entry mentions command,
// whatever shape it has.
func containsCommand(entry any, command string) bool {
	data, err := json.Marshal(entry)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), command)
}

// walkToParent resolves a "/a/b/c" pointer to the object holding the
// final key, creating intermediate objects as needed.
func walkToParent(root map[string]any, pointer string) (map[string]any, string, error) {
	segs := strings.Split(strings.Trim(pointer, "/"), "/")
	if len(segs) == 0 || segs[0] == "" {
		return nil, "", fmt.Errorf("pointer %q is empty", pointer)
	}
	cur := root
	for _, seg := range segs[:len(segs)-1] {
		next, ok := cur[seg].(map[string]any)
		if !ok {
			if _, occupied := cur[seg]; occupied {
				return nil, "", fmt.Errorf("pointer %q: %q is not an object", pointer, seg)
			}
			next = map[string]any{}
			cur[seg] = next
		}
		cur = next
	}
	return cur, segs[len(segs)-1], nil
}

// pruneEmpty drops objects that became empty, depth first.
func pruneEmpty(obj map[string]any) {
	for k, v := range obj {
		child, ok := v.(map[string]any)
		if !ok {
			continue
		}
		pruneEmpty(child)
		if len(child) == 0 {
			delete(obj, k)
		}
	}
}

// readJSONObject reads a JSON object, treating an absent or empty file
// as {}. A file that exists but is not an object is an error: silently
// replacing a user's config would be worse than refusing.
func readJSONObject(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return map[string]any{}, nil
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON, refusing to overwrite it: %w", path, err)
	}
	if root == nil {
		root = map[string]any{}
	}
	return root, nil
}

// writeJSONObject writes the config back, indented, via a temp file and
// rename so a crash mid-write cannot leave the user without a settings
// file the harness needs to start.
func writeJSONObject(path string, root map[string]any) error {
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".flow-hooks-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}
