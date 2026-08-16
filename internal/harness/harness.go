// Package harness abstracts the agent CLI (Claude Code, Codex, Gemini, …)
// that flow drives behind a per-task session.
//
// Design principles encoded in the interface:
//
//   - Flow never sets env vars on spawned harness processes. Flow only
//     reads env vars the harness itself exports (CLAUDE_CODE_SESSION_ID,
//     CODEX_THREAD_ID, GEMINI_SESSION_ID). Avoids polluting the spawned
//     environment and keeps `flow do --here`'s discovery path symmetric
//     with the first-spawn binding path.
//
//   - Every harness pre-allocates a session id from flow's perspective.
//     Claude generates locally; codex/gemini probe their CLI (e.g.
//     `codex exec` mints a session and prints the id, which the impl
//     captures). Either way NewSessionID returns a real id, so flow's
//     caller code has a single uniform spawn path — no deferred-bind
//     branches, no FLOW_TASK env injection, no pending-spawn DB column.
//
//   - Each harness owns its own transcript format end-to-end. Path
//     layout AND on-disk schema differ per harness (claude jsonl with
//     claude messages; codex jsonl with codex events; gemini single-
//     object json); the harness renders to a normalized text stream
//     so callers never decode harness-specific bytes.
package harness

import (
	"io"
	"io/fs"
)

// Name is the short identifier persisted on tasks.harness and used to
// look up an implementation.
type Name string

const (
	NameClaude Name = "claude"
	NameCodex  Name = "codex"
)

// InjectionMarker prefixes any first-user-message text injected via
// `flow do --with` so the receiving session can distinguish it from
// typed user input. Shared across harnesses — the receiver only needs
// to recognize the literal string.
const InjectionMarker = "[via flow do --with]"

// LaunchOpts are options forwarded into the spawn command builder.
// Harness adapters translate to per-CLI flags (Claude:
// --dangerously-skip-permissions, Codex: --dangerously-bypass-…, etc).
type LaunchOpts struct {
	// SkipPermissions asks the harness to run without per-tool
	// approval prompts. Each impl picks its own flag.
	SkipPermissions bool

	// Inject is the first-user-message text to wrap with
	// InjectionMarker and feed to the spawned session.
	Inject string

	// WorkDir is the directory the session should run in.
	//
	// The spawner already cd's here before running the command, so a
	// harness that inherits its cwd (claude) ignores this. It exists
	// for harnesses that want the directory passed EXPLICITLY as a
	// flag (praxis: `-cwd <dir>`), which a manifest cannot express
	// otherwise — without it, a {{.WorkDir}} template would silently
	// expand to nothing on every launch.
	WorkDir string
}

// BackgroundAgent is one entry from a harness's background-agent
// registry. For the claude adapter it is populated from
// `claude agents --json`. SessionID is the full session id flow records
// on the task; ShortID is the harness's display/handle id (claude: the
// 8-char prefix of SessionID, shown in the Agent View and accepted by
// `claude attach`/`logs`/`stop`).
type BackgroundAgent struct {
	ShortID   string
	SessionID string
	Name      string
	Cwd       string
	PID       int
	Status    string // coarse liveness, e.g. "busy" / "idle"
	State     string // finer state, e.g. "working" / "blocked" / "done"
}

// BackgroundView describes how a user inspects a harness's background
// sessions, so flow's messages can point them at the right place
// without hardcoding one harness's product vocabulary.
type BackgroundView struct {
	// Surface names where background sessions live, phrased to
	// follow a preposition — claude: "your Agent View".
	Surface string

	// Command lists or opens them — claude: "claude agents".
	// Rendered inside backticks by callers.
	Command string
}

