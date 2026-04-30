# Playbooks, Intake-Minimal, Scope-Check, Flowde Removal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement five interlocking changes to flow: (1) playbook construct as a third entity with per-invocation run-tasks, (2) intake-minimal interview shape with deferred brief sections, (3) ongoing substantive-unrelated-work check in dispatch sessions, (4) removal of the flowde wrapper in favor of direct claude invocation, (5) auxiliary markdown file references in entity dirs.

**Architecture:** Go CLI with SQLite via `modernc.org/sqlite` (no CGO), embedded skill markdown via `//go:embed`, iTerm2 tab spawning via osascript. Playbooks add a new table; per-run tasks reuse the existing `tasks` table with a `kind` discriminator. Skill is markdown content embedded into the binary at compile time and installed to `~/.claude/skills/flow/SKILL.md`.

**Tech Stack:** Go 1.25+, `modernc.org/sqlite`, `flag.FlagSet`, `database/sql`, embedded markdown, RFC3339 timestamps.

**Spec:** `docs/specs/2026-04-30-playbooks-and-skill-cleanup.md`

**Working dir:** `/Users/rohit/flow` (the user has chosen to work directly in the repo, not a worktree).

**Testing convention:** All tests live in `internal/app/*_test.go`. Use `setupFlowRoot(t)` to get a tempdir-rooted FLOW_ROOT with init done. Run with `make test` or `go test ./...`. No mocking of DB — uses real SQLite in temp dirs. iTerm spawning is mocked via `iterm.Runner` function var.

---

## File Structure

### Files to create

- `internal/app/run.go` — `flow run playbook <slug>` command handler
- `internal/app/run_test.go` — tests for run command
- `internal/app/playbook.go` — playbook command dispatch (add/show/list)
- `internal/app/playbook_test.go` — tests for playbook commands
- `internal/app/aux.go` — shared `enumerateAuxFiles(dir)` helper
- `internal/app/aux_test.go` — tests for aux file enumeration

### Files to modify

- `internal/flowdb/db.go` — add `playbooks` table DDL, migrations for `tasks.kind`/`tasks.playbook_slug`, `Playbook` struct + scan/get/list/upsert
- `internal/flowdb/db_test.go` — tests for new schema and queries
- `internal/app/app.go` — wire new subcommands (`add playbook`, `list playbooks`, `list runs`, `run`, `show playbook`)
- `internal/app/add.go` — extend dispatch for `add playbook`
- `internal/app/list.go` — add `list playbooks`, `list runs`; default `kind='regular'` filter on `list tasks`
- `internal/app/show.go` — add `show playbook`; integrate aux file enumeration in show task/project/playbook
- `internal/app/edit.go` — extend ref resolution to playbooks
- `internal/app/archive.go` — extend ref resolution to playbooks
- `internal/app/resolve.go` — add `ResolvePlaybook`
- `internal/app/do.go` — replace `flowde` invocation with direct `claude`; branch `buildBootstrapPrompt` on `kind` for playbook runs; mention `other:` files
- `internal/app/hook.go` — emit one-liner pointing at §5.14
- `internal/app/skill/SKILL.md` — comprehensive content updates (playbook awareness, §5.12, §5.13, §5.14, intake-minimal, aux files lazy-load, §9 updates, §11 update, anti-patterns, model section, command reference)
- `internal/app/skill_test.go` — content presence assertions for new sections
- `Makefile` — drop `WRAPPER`, flowde build, flowde clean
- `README.md` — drop flowde guidance, document `flow skill update`
- `CLAUDE.md` — update file structure listing (drop cmd/flowde, add new files)

### Files to delete

- `cmd/flowde/main.go`
- `cmd/flowde/main_test.go`
- `flowde` (built binary) via `make clean`

---

## Phase 1: Foundation (flowde removal + aux files)

### Task 1: Remove flowde wrapper

**Goal:** Delete the flowde wrapper. `flow do` invokes `claude` directly. Update build, docs, and tests.

**Files:**
- Delete: `cmd/flowde/main.go`, `cmd/flowde/main_test.go`
- Modify: `Makefile`, `README.md`, `internal/app/do.go`, `internal/app/do_test.go`, `CLAUDE.md`

- [ ] **Step 1: Update do_test.go to assert `claude` not `flowde`**

Look at the existing assertions in `internal/app/do_test.go`. Find any test that captures the spawn command and asserts it begins with `flowde`. Update to assert it begins with `claude`.

For example, change:
```go
if !strings.HasPrefix(captured.Command, "flowde --session-id ") {
    t.Errorf("expected flowde session-id command, got %q", captured.Command)
}
```
to:
```go
if !strings.HasPrefix(captured.Command, "claude --session-id ") {
    t.Errorf("expected claude session-id command, got %q", captured.Command)
}
```

Apply the same change to any `flowde --resume` assertion.

- [ ] **Step 2: Run do_test.go to verify failure**

Run: `go test ./internal/app/ -run TestCmdDo -v`
Expected: FAIL — current `do.go` still emits `flowde ...`, so the new assertion fails.

- [ ] **Step 3: Update do.go to use claude directly**

In `internal/app/do.go`, locate the spawn-command construction (around line 200-220):

```go
var command string
if needsBootstrap {
    prompt := buildBootstrapPrompt(task.Slug)
    command = fmt.Sprintf("flowde --session-id %s %s", sessionID, iterm.ShellQuote(prompt))
} else {
    command = "flowde --resume " + sessionID
}
```

Change to:

```go
var command string
if needsBootstrap {
    prompt := buildBootstrapPrompt(task.Slug)
    command = fmt.Sprintf("claude --session-id %s %s", sessionID, iterm.ShellQuote(prompt))
} else {
    command = "claude --resume " + sessionID
}
```

Also update the comment block above the spawn (lines ~196-204) — remove the paragraph explaining flowde rationale; replace with:

```go
// Spawn the iTerm tab.
//
// We shell out to `claude` directly (no wrapper). The skill on disk at
// ~/.claude/skills/flow/SKILL.md is whatever was last installed via
// `flow skill install` / `flow skill update`. To refresh it after
// upgrading flow, the user runs `flow skill update` manually.
```

- [ ] **Step 4: Run do_test.go to verify pass**

Run: `go test ./internal/app/ -run TestCmdDo -v`
Expected: PASS.

