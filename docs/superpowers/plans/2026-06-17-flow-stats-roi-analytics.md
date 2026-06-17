# flow stats — usage & ROI analytics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `flow stats` command (plus `flow stats --card`) that derives usage & ROI analytics — a "lookups" spine metric, ground-truth token/activity counts, and labeled counterfactual savings — purely from data flow already keeps, cached for speed.

**Architecture:** A new pure-logic `internal/stats` package scans Claude session jsonl files + flow.db + the on-disk auto-runs/owner/kb directories into a `Stats` struct, with a derived per-file cache to keep repeat runs fast. Two thin renderers in `internal/app` (`stats.go` terminal report, `card.go` HTML card) present it. No schema change, no instrumentation in hot commands.

**Tech Stack:** Go 1.25, `modernc.org/sqlite` (pure Go, no CGO), stdlib `encoding/json`. Reuses `internal/harness/claude.EncodeCwd` to locate transcripts. Reuses `internal/flowdb` for DB reads.

## Global Constraints

- **No CGO.** Pure Go only. Do not add any C-linked dependency.
- **No new third-party dependencies.** The repo deps are `go-isatty` and `modernc.org/sqlite` only. Config + cache are **JSON** (`~/.flow/stats.json`, `~/.flow/stats-cache.json`) — NOT TOML as the spec text wrote; the repo has no TOML library and the minimal-dep rule forbids adding one. `encoding/json` is stdlib. (Spec deviation, intentional.)
- **Flag parsing:** use the `flagSet(name)` helper from `internal/app/helpers.go` (`flag.FlagSet` with `ContinueOnError`), never `flag.Parse()`.
- **Exit codes:** 0 success, 1 runtime error, 2 usage error.
- **Timestamps:** RFC3339 strings everywhere they touch disk/DB.
- **Tests:** real SQLite in a temp dir; `$HOME`/`$FLOW_ROOT` overridden to temp dirs. No DB mocks. Table-driven where natural. Run with `go test ./...`.
- **Build:** `make build` (or `go build -o flow .`) must stay green; `make test` must stay green.
- **Honesty rule:** ground-truth numbers shown plainly; every counterfactual savings figure labeled `est.` with its assumption printed. Never add the addressable-memory count into any time/$ total (unit mismatch — see spec).
- **Reuse, do not duplicate:** locate transcripts with `claude.EncodeCwd(workDir)` (exported in `internal/harness/claude/claude.go`). Do not reimplement cwd encoding.

---

### Task 1: jsonl scanner — Usage, Lookup, FileRollup, ScanJSONL

**Files:**
- Create: `internal/stats/scan.go`
- Test: `internal/stats/scan_test.go`

**Interfaces:**
- Consumes: nothing (leaf package).
- Produces:
  - `type LookupKind string` with consts `LookupResume`, `LookupReference`, `LookupCrossTask`, `LookupKB` (values `"resume"`, `"reference"`, `"cross_task"`, `"kb"`).
  - `type Lookup struct { Kind LookupKind; TS time.Time }`
  - `type Usage struct { Input, Output, CacheCreation, CacheRead int64 }` with method `func (u Usage) Total() int64`
  - `type FileRollup struct { Lookups []Lookup; Usage Usage; First, Last time.Time }`
  - `func ScanJSONL(r io.Reader, ownSlug string) (FileRollup, error)`

Classification rules inside `ScanJSONL` (deterministic, order per line):
- `assistant` record's `message.usage` → accumulate into `Usage`.
- For any record, walk `message.content` blocks:
  - `tool_use` block with `name=="Bash"`: parse `input.command` (string). If it contains `"flow show task"` → `Lookup{LookupResume}`; else if it contains `"flow show "` → `Lookup{LookupReference}`; else if it contains `"flow transcript"` → `Lookup{LookupCrossTask}`.
  - `tool_use` block with `name=="Read"`: parse `input.file_path` (string). If it contains `"/.flow/kb/"` → `Lookup{LookupKB}`. Else if it contains `"/.flow/tasks/<ownSlug>/"` → skip (own bootstrap already counted via the `flow show task` marker; avoids double count). Else if it contains `"/.flow/tasks/"` or `"/.flow/projects/"` → `Lookup{LookupCrossTask}`.
- Every `Lookup.TS` is the record's parsed `timestamp` (RFC3339Nano); zero time if missing/unparseable.
- Track `First`/`Last` from each record's parsed timestamp (ignore zero times).
- Skip malformed JSON lines silently. Use a `bufio.Scanner` with a large buffer (lines can be ~10 MB).

- [ ] **Step 1: Write the failing test**

```go
package stats

import (
	"strings"
	"testing"
)

func TestScanJSONL(t *testing.T) {
	// One assistant turn with usage, a flow-show-task bootstrap (resume),
	// a flow show reference, a flow transcript cross-task, a kb read,
	// an own-task brief read (skipped), and a sibling read (cross-task).
	lines := []string{
		`{"type":"assistant","timestamp":"2026-06-01T10:00:00.000Z","message":{"role":"assistant","usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":2,"cache_read_input_tokens":100},"content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"assistant","timestamp":"2026-06-01T10:00:01.000Z","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"flow show task"}}]}}`,
		`{"type":"assistant","timestamp":"2026-06-01T10:00:02.000Z","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"flow show project flow"}}]}}`,
		`{"type":"assistant","timestamp":"2026-06-01T10:00:03.000Z","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"flow transcript sibling"}}]}}`,
		`{"type":"assistant","timestamp":"2026-06-01T10:00:04.000Z","message":{"role":"assistant","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/Users/x/.flow/kb/org.md"}}]}}`,
		`{"type":"assistant","timestamp":"2026-06-01T10:00:05.000Z","message":{"role":"assistant","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/Users/x/.flow/tasks/mine/brief.md"}}]}}`,
		`{"type":"assistant","timestamp":"2026-06-01T10:00:06.000Z","message":{"role":"assistant","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/Users/x/.flow/tasks/other/brief.md"}}]}}`,
		`garbage-line-should-skip`,
	}
	roll, err := ScanJSONL(strings.NewReader(strings.Join(lines, "\n")), "mine")
	if err != nil {
		t.Fatalf("ScanJSONL: %v", err)
	}
	if roll.Usage.Total() != 117 {
		t.Errorf("Usage.Total = %d, want 117", roll.Usage.Total())
	}
	counts := map[LookupKind]int{}
	for _, l := range roll.Lookups {
		counts[l.Kind]++
	}
	if counts[LookupResume] != 1 || counts[LookupReference] != 1 ||
		counts[LookupCrossTask] != 2 || counts[LookupKB] != 1 {
		t.Errorf("lookup counts = %v, want resume1 reference1 cross_task2 kb1", counts)
	}
	if roll.First.IsZero() || roll.Last.Before(roll.First) {
		t.Errorf("First/Last not set correctly: %v..%v", roll.First, roll.Last)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/stats/ -run TestScanJSONL -v`
Expected: FAIL — package/types undefined (build error).

- [ ] **Step 3: Write minimal implementation**

```go
// Package stats derives flow usage & ROI analytics from data already on
// disk: Claude session jsonl transcripts, flow.db, and the auto-runs /
// owner / kb directories. Nothing here writes to those sources.
package stats

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"time"
)