// BackgroundLauncher is an OPTIONAL harness capability: hosting
// terminal-free background sessions (claude's Agent View, via
// `claude --bg`). flow selects this path when spawner.IsBackground() is
// true ($FLOW_TERM=bg). A harness that returns nil from
// Harness.Background makes `$FLOW_TERM=bg flow do <task>` fail cleanly
// — flow never silently falls back to a terminal tab.
//
// Unlike the interactive path, flow does NOT pre-allocate the session id
// here: a backgrounding harness mints (and manages) its own id. flow
// captures the REAL id after spawn by querying the registry, so the
// DB-authoritative binding contract holds without fighting the harness.
type BackgroundLauncher interface {
	// View returns the user-facing vocabulary for this harness's
	// background-session surface. Used to phrase "check <surface>:
	// `<command>`" without naming a specific harness.
	View() BackgroundView

	// SpawnBackground starts a fresh background session in workDir,
	// running prompt, displayed as name. workDir is where the agent
	// begins (and what its transcript/CLAUDE.md context is keyed to) —
	// it must be the task's work_dir, not the cwd flow happened to run
	// from. It blocks only until the session is registered (no polling),
	// then resolves and returns the full session id (plus current
	// status) by querying the agent registry. The returned
	// BackgroundAgent.SessionID is what flow records on the task.
	SpawnBackground(workDir, name, prompt string, opts LaunchOpts) (BackgroundAgent, error)

	// ResumeBackground brings a no-longer-tracked background session's
	// conversation back as a fresh background agent, seeded from the
	// prior session's transcript. NOTE: a backgrounding harness manages
	// its own id, so this MINTS A NEW session id (the prior history is
	// inherited, but `claude --bg --resume <id>` does not preserve the
	// id — verified against the CLI and documented behavior). The
	// returned BackgroundAgent carries the NEW id, which flow re-records
	// on the task. workDir is where the resumed agent begins (the task's
	// work_dir). opts.Inject, if set, is delivered as the first message
	// after resume.
	ResumeBackground(workDir, sessionID string, opts LaunchOpts) (BackgroundAgent, error)

	// BackgroundAgents returns the current background-agent registry
	// (claude: `claude agents --json --all`, so exited / failed /
	// completed sessions are included, not just live ones). Used to
	// decide spawn-vs-attach-vs-resume and to surface status in
	// `flow show` / `flow list`. A registry entry with a zero PID is
	// not currently running (its process exited) but is still
	// recoverable by attaching in the Agent View.
	BackgroundAgents() ([]BackgroundAgent, error)
}

// Vocabulary is the agent-facing dialect of a harness: the names it
// uses for the concepts flow's prompts and messages refer to. Flow
// interpolates these instead of hardcoding Claude Code's idiom, so a
// prompt built for one harness reads correctly in another.
//
// Every field is always populated — Harness.Vocab returns a value, not
// a pointer, and adapters fill it from their own knowledge. AskTool is
// the one field legitimately allowed to be empty: a harness with no
// interactive-choice tool makes prompts fall back to plain prose
// rather than naming a tool that does not exist.
type Vocabulary struct {
	// Product is the human-facing name used in flow's own output,
	// e.g. "Claude" in "bind THIS Claude session to the task".
	Product string

	// ContextFile is the ambient project-instructions filename the
	// harness reads automatically (claude: "CLAUDE.md"; codex and
	// praxis: "AGENTS.md"). Prompts tell the agent to read it.
	ContextFile string

	// AskTool is the registered name of the harness's interactive
	// multiple-choice tool (claude: "AskUserQuestion"; praxis:
	// "ask"). Empty means the harness has none — callers must then
	// phrase the instruction as plain prose.
	AskTool string

	// SkillHint is the harness-idiomatic instruction for loading
	// flow's skill, e.g. "via the Skill tool" for claude or a plain
	// "read <path>" for a harness with no skill mechanism.
	SkillHint string
}

// Resumer is the OPTIONAL capability of continuing an existing session
// by id. A harness that cannot resume returns nil from Harness.Resume;
// flow then refuses to resume rather than silently starting a fresh
// session against a task that already has a transcript.
type Resumer interface {
	// ResumeCmd builds the shell command to continue an existing
	// session by id. opts.Inject (if any) is appended as the first
	// turn after resume.
	ResumeCmd(sessionID string, opts LaunchOpts) string
}

// HeadlessRunner is the OPTIONAL capability of running non-interactively.
// Required by `flow done`'s close-out sweep and by `flow do --auto`; a
// harness without it makes both fail cleanly instead of hanging on a
// prompt no human will answer.
type HeadlessRunner interface {
	// SkipPermissionsRun executes a non-interactive prompt against
	// the harness with per-tool approvals auto-allowed (used by
	// `flow done`'s close-out sweep). Stdout/stderr are discarded;
	// only the exit code matters.
	SkipPermissionsRun(prompt string, opts LaunchOpts) error

	// AutoRunArgv builds the argv for a headless, self-completing
	// autonomous run (`flow do --auto`) pinned to sessionID. This is a
	// third execution shape distinct from the other two:
	//
	//   - LaunchCmd/ResumeCmd build a SHELL STRING for an interactive
	//     terminal tab (a human drives it).
	//   - SkipPermissionsRun is sessionless and discards output (the
	//     fire-and-forget close-out sweep).
	//   - AutoRunArgv is headless like the sweep BUT pins the session
	//     id — so a transcript exists for the run's own `flow done`
	//     close-out sweep and for `flow transcript` — and returns argv
	//     (not a shell string) so the detached supervisor can set the
	//     process cwd and redirect stdout/stderr to the run log itself.
	//
	// opts.Inject (if any) is appended to the prompt behind
	// InjectionMarker, exactly as LaunchCmd does. opts.SkipPermissions
	// is honored via the harness's own flag (auto runs always set it —
	// there is no human to approve tool calls). argv[0] is the binary
	// name; the supervisor execs it via PATH lookup.
	AutoRunArgv(sessionID, prompt string, opts LaunchOpts) []string
}