- [ ] **Step 5: Delete cmd/flowde/**

```bash
rm -rf /Users/rohit/flow/cmd/flowde
```

- [ ] **Step 6: Update Makefile**

Read current `Makefile`. Remove the `WRAPPER` line and references:

```makefile
BINARY   := flow
WRAPPER  := flowde       # ← delete this line
REPO_DIR := $(shell pwd)

build:
	go build -o $(BINARY) .
	go build -o $(WRAPPER) ./cmd/flowde   # ← delete this line

clean:
	rm -f $(BINARY) $(WRAPPER)            # ← change to: rm -f $(BINARY) $(WRAPPER)
```

After edits the relevant chunks become:

```makefile
BINARY   := flow
REPO_DIR := $(shell pwd)

build:
	go build -o $(BINARY) .

clean:
	rm -f $(BINARY) flowde
```

(Keep `flowde` in clean's `rm -f` so users get the leftover binary deleted on `make clean`.)

- [ ] **Step 7: Update README.md**

Open `/Users/rohit/flow/README.md` and find the "Install" and "How it works under the hood" sections.

In the Install section, replace:
```
Then run **`flowde`** and say **"let's get to work"**. `flowde` is a thin
wrapper around `claude` that keeps the flow skill current on every launch
— use it anywhere you'd normally run `claude`. Flow will guide you from
there.
```
with:
```
Then run **`claude`** and say **"let's get to work"**. The flow skill
is installed at ~/.claude/skills/flow/SKILL.md. To refresh it after
upgrading flow, run `flow skill update` (or `make install` again).
```

In "How it works under the hood," replace the `flowde` paragraph (lines ~109-125) with:
```markdown
`flow do <task>` pre-allocates a session UUID, writes it to the task row,
and spawns a new iTerm tab running `claude --session-id <uuid>` with
`FLOW_TASK` / `FLOW_PROJECT` environment variables inlined. The jsonl
file lands at the deterministic path
`~/.claude/projects/<encoded-cwd>/<uuid>.jsonl`, so future `flow do`
calls spawn `claude --resume <uuid>` to continue the same conversation.
A SessionStart hook re-injects the task brief, updates, and CLAUDE.md
context on every resume.
```

- [ ] **Step 8: Update CLAUDE.md**

In the Project structure section, remove the `cmd/flowde` reference. Also remove the bullet about "skill embed path: `internal/app/skill/SKILL.md` is embedded ... After editing, rebuild for `flow skill update` to pick up changes." — keep it as is, it's still valid.

Actually only remove the `cmd/` reference. The structure block currently shows:
```
flow/
├── main.go
├── internal/
...
```
There's no explicit `cmd/flowde` line in the tree (CLAUDE.md doesn't show it), so likely no change needed here. Verify by reading CLAUDE.md and only edit if `cmd/flowde` is mentioned.

- [ ] **Step 9: Verify the build still works**

```bash
cd /Users/rohit/flow
make clean
make build
```
Expected: `flow` binary built; no flowde produced; no errors.

- [ ] **Step 10: Run full test suite**

```bash
go test ./...
```
Expected: All tests pass.

- [ ] **Step 11: Commit**

```bash
cd /Users/rohit/flow
git add -A
git commit -m "$(cat <<'EOF'
refactor: remove flowde wrapper; flow do invokes claude directly

The flowde wrapper existed solely to call `flow skill install --force`
on every claude launch. We're moving to an explicit `flow skill update`
upgrade path, so the wrapper is no longer needed.

- Delete cmd/flowde/
- Update do.go to spawn `claude` instead of `flowde`
- Update Makefile, README, do_test.go

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Auxiliary markdown enumeration helper

**Goal:** A shared helper that enumerates `.md` files in an entity directory, excluding `brief.md` and the `updates/` subdir.

**Files:**
- Create: `internal/app/aux.go`, `internal/app/aux_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/app/aux_test.go`:

```go
package app

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestEnumerateAuxFiles(t *testing.T) {
	dir := t.TempDir()

	// Files we expect to be excluded.
	mustWrite(t, filepath.Join(dir, "brief.md"), "brief")
	if err := os.MkdirAll(filepath.Join(dir, "updates"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "updates", "2026-04-30-foo.md"), "u1")

	// Files we expect to be included.
	mustWrite(t, filepath.Join(dir, "research.md"), "r")
	mustWrite(t, filepath.Join(dir, "design.md"), "d")

	// Non-markdown files: excluded.
	mustWrite(t, filepath.Join(dir, "notes.txt"), "ignored")
	mustWrite(t, filepath.Join(dir, "image.png"), "ignored")

	got, err := enumerateAuxFiles(dir)
	if err != nil {
		t.Fatalf("enumerateAuxFiles: %v", err)
	}
	sort.Strings(got)

	want := []string{
		filepath.Join(dir, "design.md"),
		filepath.Join(dir, "research.md"),
	}
	if len(got) != len(want) {
		t.Fatalf("got %d files (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("got[%d]=%q, want %q", i, got[i], want[i])
		}
	}
}

func TestEnumerateAuxFilesEmptyDir(t *testing.T) {
	dir := t.TempDir()
	got, err := enumerateAuxFiles(dir)
	if err != nil {
		t.Fatalf("enumerateAuxFiles: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestEnumerateAuxFilesMissingDir(t *testing.T) {
	got, err := enumerateAuxFiles(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("missing dir should not error, got: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/ -run TestEnumerateAuxFiles -v`
Expected: FAIL — `enumerateAuxFiles` undefined.

- [ ] **Step 3: Implement enumerateAuxFiles**

Create `internal/app/aux.go`:

```go
package app

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// enumerateAuxFiles returns absolute paths to top-level *.md files in dir,
// excluding brief.md. Subdirectories (notably updates/) are not descended.
// Returns ([], nil) for a missing or empty directory — callers can render
// "(none)" in that case.
//
// The result is sorted lexicographically so output is deterministic.
func enumerateAuxFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "brief.md" {
			continue
		}
		if filepath.Ext(name) != ".md" {
			continue
		}
		out = append(out, filepath.Join(dir, name))
	}
	sort.Strings(out)
	return out, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/ -run TestEnumerateAuxFiles -v`
Expected: PASS (all three subtests).

- [ ] **Step 5: Commit**

```bash
git add internal/app/aux.go internal/app/aux_test.go
git commit -m "$(cat <<'EOF'
feat: add enumerateAuxFiles helper for sidecar markdown discovery

Top-level .md files in an entity directory (other than brief.md) are
sidecar references — research notes, decision trees, etc. dropped into
the dir alongside the brief.

Helper is shared by flow show task/project/playbook and the bootstrap
prompt. Returns sorted absolute paths; missing/empty dir returns nil.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Integrate aux files into flow show task

**Goal:** `flow show task` displays an `other:` section with sidecar `.md` files. Bootstrap prompt mentions them as on-demand references.

**Files:**
- Modify: `internal/app/show.go`, `internal/app/show_test.go`, `internal/app/do.go`, `internal/app/do_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/app/show_test.go`:

```go
func TestShowTaskListsAuxFiles(t *testing.T) {
	root := setupFlowRoot(t)
	wd := t.TempDir()

	// Add a task; this will create tasks/foo/brief.md and updates/.
	if rc := cmdAdd([]string{"task", "Foo task", "--slug", "foo", "--work-dir", wd}); rc != 0 {
		t.Fatalf("cmdAdd rc=%d", rc)
	}

	// Drop sidecar files into the task dir.
	taskDir := filepath.Join(root, "tasks", "foo")
	mustWrite(t, filepath.Join(taskDir, "research.md"), "r")
	mustWrite(t, filepath.Join(taskDir, "design.md"), "d")
	mustWrite(t, filepath.Join(taskDir, "skip.txt"), "ignored")

	out := captureStdout(t, func() {
		if rc := cmdShow([]string{"task", "foo"}); rc != 0 {
			t.Fatalf("cmdShow rc=%d", rc)
		}
	})

	if !strings.Contains(out, "other:") {
		t.Errorf("expected 'other:' section in output, got:\n%s", out)
	}
	if !strings.Contains(out, "research.md") {
		t.Errorf("expected research.md in other:, got:\n%s", out)
	}
	if !strings.Contains(out, "design.md") {
		t.Errorf("expected design.md in other:, got:\n%s", out)
	}
	if strings.Contains(out, "skip.txt") {
		t.Errorf("non-md file should not appear in other:, got:\n%s", out)
	}
}

func TestShowTaskNoAuxFiles(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"task", "Bar", "--slug", "bar", "--work-dir", wd}); rc != 0 {
		t.Fatal()
	}
	out := captureStdout(t, func() {
		if rc := cmdShow([]string{"task", "bar"}); rc != 0 {
			t.Fatal()
		}
	})
	if !strings.Contains(out, "other:       (none)") {
		t.Errorf("expected 'other: (none)', got:\n%s", out)
	}
}
```

If `captureStdout` doesn't exist in `testhelpers_test.go`, add it:

```go
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	w.Close()
	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}
```

(Add `bytes` and `io` imports as needed.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/app/ -run TestShowTaskListsAuxFiles -v`
Expected: FAIL — current `flow show task` has no `other:` line.

- [ ] **Step 3: Implement aux section in show.go**

Read the current `cmdShowTask` (or equivalent) in `internal/app/show.go` to find where it prints `brief:` and `updates:`. After the `updates:` block, add:

```go
// Auxiliary .md files (sidecar references — not eagerly loaded).
auxFiles, err := enumerateAuxFiles(taskDir)
if err != nil {
    fmt.Fprintf(os.Stderr, "warning: enumerate aux files: %v\n", err)
}
if len(auxFiles) == 0 {
    fmt.Println("other:       (none)")
} else {
    fmt.Printf("other:       - %s\n", auxFiles[0])
    for _, p := range auxFiles[1:] {
        fmt.Printf("             - %s\n", p)
    }
}
```

`taskDir` is the path used to compute the brief path; reuse the existing variable. If the variable name differs, find it in the current code.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/app/ -run TestShowTask -v`
Expected: PASS for both new tests, no regressions.

- [ ] **Step 5: Update do.go bootstrap prompt to mention other:**

In `internal/app/do.go`, update `buildBootstrapPrompt`:

```go
func buildBootstrapPrompt(slug string) string {
	return fmt.Sprintf(
		"You are the execution session for flow task %s. Do ALL of the following in order before touching code:\n"+
			"1. Invoke the flow skill via the Skill tool. This loads the operating manual that governs how this session works: workflows, bootstrap contract, KB discipline, and scope-creep detection.\n"+
			"2. Run: flow show task. Read the file at the brief: path AND every file listed under updates:. Files listed under other: are sidecar references — load on demand when relevant, not eagerly.\n"+
			"3. If a project is listed on the task, run: flow show project <that-project-slug>. Read its brief AND every file under updates:. Files under other: are on-demand references.\n"+
			"4. Read CLAUDE.md in your work_dir and any nested CLAUDE.md files under subdirectories you will modify. These override any assumption from the brief.\n"+
			"5. Only then begin work. If any brief section is blank or unclear, ASK — do not infer.",
		slug,
	)
}
```

- [ ] **Step 6: Update do_test.go to assert new prompt content**

Find the existing test (likely `TestBuildBootstrapPrompt` or similar) and add:

```go
func TestBuildBootstrapPromptMentionsOther(t *testing.T) {
	got := buildBootstrapPrompt("foo")
	if !strings.Contains(got, "other:") {
		t.Errorf("expected prompt to mention other:, got:\n%s", got)
	}
	if !strings.Contains(got, "load on demand") {
		t.Errorf("expected prompt to clarify lazy loading, got:\n%s", got)
	}
}
```

- [ ] **Step 7: Run all tests**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/app/show.go internal/app/show_test.go internal/app/do.go internal/app/do_test.go internal/app/testhelpers_test.go
git commit -m "$(cat <<'EOF'
feat: surface aux markdown files in show task and bootstrap prompt

flow show task now lists top-level .md files in the task dir (other
than brief.md) under an 'other:' section. Files in updates/ are still
shown separately. Non-md files ignored.

Bootstrap prompt mentions other: as on-demand references — same
lazy-load discipline as KB files (skill §5.10) — to avoid eagerly
pulling large sidecar docs into every session.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Integrate aux files into flow show project

**Goal:** Same treatment for `flow show project`.

**Files:**
- Modify: `internal/app/show.go`, `internal/app/show_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestShowProjectListsAuxFiles(t *testing.T) {
	root := setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"project", "Auth", "--slug", "auth", "--work-dir", wd}); rc != 0 {
		t.Fatal()
	}
	pdir := filepath.Join(root, "projects", "auth")
	mustWrite(t, filepath.Join(pdir, "decisions.md"), "x")
	out := captureStdout(t, func() {
		if rc := cmdShow([]string{"project", "auth"}); rc != 0 {
			t.Fatal()
		}
	})
	if !strings.Contains(out, "other:") {
		t.Errorf("expected other: section, got:\n%s", out)
	}
	if !strings.Contains(out, "decisions.md") {
		t.Errorf("expected decisions.md, got:\n%s", out)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/app/ -run TestShowProject -v`
Expected: FAIL.

- [ ] **Step 3: Add aux section to cmdShowProject**

In `show.go`, find `cmdShowProject` and apply the same pattern as Task 3 step 3 but with `projectDir` (or the local variable name) instead of `taskDir`.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/app/ -run TestShowProject -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/show.go internal/app/show_test.go
git commit -m "$(cat <<'EOF'
feat: surface aux markdown files in show project

Mirror the show task change — top-level .md files in projects/<slug>/
are listed under other: as on-demand references.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 2: Schema (playbook table + tasks discriminator)

### Task 5: Migrate tasks table to add kind and playbook_slug

**Goal:** Add `tasks.kind` (default `'regular'`, CHECK constraint) and `tasks.playbook_slug` (FK, nullable). Idempotent migration.

**Files:**
- Modify: `internal/flowdb/db.go`, `internal/flowdb/db_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/flowdb/db_test.go`:

```go
func TestMigrationAddsTasksKindAndPlaybookSlug(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "flow.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	// Both columns should exist.
	hasKind, err := columnExists(db, "tasks", "kind")
	if err != nil {
		t.Fatal(err)
	}
	if !hasKind {
		t.Error("tasks.kind column missing after OpenDB")
	}
	hasPB, err := columnExists(db, "tasks", "playbook_slug")
	if err != nil {
		t.Fatal(err)
	}
	if !hasPB {
		t.Error("tasks.playbook_slug column missing after OpenDB")
	}

	// Default for kind should be 'regular' for new rows.
	now := NowISO()
	wd := t.TempDir()
	if _, err := db.Exec(
		`INSERT INTO tasks (slug, name, status, priority, work_dir, created_at, updated_at) VALUES (?, ?, 'backlog', 'medium', ?, ?, ?)`,
		"t1", "Task 1", wd, now, now,
	); err != nil {
		t.Fatal(err)
	}
	var kind string
	if err := db.QueryRow(`SELECT kind FROM tasks WHERE slug='t1'`).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "regular" {
		t.Errorf("default kind: got %q, want regular", kind)
	}
}

func TestMigrationIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "flow.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	// Reopen — should not error.
	db, err = OpenDB(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	db.Close()
}
```

(Note: `columnExists` is private to flowdb; if the test is in `package flowdb`, it has access. Verify package by reading the test file's first line.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/flowdb/ -run TestMigrationAddsTasksKindAndPlaybookSlug -v`
Expected: FAIL — column missing.

- [ ] **Step 3: Add migrations**

In `internal/flowdb/db.go`, find `runMigrations` and append:

```go
has, err = columnExists(db, "tasks", "kind")
if err != nil {
    return err
}
if !has {
    // SQLite doesn't allow ALTER ... ADD COLUMN with a CHECK constraint
    // referencing the new column directly; instead we add the column
    // with a default and rely on the application layer to enforce values.
    // Fresh tables get the CHECK via schemaDDL.
    if _, err := db.Exec(`ALTER TABLE tasks ADD COLUMN kind TEXT NOT NULL DEFAULT 'regular'`); err != nil {
        return fmt.Errorf("add tasks.kind: %w", err)
    }
}
has, err = columnExists(db, "tasks", "playbook_slug")
if err != nil {
    return err
}
if !has {
    if _, err := db.Exec(`ALTER TABLE tasks ADD COLUMN playbook_slug TEXT REFERENCES playbooks(slug)`); err != nil {
        // The FK reference will fail if playbooks doesn't exist yet.
        // Order matters: playbooks table must be created (in schemaDDL)
        // before this ALTER runs. See Task 6.
        return fmt.Errorf("add tasks.playbook_slug: %w", err)
    }
}
```

**Important:** This task assumes Task 6 (playbooks table DDL) is already applied. We'll order the tasks so Task 6 lands before Task 5's migrations run on first open. To make Task 5 self-contained, we can also add the playbooks table DDL here — but let's keep it ordered: Task 5 adds these migrations, but Task 6 is also part of this phase and lands together.

Actually, simplification: combine Tasks 5 and 6 into one migration commit so the order is correct on a single fresh build. Let's restructure: do Task 6 (playbooks DDL) and Task 5 (tasks columns) in one PR with one commit.

- [ ] **Step 4: Continue to Task 6 before running**

(The next task adds the playbooks table to schemaDDL. Don't run tests yet — they'll fail until both are in place.)

- [ ] **Step 5: After Task 6 step 3, return here and run tests**

Run: `go test ./internal/flowdb/ -run TestMigration -v`
Expected: PASS.

---

### Task 6: Add playbooks table to schema

**Goal:** New `playbooks` table with full DDL in `schemaDDL`.

**Files:**
- Modify: `internal/flowdb/db.go`, `internal/flowdb/db_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/flowdb/db_test.go`:

```go
func TestPlaybooksTableExists(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "flow.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name='playbooks'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Error("playbooks table missing")
	}

	// Schema sanity: insert a row, read it back.
	now := NowISO()
	wd := t.TempDir()
	if _, err := db.Exec(
		`INSERT INTO playbooks (slug, name, work_dir, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"p1", "Playbook 1", wd, now, now,
	); err != nil {
		t.Fatalf("insert playbook: %v", err)
	}
	var slug, name, gotWD string
	err = db.QueryRow(`SELECT slug, name, work_dir FROM playbooks WHERE slug='p1'`).Scan(&slug, &name, &gotWD)
	if err != nil {
		t.Fatal(err)
	}
	if name != "Playbook 1" || gotWD != wd {
		t.Errorf("unexpected: slug=%q name=%q wd=%q", slug, name, gotWD)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/flowdb/ -run TestPlaybooksTableExists -v`
Expected: FAIL — table doesn't exist.

- [ ] **Step 3: Add playbooks table to schemaDDL**

In `internal/flowdb/db.go`, append to the `schemaDDL` constant:

```go
CREATE TABLE IF NOT EXISTS playbooks (
    slug          TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    project_slug  TEXT REFERENCES projects(slug),
    work_dir      TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    archived_at   TEXT
);

CREATE INDEX IF NOT EXISTS idx_playbooks_project ON playbooks(project_slug);
```

(Add it after the workdirs table block but before the trailing indexes; or add after the existing indexes — either works since each statement is idempotent.)

- [ ] **Step 4: Add tasks indexes for kind and playbook_slug**

Append to the index block in `schemaDDL`:

```go
CREATE INDEX IF NOT EXISTS idx_tasks_kind          ON tasks(kind);
CREATE INDEX IF NOT EXISTS idx_tasks_playbook_slug ON tasks(playbook_slug);
```

(These index columns added in Task 5. They go after `idx_tasks_updated_at`.)

- [ ] **Step 5: Add `kind` column to fresh-table DDL**

Update the `tasks` CREATE TABLE statement to include the new columns up front (so fresh installs get the CHECK constraint, not just the migration default):

```sql
CREATE TABLE IF NOT EXISTS tasks (
    slug                  TEXT PRIMARY KEY,
    name                  TEXT NOT NULL,
    project_slug          TEXT REFERENCES projects(slug),
    status                TEXT NOT NULL DEFAULT 'backlog' CHECK (status IN ('backlog','in-progress','done')),
    kind                  TEXT NOT NULL DEFAULT 'regular' CHECK (kind IN ('regular','playbook_run')),
    playbook_slug         TEXT REFERENCES playbooks(slug),
    priority              TEXT NOT NULL DEFAULT 'medium' CHECK (priority IN ('high','medium','low')),
    work_dir              TEXT NOT NULL,
    waiting_on            TEXT,
    due_date              TEXT,
    status_changed_at     TEXT,
    session_id            TEXT,
    session_started       TEXT,
    session_last_resumed  TEXT,
    created_at            TEXT NOT NULL,
    updated_at            TEXT NOT NULL,
    archived_at           TEXT
);
```

- [ ] **Step 6: Run tests for both task 5 and task 6**

Run: `go test ./internal/flowdb/ -v`
Expected: All migration and playbook table tests PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/flowdb/db.go internal/flowdb/db_test.go
git commit -m "$(cat <<'EOF'
feat(db): add playbooks table and tasks.kind/playbook_slug columns

Schema for playbook construct:
- New playbooks table (slug, name, project_slug, work_dir, timestamps,
  archived_at).
- tasks.kind enum: 'regular' (default) | 'playbook_run'.
- tasks.playbook_slug FK to playbooks(slug), nullable.
- Indexes on tasks.kind and tasks.playbook_slug for filtered listings.

Migrations are idempotent — existing tasks rows get kind='regular' via
column default; fresh tables get the CHECK constraint.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: Add Playbook model + scan/get/list/upsert in flowdb

**Goal:** Go-level access to the playbooks table — `Playbook` struct, `ScanPlaybook`, `GetPlaybook`, `ListPlaybooks`, `UpsertPlaybook`. Also extend `Task` struct and `TaskFilter` with `Kind` and `PlaybookSlug`.

**Files:**
- Modify: `internal/flowdb/db.go`, `internal/flowdb/db_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestPlaybookCRUD(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenDB(filepath.Join(dir, "flow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	wd := t.TempDir()
	if err := UpsertPlaybook(db, &Playbook{
		Slug:    "triage-cs",
		Name:    "Triage CS inbox",
		WorkDir: wd,
	}); err != nil {
		t.Fatalf("UpsertPlaybook: %v", err)
	}

	pb, err := GetPlaybook(db, "triage-cs")
	if err != nil {
		t.Fatalf("GetPlaybook: %v", err)
	}
	if pb.Name != "Triage CS inbox" || pb.WorkDir != wd {
		t.Errorf("got %+v", pb)
	}

	pbs, err := ListPlaybooks(db, PlaybookFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pbs) != 1 {
		t.Errorf("ListPlaybooks: got %d, want 1", len(pbs))
	}
}

func TestTaskWithKindAndPlaybookSlug(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenDB(filepath.Join(dir, "flow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	wd := t.TempDir()
	now := NowISO()
	if err := UpsertPlaybook(db, &Playbook{Slug: "p1", Name: "P1", WorkDir: wd}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO tasks (slug, name, status, kind, playbook_slug, priority, work_dir, created_at, updated_at)
		 VALUES (?, ?, 'backlog', 'playbook_run', ?, 'medium', ?, ?, ?)`,
		"p1--2026-04-30-10-30", "p1 run", "p1", wd, now, now,
	); err != nil {
		t.Fatal(err)
	}

	task, err := GetTask(db, "p1--2026-04-30-10-30")
	if err != nil {
		t.Fatal(err)
	}
	if task.Kind != "playbook_run" {
		t.Errorf("Kind: got %q", task.Kind)
	}
	if !task.PlaybookSlug.Valid || task.PlaybookSlug.String != "p1" {
		t.Errorf("PlaybookSlug: got %+v", task.PlaybookSlug)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/flowdb/ -run TestPlaybookCRUD -v` and `... TestTaskWithKindAndPlaybookSlug -v`
Expected: FAIL — types don't exist; Task struct missing Kind/PlaybookSlug.

- [ ] **Step 3: Add Playbook struct, scan, queries, and extend Task**

In `internal/flowdb/db.go`, add after the Workdir struct:

```go
// Playbook mirrors the playbooks table.
type Playbook struct {
	Slug        string
	Name        string
	ProjectSlug sql.NullString
	WorkDir     string
	CreatedAt   string
	UpdatedAt   string
	ArchivedAt  sql.NullString
}

// PlaybookFilter holds optional filters for ListPlaybooks.
type PlaybookFilter struct {
	Project         string
	IncludeArchived bool
}

const PlaybookCols = "slug, name, project_slug, work_dir, created_at, updated_at, archived_at"

func ScanPlaybook(row interface{ Scan(dest ...any) error }) (*Playbook, error) {
	var p Playbook
	err := row.Scan(&p.Slug, &p.Name, &p.ProjectSlug, &p.WorkDir, &p.CreatedAt, &p.UpdatedAt, &p.ArchivedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func GetPlaybook(db *sql.DB, slug string) (*Playbook, error) {
	row := db.QueryRow("SELECT "+PlaybookCols+" FROM playbooks WHERE slug = ?", slug)
	return ScanPlaybook(row)
}

func ListPlaybooks(db *sql.DB, filter PlaybookFilter) ([]*Playbook, error) {
	var where []string
	var args []any
	if filter.Project != "" {
		where = append(where, "project_slug = ?")
		args = append(args, filter.Project)
	}
	if !filter.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	q := "SELECT " + PlaybookCols + " FROM playbooks"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY slug"
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list playbooks: %w", err)
	}
	defer rows.Close()
	var out []*Playbook
	for rows.Next() {
		p, err := ScanPlaybook(rows)
		if err != nil {
			return nil, fmt.Errorf("scan playbook: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpsertPlaybook inserts a new playbook or updates an existing row by slug.
// Updates touch name, project_slug, work_dir, updated_at; archived_at is
// not touched here (use a dedicated archive command).
func UpsertPlaybook(db *sql.DB, pb *Playbook) error {
	now := NowISO()
	if pb.CreatedAt == "" {
		pb.CreatedAt = now
	}
	pb.UpdatedAt = now
	_, err := db.Exec(`
		INSERT INTO playbooks (slug, name, project_slug, work_dir, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(slug) DO UPDATE SET
			name         = excluded.name,
			project_slug = excluded.project_slug,
			work_dir     = excluded.work_dir,
			updated_at   = excluded.updated_at
	`, pb.Slug, pb.Name, pb.ProjectSlug, pb.WorkDir, pb.CreatedAt, pb.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upsert playbook %s: %w", pb.Slug, err)
	}
	return nil
}
```

Also extend the `Task` struct with `Kind` and `PlaybookSlug`:

```go
type Task struct {
	Slug               string
	Name               string
	ProjectSlug        sql.NullString
	Status             string
	Kind               string         // 'regular' | 'playbook_run'
	PlaybookSlug       sql.NullString // set when Kind='playbook_run'
	Priority           string
	WorkDir            string
	WaitingOn          sql.NullString
	DueDate            sql.NullString
	StatusChangedAt    sql.NullString
	SessionID          sql.NullString
	SessionStarted     sql.NullString
	SessionLastResumed sql.NullString
	CreatedAt          string
	UpdatedAt          string
	ArchivedAt         sql.NullString
}
```

Update `TaskCols` to include the new columns in a stable order:

```go
const TaskCols = "slug, name, project_slug, status, kind, playbook_slug, priority, work_dir, waiting_on, due_date, status_changed_at, session_id, session_started, session_last_resumed, created_at, updated_at, archived_at"
```

Update `ScanTask`:

```go
func ScanTask(row interface{ Scan(dest ...any) error }) (*Task, error) {
	var t Task
	err := row.Scan(
		&t.Slug, &t.Name, &t.ProjectSlug, &t.Status, &t.Kind, &t.PlaybookSlug,
		&t.Priority, &t.WorkDir,
		&t.WaitingOn, &t.DueDate, &t.StatusChangedAt, &t.SessionID,
		&t.SessionStarted, &t.SessionLastResumed, &t.CreatedAt, &t.UpdatedAt, &t.ArchivedAt,
	)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
```

Add Kind to `TaskFilter`:

```go
type TaskFilter struct {
	Status          string
	Project         string
	Priority        string
	Kind            string // "regular" (default), "playbook_run", or "" for all
	PlaybookSlug    string // optional, filter to runs of one playbook
	Since           string
	IncludeArchived bool
}
```

Update `ListTasks` to honor `Kind` and `PlaybookSlug`:

```go
if filter.Kind != "" {
    where = append(where, "kind = ?")
    args = append(args, filter.Kind)
}
if filter.PlaybookSlug != "" {
    where = append(where, "playbook_slug = ?")
    args = append(args, filter.PlaybookSlug)
}
```

(Insert these just after the Project filter block.)

- [ ] **Step 4: Run tests**

Run: `go test ./internal/flowdb/ -v`
Expected: PASS.

But also run the existing app tests to catch regressions from the TaskCols change:

Run: `go test ./...`

If you see scan errors in app tests, fix any callers that hardcoded the column count or order. Likely places: anywhere that does `db.QueryRow("SELECT slug, name, ... FROM tasks ...")` with explicit columns. Use `flowdb.TaskCols` and `flowdb.ScanTask` everywhere instead.

- [ ] **Step 5: Commit**

```bash
git add internal/flowdb/db.go internal/flowdb/db_test.go
git commit -m "$(cat <<'EOF'
feat(db): Playbook model + scan/get/list/upsert; Task gains kind+playbook_slug

Adds the Go-side data layer for playbooks:
- Playbook struct, PlaybookFilter, PlaybookCols, ScanPlaybook
- GetPlaybook, ListPlaybooks, UpsertPlaybook

Extends Task: new Kind and PlaybookSlug fields. TaskCols and ScanTask
updated. TaskFilter gains Kind and PlaybookSlug filters.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 3: Playbook CLI commands

### Task 8: ResolvePlaybook helper

**Goal:** Given a user-supplied ref, return the matching non-archived playbook or an error.

**Files:**
- Modify: `internal/app/resolve.go`
- New: `internal/app/playbook_resolve_test.go` (or extend an existing resolve_test.go)

- [ ] **Step 1: Write the failing test**

Add to `internal/app/resolve_test.go` (or create if missing):

```go
func TestResolvePlaybook(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	wd := t.TempDir()
	if err := flowdb.UpsertPlaybook(db, &flowdb.Playbook{Slug: "triage-cs", Name: "Triage", WorkDir: wd}); err != nil {
		t.Fatal(err)
	}

	pb, err := ResolvePlaybook(db, "triage-cs", false)
	if err != nil {
		t.Fatalf("ResolvePlaybook: %v", err)
	}
	if pb.Slug != "triage-cs" {
		t.Errorf("got slug %q", pb.Slug)
	}

	// Missing
	if _, err := ResolvePlaybook(db, "no-such", false); err == nil {
		t.Errorf("expected error for missing slug")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/app/ -run TestResolvePlaybook -v`
Expected: FAIL — `ResolvePlaybook` undefined.

- [ ] **Step 3: Implement ResolvePlaybook**

In `internal/app/resolve.go`, add (mirroring `ResolveTask`):

```go
// ResolvePlaybook resolves a user-supplied ref to a playbook by exact slug
// match. If includeArchived is false (the default), archived playbooks are
// excluded.
func ResolvePlaybook(db *sql.DB, ref string, includeArchived bool) (*flowdb.Playbook, error) {
	row := db.QueryRow("SELECT "+flowdb.PlaybookCols+" FROM playbooks WHERE slug = ?", ref)
	pb, err := flowdb.ScanPlaybook(row)
	if err != nil {
		return nil, fmt.Errorf("no playbook matching %q", ref)
	}
	if !includeArchived && pb.ArchivedAt.Valid {
		return nil, fmt.Errorf("playbook %q is archived; pass --include-archived or run unarchive", ref)
	}
	return pb, nil
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/app/ -run TestResolvePlaybook -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/resolve.go internal/app/resolve_test.go
git commit -m "feat: add ResolvePlaybook for slug-based playbook lookup

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 9: flow add playbook

**Goal:** New subcommand `flow add playbook "<name>" [flags]` that creates a playbook row, materializes the directory tree, and writes a stub `brief.md`.

**Files:**
- Create: `internal/app/playbook.go`, `internal/app/playbook_test.go`
- Modify: `internal/app/add.go`, `internal/app/app.go`

- [ ] **Step 1: Write the failing test**

Create `internal/app/playbook_test.go`:

```go
package app

import (
	"os"
	"path/filepath"
	"testing"

	"flow/internal/flowdb"
)

func TestCmdAddPlaybookHappyPath(t *testing.T) {
	root := setupFlowRoot(t)
	wd := t.TempDir()

	rc := cmdAdd([]string{"playbook", "Triage CS inbox", "--work-dir", wd, "--slug", "triage-cs"})
	if rc != 0 {
		t.Fatalf("cmdAdd rc=%d", rc)
	}

	db := openFlowDB(t)
	pb, err := flowdb.GetPlaybook(db, "triage-cs")
	if err != nil {
		t.Fatalf("GetPlaybook: %v", err)
	}
	if pb.Name != "Triage CS inbox" {
		t.Errorf("name: got %q", pb.Name)
	}
	if pb.WorkDir != wd {
		t.Errorf("work_dir: got %q", pb.WorkDir)
	}

	// brief.md exists
	briefPath := filepath.Join(root, "playbooks", "triage-cs", "brief.md")
	if _, err := os.Stat(briefPath); err != nil {
		t.Errorf("brief.md missing: %v", err)
	}
	// updates/ exists
	if _, err := os.Stat(filepath.Join(root, "playbooks", "triage-cs", "updates")); err != nil {
		t.Errorf("updates/ dir missing: %v", err)
	}
}

func TestCmdAddPlaybookRequiresWorkDir(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdAdd([]string{"playbook", "NoWD"}); rc == 0 {
		t.Errorf("expected non-zero rc when --work-dir missing")
	}
}

func TestCmdAddPlaybookWithProject(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	wd := t.TempDir()
	if err := flowdb.UpsertWorkdir(db, wd, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if rc := cmdAdd([]string{"project", "CS Tools", "--slug", "cs-tools", "--work-dir", wd}); rc != 0 {
		t.Fatal()
	}

	if rc := cmdAdd([]string{"playbook", "Triage", "--slug", "tri", "--work-dir", wd, "--project", "cs-tools"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	pb, err := flowdb.GetPlaybook(db, "tri")
	if err != nil {
		t.Fatal(err)
	}
	if !pb.ProjectSlug.Valid || pb.ProjectSlug.String != "cs-tools" {
		t.Errorf("project_slug: got %+v", pb.ProjectSlug)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/app/ -run TestCmdAddPlaybook -v`
Expected: FAIL — playbook subcommand not handled.

- [ ] **Step 3: Implement cmdAddPlaybook**

Create `internal/app/playbook.go`:

```go
package app

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"flow/internal/flowdb"
)

// cmdAddPlaybook handles `flow add playbook "<name>" [flags]`.
func cmdAddPlaybook(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: add playbook requires a name")
		return 2
	}
	name := args[0]

	fs := flagSet("add playbook")
	slug := fs.String("slug", "", "explicit slug (default: derived from name)")
	project := fs.String("project", "", "attach to project slug")
	workDir := fs.String("work-dir", "", "work_dir for runs of this playbook")
	mkdir := fs.Bool("mkdir", false, "create work_dir if missing")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *workDir == "" {
		fmt.Fprintln(os.Stderr, "error: --work-dir is required")
		return 2
	}

	if *mkdir {
		if err := os.MkdirAll(*workDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "error: mkdir %s: %v\n", *workDir, err)
			return 1
		}
	} else if _, err := os.Stat(*workDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: work_dir %s: %v\n", *workDir, err)
		return 1
	}

	pSlug := *slug
	if pSlug == "" {
		pSlug = NameToSlug(name)
	}

	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := flowdb.OpenDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	// Reject if playbook already exists.
	if _, err := flowdb.GetPlaybook(db, pSlug); err == nil {
		fmt.Fprintf(os.Stderr, "error: playbook %q already exists\n", pSlug)
		return 1
	} else if !errors.Is(err, sql.ErrNoRows) {
		fmt.Fprintf(os.Stderr, "error: lookup playbook: %v\n", err)
		return 1
	}

	// Validate project if supplied.
	var projectSlug sql.NullString
	if *project != "" {
		if _, err := flowdb.GetProject(db, *project); err != nil {
			fmt.Fprintf(os.Stderr, "error: project %q not found\n", *project)
			return 1
		}
		projectSlug = sql.NullString{String: *project, Valid: true}
	}

	pb := &flowdb.Playbook{
		Slug:        pSlug,
		Name:        name,
		ProjectSlug: projectSlug,
		WorkDir:     *workDir,
	}
	if err := flowdb.UpsertPlaybook(db, pb); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// Auto-register workdir.
	if err := flowdb.UpsertWorkdir(db, *workDir, "", "", ""); err != nil {
		fmt.Fprintf(os.Stderr, "warning: register workdir: %v\n", err)
	}

	// Materialize the directory and stub brief.md.
	root, err := flowRootDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: flow root: %v\n", err)
		return 1
	}
	pbDir := filepath.Join(root, "playbooks", pSlug)
	if err := os.MkdirAll(filepath.Join(pbDir, "updates"), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "error: mkdir %s: %v\n", pbDir, err)
		return 1
	}
	briefPath := filepath.Join(pbDir, "brief.md")
	if _, err := os.Stat(briefPath); errors.Is(err, fs.ErrNotExist) {
		stub := fmt.Sprintf(playbookBriefStub, name, *workDir, pSlug)
		if err := os.WriteFile(briefPath, []byte(stub), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "error: write brief.md: %v\n", err)
			return 1
		}
	}

	fmt.Printf("Added playbook %q (slug %s)\n", name, pSlug)
	fmt.Printf("Brief: %s\n", briefPath)
	return 0
}

// playbookBriefStub is the initial content written to a new playbook's
// brief.md. Skill-driven intake will overwrite this with full content;
// CLI-only adders see this minimal scaffold.
const playbookBriefStub = `# %s

## What
*Fill in: one sentence describing what each run does.*

## Why
*Fill in: why this playbook exists.*

## Where
work_dir: %s

## Each run does
- *Fill in: steps that every invocation performs.*

## Out of scope
- *Fill in non-goals.*

## Signals to watch for
- *Fill in: signals that should change behavior or escalate.*

---
*Run with ` + "`flow run playbook %s`" + `. Each run gets its own session
and a snapshot of this brief at run time. Editing this file does not
retroactively change past runs.*
`
```

(You may need to add `"io/fs"` to imports for `fs.ErrNotExist`. Or use `os.IsNotExist(err)` if `fs.ErrNotExist` is not imported elsewhere. The codebase pattern uses `os.IsNotExist` — match that:)

```go
if _, err := os.Stat(briefPath); err != nil && os.IsNotExist(err) {
    // write stub
}
```

(Adjust the example accordingly. `flowRootDir` may be named differently — check `internal/app/init.go` for `flowRoot()` which returns the root path. If it doesn't exist, look for the helper that constructs `~/.flow/`.)

- [ ] **Step 4: Wire into cmdAdd**

In `internal/app/add.go`, find the `cmdAdd` dispatcher:

```go
func cmdAdd(args []string) int {
    if len(args) == 0 {
        fmt.Fprintln(os.Stderr, "error: add requires a subcommand (project|task)")
        return 2
    }
    sub, rest := args[0], args[1:]
    switch sub {
    case "project":
        return cmdAddProject(rest)
    case "task":
        return cmdAddTask(rest)
    default:
        fmt.Fprintf(os.Stderr, "error: unknown subcommand %q\n", sub)
        return 2
    }
}
```

Add `playbook` case:

```go
case "playbook":
    return cmdAddPlaybook(rest)
```

Update the error message: `"error: add requires a subcommand (project|task|playbook)"`.

- [ ] **Step 5: Run tests**

Run: `go test ./internal/app/ -run TestCmdAddPlaybook -v`
Expected: PASS.

- [ ] **Step 6: Run full suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/app/playbook.go internal/app/playbook_test.go internal/app/add.go
git commit -m "$(cat <<'EOF'
feat: flow add playbook command

Creates a playbook row, materializes ~/.flow/playbooks/<slug>/, writes
a stub brief.md (intake-driven content overwrites it). Optional --project
links to an existing project; --work-dir is required.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 10: flow list playbooks

**Goal:** List all non-archived playbooks (or all with `--include-archived`), optionally filtered by project.

**Files:**
- Modify: `internal/app/list.go`, `internal/app/list_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/app/list_test.go`:

```go
func TestCmdListPlaybooks(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	wd := t.TempDir()
	if err := flowdb.UpsertPlaybook(db, &flowdb.Playbook{Slug: "a", Name: "A", WorkDir: wd}); err != nil {
		t.Fatal(err)
	}
	if err := flowdb.UpsertPlaybook(db, &flowdb.Playbook{Slug: "b", Name: "B", WorkDir: wd}); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if rc := cmdList([]string{"playbooks"}); rc != 0 {
			t.Fatal()
		}
	})
	if !strings.Contains(out, "a") || !strings.Contains(out, "b") {
		t.Errorf("expected playbooks a and b in output:\n%s", out)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Expected: FAIL.

- [ ] **Step 3: Implement cmdListPlaybooks**

In `internal/app/list.go`, find `cmdList` (the dispatcher) and add a `playbooks` case. Then add the handler:

```go
func cmdListPlaybooks(args []string) int {
    fs := flagSet("list playbooks")
    project := fs.String("project", "", "filter by project slug")
    includeArchived := fs.Bool("include-archived", false, "include archived")
    if err := fs.Parse(args); err != nil {
        return 2
    }

    dbPath, err := flowDBPath()
    if err != nil {
        fmt.Fprintf(os.Stderr, "error: %v\n", err)
        return 1
    }
    db, err := flowdb.OpenDB(dbPath)
    if err != nil {
        fmt.Fprintf(os.Stderr, "error: %v\n", err)
        return 1
    }
    defer db.Close()

    pbs, err := flowdb.ListPlaybooks(db, flowdb.PlaybookFilter{
        Project:         *project,
        IncludeArchived: *includeArchived,
    })
    if err != nil {
        fmt.Fprintf(os.Stderr, "error: %v\n", err)
        return 1
    }
    for _, pb := range pbs {
        proj := ""
        if pb.ProjectSlug.Valid {
            proj = "(" + pb.ProjectSlug.String + ")"
        }
        fmt.Printf("  %-40s %s\n", pb.Slug, proj)
    }
    return 0
}
```

(Match the formatting style of `cmdListTasks`/`cmdListProjects` more carefully — read those for the exact column widths and conventions; this is approximate.)

Update the dispatcher to include the new case and update its error message.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/app/ -run TestCmdListPlaybooks -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/list.go internal/app/list_test.go
git commit -m "feat: flow list playbooks (with --project and --include-archived)

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 11: flow show playbook

**Goal:** Display a playbook's metadata, brief path, recent runs, kb refs, and aux files.

**Files:**
- Modify: `internal/app/show.go`, `internal/app/show_test.go`, `internal/app/playbook.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/app/show_test.go`:

```go
func TestCmdShowPlaybook(t *testing.T) {
	root := setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"playbook", "Triage", "--slug", "tri", "--work-dir", wd}); rc != 0 {
		t.Fatal()
	}
	out := captureStdout(t, func() {
		if rc := cmdShow([]string{"playbook", "tri"}); rc != 0 {
			t.Fatal()
		}
	})
	if !strings.Contains(out, "slug:") || !strings.Contains(out, "tri") {
		t.Errorf("expected slug line:\n%s", out)
	}
	if !strings.Contains(out, "brief:") {
		t.Errorf("expected brief: line:\n%s", out)
	}
	if !strings.Contains(out, "runs (last 5):") {
		t.Errorf("expected runs section:\n%s", out)
	}
	if !strings.Contains(out, "kb:") {
		t.Errorf("expected kb section:\n%s", out)
	}
	if !strings.Contains(out, "other:") {
		t.Errorf("expected other: section:\n%s", out)
	}
	briefPath := filepath.Join(root, "playbooks", "tri", "brief.md")
	if !strings.Contains(out, briefPath) {
		t.Errorf("expected brief path %q in output:\n%s", briefPath, out)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Expected: FAIL — `show playbook` not handled.

- [ ] **Step 3: Implement cmdShowPlaybook**

Add to `internal/app/show.go`:

```go
func cmdShowPlaybook(args []string) int {
    var ref string
    if len(args) > 0 {
        ref = args[0]
    } else {
        ref = os.Getenv("FLOW_PLAYBOOK")
        if ref == "" {
            fmt.Fprintln(os.Stderr, "error: show playbook requires a ref or FLOW_PLAYBOOK")
            return 2
        }
    }

    dbPath, err := flowDBPath()
    if err != nil {
        fmt.Fprintf(os.Stderr, "error: %v\n", err)
        return 1
    }
    db, err := flowdb.OpenDB(dbPath)
    if err != nil {
        fmt.Fprintf(os.Stderr, "error: %v\n", err)
        return 1
    }
    defer db.Close()

    pb, err := ResolvePlaybook(db, ref, true)
    if err != nil {
        fmt.Fprintf(os.Stderr, "error: %v\n", err)
        return 1
    }

    root, err := flowRootDir()
    if err != nil {
        fmt.Fprintf(os.Stderr, "error: %v\n", err)
        return 1
    }
    pbDir := filepath.Join(root, "playbooks", pb.Slug)
    briefPath := filepath.Join(pbDir, "brief.md")

    fmt.Printf("slug:        %s\n", pb.Slug)
    fmt.Printf("name:        %s\n", pb.Name)
    if pb.ProjectSlug.Valid {
        fmt.Printf("project:     %s\n", pb.ProjectSlug.String)
    } else {
        fmt.Printf("project:     (floating)\n")
    }
    fmt.Printf("work_dir:    %s\n", pb.WorkDir)
    fmt.Printf("created:     %s\n", pb.CreatedAt)
    fmt.Printf("updated:     %s\n", pb.UpdatedAt)
    if pb.ArchivedAt.Valid {
        fmt.Printf("archived:    %s\n", pb.ArchivedAt.String)
    }
    fmt.Printf("brief:       %s\n", briefPath)

    // Updates from the playbook dir.
    updatesDir := filepath.Join(pbDir, "updates")
    updates, _ := os.ReadDir(updatesDir)
    if len(updates) == 0 {
        fmt.Println("updates:     (none)")
    } else {
        fmt.Print("updates:     ")
        for i, e := range updates {
            if i == 0 {
                fmt.Printf("- %s\n", filepath.Join(updatesDir, e.Name()))
            } else {
                fmt.Printf("             - %s\n", filepath.Join(updatesDir, e.Name()))
            }
        }
    }

    // Recent runs (last 5).
    runs, err := flowdb.ListTasks(db, flowdb.TaskFilter{
        Kind:         "playbook_run",
        PlaybookSlug: pb.Slug,
    })
    if err != nil {
        fmt.Fprintf(os.Stderr, "warning: list runs: %v\n", err)
    }
    if len(runs) == 0 {
        fmt.Println("runs (last 5): (none)")
    } else {
        fmt.Println("runs (last 5):")
        max := 5
        if len(runs) < max {
            max = len(runs)
        }
        // Sort by created_at desc — ListTasks orders by priority then slug.
        // Quick re-sort:
        sort.Slice(runs, func(i, j int) bool {
            return runs[i].CreatedAt > runs[j].CreatedAt
        })
        for _, r := range runs[:max] {
            fmt.Printf("  %-50s [%s]\n", r.Slug, statusAbbrev(r.Status))
        }
    }

    // Aux files.
    auxFiles, err := enumerateAuxFiles(pbDir)
    if err != nil {
        fmt.Fprintf(os.Stderr, "warning: aux files: %v\n", err)
    }
    if len(auxFiles) == 0 {
        fmt.Println("other:       (none)")
    } else {
        fmt.Print("other:       ")
        for i, p := range auxFiles {
            if i == 0 {
                fmt.Printf("- %s\n", p)
            } else {
                fmt.Printf("             - %s\n", p)
            }
        }
    }

    // KB refs.
    kbDir := filepath.Join(root, "kb")
    fmt.Println("kb:")
    for _, name := range []string{"user.md", "org.md", "products.md", "processes.md", "business.md"} {
        fmt.Printf("  - %s\n", filepath.Join(kbDir, name))
    }
    return 0
}
```

(Note: `statusAbbrev` is a helper that exists in `list.go` returning "BL"/"IP"/"DN"/"AR". Reuse it. Add `sort` to imports.)

Wire into the `cmdShow` dispatcher:

```go
case "playbook":
    return cmdShowPlaybook(rest)
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/app/ -run TestCmdShowPlaybook -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/show.go internal/app/show_test.go
git commit -m "feat: flow show playbook (with recent runs, aux files, kb refs)

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 12: Extend flow edit to handle playbook refs

**Goal:** `flow edit <playbook-slug>` opens the playbook's `brief.md`.

**Files:**
- Modify: `internal/app/edit.go`, `internal/app/edit_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestCmdEditPlaybook(t *testing.T) {
	root := setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"playbook", "P", "--slug", "p", "--work-dir", wd}); rc != 0 {
		t.Fatal()
	}
	// Stub the editor by pointing $EDITOR at /usr/bin/true (no-op success).
	t.Setenv("EDITOR", "/usr/bin/true")
	if rc := cmdEdit([]string{"p"}); rc != 0 {
		t.Errorf("rc=%d", rc)
	}
	briefPath := filepath.Join(root, "playbooks", "p", "brief.md")
	if _, err := os.Stat(briefPath); err != nil {
		t.Errorf("brief.md missing: %v", err)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Expected: FAIL — current `cmdEdit` only resolves tasks/projects.

- [ ] **Step 3: Implement playbook fallback in cmdEdit**

Read current `cmdEdit` resolution order. Add a third resolver after task and project:

```go
// Try as playbook
pb, perr := ResolvePlaybook(db, ref, true)
if perr == nil {
    briefPath := filepath.Join(root, "playbooks", pb.Slug, "brief.md")
    return openInEditor(briefPath, db, "playbook", pb.Slug)
}
```

(Match the existing pattern. The exact code depends on how cmdEdit is structured — read it before implementing.)

- [ ] **Step 4: Run tests**

Run: `go test ./internal/app/ -run TestCmdEditPlaybook -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/edit.go internal/app/edit_test.go
git commit -m "feat: flow edit handles playbook refs (opens brief.md)

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 13: Extend flow archive/unarchive to handle playbook refs

**Goal:** `flow archive <playbook-slug>` and `flow unarchive <playbook-slug>` work on playbooks (set/clear `archived_at`). Past runs are independent and unaffected.

**Files:**
- Modify: `internal/app/archive.go`, `internal/app/archive_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestCmdArchivePlaybook(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	wd := t.TempDir()
	if err := flowdb.UpsertPlaybook(db, &flowdb.Playbook{Slug: "p", Name: "P", WorkDir: wd}); err != nil {
		t.Fatal(err)
	}
	if rc := cmdArchive([]string{"p"}); rc != 0 {
		t.Errorf("archive rc=%d", rc)
	}
	pb, err := flowdb.GetPlaybook(db, "p")
	if err != nil {
		t.Fatal(err)
	}
	if !pb.ArchivedAt.Valid {
		t.Errorf("ArchivedAt should be set")
	}
	if rc := cmdUnarchive([]string{"p"}); rc != 0 {
		t.Errorf("unarchive rc=%d", rc)
	}
	pb, _ = flowdb.GetPlaybook(db, "p")
	if pb.ArchivedAt.Valid {
		t.Errorf("ArchivedAt should be cleared")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Expected: FAIL.

- [ ] **Step 3: Add playbook handling to cmdArchive and cmdUnarchive**

Read current code; add playbook lookup as third case (after task and project):

```go
// In cmdArchive, after task + project lookups fail:
pb, perr := flowdb.GetPlaybook(db, ref)
if perr == nil {
    now := flowdb.NowISO()
    if _, err := db.Exec(`UPDATE playbooks SET archived_at = ?, updated_at = ? WHERE slug = ?`, now, now, pb.Slug); err != nil {
        fmt.Fprintf(os.Stderr, "error: %v\n", err)
        return 1
    }
    fmt.Printf("Archived playbook %s\n", pb.Slug)
    return 0
}
```

Same pattern for `cmdUnarchive` setting `archived_at = NULL`.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/app/ -run TestCmdArchivePlaybook -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/archive.go internal/app/archive_test.go
git commit -m "feat: archive/unarchive handle playbook refs

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Phase 4: Run mechanism

### Task 14: Run slug generator with collision cascade

**Goal:** A pure helper that produces the run slug `<playbook>--YYYY-MM-DD-HH-MM`, with seconds appended on minute collision and `-N` on second collision.

**Files:**
- New: `internal/app/run.go` (start), `internal/app/run_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/app/run_test.go`:

```go
package app

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"flow/internal/flowdb"
)

func TestRunSlugBasic(t *testing.T) {
	db := openTempDB(t)
	now := time.Date(2026, 4, 30, 10, 30, 45, 0, time.UTC)
	got, err := generateRunSlug(db, "triage-cs", now)
	if err != nil {
		t.Fatal(err)
	}
	want := "triage-cs--2026-04-30-10-30"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRunSlugMinuteCollision(t *testing.T) {
	db := openTempDB(t)
	wd := t.TempDir()
	if err := flowdb.UpsertPlaybook(db, &flowdb.Playbook{Slug: "p", Name: "P", WorkDir: wd}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 4, 30, 10, 30, 45, 0, time.UTC)

	// First slug should be minute-only.
	first, _ := generateRunSlug(db, "p", now)
	insertRunTask(t, db, first, "p", wd)

	// Second invocation in the same minute should cascade to seconds.
	second, err := generateRunSlug(db, "p", now)
	if err != nil {
		t.Fatal(err)
	}
	want := "p--2026-04-30-10-30-45"
	if second != want {
		t.Errorf("got %q, want %q", second, want)
	}
}

func TestRunSlugSecondCollision(t *testing.T) {
	db := openTempDB(t)
	wd := t.TempDir()
	if err := flowdb.UpsertPlaybook(db, &flowdb.Playbook{Slug: "p", Name: "P", WorkDir: wd}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 4, 30, 10, 30, 45, 0, time.UTC)
	insertRunTask(t, db, "p--2026-04-30-10-30", "p", wd)
	insertRunTask(t, db, "p--2026-04-30-10-30-45", "p", wd)
	got, err := generateRunSlug(db, "p", now)
	if err != nil {
		t.Fatal(err)
	}
	want := "p--2026-04-30-10-30-45-2"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func insertRunTask(t *testing.T, db *sql.DB, slug, pbSlug, wd string) {
	t.Helper()
	now := flowdb.NowISO()
	_, err := db.Exec(
		`INSERT INTO tasks (slug, name, status, kind, playbook_slug, priority, work_dir, created_at, updated_at)
		 VALUES (?, ?, 'backlog', 'playbook_run', ?, 'medium', ?, ?, ?)`,
		slug, slug, pbSlug, wd, now, now,
	)
	if err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/app/ -run TestRunSlug -v`
Expected: FAIL — `generateRunSlug` undefined.

- [ ] **Step 3: Implement generateRunSlug**

Create `internal/app/run.go`:

```go
package app

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// generateRunSlug computes the unique slug for a new playbook run.
//
// Cascade:
//
//  1. <pb>--YYYY-MM-DD-HH-MM
//  2. <pb>--YYYY-MM-DD-HH-MM-SS  (on minute collision)
//  3. <pb>--YYYY-MM-DD-HH-MM-SS-N (N starts at 2; on second collision)
//
// Existence is determined by SELECT slug FROM tasks WHERE slug = ?.
func generateRunSlug(db *sql.DB, playbookSlug string, t time.Time) (string, error) {
	t = t.UTC()
	minute := fmt.Sprintf("%s--%04d-%02d-%02d-%02d-%02d",
		playbookSlug, t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute())
	if !runSlugExists(db, minute) {
		return minute, nil
	}
	second := fmt.Sprintf("%s-%02d", minute, t.Second())
	if !runSlugExists(db, second) {
		return second, nil
	}
	for n := 2; n < 1000; n++ {
		candidate := fmt.Sprintf("%s-%d", second, n)
		if !runSlugExists(db, candidate) {
			return candidate, nil
		}
	}
	return "", errors.New("could not generate unique run slug after 1000 attempts")
}

// runSlugExists returns true iff a tasks row with the given slug exists,
// regardless of kind. We check across all tasks (not just kind=playbook_run)
// because the task slug is a primary key.
func runSlugExists(db *sql.DB, slug string) bool {
	var got string
	err := db.QueryRow(`SELECT slug FROM tasks WHERE slug = ?`, slug).Scan(&got)
	return err == nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/app/ -run TestRunSlug -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/run.go internal/app/run_test.go
git commit -m "feat: generateRunSlug with cascading collision precision

Default: <playbook>--YYYY-MM-DD-HH-MM
Minute collision: append seconds.
Second collision: append -N starting at 2.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 15: flow run playbook command

**Goal:** `flow run playbook <slug>` — creates a run-task, snapshots the playbook brief into it, then invokes the existing `cmdDo` logic.

**Files:**
- Modify: `internal/app/run.go`, `internal/app/run_test.go`, `internal/app/app.go`, `internal/app/do.go`

- [ ] **Step 1: Write the failing test**

```go
func TestCmdRunPlaybookCreatesRunTask(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"playbook", "Triage", "--slug", "tri", "--work-dir", wd}); rc != 0 {
		t.Fatal()
	}

	// Mock iterm.Runner so we don't actually spawn.
	prevRunner := iterm.Runner
	defer func() { iterm.Runner = prevRunner }()
	var captured iterm.SpawnReq
	iterm.Runner = func(req iterm.SpawnReq) error {
		captured = req
		return nil
	}

	if rc := cmdRun([]string{"playbook", "tri"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}

	// A new task with kind=playbook_run should exist.
	db := openFlowDB(t)
	rows, err := db.Query(`SELECT slug FROM tasks WHERE kind='playbook_run' AND playbook_slug='tri'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var runSlug string
	count := 0
	for rows.Next() {
		count++
		if err := rows.Scan(&runSlug); err != nil {
			t.Fatal(err)
		}
	}
	if count != 1 {
		t.Fatalf("expected 1 run task, got %d", count)
	}
	if !strings.HasPrefix(runSlug, "tri--") {
		t.Errorf("expected slug prefix 'tri--', got %q", runSlug)
	}

	// Brief should be a copy of playbook brief.md.
	root, _ := flowRootDir()
	pbBriefPath := filepath.Join(root, "playbooks", "tri", "brief.md")
	runBriefPath := filepath.Join(root, "tasks", runSlug, "brief.md")
	pbBrief, _ := os.ReadFile(pbBriefPath)
	runBrief, err := os.ReadFile(runBriefPath)
	if err != nil {
		t.Errorf("run brief.md missing: %v", err)
	}
	if string(pbBrief) != string(runBrief) {
		t.Errorf("run brief should be verbatim copy of playbook brief")
	}

	// iTerm should have been called with a 'claude' command (not flowde).
	if !strings.HasPrefix(captured.Command, "claude --session-id ") {
		t.Errorf("expected claude session-id command, got %q", captured.Command)
	}
}

func TestCmdRunPlaybookSnapshotIsolation(t *testing.T) {
	root := setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"playbook", "P", "--slug", "p", "--work-dir", wd}); rc != 0 {
		t.Fatal()
	}
	pbBriefPath := filepath.Join(root, "playbooks", "p", "brief.md")
	mustWrite(t, pbBriefPath, "ORIGINAL")

	prevRunner := iterm.Runner
	defer func() { iterm.Runner = prevRunner }()
	iterm.Runner = func(req iterm.SpawnReq) error { return nil }

	if rc := cmdRun([]string{"playbook", "p"}); rc != 0 {
		t.Fatal()
	}

	// Find the run slug
	db := openFlowDB(t)
	var runSlug string
	if err := db.QueryRow(`SELECT slug FROM tasks WHERE kind='playbook_run' AND playbook_slug='p'`).Scan(&runSlug); err != nil {
		t.Fatal(err)
	}

	// Now mutate the playbook brief.
	mustWrite(t, pbBriefPath, "MUTATED")

	// Run brief should still be ORIGINAL.
	runBrief, _ := os.ReadFile(filepath.Join(root, "tasks", runSlug, "brief.md"))
	if string(runBrief) != "ORIGINAL" {
		t.Errorf("snapshot leaked: got %q, want ORIGINAL", string(runBrief))
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/app/ -run TestCmdRunPlaybook -v`
Expected: FAIL — `cmdRun` undefined.

- [ ] **Step 3: Implement cmdRun**

Append to `internal/app/run.go`:

```go
// cmdRun handles `flow run <subcommand>`. Currently only `run playbook <slug>`
// is supported.
func cmdRun(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: run requires a subcommand (playbook)")
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "playbook":
		return cmdRunPlaybook(rest)
	default:
		fmt.Fprintf(os.Stderr, "error: unknown run subcommand %q\n", sub)
		return 2
	}
}

func cmdRunPlaybook(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: run playbook requires a slug")
		return 2
	}
	slug := args[0]
	fs := flagSet("run playbook")
	dangerSkip := fs.Bool("dangerously-skip-permissions", false, "pass --dangerously-skip-permissions through to claude")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := openConcurrentDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	pb, err := ResolvePlaybook(db, slug, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	root, err := flowRootDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	pbBriefPath := filepath.Join(root, "playbooks", pb.Slug, "brief.md")
	pbBriefBytes, err := os.ReadFile(pbBriefPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: read playbook brief %s: %v\n", pbBriefPath, err)
		return 1
	}

	runSlug, err := generateRunSlug(db, pb.Slug, time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// Insert the run-task row.
	now := flowdb.NowISO()
	_, err = db.Exec(
		`INSERT INTO tasks (slug, name, project_slug, status, kind, playbook_slug, priority, work_dir, created_at, updated_at)
		 VALUES (?, ?, ?, 'backlog', 'playbook_run', ?, 'medium', ?, ?, ?)`,
		runSlug,
		fmt.Sprintf("%s run %s", pb.Slug, runSlug),
		pb.ProjectSlug,
		pb.Slug,
		pb.WorkDir,
		now, now,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: insert run task: %v\n", err)
		return 1
	}

	// Materialize tasks/<run-slug>/ and snapshot brief.md.
	runDir := filepath.Join(root, "tasks", runSlug)
	if err := os.MkdirAll(filepath.Join(runDir, "updates"), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "error: mkdir %s: %v\n", runDir, err)
		return 1
	}
	if err := os.WriteFile(filepath.Join(runDir, "brief.md"), pbBriefBytes, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "error: write run brief.md: %v\n", err)
		return 1
	}

	// Delegate to cmdDo to spawn the session.
	doArgs := []string{runSlug}
	if *dangerSkip {
		doArgs = append(doArgs, "--dangerously-skip-permissions")
	}
	return cmdDo(doArgs)
}
```

(`flowRootDir` may be named `flowRoot` — check `init.go`.)

- [ ] **Step 4: Wire cmdRun into the dispatcher**

In `internal/app/app.go`, find the `Run` function's switch on `args[0]`. Add:

```go
case "run":
    return cmdRun(args[1:])
```

Update `printUsage` to mention the new command.

- [ ] **Step 5: Run tests**

Run: `go test ./internal/app/ -run TestCmdRunPlaybook -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/app/run.go internal/app/run_test.go internal/app/app.go
git commit -m "$(cat <<'EOF'
feat: flow run playbook <slug>

Creates a kind=playbook_run task with auto-generated run slug, snapshots
the playbook's brief.md into the run-task's brief.md (verbatim, frozen),
then delegates to cmdDo to spawn an iTerm tab.

Snapshot semantics: editing the playbook's brief later does NOT change
past run briefs. Each run is reproducible.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 16: Bootstrap prompt variant for playbook runs

**Goal:** `buildBootstrapPrompt` branches on `kind`. For `kind='playbook_run'`, emit the playbook-aware prompt.

**Files:**
- Modify: `internal/app/do.go`, `internal/app/do_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/app/do_test.go`:

```go
func TestBuildBootstrapPromptForPlaybookRun(t *testing.T) {
	got := buildBootstrapPromptForKind("p--2026-04-30-10-30", "playbook_run", "p")
	if !strings.Contains(got, "playbook `p`") {
		t.Errorf("expected playbook reference, got:\n%s", got)
	}
	if !strings.Contains(got, "flow show playbook p") {
		t.Errorf("expected flow show playbook command, got:\n%s", got)
	}
	if !strings.Contains(got, "snapshotted from the playbook") {
		t.Errorf("expected snapshot framing, got:\n%s", got)
	}
}

func TestBuildBootstrapPromptForRegularTask(t *testing.T) {
	got := buildBootstrapPromptForKind("foo", "regular", "")
	if strings.Contains(got, "playbook") {
		t.Errorf("regular task prompt shouldn't mention playbook:\n%s", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Expected: FAIL — `buildBootstrapPromptForKind` undefined.

- [ ] **Step 3: Refactor buildBootstrapPrompt to be kind-aware**

In `internal/app/do.go`, rename the existing `buildBootstrapPrompt(slug)` and add a kind-aware variant:

```go
func buildBootstrapPromptForKind(slug, kind, playbookSlug string) string {
	if kind == "playbook_run" {
		return buildPlaybookRunBootstrapPrompt(slug, playbookSlug)
	}
	return buildTaskBootstrapPrompt(slug)
}

func buildTaskBootstrapPrompt(slug string) string {
	return fmt.Sprintf(
		"You are the execution session for flow task %s. Do ALL of the following in order before touching code:\n"+
			"1. Invoke the flow skill via the Skill tool. This loads the operating manual that governs how this session works: workflows, bootstrap contract, KB discipline, and scope-creep detection.\n"+
			"2. Run: flow show task. Read the file at the brief: path AND every file listed under updates:. Files listed under other: are sidecar references — load on demand when relevant, not eagerly.\n"+
			"3. If a project is listed on the task, run: flow show project <that-project-slug>. Read its brief AND every file under updates:. Files under other: are on-demand references.\n"+
			"4. Read CLAUDE.md in your work_dir and any nested CLAUDE.md files under subdirectories you will modify. These override any assumption from the brief.\n"+
			"5. Only then begin work. If any brief section is blank or unclear, ASK — do not infer.",
		slug,
	)
}

func buildPlaybookRunBootstrapPrompt(runSlug, playbookSlug string) string {
	return fmt.Sprintf(
		"You are running playbook `%s` as run `%s`. Do ALL of the following in order before executing anything:\n"+
			"1. Invoke the flow skill via the Skill tool. This loads the operating manual that governs how this session works.\n"+
			"2. Run: flow show playbook %s. This shows the playbook's definition and recent runs — context only, not your instructions. Note any files listed under other: — they're sidecar references (research, decision trees, etc.) you can Read on demand if relevant; do not eagerly load them.\n"+
			"3. Run: flow show task. Read the file at the brief: path AND every file listed under updates:. Files under other: are references for THIS run; load on demand when relevant. The brief is your authoritative instructions for this run — it was snapshotted from the playbook at the moment this run started. Execute against this, not the live playbook brief.\n"+
			"4. If a project is listed on the task, run: flow show project <that-project-slug>. Read its brief and every file under updates:. Files under other: are on-demand references.\n"+
			"5. Read CLAUDE.md in your work_dir.\n"+
			"6. Only then begin executing your brief.",
		playbookSlug, runSlug, playbookSlug,
	)
}

// Backwards-compat shim — keep callers that pass only the slug working.
// Used by tests; the real cmdDo path now passes kind/playbookSlug.
func buildBootstrapPrompt(slug string) string {
	return buildTaskBootstrapPrompt(slug)
}
```

- [ ] **Step 4: Update cmdDo to call the kind-aware variant**

In `cmdDo`, find where `buildBootstrapPrompt(task.Slug)` is called. Replace with:

```go
playbookSlug := ""
if task.PlaybookSlug.Valid {
    playbookSlug = task.PlaybookSlug.String
}
prompt := buildBootstrapPromptForKind(task.Slug, task.Kind, playbookSlug)
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/app/ -run TestBuildBootstrapPrompt -v`
Expected: PASS.

Run full suite:
Run: `go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/app/do.go internal/app/do_test.go
git commit -m "$(cat <<'EOF'
feat: bootstrap prompt branches on task kind

Regular tasks get the existing 5-step prompt. Playbook runs get a 6-step
prompt that adds 'flow show playbook <slug>' as a context-load step and
explicitly tells the session its brief is a snapshot — execute against
the snapshot, not the live playbook brief.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 17: Default kind=regular filter on flow list tasks

**Goal:** `flow list tasks` (no flags) excludes `kind='playbook_run'` so playbook runs don't pollute the personal-task view. Add `--kind` flag override.

**Files:**
- Modify: `internal/app/list.go`, `internal/app/list_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestListTasksDefaultExcludesPlaybookRuns(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	wd := t.TempDir()

	if err := flowdb.UpsertPlaybook(db, &flowdb.Playbook{Slug: "pb", Name: "PB", WorkDir: wd}); err != nil {
		t.Fatal(err)
	}
	insertTask(t, db, "regular-1", "Regular 1", "in-progress", "high", wd, nil)
	now := flowdb.NowISO()
	if _, err := db.Exec(
		`INSERT INTO tasks (slug, name, status, kind, playbook_slug, priority, work_dir, created_at, updated_at)
		 VALUES ('pb--2026-04-30-10-30', 'pb run', 'in-progress', 'playbook_run', 'pb', 'medium', ?, ?, ?)`,
		wd, now, now,
	); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if rc := cmdList([]string{"tasks"}); rc != 0 {
			t.Fatal()
		}
	})
	if !strings.Contains(out, "regular-1") {
		t.Errorf("regular task missing:\n%s", out)
	}
	if strings.Contains(out, "pb--2026-04-30-10-30") {
		t.Errorf("playbook run should be hidden by default:\n%s", out)
	}
}

func TestListTasksKindOverride(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	wd := t.TempDir()
	if err := flowdb.UpsertPlaybook(db, &flowdb.Playbook{Slug: "pb", Name: "PB", WorkDir: wd}); err != nil {
		t.Fatal(err)
	}
	now := flowdb.NowISO()
	if _, err := db.Exec(
		`INSERT INTO tasks (slug, name, status, kind, playbook_slug, priority, work_dir, created_at, updated_at)
		 VALUES ('pb--2026-04-30-10-30', 'r', 'in-progress', 'playbook_run', 'pb', 'medium', ?, ?, ?)`,
		wd, now, now,
	); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if rc := cmdList([]string{"tasks", "--kind", "playbook_run"}); rc != 0 {
			t.Fatal()
		}
	})
	if !strings.Contains(out, "pb--2026-04-30-10-30") {
		t.Errorf("--kind playbook_run should show runs:\n%s", out)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Expected: FAIL.

- [ ] **Step 3: Add --kind flag and default filter**

In `cmdListTasks` in `list.go`, add the flag:

```go
kind := fs.String("kind", "regular", "filter by task kind: regular | playbook_run | all")
```

When building the `flowdb.TaskFilter`, set:

```go
if *kind != "all" {
    filter.Kind = *kind
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/app/ -run TestListTasks -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/list.go internal/app/list_test.go
git commit -m "feat: list tasks defaults to kind=regular, --kind to override

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 18: flow list runs

**Goal:** New `flow list runs [<playbook-slug>]` lists all `kind='playbook_run'` tasks, optionally filtered to one playbook.

**Files:**
- Modify: `internal/app/list.go`, `internal/app/list_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestCmdListRuns(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	wd := t.TempDir()
	if err := flowdb.UpsertPlaybook(db, &flowdb.Playbook{Slug: "p1", Name: "P1", WorkDir: wd}); err != nil {
		t.Fatal(err)
	}
	if err := flowdb.UpsertPlaybook(db, &flowdb.Playbook{Slug: "p2", Name: "P2", WorkDir: wd}); err != nil {
		t.Fatal(err)
	}
	now := flowdb.NowISO()
	for _, slug := range []string{"p1--2026-04-30-10-30", "p1--2026-04-30-11-00"} {
		if _, err := db.Exec(
			`INSERT INTO tasks (slug, name, status, kind, playbook_slug, priority, work_dir, created_at, updated_at)
			 VALUES (?, ?, 'in-progress', 'playbook_run', 'p1', 'medium', ?, ?, ?)`,
			slug, slug, wd, now, now,
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(
		`INSERT INTO tasks (slug, name, status, kind, playbook_slug, priority, work_dir, created_at, updated_at)
		 VALUES ('p2--2026-04-30-10-30', 'p2-r', 'done', 'playbook_run', 'p2', 'medium', ?, ?, ?)`,
		wd, now, now,
	); err != nil {
		t.Fatal(err)
	}

	// All runs
	out := captureStdout(t, func() {
		if rc := cmdList([]string{"runs"}); rc != 0 {
			t.Fatal()
		}
	})
	if !strings.Contains(out, "p1--") || !strings.Contains(out, "p2--") {
		t.Errorf("expected all runs:\n%s", out)
	}

	// Filtered by playbook
	out = captureStdout(t, func() {
		if rc := cmdList([]string{"runs", "p1"}); rc != 0 {
			t.Fatal()
		}
	})
	if !strings.Contains(out, "p1--") {
		t.Errorf("expected p1 runs:\n%s", out)
	}
	if strings.Contains(out, "p2--") {
		t.Errorf("p2 runs should be filtered out:\n%s", out)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Expected: FAIL — `runs` not handled by cmdList.

- [ ] **Step 3: Implement cmdListRuns**

Add to `list.go`:

```go
func cmdListRuns(args []string) int {
    fs := flagSet("list runs")
    status := fs.String("status", "", "filter by status: backlog | in-progress | done")
    includeArchived := fs.Bool("include-archived", false, "include archived")
    if err := fs.Parse(args); err != nil {
        return 2
    }
    var playbookSlug string
    rest := fs.Args()
    if len(rest) > 0 {
        playbookSlug = rest[0]
    }

    dbPath, err := flowDBPath()
    if err != nil {
        fmt.Fprintf(os.Stderr, "error: %v\n", err)
        return 1
    }
    db, err := flowdb.OpenDB(dbPath)
    if err != nil {
        fmt.Fprintf(os.Stderr, "error: %v\n", err)
        return 1
    }
    defer db.Close()

    tasks, err := flowdb.ListTasks(db, flowdb.TaskFilter{
        Kind:            "playbook_run",
        PlaybookSlug:    playbookSlug,
        Status:          *status,
        IncludeArchived: *includeArchived,
    })
    if err != nil {
        fmt.Fprintf(os.Stderr, "error: %v\n", err)
        return 1
    }
    for _, t := range tasks {
        fmt.Printf("  [%s] %-50s (%s)\n", statusAbbrev(t.Status), t.Slug, t.PlaybookSlug.String)
    }
    return 0
}
```

Wire into `cmdList` dispatcher:

```go
case "runs":
    return cmdListRuns(rest)
```

Update the dispatch error message: `"error: list requires a subcommand (tasks|projects|playbooks|runs)"`.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/app/ -run TestCmdListRuns -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/list.go internal/app/list_test.go
git commit -m "feat: flow list runs [<playbook>] with status and archived filters

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Phase 5: Skill content updates (playbook awareness)

These tasks edit `internal/app/skill/SKILL.md` content. Skill changes are tested via grep-based content presence assertions in `internal/app/skill_test.go`. After each commit, rebuild (`make build`) so the embedded skill is fresh.

### Task 19: Skill — model section, command reference, and start-the-day

**Goal:** Update §2 (The model), §4 (Commands), §5.1 (Start the day) to include playbooks.

**Files:**
- Modify: `internal/app/skill/SKILL.md`, `internal/app/skill_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/app/skill_test.go` (or create if missing):

```go
func TestSkillMentionsPlaybooks(t *testing.T) {
	got := string(embeddedSkill)
	for _, want := range []string{
		"## 2. The model",
		"**Playbooks**",
		"flow add playbook",
		"flow run playbook",
		"flow list playbooks",
		"flow show playbook",
		"flow list runs",
		"Active playbooks",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("skill missing %q", want)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Expected: FAIL — current skill has none of these.

- [ ] **Step 3: Edit SKILL.md §2**

In `internal/app/skill/SKILL.md`, locate section "## 2. The model" (around line 45-70). After the **Tasks** bullet, add a **Playbooks** bullet:

```markdown
- **Playbooks** are reusable, runnable definitions. A playbook has a
  name, slug, work_dir, optional `project_slug`, and a `brief.md` that
  describes what each invocation should do. Each invocation creates a
  **playbook-run** — a task with `kind=playbook_run` — that has its
  own session, its own snapshotted `brief.md`, and its own
  `updates/`. Editing a playbook's `brief.md` does not affect past
  runs; runs are reproducible.
```

- [ ] **Step 4: Edit SKILL.md §4**

Find the Command reference cheat sheet block in section "## 4. Command reference" and add new commands. Specifically, in the "Create" subsection add:

```
  flow add playbook "<name>" --work-dir <path> [--slug <s>] [--project <slug>] [--mkdir]
```

In a new "Run a playbook" subsection (placed after "Sessions"):

```
Playbook runs
  flow run playbook <slug>                  spawn a fresh run session
  flow list playbook-runs not used; use:
  flow list runs [<playbook-slug>]          list playbook runs
```

Wait — I introduced "list playbook-runs" then dropped it. Use just `flow list runs`. Drop the confusing line:

```
Playbook runs
  flow run playbook <slug>          spawn a fresh run session
  flow list runs [<playbook-slug>]  list playbook runs (filter by playbook optional)
```

In the "Read" subsection, add:

```
  flow show playbook [<ref>]
  flow list playbooks [--project <slug>] [--include-archived]
```

In the "Edit / mutate" subsection, note that `flow edit`, `flow archive`, and `flow unarchive` accept playbook refs.

- [ ] **Step 5: Edit SKILL.md §5.1**

In "### 4.1 Start the day" (note: spec uses §5.1 but skill uses §4.1 numbering — match the actual skill), find the recipe step that summarizes in 4 sections. Add a new section:

```markdown
   - **Active playbooks**: any playbook with a run in the past 7 days.
     Pull from `flow list runs --since 7d` grouped by playbook; show
     playbook slug + most recent run timestamp.
```

(If skill uses §4 not §5 numbering, this whole plan applies to whatever the actual heading is. Use the skill's actual numbers as you find them.)

- [ ] **Step 6: Rebuild and run tests**

```bash
cd /Users/rohit/flow
go build -o flow .
go test ./internal/app/ -run TestSkillMentionsPlaybooks -v
```
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/app/skill/SKILL.md internal/app/skill_test.go
git commit -m "$(cat <<'EOF'
feat(skill): playbook awareness in §2 model, §4 commands, §4.1 start-the-day

- §2 adds Playbooks as the third entity alongside Projects and Tasks
- §4 adds add/show/list/run playbook commands and list runs
- §4.1 adds an 'Active playbooks' subsection to the daily summary

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 20: Skill — §5.5/§5.7/§5.8/§5.9 (notes, done, archive, weekly review)

**Goal:** Update progress notes, mark done, archive, weekly review sections to handle playbooks and runs.

**Files:**
- Modify: `internal/app/skill/SKILL.md`, `internal/app/skill_test.go`

- [ ] **Step 1: Write the failing test**

Append to `TestSkillMentionsPlaybooks`:

```go
for _, want := range []string{
    "Save a progress note",
    "playbooks/<slug>/updates/",
    "Mark done",
    "are never \"done\" — they're archived",
    "flow archive <playbook-slug>",
    "Playbook activity",
} {
    if !strings.Contains(got, want) {
        t.Errorf("skill missing %q", want)
    }
}
```

- [ ] **Step 2: Run to verify failure**

Expected: FAIL.

- [ ] **Step 3: Edit §5.5 (Save a progress note)**

In the existing recipe, add a step distinguishing tasks from playbooks:

```markdown
4. Determine the entity:
   - For a **playbook run**, notes go under
     `~/.flow/tasks/<run-slug>/updates/` (runs are tasks).
   - For a **playbook definition**, notes go under
     `~/.flow/playbooks/<slug>/updates/` for cross-invocation observations.
   - For a regular task, the existing rule applies.
```

- [ ] **Step 4: Edit §5.7 (Mark done)**

Add a clarification:

```markdown
**Run-tasks** (kind=playbook_run) support `flow done <run-slug>` like any task.

**Playbook definitions are never "done" — they're archived.** When a
playbook is no longer in use, run `flow archive <playbook-slug>`. There
is no `flow done playbook` command.
```

- [ ] **Step 5: Edit §5.8 (Archive / cleanup)**

Add a paragraph:

```markdown
**Playbooks:**
- `flow archive <playbook-slug>` hides the playbook from
  `flow list playbooks` but does not affect past runs (they're independent
  task rows). Past runs can be archived independently with
  `flow archive <run-slug>`.
- "Bulk clean up done runs" pattern: `flow list runs --status done`,
  then archive each.
```

- [ ] **Step 6: Edit §5.9 (Weekly review)**

In the digest template, add a section after "Workdir hygiene":

```markdown
## Playbook activity
- <playbook-slug> — N runs this week, most recent <date>
```

In the recipe, add:

```markdown
6. `flow list runs --since monday` — group by playbook slug, count and
   pull each playbook's most-recent run timestamp.
```

- [ ] **Step 7: Rebuild and test**

```bash
go build -o flow .
go test ./internal/app/ -run TestSkillMentionsPlaybooks -v
```
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/app/skill/SKILL.md internal/app/skill_test.go
git commit -m "$(cat <<'EOF'
feat(skill): playbook handling in notes, done, archive, weekly review

- §5.5: notes go to playbooks/<slug>/updates/ for definitions, or
  tasks/<run-slug>/updates/ for runs
- §5.7: playbooks are never done — they're archived
- §5.8: archive playbook hides it but past runs are independent
- §5.9: weekly review surfaces 'Playbook activity' with run counts

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 21: Skill — §5.11/§7/§8 (scope-creep, brief format, anti-patterns)

**Goal:** Update scope-creep guidance to apply to playbook-run sessions, add the playbook brief template to §7, add three playbook anti-patterns to §8.

**Files:**
- Modify: `internal/app/skill/SKILL.md`, `internal/app/skill_test.go`

- [ ] **Step 1: Write the failing test**

```go
for _, want := range []string{
    "playbook-run sessions",
    "Each run does",
    "Signals to watch for",
    "Do not auto-fire `flow run playbook`",
    "snapshot",
    "Do not propose scheduling during playbook intake",
} {
    if !strings.Contains(got, want) {
        t.Errorf("skill missing %q", want)
    }
}
```

- [ ] **Step 2: Run to verify failure**

Expected: FAIL.

- [ ] **Step 3: Edit §5.11 (scope-creep detection)**

Add at the end of the existing section:

```markdown
**Note:** "the bootstrapped task" includes playbook-run tasks. The
triggers and recipe are identical for playbook-run sessions —
edits/debugging that drift outside the playbook's scope warrant the
same prompt.
```

- [ ] **Step 4: Add §7 playbook brief template**

In §7 ("The task brief format") — after the existing task and project templates, add:

```markdown
**Playbook brief template:**

```markdown
# <name>

## What
<one sentence describing what each run does>

## Why
<short paragraph>

## Where
work_dir: <absolute path>

## Each run does
- <step 1>
- <step 2>
- <step 3>

## Out of scope
- <non-goal 1>

## Signals to watch for
- <signal 1>

---
*Run with `flow run playbook <slug>`. Each run gets its own session
and a snapshot of this brief at run time. Editing this file does not
retroactively change past runs.*
```

Notes:
- No "Done when" — playbooks are never done.
- "Each run does" replaces "Done when" as the action-oriented section.
- "Signals to watch for" replaces "Open questions" — playbooks are
  long-running, so the relevant prospective concern is signals to
  notice and respond to, not open questions to resolve.
```

- [ ] **Step 5: Add anti-patterns to §8**

Append three bullets to the §8 list:

```markdown
- **Do not auto-fire `flow run playbook`.** Playbooks are
  manual-trigger only. Even if a user mentions a playbook by name in
  passing, do NOT run it without an explicit verb ("run", "trigger",
  "fire", "start").
- **Do not edit a run-task's `brief.md` to change the playbook's
  behavior for future runs.** That brief is a frozen snapshot. To
  change behavior, edit the playbook's `brief.md` and start a new
  run.
- **Do not propose scheduling during playbook intake.** Scheduled
  invocation is out of scope for v1; playbooks are manual.
```

- [ ] **Step 6: Rebuild and test**

```bash
go build -o flow .
go test ./internal/app/ -run TestSkillMentionsPlaybooks -v
```
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/app/skill/SKILL.md internal/app/skill_test.go
git commit -m "$(cat <<'EOF'
feat(skill): playbook scope-creep, brief template, anti-patterns

- §5.11: scope-creep applies inside playbook-run sessions
- §7: adds playbook brief template (no Done when; Each run does;
  Signals to watch for)
- §8: three playbook anti-patterns (no auto-fire; don't edit run brief
  to change future runs; no scheduling proposals during intake)

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 22: Skill — §5.12 (Add a playbook) and §5.13 (Run a playbook)

**Goal:** Two new skill sections that govern interview-driven playbook intake and explicit playbook invocation.

**Files:**
- Modify: `internal/app/skill/SKILL.md`, `internal/app/skill_test.go`

- [ ] **Step 1: Write the failing test**

```go
for _, want := range []string{
    "### 4.12 Add a playbook",
    "### 4.13 Run a playbook",
    "Each run does",
    "fire the X agent",
} {
    if !strings.Contains(got, want) {
        t.Errorf("skill missing %q", want)
    }
}
```

(Note: section numbering in the skill is "### 4.X" not "§5.X" — adjust to match existing convention.)

- [ ] **Step 2: Run to verify failure**

Expected: FAIL.

- [ ] **Step 3: Add §5.12 (Add a playbook)**

After the existing "### 4.11 Scope-creep detection" section, add:

```markdown
### 4.12 Add a playbook

**Triggers:** "add a playbook", "create a playbook for X",
"track this as a playbook", "this is something I'll re-run".

**The interview is the whole point** (same philosophy as §4.2).

**Sections to ask, ONE AT A TIME, in this order:**

1. **What?** One sentence describing what each run does.
2. **Why?** Why this playbook exists and what value it produces.
3. **Where?** Work_dir for runs (use §6 recipe).
4. **Each run does** — concrete steps every invocation performs. Bullet
   form. Replaces "Done when" from task intake.
5. **Out of scope?** Non-goals. Optional.
6. **Signals to watch for** — observable conditions that should change
   the run's behavior or trigger an escalation. Replaces "Open
   questions" — playbooks have long lifespans so prospective signals
   matter more than open questions.

**Then before calling `flow add playbook`:**

- Suggest 2-3 slug candidates (same pattern as §4.2). Use AskUserQuestion.
- Ask about project attachment (same pattern). Optional — playbooks can
  be floating.
- `--mkdir` if work_dir doesn't exist.

**Draft the brief, show to the user, get "Save it" confirmation.** Then
run `flow add playbook` and overwrite the stub `brief.md` with the full
content. Use the playbook brief template from §7.

After save, use AskUserQuestion to offer:
- "Run it now" → proceed to §4.13
- "Just save the definition for now"
```

- [ ] **Step 4: Add §5.13 (Run a playbook)**

```markdown
### 4.13 Run a playbook

**Triggers — any of these means "run `flow run playbook <slug>`":**
- "run the X playbook" / "trigger X" / "fire the X playbook"
- "fire the X agent" (legacy term users may use)
- "start a run of X" / "kick off X"
- A bare `flow run playbook X` typed as command

**Recipe:**

1. Ask session-mode (Regular vs Skip permissions) via AskUserQuestion —
   reuses the §4.4 pattern. Skip if user already specified.
2. Run: `flow run playbook <slug>` (with `--dangerously-skip-permissions`
   if chosen).
3. The command creates a kind=playbook_run task, snapshots the brief,
   and spawns an iTerm tab. The new tab will boot the flow skill via its
   bootstrap prompt and execute against the snapshotted brief.

**Anti-pattern (per §8):** never auto-fire. Manual trigger only.
```

- [ ] **Step 5: Rebuild and test**

```bash
go build -o flow .
go test ./internal/app/ -run TestSkillMentionsPlaybooks -v
```
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/app/skill/SKILL.md internal/app/skill_test.go
git commit -m "$(cat <<'EOF'
feat(skill): §4.12 Add playbook and §4.13 Run playbook

§4.12 is interview-driven intake (What/Why/Where/Each run does/Out of
scope/Signals to watch for) — six sections, no 'Done when'. After save,
offer to run immediately or save-only.

§4.13 covers explicit invocation. Triggers include 'fire the X agent'
(legacy term). Reuses §4.4 session-mode prompt.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 23: Skill — §9 bootstrap contract for playbook runs and aux files

**Goal:** Update §9 to handle playbook-run sessions and add the lazy-load instruction for `other:` files.

**Files:**
- Modify: `internal/app/skill/SKILL.md`, `internal/app/skill_test.go`

- [ ] **Step 1: Write the failing test**

```go
for _, want := range []string{
    "kind: playbook_run",
    "snapshot taken when this run started",
    "Files listed under `other:`",
    "load on demand",
    "lazy-load principle",
} {
    if !strings.Contains(got, want) {
        t.Errorf("skill missing %q", want)
    }
}
```

- [ ] **Step 2: Run to verify failure**

Expected: FAIL.

- [ ] **Step 3: Edit §9 bootstrap contract**

Find "## 9. The execution-session bootstrap contract" and the steps. After the existing step 2 ("Load the task context"), add:

```markdown
**If `flow show task` indicates `kind: playbook_run`:** also run
`flow show playbook <playbook-slug>` first (for context: the playbook's
intent and recent runs). Note any files under its `other:` section —
they're sidecar references you can load on demand. Then read your task's
`brief.md` — that's the snapshot taken when this run started, and it's
your authoritative instructions. The playbook's live `brief.md` may
have evolved since; you don't need to re-read it.

**Files listed under `other:`** in any `flow show` output (task,
project, or playbook) are sidecar references — research notes, decision
trees, design docs, etc. dropped into the entity's directory. Do **not**
read them eagerly. Read them on demand when something in the brief, in
user input, or in the work makes them relevant. This matches the
lazy-load principle for KB files (§5.10).
```

- [ ] **Step 4: Edit §5.10 to mention aux files**

Append a paragraph:

```markdown
**Auxiliary files in entity directories** (any `.md` files in
`tasks/<slug>/`, `projects/<slug>/`, or `playbooks/<slug>/` other than
`brief.md` and the contents of `updates/`) are surfaced by `flow show`
under an `other:` section. Apply the same lazy-load discipline: load
them on demand when relevant to the work, not preemptively.
```

- [ ] **Step 5: Rebuild and test**

```bash
go build -o flow .
go test ./internal/app/ -run TestSkillMentionsPlaybooks -v
```
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/app/skill/SKILL.md internal/app/skill_test.go
git commit -m "$(cat <<'EOF'
feat(skill): §9 bootstrap for playbook runs and aux file lazy-load

- §9: branches on kind=playbook_run; loads playbook context, reads
  snapshot brief, treats live playbook brief as informational only
- §9 + §5.10: aux files in entity dirs (other:) are on-demand
  references, not eager loads — same discipline as KB files

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 6: Skill — Intake-minimal

### Task 24: Rewrite §5.2 task intake interview

**Goal:** Capture name + slug + work_dir + priority first, then offer to defer the rest. Update brief template with thin-brief variant. Update §9 to detect deferred sections at task start.

**Files:**
- Modify: `internal/app/skill/SKILL.md`, `internal/app/skill_test.go`

- [ ] **Step 1: Write the failing test**

```go
for _, want := range []string{
    "Required (always asked)",
    "Optional (offered, can be deferred)",
    "Detail now",
    "Defer until you start the task",
    "*Deferred — fill in at task start.*",
    "deferred-section prompt",
} {
    if !strings.Contains(got, want) {
        t.Errorf("skill missing %q", want)
    }
}
```

- [ ] **Step 2: Run to verify failure**

Expected: FAIL.

- [ ] **Step 3: Rewrite §5.2 (skill heading: ### 4.2 Add a task)**

Replace the existing section's "Sections to ask" block with:

```markdown
**Required (always asked, in this order):**

1. **Name** — one-sentence description of the work.
2. **Slug** — auto-suggested from name; user picks one via
   AskUserQuestion (or types a custom slug).
3. **Work_dir** — full §6 recipe.
4. **Priority** — High / Medium / Low via AskUserQuestion.

**Optional (offered after the four required fields):**

After the required fields, use AskUserQuestion:

> "Want to capture more detail now (Why, Done when, Out of scope,
> Open questions), or defer until you start the task?"
> - Detail now
> - Defer until you start the task

**Detail now:** run the rest of the original §4.2 sections (Why, Done
when, Out of scope, Open questions), then draft the full brief.

**Defer:** save the task with a thin brief. The bootstrap-time prompt
(§9) will walk the user through the missing sections when they `flow do`
the task.

**Confirmation flow** (both paths):
- Show the drafted brief.
- AskUserQuestion: "Brief — Save it / Revise"
- Save → `flow add task ...` → overwrite stub brief with content.
```

- [ ] **Step 4: Update §7 with thin-brief template**

In §7, after the existing task brief template, add:

```markdown
**Thin task brief (intake-minimal):**

```markdown
# <name>

## What
<one sentence from intake>

## Why
*Deferred — fill in at task start.*

## Where
work_dir: <path>

## Done when
*Deferred — fill in at task start.*

## Out of scope
*Deferred*

## Open questions
*Deferred*

---
*This brief is thin. Before you start substantive work, the bootstrap
session will prompt you to fill in the deferred sections.*
```

A section is "deferred" if its body is the literal string
`*Deferred — fill in at task start.*` or `*Deferred*`. The bootstrap
session detects this and offers the user a deferred-section prompt
(§9).
```

- [ ] **Step 5: Update §9 to detect deferred sections**

In §9, after the bootstrap reading step, add:

```markdown
**Deferred-section prompt:** if any section body in your brief is the
literal `*Deferred — fill in at task start.*` or `*Deferred*`, pause
before doing any work and offer the user (via AskUserQuestion):

- **Fill in now** — run a mini-§4.2 interview for just the missing
  sections (Why, Done when, Out of scope, Open questions). Save the
  filled-in brief by overwriting the existing brief.md.
- **Skip — proceed** — accept that scope is implicit. Reasonable for
  small/known tasks.

This shifts the intake burden from intake-time to task-start-time,
where the user has more context.
```

- [ ] **Step 6: Rebuild and test**

```bash
go build -o flow .
go test ./internal/app/ -run TestSkillMentionsPlaybooks -v
```
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/app/skill/SKILL.md internal/app/skill_test.go
git commit -m "$(cat <<'EOF'
feat(skill): intake-minimal — required first, detail or defer

§4.2 task intake now captures only Name + Slug + Work_dir + Priority by
default. The remaining four sections (Why / Done when / Out of scope /
Open questions) are deferred unless the user opts in.

§7 adds a thin-brief template with '*Deferred*' markers.

§9 bootstrap contract detects deferred sections and offers the user a
mini-interview at task start — interview burden shifts to where the
user has more context.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 7: Skill — substantive-unrelated-work check

### Task 25: Add §5.14 to skill

**Goal:** New skill section that fires the three-choice prompt every turn in dispatch sessions when substantive-unrelated-work signals appear.

**Files:**
- Modify: `internal/app/skill/SKILL.md`, `internal/app/skill_test.go`

- [ ] **Step 1: Write the failing test**

```go
for _, want := range []string{
    "### 4.14 Substantive-unrelated-work check",
    "ongoing check, not one-shot",
    "superpowers:brainstorming",
    "Re-evaluate on every turn",
    "process-skill invocation",
} {
    if !strings.Contains(got, want) {
        t.Errorf("skill missing %q", want)
    }
}
```

- [ ] **Step 2: Run to verify failure**

Expected: FAIL.

- [ ] **Step 3: Add §5.14**

Append to §5 (after §5.13 Run a playbook):

```markdown
### 4.14 Substantive-unrelated-work check (passive, ongoing)

This is a **passive workflow** that runs alongside every other workflow.
It fires when substantive work emerges that doesn't belong to the
current task binding.

**Triggers (any one is enough):**

- In a **dispatch session** (FLOW_TASK unset):
  - You've been in active design / brainstorming / debugging
    discussion for ≥ 2 turns about a concrete topic, OR
  - You've made any Edit/Write tool calls, OR
  - You've invoked a process skill (`superpowers:brainstorming`,
    `superpowers:writing-plans`, `superpowers:executing-plans`,
    `superpowers:systematic-debugging`,
    `superpowers:test-driven-development`) — a process-skill invocation
    is itself a substantive-work signal.
- In a **bound session** (FLOW_TASK set): same triggers as §5.11
  (work moved off the bootstrapped task's scope).

**NOT a trigger:**

- One-off question answered in a single turn.
- Reading files / running queries to inform an answer.
- The very first message after session start (you don't yet know if
  this is one-off or substantive).

**Recipe:**

1. Pause current work.
2. Run `flow list tasks --status in-progress` and
   `flow list tasks --status backlog --priority high` to see candidates.
3. Use AskUserQuestion to offer three options:
   - **Create a new flow task** for this work (run §5.2 minimal intake,
     then optionally `flow do <new-slug>`).
   - **Switch to an existing task** (list candidates as options;
     on selection, spawn `flow do <slug>`).
   - **Proceed ad-hoc** (user accepts no resumability, no context
     accumulation).

**Process-skill ordering:** when a process skill triggers this check,
load the skill first (so the user sees the right tool engage), then
**before** taking the skill's first concrete action, run the check.
If the user picks "create new task" or "switch to existing task," the
process skill resumes inside the new session, not this one.

**Important: this is an ongoing check, not one-shot.** Re-evaluate the
triggers each turn — especially when transitioning into design /
implementation / debugging work. The SessionStart hook gets you the
first check; you are responsible for every subsequent check.
```

- [ ] **Step 4: Update §11 ("When in doubt") to reference §5.14**

Find §11 and add:

```markdown
In a dispatch session, also re-check §5.14 (substantive-unrelated-work)
on every turn. The skill is responsible for ongoing detection; the
SessionStart hook is only a one-shot trigger.
```

- [ ] **Step 5: Rebuild and test**

```bash
go build -o flow .
go test ./internal/app/ -run TestSkillMentionsPlaybooks -v
```
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/app/skill/SKILL.md internal/app/skill_test.go
git commit -m "$(cat <<'EOF'
feat(skill): §4.14 substantive-unrelated-work check (ongoing)

Replaces the one-shot SessionStart instruction. The skill itself now
re-evaluates triggers every turn:

- Dispatch session + ≥2-turn design/debug/brainstorm discussion
- Dispatch session + any Edit/Write tool call
- Dispatch session + process-skill invocation (brainstorming,
  writing-plans, executing-plans, debugging, TDD)
- Bound session + scope drift (delegates to §4.11)

When triggered, offer the three-choice prompt: create new task, switch
to existing, or proceed ad-hoc. For process skills: load the skill
first, run the check before its first concrete action.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 26: Update flow hook session-start output

**Goal:** Replace the verbose three-choice instruction with a one-liner pointing at §5.14.

**Files:**
- Modify: `internal/app/hook.go`, `internal/app/hook_test.go`

- [ ] **Step 1: Write the failing test**

In `internal/app/hook_test.go`, find the existing test that captures the hook output. Update assertions:

```go
func TestHookSessionStartPointsAtSection(t *testing.T) {
	out := captureStdout(t, func() {
		if rc := cmdHookSessionStart(nil); rc != 0 {
			t.Fatal()
		}
	})
	if !strings.Contains(out, "§4.14") && !strings.Contains(out, "5.14") {
		t.Errorf("hook output should reference §4.14 or §5.14, got:\n%s", out)
	}
	if !strings.Contains(out, "ongoing, not one-shot") {
		t.Errorf("hook output should clarify ongoing nature, got:\n%s", out)
	}
}
```

(Adjust function name `cmdHookSessionStart` to whatever the hook handler is actually named — read `hook.go` to confirm.)

- [ ] **Step 2: Run to verify failure**

Expected: FAIL.

- [ ] **Step 3: Update hook.go output**

Read `internal/app/hook.go`. Find where it emits the verbose prompt about substantive work. Replace with:

```go
fmt.Println("This session is not bound to any flow task (FLOW_TASK is unset).")
fmt.Println("When substantive work emerges, run §4.14 of the flow skill to offer the user a flow task.")
fmt.Println("The check is ongoing, not one-shot — re-evaluate on every turn.")
```

(The bound-session branch still injects the existing brief reload. Only the unbound branch's text changes.)

- [ ] **Step 4: Run tests**

Run: `go test ./internal/app/ -run TestHookSessionStart -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/hook.go internal/app/hook_test.go
git commit -m "$(cat <<'EOF'
feat: hook session-start delegates to skill §4.14 (one-liner)

The hook used to embed the full three-choice prompt inline. With §4.14
now defining the check as ongoing in the skill itself, the hook becomes
a thin pointer: 'see §4.14, the check is ongoing.'

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 8: Verification

### Task 27: Full end-to-end verification

**Goal:** Confirm everything works together. Run the full suite, manually exercise the new commands, install the rebuilt skill, and verify a fresh session bootstraps correctly.

- [ ] **Step 1: Run full test suite**

```bash
cd /Users/rohit/flow
go test ./... -v
```
Expected: All tests PASS. No flaky failures, no SQLite lock errors.

- [ ] **Step 2: Build and install**

```bash
make clean
make install
```
Expected: `flow` binary builds, skill installed to `~/.claude/skills/flow/SKILL.md` (will overwrite the existing one with the new content).

- [ ] **Step 3: Manually exercise commands against a temp FLOW_ROOT**

```bash
export FLOW_ROOT=/tmp/flow-manual-$$
flow init
flow add project "Manual Test" --slug mt --work-dir /tmp
flow add playbook "Manual Playbook" --slug mp --project mt --work-dir /tmp
flow list playbooks
flow show playbook mp
flow list runs
# (don't actually fire flow run playbook — would spawn iTerm)
flow add task "Test Task" --slug tt --work-dir /tmp --priority high
flow list tasks
flow show task tt
unset FLOW_ROOT
```

Verify each output looks correct. Check:
- `flow show playbook mp` shows brief path, runs (none), kb, other (none)
- `flow list tasks` shows tt but NOT any playbook runs
- `flow list runs` shows nothing yet
- All output is readable

- [ ] **Step 4: Drop sidecar files and re-show**

```bash
export FLOW_ROOT=/tmp/flow-manual-$$
echo "test research" > $FLOW_ROOT/playbooks/mp/research.md
echo "decision tree" > $FLOW_ROOT/playbooks/mp/decisions.md
flow show playbook mp
```

Expected: `other:` section lists `research.md` and `decisions.md`.

- [ ] **Step 5: Smoke-test bootstrap prompt content**

```bash
go run -tags test_helpers ./internal/app/ # if there's a quick way; else:
```

Or write a tiny verification:

```bash
flow show task tt > /tmp/show.txt
grep "other:" /tmp/show.txt
grep "brief:" /tmp/show.txt
```

Expected: both lines present.

- [ ] **Step 6: Verify skill content via flow skill update**

```bash
flow skill update --force
diff /Users/rohit/flow/internal/app/skill/SKILL.md ~/.claude/skills/flow/SKILL.md
```
Expected: no diff.

- [ ] **Step 7: Cleanup**

```bash
rm -rf /tmp/flow-manual-$$
```

- [ ] **Step 8: Update real installation**

If everything looks good, the user can `flow skill update` (or `make install` again) to refresh their actual `~/.claude/skills/flow/SKILL.md`.

- [ ] **Step 9: Final commit (if any small fixes were made during verification)**

If verification surfaced any fixes:

```bash
git add -A
git commit -m "fix: minor verification fixes (typos / ordering)

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

If no fixes needed, no commit. The plan is complete.

---

## Self-Review

**Spec coverage:**

- §1 Playbook (schema, FS, commands, run mechanism, bootstrap variant) → Tasks 5–18
- §2 Intake-minimal → Task 24
- §3 Substantive-unrelated-work check → Tasks 25–26
- §4 Remove flowde → Task 1
- §5 Auxiliary markdown files → Tasks 2, 3, 4 (and aux integration in show playbook in Task 11)
- Skill comprehensive updates → Tasks 19–25

All sections covered.

**Placeholder scan:** No "TBD" / "TODO" / "appropriate error handling" / "similar to Task N" patterns. All test code and implementation code is concrete.

**Type consistency:**

- `Playbook` struct fields used identically across Tasks 7, 9–13, 15: Slug, Name, ProjectSlug, WorkDir, CreatedAt, UpdatedAt, ArchivedAt
- `Task.Kind` (string) and `Task.PlaybookSlug` (sql.NullString) used identically across Tasks 5–7, 14–18
- `enumerateAuxFiles(dir string) ([]string, error)` signature consistent across Tasks 2, 3, 4, 11
- `generateRunSlug(db, playbookSlug, t)` signature consistent in Task 14, 15
- `buildBootstrapPromptForKind(slug, kind, playbookSlug)` consistent in Task 16
- All commit message footers use the standard `Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>`

**Notes:**
- Skill section numbering (§4.X vs §5.X): the spec uses §5.X but the actual SKILL.md file in the repo uses §4.X. The plan matches the actual file convention; tests assert against `### 4.X`. Verify by reading SKILL.md before editing.
- `flowRootDir()` helper name is unverified — the codebase may name it `flowRoot()` (used in `init.go`). Read before using; rename consistently.
- `cmdHookSessionStart` function name is unverified — read `hook.go` for the actual handler name.

---

## Execution Handoff

Plan complete and saved to `/Users/rohit/flow/docs/plans/2026-04-30-playbooks-and-skill-cleanup.md`. Two execution options:

1. **Subagent-Driven (recommended)** — Dispatch a fresh subagent per task, review between tasks, fast iteration. Each task gets a clean context window.
2. **Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
