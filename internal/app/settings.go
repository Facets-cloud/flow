package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

// readSettingsFile reads settings.json into a generic map and reports whether
// the file was present-but-unusable. A missing file is NOT an error — it
// yields an empty map (all defaults) with bad=false. A present file that is
// unreadable or not valid JSON yields (nil, true): callers decide what a
// broken settings file means. The privacy-sensitive readers FAIL CLOSED on
// bad=true (transcript copy OFF); `flow config set` REFUSES to overwrite it.
func readSettingsFile() (m map[string]any, bad bool) {
	p, err := settingsPath()
	if err != nil {
		return map[string]any{}, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, false
		}
		return nil, true // present but unreadable — treat as broken
	}
	var mm map[string]any
	if json.Unmarshal(b, &mm) != nil || mm == nil {
		return nil, true // present but not valid JSON object
	}
	return mm, false
}

// loadSettings reads settings.json into a generic map. A missing OR malformed
// file yields an empty map (all defaults) so read-only display paths
// (`config get`/`list`) never fail loud. Mutation and privacy-sensitive paths
// use readSettingsFile directly so they can tell a broken file apart.
func loadSettings() map[string]any {
	m, bad := readSettingsFile()
	if bad {
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

// transcriptCopyDefault is the shipped default for transcripts.copy: OFF.
// Keeping a second copy of every session transcript is opt-in — the user
// turns it on with `flow config set transcripts.copy true` (or per task).
const transcriptCopyDefault = false

// transcriptMaxToolResultDefault caps tool-result text per record in the
// flow copy by default (16 KiB). A pathological tool dump can be many MB;
// recall never needs the full blob. 0 means unlimited.
const transcriptMaxToolResultDefault = 16 << 10

// transcriptsCopyEnabled reports whether flow should keep its own copy of
// the given task's transcript. Precedence: a per-task override
// (transcripts.tasks.<slug>.copy) wins; else the global transcripts.copy;
// else the default (OFF — opt-IN). It FAILS CLOSED: a malformed or
// unreadable settings.json disables copying (and is logged) rather than
// silently falling back to a permissive default.
func transcriptsCopyEnabled(slug string) bool {
	m, bad := readSettingsFile()
	if bad {
		logTranscriptErr("settings.json unreadable/malformed — transcript copy disabled (fail-closed)")
		return false
	}
	global := settingBool(m, "transcripts.copy", transcriptCopyDefault)
	if v, ok := settingGet(m, "transcripts.tasks."+slug+".copy"); ok {
		return boolish(v, global)
	}
	return global
}

// transcriptsMaxToolResultBytes is the per-record cap on tool-result text in
// the flow copy. Defaults to transcriptMaxToolResultDefault; 0 means
// unlimited. Fails closed to the default on a broken settings file.
func transcriptsMaxToolResultBytes() int {
	m, bad := readSettingsFile()
	if bad {
		return transcriptMaxToolResultDefault
	}
	return settingInt(m, "transcripts.max_tool_result_bytes", transcriptMaxToolResultDefault)
}

// transcriptsRetentionDays is the age (in days) past which flow copies are
// pruned by backfill and purge. 0 (the default) keeps copies forever.
func transcriptsRetentionDays() int {
	m, bad := readSettingsFile()
	if bad {
		return 0
	}
	return settingInt(m, "transcripts.retention_days", 0)
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

// settingKind is the value type a setting accepts, used to type-check
// `config set` input and reject e.g. a string for a bool knob.
type settingKind int

const (
	kindBool settingKind = iota
	kindInt
	kindFloat
	kindString
)

func (k settingKind) String() string {
	switch k {
	case kindBool:
		return "boolean (true/false, on/off, yes/no)"
	case kindInt:
		return "integer"
	case kindFloat:
		return "number"
	default:
		return "string"
	}
}

// knownSetting is a documented GLOBAL setting with its default value and type,
// so `flow config get`/`list` can surface a key and its default before it has
// ever been written, and `config set` can validate the key and value. Per-task
// override keys (…tasks.<slug>.…) are dynamic and validated by pattern (see
// validateSettingKey); they fall back to the matching global default.
type knownSetting struct {
	Key  string
	Def  any
	Kind settingKind
}

// knownSettings is the registry behind get/list default display and set
// validation. Keep it in sync with the typed readers above and
// docs/settings.md.
var knownSettings = []knownSetting{
	{"transcripts.copy", transcriptCopyDefault, kindBool},
	{"transcripts.max_tool_result_bytes", int64(transcriptMaxToolResultDefault), kindInt},
	{"transcripts.retention_days", int64(0), kindInt},
	{"nudge.notes.enabled", true, kindBool},
	{"nudge.notes.base_turns", int64(15), kindInt},
	{"nudge.notes.step", 0.2, kindFloat},
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

// rePerTask matches a per-task override key: a documented global key with a
// `.tasks.<slug>.` segment spliced in (e.g. transcripts.tasks.foo.copy from
// transcripts.copy, nudge.notes.tasks.foo.enabled from nudge.notes.enabled).
var rePerTask = regexp.MustCompile(`^(.+)\.tasks\.[^.]+\.(.+)$`)

// rePerTaskCopy matches a per-task transcript-copy override key (used by the
// first-enable notice).
var rePerTaskCopy = regexp.MustCompile(`^transcripts\.tasks\.[^.]+\.copy$`)

// perTaskGlobal returns the global key a per-task override maps to, and
// whether key is in per-task form at all.
func perTaskGlobal(key string) (string, bool) {
	if m := rePerTask.FindStringSubmatch(key); m != nil {
		return m[1] + "." + m[2], true
	}
	return "", false
}

// validateSettingKey reports whether key is a settable setting and, when it is
// not, the closest known key as a "did you mean" suggestion (empty if nothing
// is close). It returns the value-kind to type-check against for a valid key.
// A per-task override (X.tasks.<slug>.Y) is valid iff its global X.Y is known.
func validateSettingKey(key string) (ok bool, kind settingKind, suggestion string) {
	if d, known := knownSettingDefault(key); known {
		return true, kindOf(d), ""
	}
	if global, isPerTask := perTaskGlobal(key); isPerTask {
		if d, known := knownSettingDefault(global); known {
			return true, kindOf(d), ""
		}
	}
	// Unknown key: suggest the nearest documented key (or its per-task
	// template) within a small edit distance so a typo like transcript.copy
	// points at transcripts.copy. Compare a per-task key against per-task
	// templates so the slug segment doesn't swamp the distance.
	cmp := key
	if global, isPerTask := perTaskGlobal(key); isPerTask {
		cmp = global
	}
	best, bestDist := "", 1<<30
	for _, s := range knownSettings {
		if d := levenshtein(cmp, s.Key); d < bestDist {
			best, bestDist = s.Key, d
		}
	}
	limit := len(cmp) / 3
	if limit < 3 {
		limit = 3
	}
	if bestDist <= limit {
		return false, kindString, best
	}
	return false, kindString, ""
}

// kindOf infers a settingKind from a registry default value.
func kindOf(def any) settingKind {
	switch def.(type) {
	case bool:
		return kindBool
	case int, int64:
		return kindInt
	case float64:
		return kindFloat
	default:
		return kindString
	}
}

// valueMatchesKind reports whether a parsed CLI value is the right type for a
// setting. An int is accepted where a float is wanted (3 is a valid number).
func valueMatchesKind(v any, kind settingKind) bool {
	switch kind {
	case kindBool:
		_, ok := v.(bool)
		return ok
	case kindInt:
		_, ok := v.(int64)
		return ok
	case kindFloat:
		switch v.(type) {
		case int64, float64:
			return true
		}
		return false
	default:
		return true
	}
}

// levenshtein is the edit distance between two short strings (settings keys),
// used only for the "did you mean" hint.
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min3(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
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
		key, rawVal := rest[0], rest[1]
		// Validate the key BEFORE touching the file: an unknown key is almost
		// always a typo (e.g. transcript.copy for transcripts.copy). Refuse it
		// with a "did you mean" hint rather than silently storing a dead key
		// while the real setting keeps its default.
		ok, kind, suggestion := validateSettingKey(key)
		if !ok {
			if suggestion != "" {
				fmt.Fprintf(os.Stderr, "error: unknown setting %q — did you mean %q?\n", key, suggestion)
			} else {
				fmt.Fprintf(os.Stderr, "error: unknown setting %q (see `flow config list` for valid keys)\n", key)
			}
			return 1
		}
		val := parseSettingValue(rawVal)
		if !valueMatchesKind(val, kind) {
			fmt.Fprintf(os.Stderr, "error: %q expects a %s; got %q\n", key, kind, rawVal)
			return 1
		}
		// FAIL CLOSED on a broken settings file: refuse to overwrite it (which
		// would silently drop every other key) — explain how to recover.
		m, bad := readSettingsFile()
		if bad {
			p, _ := settingsPath()
			fmt.Fprintf(os.Stderr, "error: %s is not valid JSON; flow refuses to overwrite it.\n", p)
			fmt.Fprintln(os.Stderr, "fix the file by hand (or delete it to reset to defaults), then retry.")
			return 1
		}
		// Was transcript copy already on? (for the first-enable notice.)
		wasCopyOn := transcriptsCopyEnabled("")
		if err := settingSet(m, key, val); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if err := saveSettings(m); err != nil {
			fmt.Fprintf(os.Stderr, "error: write settings: %v\n", err)
			return 1
		}
		fmt.Printf("%s = %s\n", key, rawVal)
		// One-time notice the first time transcript copying is switched on, so
		// the user knows what flow now stores and how to remove it.
		if b, isBool := val.(bool); isBool && b && !wasCopyOn && (key == "transcripts.copy" || rePerTaskCopy.MatchString(key)) {
			printFirstEnableNotice()
		}
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

// printFirstEnableNotice tells the user, the first time transcript copying is
// switched on, what flow now keeps and how to remove it.
func printFirstEnableNotice() {
	fmt.Println("notice: flow will now keep its own copy of this task's session transcript under " +
		"$FLOW_ROOT/tasks/<slug>/transcripts/ (0600, tool output capped). Remove copies anytime with `flow transcript purge`.")
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