// TranscriptSource is the OPTIONAL capability of reading back a
// session's conversation. Each impl owns both path resolution AND
// format decoding, so callers never touch harness-specific bytes.
type TranscriptSource interface {
	// RenderTranscript reads the harness's on-disk transcript for
	// (cwd, sessionID) and writes a normalized human-readable form
	// to w. Each impl owns both path resolution AND format decoding
	// — claude's jsonl, codex's event log, gemini's single-object
	// json all converge to the same text shape on w.
	//
	// cwd is the directory the harness session was started in (NOT
	// necessarily the task's work_dir; callers fall back to work_dir).
	// compact omits tool results and thinking blocks. The whole
	// transcript is rendered — there is no time cutoff. (An earlier
	// design scoped output to entries after tasks.session_started, but
	// that elided all real work on retrospective `flow do --here`
	// binds, where session_started is stamped at bind time AFTER the
	// conversation.) Returns an error if the transcript can't be found
	// or decoded.
	RenderTranscript(cwd, sessionID string, compact bool, w io.Writer) error
}

// SkillInstaller is the OPTIONAL capability of placing flow's skill
// tree where the harness will discover it. A harness returning nil
// from Harness.Skills has no skill mechanism flow can write to; flow
// says so plainly rather than pretending the install succeeded.
type SkillInstaller interface {
	// SkillInstallPath returns where flow's skill markdown lives for
	// this harness (e.g. ~/.claude/skills/flow/SKILL.md).
	SkillInstallPath() (string, error)

	// SkillVersionPath returns the sidecar file recording which
	// flow binary version wrote the current skill content. Used by
	// the auto-upgrade gate.
	SkillVersionPath() (string, error)

	// InstallSkill writes the skill tree rooted at SkillInstallPath's
	// directory. The passed fs.FS is walked and every file is written
	// preserving its relative path — so "SKILL.md" lands next to
	// "references/<x>.md". Creates parent dirs as needed. Idempotent —
	// callers gate "already installed" themselves.
	InstallSkill(files fs.FS) error

	// UninstallSkill removes this harness's registration of the skill.
	// Whether that includes deleting the directory depends on
	// OwnsSkillDir.
	UninstallSkill() error

	// OwnsSkillDir reports whether UninstallSkill will delete the skill
	// directory, or only unregister the harness from it.
	//
	// Harnesses share directories in practice — praxis natively scans
	// ~/.claude/skills, so its manifest points at claude's tree and
	// must not delete it. Callers need this to describe what they did
	// without claiming a removal that never happened.
	OwnsSkillDir() bool
}

// Hook delivery strategies. A harness may use several at once; they
// differ in what they cover and how strongly they guarantee it.
const (
	// StrategyConfigPatch registers flow's commands in the harness's
	// own hook config. Deterministic, and the only strategy that
	// covers sessions the user starts outside flow.
	StrategyConfigPatch = "config-patch"

	// StrategyPromptPrelude prepends flow's context to the launch
	// prompt. Deterministic, but only for sessions flow spawns.
	StrategyPromptPrelude = "prompt-prelude"

	// StrategyInstructionDirective asks the agent, via the instructions
	// file, to invoke flow's hook commands itself. Covers ad-hoc
	// sessions and drift, but is best-effort: it depends on the agent
	// complying.
	StrategyInstructionDirective = "instruction-directive"
)

// HookWirer is the OPTIONAL capability of registering flow's hook
// commands with the harness's own lifecycle-event system. A harness
// returning nil from Harness.Hooks has no such system; flow reports
// that instead of silently skipping the wiring.
type HookWirer interface {
	// InstallSessionStartHook idempotently registers `command` as a
	// SessionStart hook (matcher: startup|resume equivalent). Returns
	// (added=true) iff the on-disk hook config was actually modified.
	InstallSessionStartHook(command string) (added bool, err error)

	// UninstallSessionStartHook removes any SessionStart entry whose
	// inner command matches `command`.
	UninstallSessionStartHook(command string) (removed bool, err error)

	// InstallUserPromptSubmitHook idempotently registers `command` as a
	// UserPromptSubmit hook (no matcher). Fires on every user prompt;
	// the command itself no-ops in unbound sessions and injects a tiny
	// drift/close-out anchor in bound ones. Returns (added=true) iff the
	// on-disk hook config was actually modified.
	InstallUserPromptSubmitHook(command string) (added bool, err error)

	// UninstallUserPromptSubmitHook removes any UserPromptSubmit entry
	// matching `command`. Used by `flow skill uninstall`.
	UninstallUserPromptSubmitHook(command string) (removed bool, err error)

	// PreparePrompt applies any launch-time hook delivery strategy to a
	// prompt. Config-backed harnesses return prompt unchanged; a harness
	// using prompt-prelude prepends sessionStartContext. An empty prompt is
	// valid on resume and produces only the context payload.
	PreparePrompt(prompt, sessionStartContext string) string

	// Strategies lists how this harness receives flow's hook context.
	//
	// Callers need it to describe what an install actually did. Without
	// it, the Install* methods' added=false is ambiguous between
	// "already registered" and "this harness registers nothing here
	// because its strategies act elsewhere" — and reporting the wrong
	// one tells the user their hooks are wired when they are not.
	Strategies() []string
}