// LookupKind classifies a retrieval — a moment flow served stored context.
type LookupKind string

const (
	LookupResume    LookupKind = "resume"
	LookupReference LookupKind = "reference"
	LookupCrossTask LookupKind = "cross_task"
	LookupKB        LookupKind = "kb"
)

// Lookup is one retrieval event mined from a session jsonl.
type Lookup struct {
	Kind LookupKind `json:"kind"`
	TS   time.Time  `json:"ts"`
}

// Usage is a token tally summed across a session's assistant turns.
type Usage struct {
	Input         int64 `json:"input"`
	Output        int64 `json:"output"`
	CacheCreation int64 `json:"cache_creation"`
	CacheRead     int64 `json:"cache_read"`
}

// Total is all tokens flow-managed sessions actually processed.
func (u Usage) Total() int64 { return u.Input + u.Output + u.CacheCreation + u.CacheRead }

// FileRollup is the per-jsonl scan result. It is JSON-serializable so the
// cache can persist it.
type FileRollup struct {
	Lookups []Lookup  `json:"lookups"`
	Usage   Usage     `json:"usage"`
	First   time.Time `json:"first"`
	Last    time.Time `json:"last"`
}

// ---- raw jsonl shapes (only the fields we need) ----

type rawRecord struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

type rawMessage struct {
	Usage   *rawUsage       `json:"usage"`
	Content json.RawMessage `json:"content"`
}

type rawUsage struct {
	Input         int64 `json:"input_tokens"`
	Output        int64 `json:"output_tokens"`
	CacheCreation int64 `json:"cache_creation_input_tokens"`
	CacheRead     int64 `json:"cache_read_input_tokens"`
}

