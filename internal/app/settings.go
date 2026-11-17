package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Flow keeps user-tunable settings in a single generic JSON file at
// $FLOW_ROOT/settings.json. It is intentionally schemaless on disk: a
// nested object addressed by dotted keys (e.g. "transcripts.copy",
// "transcripts.tasks.<slug>.copy"). Missing file == all defaults; a
// missing key == that key's documented default. `flow config get|set`
// edits it; typed helpers below read it with defaults baked in so callers
// never special-case an absent file.
//
// Keeping settings here (not in flow.db) means they are human-readable,
// git-diffable, and editable without the binary — and the loader stays a
// few lines with no migration surface.

// settingsPath is $FLOW_ROOT/settings.json.
func settingsPath() (string, error) {
	root, err := flowRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "settings.json"), nil
}

// loadSettings reads settings.json into a generic map. A missing file is
// not an error — it yields an empty map (all defaults). A malformed file
// is also treated as empty so a hand-edit typo can never wedge capture or
// the hooks (which must never fail loud); callers get defaults.
func loadSettings() map[string]any {
	p, err := settingsPath()
	if err != nil {
		return map[string]any{}
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil || m == nil {
		return map[string]any{}
	}
	return m
}

// saveSettings writes the settings map atomically.
func saveSettings(m map[string]any) error {
	p, err := settingsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// settingGet walks a dotted key path and returns the value (or nil, false).
func settingGet(m map[string]any, key string) (any, bool) {
	parts := strings.Split(key, ".")
	var cur any = m
	for _, part := range parts {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = obj[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// settingSet writes value at a dotted key path, creating intermediate
// objects as needed. Returns an error only if the path runs through a
// non-object (a scalar already occupies an intermediate key).
func settingSet(m map[string]any, key string, value any) error {
	parts := strings.Split(key, ".")
	cur := m
	for i, part := range parts[:len(parts)-1] {
		next, ok := cur[part]
		if !ok {
			child := map[string]any{}
			cur[part] = child
			cur = child
			continue
		}
		child, ok := next.(map[string]any)
		if !ok {
			return fmt.Errorf("cannot set %q: %q is a value, not a section", key, strings.Join(parts[:i+1], "."))
		}
		cur = child
	}
	cur[parts[len(parts)-1]] = value
	return nil
}

// parseSettingValue coerces a CLI string to bool, int, or string (in that
// order) so `flow config set transcripts.copy off` stores a real bool. The
// common on/off and yes/no spellings are accepted alongside true/false.
func parseSettingValue(s string) any {
	if b, ok := parseBoolish(s); ok {
		return b
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
		return f
	}
	return s
}

// parseBoolish recognizes the boolean spellings flow accepts in settings
// (true/false, on/off, yes/no), case-insensitively.
func parseBoolish(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "on", "yes":
		return true, true
	case "false", "off", "no":
		return false, true
	}
	return false, false
}

// settingBool reads a dotted key as a bool, returning def when absent or
// not a bool. JSON numbers/strings that look boolean are tolerated.
func settingBool(m map[string]any, key string, def bool) bool {
	v, ok := settingGet(m, key)
	if !ok {
		return def
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		if b, ok := parseBoolish(t); ok {
			return b
		}
	}
	return def
}

// settingInt reads a dotted key as an int, returning def when absent or not
// numeric. JSON decodes numbers as float64; a numeric string is tolerated.
func settingInt(m map[string]any, key string, def int) int {
	v, ok := settingGet(m, key)
	if !ok {
		return def
	}
	switch t := v.(type) {
	case float64:
		return int(t)
	case int64:
		return int(t)
	case int:
		return t
	case string:
		if n, err := strconv.Atoi(t); err == nil {
			return n
		}
	}
	return def
}

// settingFloat reads a dotted key as a float64, returning def when absent
// or not numeric. JSON decodes numbers as float64; an int or a numeric
// string is tolerated.
func settingFloat(m map[string]any, key string, def float64) float64 {
	v, ok := settingGet(m, key)
	if !ok {
		return def
	}
	switch t := v.(type) {
	case float64:
		return t
	case int64:
		return float64(t)
	case int:
		return float64(t)
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil {
			return f
		}
	}
	return def
}

// ----- transcript-copy settings (typed, with documented defaults) -----

// transcriptsCopyEnabled reports whether flow should keep its own copy of
// the given task's transcript. Precedence: a per-task override
// (transcripts.tasks.<slug>.copy) wins; else the global transcripts.copy;
// else the default (true — opt-OUT, per the user's choice).
func transcriptsCopyEnabled(slug string) bool {
	m := loadSettings()
	if v, ok := settingGet(m, "transcripts.tasks."+slug+".copy"); ok {
		return boolish(v, true)
	}
	return settingBool(m, "transcripts.copy", true)
}

// transcriptsMaxToolResultBytes is the per-record cap on tool-result text
// in the flow copy. 0 (the default) means unlimited — the user chose the
// full transcript; this is only an escape hatch for pathological outputs.
func transcriptsMaxToolResultBytes() int {
	return settingInt(loadSettings(), "transcripts.max_tool_result_bytes", 0)
}

func boolish(v any, def bool) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		if b, ok := parseBoolish(t); ok {
			return b
		}
	}
	return def
}

// knownSetting is a documented GLOBAL setting with its default value, so
// `flow config get`/`list` can surface a key and its default before it has
// ever been written. Per-task override keys (…tasks.<slug>.…) are dynamic
// and not listed; they fall back to the matching global default.
type knownSetting struct {
	Key string
	Def any
}

// knownSettings is the registry behind get/list default display. Keep it in
// sync with the typed readers above and docs/settings.md.
var knownSettings = []knownSetting{
	{"transcripts.copy", true},
	{"transcripts.max_tool_result_bytes", int64(0)},
	{"nudge.notes.enabled", true},
	{"nudge.notes.base_turns", int64(15)},
	{"nudge.notes.step", 0.2},
}

// knownSettingDefault returns the documented default for a known global key.
func knownSettingDefault(key string) (any, bool) {
	for _, s := range knownSettings {
		if s.Key == key {
			return s.Def, true
		}
	}
	return nil, false
}

// formatSettingValue renders a stored setting value as the one-line string
// the CLI prints (integers without a trailing .0; bools as true/false).
func formatSettingValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	case int64:
		return strconv.FormatInt(t, 10)
	case int:
		return strconv.Itoa(t)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// flatSetting is one leaf of the settings tree as a dotted key.
type flatSetting struct {
	key string
	val any
}

// flattenSettings walks the nested settings map into dotted leaf keys, in
// deterministic (sorted) order.
func flattenSettings(m map[string]any) []flatSetting {
	var out []flatSetting
	var walk func(prefix string, cur map[string]any)
	walk = func(prefix string, cur map[string]any) {
		keys := make([]string, 0, len(cur))
		for k := range cur {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			full := k
			if prefix != "" {
				full = prefix + "." + k
			}
			if child, ok := cur[k].(map[string]any); ok {
				walk(full, child)
			} else {
				out = append(out, flatSetting{full, cur[k]})
			}
		}
	}
	walk("", m)
	return out
}

// cmdConfig implements `flow config get|set|list`.
func cmdConfig(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: config requires a subcommand (get|set|list)")
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "get":
		if len(rest) != 1 {
			fmt.Fprintln(os.Stderr, "usage: flow config get <key>")
			return 2
		}
		v, ok := settingGet(loadSettings(), rest[0])
		if ok {
			printSettingValue(v)
			return 0
		}
		// Unset but documented: show its default, marked.
		if def, known := knownSettingDefault(rest[0]); known {
			fmt.Printf("%s (default)\n", formatSettingValue(def))
			return 0
		}
		fmt.Fprintf(os.Stderr, "error: no such setting %q\n", rest[0])
		return 1
	case "set":
		if len(rest) != 2 {
			fmt.Fprintln(os.Stderr, "usage: flow config set <key> <value>")
			return 2
		}
		m := loadSettings()
		if err := settingSet(m, rest[0], parseSettingValue(rest[1])); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if err := saveSettings(m); err != nil {
			fmt.Fprintf(os.Stderr, "error: write settings: %v\n", err)
			return 1
		}
		fmt.Printf("%s = %s\n", rest[0], rest[1])
		return 0
	case "list":
		m := loadSettings()
		// Every documented global key, filling in its default where unset.
		for _, s := range knownSettings {
			if v, ok := settingGet(m, s.Key); ok {
				fmt.Printf("%s = %s\n", s.Key, formatSettingValue(v))
			} else {
				fmt.Printf("%s = %s (default)\n", s.Key, formatSettingValue(s.Def))
			}
		}
		// Any extra stored keys (per-task overrides, hand-added keys).
		for _, kv := range flattenSettings(m) {
			if _, known := knownSettingDefault(kv.key); known {
				continue
			}
			fmt.Printf("%s = %s\n", kv.key, formatSettingValue(kv.val))
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "error: unknown config subcommand %q (get|set|list)\n", sub)
		return 2
	}
}

func printSettingValue(v any) {
	switch t := v.(type) {
	case string:
		fmt.Println(t)
	case bool:
		fmt.Println(t)
	case float64:
		// Print integers without a trailing .0
		if t == float64(int64(t)) {
			fmt.Println(int64(t))
		} else {
			fmt.Println(t)
		}
	default:
		b, _ := json.Marshal(v)
		fmt.Println(string(b))
	}
}