// Harness is the contract every agent-CLI adapter implements. The
// methods below are REQUIRED; everything a harness may legitimately
// lack lives behind the capability accessors at the bottom, each of
// which returns nil when the harness does not support it.
type Harness interface {
	// Identity ---------------------------------------------------------

	// Name returns the canonical short id (stored on tasks.harness).
	Name() Name

	// Binary returns the executable name (e.g. "claude", "codex").
	// Exposed so flow's process-table scan can filter to lines that
	// mention the right binary.
	Binary() string

	// SessionIDEnvVar returns the env var the harness exports inside
	// each running session so flow can reverse-lookup the bound task
	// (e.g. "CLAUDE_CODE_SESSION_ID"). Flow reads this; it never sets
	// it.
	SessionIDEnvVar() string

	// Session allocation -----------------------------------------------

	// NewSessionID returns the session id flow should claim before
	// spawning. Implementations either generate locally (claude
	// synthesizes a v4 UUID) or probe the harness (codex/gemini exec
	// a one-shot to mint and capture an id). Always returns a real
	// id on success — flow's caller has a single uniform spawn path.
	NewSessionID() (string, error)

	// ValidateSessionID rejects strings that can't be a session id for
	// this harness. Used by `flow do --here` to gate the env-var-
	// supplied id before writing it to the DB.
	ValidateSessionID(s string) error

	// ValidateSession verifies that the on-disk state for
	// (workDir, sessionID) is consistent with what a future
	// `flow do <slug>` resume would expect — for cwd-keyed
	// harnesses (claude, gemini) this means stat'ing the
	// transcript at the path the harness would write it. Returns
	// nil if the layout checks out, an error describing the
	// mismatch otherwise.
	//
	// Used to enforce the "any task with a session_id has work_dir
	// == the cwd that session was created at" invariant — gates
	// `flow do --here` binds and `flow update task --work-dir`
	// changes. Comparing os.Getwd() to work_dir is unreliable
	// (chained `cd && flow do --here` from inside a harness Bash
	// invocation fools it); this method does the honest check.
	//
	// Harnesses whose transcripts are sid-only (e.g. codex)
	// should return nil unconditionally.
	ValidateSession(workDir, sessionID string) error

	// Launching --------------------------------------------------------

	// LaunchCmd builds the shell command to start a fresh session
	// with the given session id. For claude this is `--session-id
	// <id>`; for codex/gemini it's a resume of the id minted during
	// NewSessionID. The returned string is fed verbatim to
	// spawner.SpawnTab.
	LaunchCmd(sessionID, prompt string, opts LaunchOpts) string

	// Live-session detection -------------------------------------------

	// LiveSessionIDs returns the count of running processes per
	// session id. Used both for the "[live]" marker (count > 0) and
	// the duplicate-detection warning (count > 1) in `flow do`.
	// Implementations scan the process table (or equivalent) and key
	// by lowercase id. ps failures return (nil, error); empty map +
	// no error means "nothing running."
	LiveSessionIDs() (map[string]int, error)

	// Capabilities -----------------------------------------------------
	//
	// Each accessor returns nil when the harness does not support that
	// capability. Callers MUST nil-check and report the gap in the
	// harness's own terms rather than assuming every adapter can do
	// everything claude can. A nil accessor is a fact about the
	// harness, not an error.

	// Vocab returns the harness's agent-facing dialect. Always
	// populated — a Vocabulary value, never nil.
	Vocab() Vocabulary

	// Resume returns the resume capability, or nil if the harness
	// cannot continue a session by id.
	Resume() Resumer

	// Headless returns the non-interactive execution capability, or
	// nil if the harness has no headless mode. Gates `flow done`'s
	// close-out sweep and `flow do --auto`.
	Headless() HeadlessRunner

	// Transcript returns the transcript-reading capability, or nil if
	// the harness persists no readable conversation.
	Transcript() TranscriptSource

	// Skills returns the skill-install capability, or nil if the
	// harness has no skill mechanism flow can write to.
	Skills() SkillInstaller

	// Hooks returns the hook-wiring capability, or nil if the harness
	// has no lifecycle-event system.
	Hooks() HookWirer

	// Background returns the background-session capability, or nil if
	// the harness cannot run detached agents.
	Background() BackgroundLauncher
}