type rawBlock struct {
	Type  string          `json:"type"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type rawInput struct {
	Command  string `json:"command"`
	FilePath string `json:"file_path"`
}

// ScanJSONL reads a Claude session jsonl byte-stream and extracts a
// FileRollup. ownSlug is the slug of the task that owns this transcript,
// used to distinguish own-bootstrap reads (skipped) from sibling reads
// (cross_task). Malformed lines are skipped silently.
func ScanJSONL(r io.Reader, ownSlug string) (FileRollup, error) {
	var roll FileRollup
	ownPrefix := "/.flow/tasks/" + ownSlug + "/"

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec rawRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		ts := parseTS(rec.Timestamp)
		if !ts.IsZero() {
			if roll.First.IsZero() || ts.Before(roll.First) {
				roll.First = ts
			}
			if ts.After(roll.Last) {
				roll.Last = ts
			}
		}
		if len(rec.Message) == 0 {
			continue
		}
		var msg rawMessage
		if err := json.Unmarshal(rec.Message, &msg); err != nil {
			continue
		}
		if msg.Usage != nil {
			roll.Usage.Input += msg.Usage.Input
			roll.Usage.Output += msg.Usage.Output
			roll.Usage.CacheCreation += msg.Usage.CacheCreation
			roll.Usage.CacheRead += msg.Usage.CacheRead
		}
		if len(msg.Content) == 0 {
			continue
		}
		var blocks []rawBlock
		if err := json.Unmarshal(msg.Content, &blocks); err != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type != "tool_use" || len(b.Input) == 0 {
				continue
			}
			var in rawInput
			if err := json.Unmarshal(b.Input, &in); err != nil {
				continue
			}
			if kind, ok := classify(b.Name, in, ownPrefix); ok {
				roll.Lookups = append(roll.Lookups, Lookup{Kind: kind, TS: ts})
			}
		}
	}
	if err := sc.Err(); err != nil {
		return roll, err
	}
	return roll, nil
}

// classify maps a tool_use block to a lookup kind. The bool is false when
// the block is not a lookup (or is an own-bootstrap read we skip).
func classify(name string, in rawInput, ownPrefix string) (LookupKind, bool) {
	switch name {
	case "Bash":
		c := in.Command
		switch {
		case strings.Contains(c, "flow show task"):
			return LookupResume, true
		case strings.Contains(c, "flow show "):
			return LookupReference, true
		case strings.Contains(c, "flow transcript"):
			return LookupCrossTask, true
		}
	case "Read":
		p := in.FilePath
		switch {
		case strings.Contains(p, "/.flow/kb/"):
			return LookupKB, true
		case strings.Contains(p, ownPrefix):
			return "", false // own bootstrap read; already counted
		case strings.Contains(p, "/.flow/tasks/"), strings.Contains(p, "/.flow/projects/"):
			return LookupCrossTask, true
		}
	}
	return "", false
}

func parseTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	return time.Time{}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/stats/ -run TestScanJSONL -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/stats/scan.go internal/stats/scan_test.go
git commit -m "feat(stats): jsonl scanner for lookups and token usage"
```

---

### Task 2: per-file cache

**Files:**
- Create: `internal/stats/cache.go`
- Test: `internal/stats/cache_test.go`

**Interfaces:**
- Consumes: `FileRollup`, `ScanJSONL` from Task 1.
- Produces:
  - `type Cache struct { Entries map[string]cacheEntry }` (Entries exported field, `cacheEntry` unexported, both JSON-tagged).
  - `func LoadCache(path string) *Cache` — never errors; returns an empty cache on missing/corrupt file.
  - `func (c *Cache) Save(path string) error`
  - `func (c *Cache) ScanFile(path, ownSlug string) (FileRollup, error)` — returns cached rollup when the file's `{mod_ns,size}` is unchanged, else rescans and updates the entry.
  - `func (c *Cache) Prune(seen map[string]bool)` — drops entries whose path is not in `seen`.

- [ ] **Step 1: Write the failing test**

```go
package stats

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCacheHitMissAndPersist(t *testing.T) {
	dir := t.TempDir()
	jsonl := filepath.Join(dir, "s.jsonl")
	line := `{"type":"assistant","timestamp":"2026-06-01T10:00:00.000Z","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"flow show task"}}]}}`
	if err := os.WriteFile(jsonl, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	c := LoadCache(filepath.Join(dir, "cache.json")) // missing → empty
	r1, err := c.ScanFile(jsonl, "mine")
	if err != nil {
		t.Fatal(err)
	}
	if len(r1.Lookups) != 1 {
		t.Fatalf("first scan lookups = %d, want 1", len(r1.Lookups))
	}
	if len(c.Entries) != 1 {
		t.Fatalf("cache should have 1 entry, got %d", len(c.Entries))
	}

	// Second scan, unchanged file → served from cache (entry count stable).
	if _, err := c.ScanFile(jsonl, "mine"); err != nil {
		t.Fatal(err)
	}
	if len(c.Entries) != 1 {
		t.Fatalf("cache entry count changed unexpectedly: %d", len(c.Entries))
	}

	// Persist + reload round-trips.
	cpath := filepath.Join(dir, "cache.json")
	if err := c.Save(cpath); err != nil {
		t.Fatal(err)
	}
	c2 := LoadCache(cpath)
	if len(c2.Entries) != 1 {
		t.Fatalf("reloaded cache entries = %d, want 1", len(c2.Entries))
	}

	// Prune drops unseen entries.
	c2.Prune(map[string]bool{}) // nothing seen
	if len(c2.Entries) != 0 {
		t.Fatalf("prune left %d entries, want 0", len(c2.Entries))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/stats/ -run TestCacheHitMissAndPersist -v`
Expected: FAIL — `LoadCache`/`Cache` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
package stats

import (
	"encoding/json"
	"os"
)

type cacheEntry struct {
	ModNS  int64      `json:"mod_ns"`
	Size   int64      `json:"size"`
	Rollup FileRollup `json:"rollup"`
}

// Cache maps a jsonl path to its last-scanned rollup, keyed by file
// identity (mod time + size) so unchanged files are not rescanned.
type Cache struct {
	Entries map[string]cacheEntry `json:"entries"`
}

// LoadCache reads a cache file. A missing or corrupt file yields an empty
// (usable) cache — never an error. Stats must never fail on a bad cache.
func LoadCache(path string) *Cache {
	c := &Cache{Entries: map[string]cacheEntry{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	var loaded Cache
	if err := json.Unmarshal(data, &loaded); err != nil || loaded.Entries == nil {
		return c
	}
	return &loaded
}

// Save writes the cache as JSON.
func (c *Cache) Save(path string) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// ScanFile returns the rollup for a jsonl file, reusing the cached result
// when the file's mod time and size are unchanged.
func (c *Cache) ScanFile(path, ownSlug string) (FileRollup, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return FileRollup{}, err
	}
	modNS, size := fi.ModTime().UnixNano(), fi.Size()
	if e, ok := c.Entries[path]; ok && e.ModNS == modNS && e.Size == size {
		return e.Rollup, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return FileRollup{}, err
	}
	defer f.Close()
	roll, err := ScanJSONL(f, ownSlug)
	if err != nil {
		return FileRollup{}, err
	}
	c.Entries[path] = cacheEntry{ModNS: modNS, Size: size, Rollup: roll}
	return roll, nil
}

// Prune drops cache entries whose path was not seen in the latest run.
func (c *Cache) Prune(seen map[string]bool) {
	for p := range c.Entries {
		if !seen[p] {
			delete(c.Entries, p)
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/stats/ -run TestCacheHitMissAndPersist -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/stats/cache.go internal/stats/cache_test.go
git commit -m "feat(stats): derived per-file cache keyed by mtime+size"
```

---

### Task 3: constants + savings model

**Files:**
- Create: `internal/stats/model.go`
- Test: `internal/stats/model_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks (leaf).
- Produces:
  - `type Constants struct { MinutesPerUnattendedRun, MinutesPerContextSwitch float64; TokensPerKBLookup int64; DollarPerHour float64 }` (JSON-tagged: `minutes_per_unattended_run`, `minutes_per_context_switch`, `tokens_per_kb_lookup`, `dollar_per_hour`).
  - `func DefaultConstants() Constants` → `{20, 5, 1500, 100}`.
  - `func LoadConstants(path string) Constants` — defaults on missing/corrupt (prints a stderr notice on corrupt, not on missing); any field ≤ 0 falls back to its default.
  - `type Counts struct { AutoRuns, OwnerTicks, ResumeLookups, RefLookups, KBLookups, CrossLookups int }`
  - `type Savings struct { AutomationHours, ContextSwitchHours float64; KBTokens int64; AddressableCount int; TotalHours, TotalDollars float64 }`
  - `func ComputeSavings(c Constants, n Counts) Savings`

- [ ] **Step 1: Write the failing test**

```go
package stats

import (
	"os"
	"path/filepath"
	"testing"
)

func TestComputeSavings(t *testing.T) {
	c := DefaultConstants() // 20 min/run, 5 min/switch, 1500 tok/kb, $100/hr
	n := Counts{AutoRuns: 3, OwnerTicks: 3, ResumeLookups: 6, RefLookups: 6, KBLookups: 2, CrossLookups: 4}
	s := ComputeSavings(c, n)

	// automation: (3+3)*20/60 = 2.0 hrs ; switch: (6+6)*5/60 = 1.0 hr
	if s.AutomationHours != 2.0 || s.ContextSwitchHours != 1.0 {
		t.Errorf("hours = %v/%v, want 2.0/1.0", s.AutomationHours, s.ContextSwitchHours)
	}
	if s.KBTokens != 3000 { // 2*1500
		t.Errorf("KBTokens = %d, want 3000", s.KBTokens)
	}
	if s.AddressableCount != 10 { // cross4 + ref6
		t.Errorf("AddressableCount = %d, want 10", s.AddressableCount)
	}
	if s.TotalHours != 3.0 || s.TotalDollars != 300.0 { // 3hrs * $100
		t.Errorf("total = %v hrs / $%v, want 3.0/300", s.TotalHours, s.TotalDollars)
	}
}

func TestLoadConstantsDefaultsAndOverride(t *testing.T) {
	dir := t.TempDir()
	// missing file → defaults
	if got := LoadConstants(filepath.Join(dir, "nope.json")); got != DefaultConstants() {
		t.Errorf("missing file should give defaults, got %+v", got)
	}
	// valid override
	p := filepath.Join(dir, "stats.json")
	os.WriteFile(p, []byte(`{"minutes_per_unattended_run":30,"dollar_per_hour":150}`), 0o644)
	got := LoadConstants(p)
	if got.MinutesPerUnattendedRun != 30 || got.DollarPerHour != 150 {
		t.Errorf("override not applied: %+v", got)
	}
	// zero/missing fields fall back to defaults
	if got.MinutesPerContextSwitch != 5 || got.TokensPerKBLookup != 1500 {
		t.Errorf("unset fields should default: %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/stats/ -run 'TestComputeSavings|TestLoadConstants' -v`
Expected: FAIL — undefined identifiers.

- [ ] **Step 3: Write minimal implementation**

```go
package stats

import (
	"encoding/json"
	"fmt"
	"os"
)

// Constants are the user-editable counterfactual multipliers. They live in
// ~/.flow/stats.json (JSON, not TOML — the repo has no TOML dependency).
type Constants struct {
	MinutesPerUnattendedRun float64 `json:"minutes_per_unattended_run"`
	MinutesPerContextSwitch float64 `json:"minutes_per_context_switch"`
	TokensPerKBLookup       int64   `json:"tokens_per_kb_lookup"`
	DollarPerHour           float64 `json:"dollar_per_hour"`
}

// DefaultConstants are the shipped defaults.
func DefaultConstants() Constants {
	return Constants{
		MinutesPerUnattendedRun: 20,
		MinutesPerContextSwitch: 5,
		TokensPerKBLookup:       1500,
		DollarPerHour:           100,
	}
}

// LoadConstants reads ~/.flow/stats.json. A missing file yields defaults
// silently; a corrupt file yields defaults with a stderr notice. Any field
// ≤ 0 (including unset) falls back to its individual default.
func LoadConstants(path string) Constants {
	def := DefaultConstants()
	data, err := os.ReadFile(path)
	if err != nil {
		return def
	}
	var c Constants
	if err := json.Unmarshal(data, &c); err != nil {
		fmt.Fprintf(os.Stderr, "warning: stats.json is malformed (%v); using defaults\n", err)
		return def
	}
	if c.MinutesPerUnattendedRun <= 0 {
		c.MinutesPerUnattendedRun = def.MinutesPerUnattendedRun
	}
	if c.MinutesPerContextSwitch <= 0 {
		c.MinutesPerContextSwitch = def.MinutesPerContextSwitch
	}
	if c.TokensPerKBLookup <= 0 {
		c.TokensPerKBLookup = def.TokensPerKBLookup
	}
	if c.DollarPerHour <= 0 {
		c.DollarPerHour = def.DollarPerHour
	}
	return c
}

// Counts are the raw inputs to the savings model.
type Counts struct {
	AutoRuns      int
	OwnerTicks    int
	ResumeLookups int
	RefLookups    int
	KBLookups     int
	CrossLookups  int
}

// Savings are the counterfactual estimates. AddressableCount is a count,
// NOT time/$ — it must never be summed into TotalHours/TotalDollars.
type Savings struct {
	AutomationHours    float64
	ContextSwitchHours float64
	KBTokens           int64
	AddressableCount   int
	TotalHours         float64
	TotalDollars       float64
}

// ComputeSavings applies the counterfactual model. TotalHours sums only the
// two time-valued levers (automation + context-switch); KBTokens (tokens)
// and AddressableCount (a count) are reported separately by design.
func ComputeSavings(c Constants, n Counts) Savings {
	auto := float64(n.AutoRuns+n.OwnerTicks) * c.MinutesPerUnattendedRun / 60.0
	sw := float64(n.ResumeLookups+n.RefLookups) * c.MinutesPerContextSwitch / 60.0
	total := auto + sw
	return Savings{
		AutomationHours:    auto,
		ContextSwitchHours: sw,
		KBTokens:           int64(n.KBLookups) * c.TokensPerKBLookup,
		AddressableCount:   n.CrossLookups + n.RefLookups,
		TotalHours:         total,
		TotalDollars:       total * c.DollarPerHour,
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/stats/ -run 'TestComputeSavings|TestLoadConstants' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/stats/model.go internal/stats/model_test.go
git commit -m "feat(stats): counterfactual constants + savings model"
```

---

### Task 4: report aggregation — BuildStats

**Files:**
- Create: `internal/stats/report.go`
- Test: `internal/stats/report_test.go`

**Interfaces:**
- Consumes: `Cache.ScanFile`, `Lookup`, `Usage` (T1/T2), `Constants`, `Counts`, `ComputeSavings`, `Savings` (T3); `flowdb.OpenDB`, `flowdb.ListTasks`, `flowdb.TaskFilter`, `flowdb.Task`; `claude.EncodeCwd`.
- Produces:
  - `type WeeklyPoint struct { WeekStart time.Time; Lookups int; Tokens int64 }`
  - `type Stats struct { Window string; Project string; LookupsByKind map[LookupKind]int; LookupsTotal int; Tokens Usage; TasksDone, AutoRuns, OwnerTicks, PlaybookRuns, KBFacts int; Savings Savings; Weekly []WeeklyPoint }`
  - `type BuildOpts struct { Root, ClaudeProjects string; DB *sql.DB; Cache *Cache; Constants Constants; Since time.Time; Project string }`
  - `func BuildStats(o BuildOpts) (Stats, error)`
  - `func ParseSince(s string, now time.Time) (time.Time, error)` — `"all"`/`""`→zero; `"7d"`,`"30d"`,`"<N>d"`→`now-N*24h`; otherwise RFC3339.

BuildStats algorithm:
1. List tasks: `flowdb.ListTasks(o.DB, flowdb.TaskFilter{IncludeArchived: true, Project: o.Project})`.
2. For each task with a non-empty `SessionID`: locate jsonl at `filepath.Join(o.ClaudeProjects, claude.EncodeCwd(task.WorkDir), task.SessionID.String+".jsonl")`; if it exists, `o.Cache.ScanFile(path, task.Slug)`; record the path in a `seen` set. Skip tasks whose jsonl is missing.
3. Aggregate lookups (filter each `Lookup` with `TS` ≥ `o.Since` when `o.Since` is non-zero; a zero-`TS` lookup is included only for all-time) and sum `Usage`. Build `LookupsByKind` + `LookupsTotal` + per-week `Weekly` points (group by Monday 00:00 UTC of each lookup's TS; also tally tokens per week from each file's `Last` timestamp bucket — simpler: tokens-per-week from the file's rollup attributed to the file's `Last` week).
4. Counts that come from the DB / dirs:
   - `TasksDone` = `len(ListTasks(DB, {Status:"done", IncludeArchived:true, Project}))`.
   - `PlaybookRuns` = `len(ListTasks(DB, {Kind:"playbook_run", IncludeArchived:true, Project}))`.
   - `AutoRuns` = count of `*.log` files under `o.Root/tasks/*/auto-runs/`.
   - `OwnerTicks` = count of `*.md` files under `o.Root/owners/*/updates/`.
   - `KBFacts` = count of lines matching the KB entry format (lines beginning with `"- "`) across `o.Root/kb/*.md`.
5. `o.Cache.Prune(seen)`.
6. `ComputeSavings` with a `Counts` filled from the by-kind tallies + AutoRuns/OwnerTicks.
7. Set `Stats.Window` from `o.Since` (`"all-time"` if zero, else e.g. `"since 2026-05-18"`).

- [ ] **Step 1: Write the failing test**

```go
package stats

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"flow/internal/flowdb"
	"flow/internal/harness/claude"
)

func TestBuildStatsEndToEnd(t *testing.T) {
	root := t.TempDir()
	claudeProj := t.TempDir()

	// flow.db with one done task that has a session.
	db, err := flowdb.OpenDB(filepath.Join(root, "flow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	work := "/work/app"
	mustExec(t, db, `INSERT INTO tasks (slug,name,status,work_dir,session_id,created_at,updated_at)
		VALUES ('t1','T1','done',?, '00000000-0000-4000-8000-000000000001', '2026-06-01T00:00:00Z','2026-06-01T00:00:00Z')`, work)

	// Its jsonl under the encoded cwd path.
	jdir := filepath.Join(claudeProj, claude.EncodeCwd(work))
	os.MkdirAll(jdir, 0o755)
	line := `{"type":"assistant","timestamp":"2026-06-10T10:00:00.000Z","message":{"role":"assistant","usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":0,"cache_read_input_tokens":0},"content":[{"type":"tool_use","name":"Bash","input":{"command":"flow show task"}}]}}`
	os.WriteFile(filepath.Join(jdir, "00000000-0000-4000-8000-000000000001.jsonl"), []byte(line), 0o644)

	// auto-runs + owner + kb fixtures.
	os.MkdirAll(filepath.Join(root, "tasks", "t1", "auto-runs"), 0o755)
	os.WriteFile(filepath.Join(root, "tasks", "t1", "auto-runs", "r.log"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(root, "owners", "o1", "updates"), 0o755)
	os.WriteFile(filepath.Join(root, "owners", "o1", "updates", "tick.md"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(root, "kb"), 0o755)
	os.WriteFile(filepath.Join(root, "kb", "org.md"), []byte("# org\n- 2026-06-01 — fact one\n- 2026-06-02 — fact two\n"), 0o644)

	c := LoadCache(filepath.Join(root, "stats-cache.json"))
	s, err := BuildStats(BuildOpts{
		Root: root, ClaudeProjects: claudeProj, DB: db, Cache: c,
		Constants: DefaultConstants(), Since: time.Time{}, Project: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.LookupsTotal != 1 || s.LookupsByKind[LookupResume] != 1 {
		t.Errorf("lookups = %d %v, want 1 resume", s.LookupsTotal, s.LookupsByKind)
	}
	if s.Tokens.Total() != 15 {
		t.Errorf("tokens = %d, want 15", s.Tokens.Total())
	}
	if s.TasksDone != 1 || s.AutoRuns != 1 || s.OwnerTicks != 1 || s.KBFacts != 2 {
		t.Errorf("counts done=%d auto=%d ticks=%d kb=%d", s.TasksDone, s.AutoRuns, s.OwnerTicks, s.KBFacts)
	}
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC)
	if got, _ := ParseSince("all", now); !got.IsZero() {
		t.Errorf("all → %v, want zero", got)
	}
	if got, _ := ParseSince("7d", now); !got.Equal(now.AddDate(0, 0, -7)) {
		t.Errorf("7d → %v", got)
	}
	if _, err := ParseSince("garbage", now); err == nil {
		t.Errorf("garbage should error")
	}
}
```

Add this helper at the bottom of the test file:

```go
func mustExec(t *testing.T, db interface {
	Exec(string, ...any) (interface{ LastInsertId() (int64, error) }, error)
}, q string, args ...any) {
	t.Helper()
	// Use the *sql.DB directly instead — see note below.
}
```

> **Note for the implementer:** the `mustExec` stub above is illustrative; replace it with a direct `db.Exec(q, args...)` call using the `*sql.DB` returned by `flowdb.OpenDB`. Concretely:
> ```go
> func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
> 	t.Helper()
> 	if _, err := db.Exec(q, args...); err != nil {
> 		t.Fatalf("exec: %v", err)
> 	}
> }
> ```
> and add `"database/sql"` to the test imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/stats/ -run 'TestBuildStatsEndToEnd|TestParseSince' -v`
Expected: FAIL — `BuildStats`/`ParseSince` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
package stats

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"flow/internal/flowdb"
	"flow/internal/harness/claude"
)

// WeeklyPoint is one bucket of the lookups/tokens sparkline.
type WeeklyPoint struct {
	WeekStart time.Time
	Lookups   int
	Tokens    int64
}

// Stats is the fully-aggregated analytics result for one window/project.
type Stats struct {
	Window        string
	Project       string
	LookupsByKind map[LookupKind]int
	LookupsTotal  int
	Tokens        Usage
	TasksDone     int
	AutoRuns      int
	OwnerTicks    int
	PlaybookRuns  int
	KBFacts       int
	Savings       Savings
	Weekly        []WeeklyPoint
}

// BuildOpts are the injectable inputs to BuildStats (paths injected so
// tests can point at temp dirs).
type BuildOpts struct {
	Root           string
	ClaudeProjects string
	DB             *sql.DB
	Cache          *Cache
	Constants      Constants
	Since          time.Time // zero = all-time
	Project        string    // "" = all
}

// BuildStats derives a Stats from flow.db + transcripts + on-disk dirs.
func BuildStats(o BuildOpts) (Stats, error) {
	s := Stats{
		Window:        windowLabel(o.Since),
		Project:       o.Project,
		LookupsByKind: map[LookupKind]int{},
	}

	tasks, err := flowdb.ListTasks(o.DB, flowdb.TaskFilter{IncludeArchived: true, Project: o.Project})
	if err != nil {
		return s, fmt.Errorf("list tasks: %w", err)
	}

	weekly := map[time.Time]*WeeklyPoint{}
	seen := map[string]bool{}
	for _, t := range tasks {
		if !t.SessionID.Valid || t.SessionID.String == "" {
			continue
		}
		path := filepath.Join(o.ClaudeProjects, claude.EncodeCwd(t.WorkDir), t.SessionID.String+".jsonl")
		if _, statErr := os.Stat(path); statErr != nil {
			continue
		}
		roll, scanErr := o.Cache.ScanFile(path, t.Slug)
		if scanErr != nil {
			continue
		}
		seen[path] = true

		for _, l := range roll.Lookups {
			if !o.Since.IsZero() && !l.TS.IsZero() && l.TS.Before(o.Since) {
				continue
			}
			s.LookupsByKind[l.Kind]++
			s.LookupsTotal++
			wk := weekStart(l.TS)
			wp := weekly[wk]
			if wp == nil {
				wp = &WeeklyPoint{WeekStart: wk}
				weekly[wk] = wp
			}
			wp.Lookups++
		}

		// Tokens: include the whole file's usage when its last activity is
		// in-window (token usage is not per-record-timestamped here).
		if o.Since.IsZero() || roll.Last.IsZero() || !roll.Last.Before(o.Since) {
			s.Tokens.Input += roll.Usage.Input
			s.Tokens.Output += roll.Usage.Output
			s.Tokens.CacheCreation += roll.Usage.CacheCreation
			s.Tokens.CacheRead += roll.Usage.CacheRead
			wk := weekStart(roll.Last)
			wp := weekly[wk]
			if wp == nil {
				wp = &WeeklyPoint{WeekStart: wk}
				weekly[wk] = wp
			}
			wp.Tokens += roll.Usage.Total()
		}
	}
	o.Cache.Prune(seen)

	done, err := flowdb.ListTasks(o.DB, flowdb.TaskFilter{Status: "done", IncludeArchived: true, Project: o.Project})
	if err != nil {
		return s, fmt.Errorf("list done: %w", err)
	}
	s.TasksDone = len(done)

	runs, err := flowdb.ListTasks(o.DB, flowdb.TaskFilter{Kind: "playbook_run", IncludeArchived: true, Project: o.Project})
	if err != nil {
		return s, fmt.Errorf("list runs: %w", err)
	}
	s.PlaybookRuns = len(runs)

	s.AutoRuns = countFiles(filepath.Join(o.Root, "tasks"), "auto-runs", ".log")
	s.OwnerTicks = countFiles(filepath.Join(o.Root, "owners"), "updates", ".md")
	s.KBFacts = countKBFacts(filepath.Join(o.Root, "kb"))

	s.Savings = ComputeSavings(o.Constants, Counts{
		AutoRuns:      s.AutoRuns,
		OwnerTicks:    s.OwnerTicks,
		ResumeLookups: s.LookupsByKind[LookupResume],
		RefLookups:    s.LookupsByKind[LookupReference],
		KBLookups:     s.LookupsByKind[LookupKB],
		CrossLookups:  s.LookupsByKind[LookupCrossTask],
	})

	s.Weekly = sortedWeekly(weekly)
	return s, nil
}

// ParseSince converts a --since value to a lower-bound time. "all"/"" → zero
// (no bound); "<N>d" → now minus N days; otherwise RFC3339.
func ParseSince(s string, now time.Time) (time.Time, error) {
	if s == "" || s == "all" {
		return time.Time{}, nil
	}
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || n < 0 {
			return time.Time{}, fmt.Errorf("invalid --since %q", s)
		}
		return now.AddDate(0, 0, -n), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid --since %q (use all, <N>d, or RFC3339)", s)
	}
	return t, nil
}

func windowLabel(since time.Time) string {
	if since.IsZero() {
		return "all-time"
	}
	return "since " + since.Format("2006-01-02")
}

// weekStart returns Monday 00:00 UTC of t's ISO week (zero in → zero out).
func weekStart(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	t = t.UTC()
	wd := (int(t.Weekday()) + 6) % 7 // Mon=0 … Sun=6
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	return d.AddDate(0, 0, -wd)
}

func sortedWeekly(m map[time.Time]*WeeklyPoint) []WeeklyPoint {
	out := make([]WeeklyPoint, 0, len(m))
	for _, wp := range m {
		out = append(out, *wp)
	}
	for i := 1; i < len(out); i++ { // insertion sort by WeekStart asc
		for j := i; j > 0 && out[j].WeekStart.Before(out[j-1].WeekStart); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// countFiles counts files ending in ext inside <base>/*/<sub>/.
func countFiles(base, sub, ext string) int {
	entries, err := os.ReadDir(base)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		inner := filepath.Join(base, e.Name(), sub)
		files, err := os.ReadDir(inner)
		if err != nil {
			continue
		}
		for _, f := range files {
			if !f.IsDir() && strings.HasSuffix(f.Name(), ext) {
				n++
			}
		}
	}
	return n
}

// countKBFacts counts entry lines (starting with "- ") across kb/*.md.
func countKBFacts(kbDir string) int {
	entries, err := os.ReadDir(kbDir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(kbDir, e.Name()))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "- ") {
				n++
			}
		}
	}
	return n
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/stats/ -v`
Expected: PASS (all stats package tests).

- [ ] **Step 5: Commit**

```bash
git add internal/stats/report.go internal/stats/report_test.go
git commit -m "feat(stats): BuildStats aggregation over db, transcripts, dirs"
```

---

### Task 5: `flow stats` command + terminal renderer + dispatch

**Files:**
- Create: `internal/app/stats.go`
- Test: `internal/app/stats_test.go`
- Modify: `internal/app/app.go` (add `case "stats"` in the dispatch switch around line 74, after the `transcript` case; add a usage line in `printUsage`)

**Interfaces:**
- Consumes: `stats.BuildStats`, `stats.BuildOpts`, `stats.LoadCache`, `stats.LoadConstants`, `stats.ParseSince`, `stats.Stats`, `stats.LookupKind` consts (Task 1–4); `flowRoot()`, `flowDBPath()` (`internal/app/init.go`); `flagSet()` (`internal/app/helpers.go`); `flowdb.OpenDB`.
- Produces:
  - `func cmdStats(args []string) int`
  - `func renderReport(w io.Writer, s stats.Stats) error`
  - `func sparkline(values []int) string`

`cmdStats` flow: parse flags (`--since` default `"all"`, `--project` default `""`, `--card` bool, `--out` string); resolve `flowRoot()` + `flowDBPath()`; `OpenDB`; load constants from `<root>/stats.json` and cache from `<root>/stats-cache.json`; `ParseSince(*since, time.Now())`; `BuildStats(...)`; save cache (best-effort; log to stderr on error but still exit 0); if `--card` call `renderCard` (Task 6) to the out path, else `renderReport(os.Stdout, s)`. Return 1 on hard errors (db open, build), 2 on bad flags / bad `--since`.

- [ ] **Step 1: Write the failing test**

```go
package app

import (
	"bytes"
	"strings"
	"testing"

	"flow/internal/stats"
)

func TestRenderReportContainsSections(t *testing.T) {
	s := stats.Stats{
		Window:        "all-time",
		LookupsByKind: map[stats.LookupKind]int{stats.LookupResume: 4, stats.LookupKB: 2},
		LookupsTotal:  6,
		Tokens:        stats.Usage{Input: 100, Output: 50},
		TasksDone:     3,
		AutoRuns:      2,
		OwnerTicks:    1,
		PlaybookRuns:  1,
		KBFacts:       5,
		Savings:       stats.Savings{AutomationHours: 1.0, TotalHours: 1.5, TotalDollars: 150, KBTokens: 3000, AddressableCount: 4},
		Weekly:        []stats.WeeklyPoint{{Lookups: 1}, {Lookups: 4}, {Lookups: 6}},
	}
	var buf bytes.Buffer
	if err := renderReport(&buf, s); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"flow served you stored context", "6", "all-time", "Tokens", "est.", "Saved"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q.\n---\n%s", want, out)
		}
	}
}

func TestSparkline(t *testing.T) {
	got := sparkline([]int{0, 5, 10})
	if len([]rune(got)) != 3 {
		t.Errorf("sparkline len = %d runes, want 3 (%q)", len([]rune(got)), got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/ -run 'TestRenderReport|TestSparkline' -v`
Expected: FAIL — `renderReport`/`sparkline` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/app/stats.go`:

```go
package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"flow/internal/flowdb"
	"flow/internal/stats"
)

// cmdStats implements `flow stats` — usage & ROI analytics derived from
// flow.db, session transcripts, and on-disk auto-runs/owner/kb dirs.
func cmdStats(args []string) int {
	fs := flagSet("stats")
	since := fs.String("since", "all", "window: all | <N>d | RFC3339")
	project := fs.String("project", "", "limit to one project slug")
	card := fs.Bool("card", false, "render a shareable HTML card instead of the terminal report")
	out := fs.String("out", "", "output path for --card (default <flow-root>/stats-card.html)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	root, err := flowRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := flowdb.OpenDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: open db: %v\n", err)
		return 1
	}
	defer db.Close()

	sinceTime, err := stats.ParseSince(*since, time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: no home dir: %v\n", err)
		return 1
	}
	cachePath := filepath.Join(root, "stats-cache.json")
	cache := stats.LoadCache(cachePath)
	consts := stats.LoadConstants(filepath.Join(root, "stats.json"))

	s, err := stats.BuildStats(stats.BuildOpts{
		Root:           root,
		ClaudeProjects: filepath.Join(home, ".claude", "projects"),
		DB:             db,
		Cache:          cache,
		Constants:      consts,
		Since:          sinceTime,
		Project:        *project,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: build stats: %v\n", err)
		return 1
	}
	if saveErr := cache.Save(cachePath); saveErr != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write stats cache: %v\n", saveErr)
	}

	if *card {
		outPath := *out
		if outPath == "" {
			outPath = filepath.Join(root, "stats-card.html")
		}
		if err := writeCard(outPath, s); err != nil {
			fmt.Fprintf(os.Stderr, "error: write card: %v\n", err)
			return 1
		}
		fmt.Printf("card written: %s\n", outPath)
		return 0
	}

	if err := renderReport(os.Stdout, s); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

// renderReport prints the terminal analytics report.
func renderReport(w io.Writer, s stats.Stats) error {
	fmt.Fprintf(w, "flow stats — %s", s.Window)
	if s.Project != "" {
		fmt.Fprintf(w, " · project %s", s.Project)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w)

	fmt.Fprintf(w, "  flow served you stored context %d times\n", s.LookupsTotal)
	fmt.Fprintf(w, "    resume %d · reference %d · cross-task %d · kb %d\n",
		s.LookupsByKind[stats.LookupResume], s.LookupsByKind[stats.LookupReference],
		s.LookupsByKind[stats.LookupCrossTask], s.LookupsByKind[stats.LookupKB])
	fmt.Fprintln(w)

	fmt.Fprintln(w, "  Ground truth")
	fmt.Fprintf(w, "    Tokens processed : %d\n", s.Tokens.Total())
	fmt.Fprintf(w, "    Tasks done       : %d\n", s.TasksDone)
	fmt.Fprintf(w, "    Auto runs        : %d\n", s.AutoRuns)
	fmt.Fprintf(w, "    Owner ticks      : %d\n", s.OwnerTicks)
	fmt.Fprintf(w, "    Playbook runs    : %d\n", s.PlaybookRuns)
	fmt.Fprintf(w, "    KB facts         : %d\n", s.KBFacts)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "  Estimated savings (est. — assumptions in stats.json)")
	fmt.Fprintf(w, "    Automation       : ~%.1f hrs (est.)\n", s.Savings.AutomationHours)
	fmt.Fprintf(w, "    Context recovery : ~%.1f hrs (est.)\n", s.Savings.ContextSwitchHours)
	fmt.Fprintf(w, "    KB reuse         : ~%d tokens (est.)\n", s.Savings.KBTokens)
	fmt.Fprintf(w, "    Addressed by slug: %d (never hunted a UUID)\n", s.Savings.AddressableCount)
	fmt.Fprintf(w, "    Saved            : ~%.1f hrs · ~$%.0f (est.)\n", s.Savings.TotalHours, s.Savings.TotalDollars)

	if len(s.Weekly) > 0 {
		vals := make([]int, len(s.Weekly))
		for i, wp := range s.Weekly {
			vals[i] = wp.Lookups
		}
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  Weekly lookups   : %s\n", sparkline(vals))
	}
	return nil
}

var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// sparkline renders ints as a unicode bar string (one rune per value).
func sparkline(values []int) string {
	if len(values) == 0 {
		return ""
	}
	max := 0
	for _, v := range values {
		if v > max {
			max = v
		}
	}
	out := make([]rune, len(values))
	for i, v := range values {
		idx := 0
		if max > 0 {
			idx = v * (len(sparkRunes) - 1) / max
		}
		out[i] = sparkRunes[idx]
	}
	return string(out)
}
```

Then add dispatch + usage in `internal/app/app.go`. After the `case "transcript":` block (around line 74-75):

```go
	case "stats":
		return cmdStats(rest)
```

And in `printUsage`, under the `Read:` section, add a line:

```go
  flow stats           [--since all|<N>d] [--project <slug>] [--card] [--out <path>]
```

> The `writeCard` function referenced by `cmdStats` is defined in Task 6. To keep this task self-contained and compiling, add a **temporary stub** at the bottom of `stats.go` now and replace it in Task 6:
> ```go
> // writeCard is implemented in card.go (Task 6).
> func writeCard(path string, s stats.Stats) error { return os.WriteFile(path, []byte("card pending"), 0o644) }
> ```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/ -run 'TestRenderReport|TestSparkline' -v && go build ./...`
Expected: PASS and a clean build.

- [ ] **Step 5: Commit**

```bash
git add internal/app/stats.go internal/app/stats_test.go internal/app/app.go
git commit -m "feat(stats): flow stats command + terminal report renderer"
```

---

### Task 6: `flow stats --card` HTML renderer

**Files:**
- Create: `internal/app/card.go`
- Test: `internal/app/card_test.go`
- Modify: `internal/app/stats.go` (remove the temporary `writeCard` stub added in Task 5)

**Interfaces:**
- Consumes: `stats.Stats`, `stats.LookupKind` consts.
- Produces:
  - `func writeCard(path string, s stats.Stats) error`
  - `func renderCardHTML(w io.Writer, s stats.Stats) error`

The card is a self-contained HTML document (inline CSS, no external assets), leading with the ground-truth headline and the flow wordmark as styled text. Use `html/template` with escaping, or `text/template` with pre-escaped numeric values (numbers only — safe). Keep it one template string.

- [ ] **Step 1: Write the failing test**

```go
package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/stats"
)

func TestRenderCardHTML(t *testing.T) {
	s := stats.Stats{
		Window:        "all-time",
		LookupsTotal:  42,
		Tokens:        stats.Usage{Input: 1000, Output: 500},
		TasksDone:     7,
		Savings:       stats.Savings{TotalHours: 3.5, TotalDollars: 350},
		LookupsByKind: map[stats.LookupKind]int{},
	}
	var buf bytes.Buffer
	if err := renderCardHTML(&buf, s); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	for _, want := range []string{"<!doctype html", "flow", "42", "served you stored context", "est."} {
		if !strings.Contains(strings.ToLower(html), strings.ToLower(want)) {
			t.Errorf("card html missing %q", want)
		}
	}
}

func TestWriteCard(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "card.html")
	if err := writeCard(p, stats.Stats{LookupsTotal: 1, LookupsByKind: map[stats.LookupKind]int{}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(data), "<!doctype html") {
		t.Fatalf("card file not written as html: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/ -run 'TestRenderCardHTML|TestWriteCard' -v`
Expected: FAIL — `renderCardHTML` undefined and/or the stub returns non-html.

- [ ] **Step 3: Write minimal implementation**

First remove the temporary stub from `stats.go` (the `func writeCard(...) { return os.WriteFile(...,"card pending"...) }` line). Then create `internal/app/card.go`:

```go
package app

import (
	"html/template"
	"io"
	"os"

	"flow/internal/stats"
)

// cardData is the view model handed to the HTML template.
type cardData struct {
	Window       string
	Lookups      int
	Tokens       int64
	TasksDone    int
	AutoRuns     int
	OwnerTicks   int
	TotalHours   float64
	TotalDollars float64
}

var cardTmpl = template.Must(template.New("card").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<title>flow stats</title>
<style>
  body{margin:0;background:#1b1714;font-family:-apple-system,Segoe UI,Roboto,sans-serif}
  .card{width:680px;margin:32px auto;padding:48px;border-radius:20px;
        background:linear-gradient(135deg,#2a2420,#3a322b);color:#f3ede4}
  .wordmark{font-weight:800;letter-spacing:.5px;color:#e8a87c;font-size:22px}
  .head{font-size:18px;opacity:.7;margin-top:24px}
  .big{font-size:64px;font-weight:800;margin:8px 0}
  .sub{font-size:16px;opacity:.85}
  .grid{display:flex;gap:32px;margin-top:32px;flex-wrap:wrap}
  .stat .n{font-size:30px;font-weight:700}
  .stat .l{font-size:13px;opacity:.7}
  .est{margin-top:28px;font-size:15px;opacity:.9}
  .foot{margin-top:24px;font-size:12px;opacity:.55}
</style></head><body>
<div class="card">
  <div class="wordmark">✦ flow</div>
  <div class="head">{{.Window}} · flow served you stored context</div>
  <div class="big">{{.Lookups}}×</div>
  <div class="sub">times it recalled context so you didn't have to.</div>
  <div class="grid">
    <div class="stat"><div class="n">{{.Tokens}}</div><div class="l">tokens processed</div></div>
    <div class="stat"><div class="n">{{.TasksDone}}</div><div class="l">tasks done</div></div>
    <div class="stat"><div class="n">{{.AutoRuns}}</div><div class="l">auto runs</div></div>
    <div class="stat"><div class="n">{{.OwnerTicks}}</div><div class="l">owner ticks</div></div>
  </div>
  <div class="est">≈ {{printf "%.1f" .TotalHours}} hrs · ${{printf "%.0f" .TotalDollars}} saved <em>(est.)</em></div>
  <div class="foot">Estimates use your ~/.flow/stats.json assumptions. Ground-truth counts are exact.</div>
</div>
</body></html>
`))

// renderCardHTML writes a self-contained HTML card for the stats.
func renderCardHTML(w io.Writer, s stats.Stats) error {
	return cardTmpl.Execute(w, cardData{
		Window:       s.Window,
		Lookups:      s.LookupsTotal,
		Tokens:       s.Tokens.Total(),
		TasksDone:    s.TasksDone,
		AutoRuns:     s.AutoRuns,
		OwnerTicks:   s.OwnerTicks,
		TotalHours:   s.Savings.TotalHours,
		TotalDollars: s.Savings.TotalDollars,
	})
}

// writeCard renders the HTML card to a file.
func writeCard(path string, s stats.Stats) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return renderCardHTML(f, s)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/ -run 'TestRenderCardHTML|TestWriteCard' -v && go build ./...`
Expected: PASS and clean build.

- [ ] **Step 5: Commit**

```bash
git add internal/app/card.go internal/app/card_test.go internal/app/stats.go
git commit -m "feat(stats): shareable HTML card via flow stats --card"
```

---

### Task 7: e2e coverage + docs/.gitignore guidance

**Files:**
- Modify: `internal/app/e2e_test.go` (append a stats sub-test to the existing e2e flow)
- Modify: `README.md` (add a short `flow stats` entry under the command reference / usage section — match the surrounding style)
- Modify: `internal/app/init.go` (extend `kbSeeds` / init to mention stats-cache in the `~/.flow` `.gitignore` guidance IF such guidance exists; otherwise skip the init change and only document in README)

**Interfaces:**
- Consumes: the full `flow stats` surface from Tasks 1–6.
- Produces: an end-to-end assertion that `Run([]string{"stats"})` exits 0 against a seeded flow root.

- [ ] **Step 1: Write the failing test**

First inspect how `e2e_test.go` sets up `$FLOW_ROOT`/`$HOME` and calls `Run`. Then add a focused sub-test (standalone test function is fine if the existing e2e is one big function):

```go
func TestStatsE2E(t *testing.T) {
	// Mirror the env setup used by the existing e2e test: temp FLOW_ROOT + HOME.
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("FLOW_ROOT", root)
	t.Setenv("HOME", home)

	if rc := Run([]string{"init"}); rc != 0 {
		t.Fatalf("init rc=%d", rc)
	}
	// stats on an empty-but-initialized root must succeed (zeros, no panic).
	if rc := Run([]string{"stats"}); rc != 0 {
		t.Fatalf("stats rc=%d, want 0", rc)
	}
	// --card writes a file and exits 0.
	card := filepath.Join(root, "card.html")
	if rc := Run([]string{"stats", "--card", "--out", card}); rc != 0 {
		t.Fatalf("stats --card rc=%d, want 0", rc)
	}
	if _, err := os.Stat(card); err != nil {
		t.Fatalf("card not written: %v", err)
	}
	// bad --since is a usage error.
	if rc := Run([]string{"stats", "--since", "garbage"}); rc != 2 {
		t.Fatalf("bad --since rc=%d, want 2", rc)
	}
}
```

> **Implementer note:** confirm the exact env-var names and `Run` entry point by reading the top of `internal/app/e2e_test.go` first. Add imports `os`, `path/filepath` if not already present in that file. If `init` requires the skill install to be stubbed (check whether the existing e2e test stubs `claude`/osascript), follow the same stubbing the existing test uses.

- [ ] **Step 2: Run test to verify it fails (or passes if surface is complete)**

Run: `go test ./internal/app/ -run TestStatsE2E -v`
Expected: PASS if Tasks 1–6 are complete. If it FAILs on init-side stubbing, adjust setup to match the existing e2e harness, then re-run.

- [ ] **Step 3: Implement docs**

Add to `README.md` under the command list (match existing formatting):

```markdown
### `flow stats`

Show usage & ROI analytics derived from your own flow history — how many
times flow recalled stored context for you, tokens processed, tasks done,
automation runs, and estimated time/$ saved.

    flow stats                      # all-time terminal report
    flow stats --since 30d          # last 30 days
    flow stats --project <slug>     # scope to one project
    flow stats --card               # write a shareable HTML card to ~/.flow/stats-card.html

Savings figures are estimates driven by `~/.flow/stats.json` (auto-created
with defaults); ground-truth counts are exact. `~/.flow/stats-cache.json`
is a derived cache — safe to delete, and should be gitignored if you track
`~/.flow` in git.
```

- [ ] **Step 4: Run the full suite**

Run: `make test`
Expected: PASS (entire suite green, including the new e2e).

- [ ] **Step 5: Commit**

```bash
git add internal/app/e2e_test.go README.md
git commit -m "test(stats): e2e coverage + docs for flow stats"
```

---

## Self-Review

**1. Spec coverage:**
- Lookups spine (every retrieval counts) → Task 1 (`ScanJSONL` classify) + Task 4 aggregation. ✓
- Lookup kinds resume/reference/cross_task/kb → Task 1 consts + classify rules. ✓
- Derive-only + cache (decision A′), no schema change → Tasks 2 (cache) + 4 (BuildStats); zero DB writes. ✓
- Resume count from bootstrap repetition → Task 1 (`flow show task` → resume) ; reference/cross-task/kb mining → Task 1. ✓
- Ground-truth tokens via `message.usage` → Task 1 (`rawUsage`) + Task 4 sum. ✓
- Ground-truth counts (tasks done, auto runs, owner ticks, playbook runs, KB facts) → Task 4. ✓
- Counterfactual savings + user-editable constants + honesty labels + no double-count → Task 3 model + Task 5 renderer (`est.` labels) + Global Constraints. ✓ (Constants are JSON per the documented spec deviation.)
- Trends/windows + sparkline → Task 4 (`ParseSince`, `Weekly`) + Task 5 (`sparkline`). ✓
- Surfaces: `flow stats` terminal + `flow stats --card` HTML → Tasks 5 + 6. ✓
- Cache keyed by path+mtime+size, incremental, non-fatal on corrupt, pruned → Task 2 + Task 4. ✓
- Testing strategy (real SQLite, temp HOME/FLOW_ROOT, fixtures, e2e) → Tasks 1–7. ✓
- Out-of-scope items (PNG, dashboard, Slack, forward instrumentation, cross-machine, led-to-work proxy) → none implemented. ✓

**2. Placeholder scan:** No "TBD"/"handle edge cases"/"similar to Task N". The Task 4 test includes a `mustExec` helper with an explicit concrete replacement spelled out; the Task 5 temporary `writeCard` stub is explicitly introduced and explicitly removed in Task 6. The Task 7 init/.gitignore step is conditional and documented as "skip if absent" — acceptable since it depends on a fact the implementer verifies.

**3. Type consistency:** `FileRollup`, `Lookup`, `Usage`, `LookupKind` consts, `Constants`, `Counts`, `Savings`, `Stats`, `BuildOpts`, `WeeklyPoint`, `ParseSince`, `BuildStats`, `renderReport`, `sparkline`, `writeCard`, `renderCardHTML` are referenced with identical signatures across tasks. `Cache.ScanFile(path, ownSlug)` matches its use in Task 4. `claude.EncodeCwd` matches the real exported signature. `flowdb.ListTasks(db, flowdb.TaskFilter{...})` matches the real API. No drift found.
