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
)

// Name is the short identifier persisted on tasks.harness and used to
// look up an implementation.
type Name string

const (
	NameClaude Name = "claude"
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

// BackgroundLauncher is an OPTIONAL harness capability: hosting
// terminal-free background sessions (claude's Agent View, via
// `claude --bg`). flow selects this path when spawner.IsBackground() is
// true ($FLOW_TERM=bg). A harness that does NOT implement it makes
// `$FLOW_TERM=bg flow do <task>` fail cleanly — flow never silently
// falls back to a terminal tab.
//
// Unlike the interactive path, flow does NOT pre-allocate the session id
// here: a backgrounding harness mints (and manages) its own id. flow
// captures the REAL id after spawn by querying the registry, so the
// DB-authoritative binding contract holds without fighting the harness.
type BackgroundLauncher interface {
	// SpawnBackground starts a fresh background session running prompt,
	// displayed as name. It blocks only until the session is registered
	// (no polling), then resolves and returns the full session id (plus
	// current status) by querying the agent registry. The returned
	// BackgroundAgent.SessionID is what flow records on the task.
	SpawnBackground(name, prompt string, opts LaunchOpts) (BackgroundAgent, error)

	// ResumeBackground resumes an existing background session by full
	// session id (transcript preserved, same id). opts.Inject, if set,
	// is delivered as the first message after resume.
	ResumeBackground(sessionID string, opts LaunchOpts) error

	// BackgroundAgents returns the current background-agent registry.
	// Used to decide spawn-vs-resume-vs-already-running and to surface
	// live status in `flow show` / `flow list`.
	BackgroundAgents() ([]BackgroundAgent, error)
}

// Harness is the contract every agent-CLI adapter implements.
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

	// ResumeCmd builds the shell command to continue an existing
	// session by id. opts.Inject (if any) is appended as the first
	// turn after resume.
	ResumeCmd(sessionID string, opts LaunchOpts) string

	// SkipPermissionsRun executes a non-interactive prompt against
	// the harness with per-tool approvals auto-allowed (used by
	// `flow done`'s close-out sweep). Stdout/stderr are discarded;
	// only the exit code matters.
	SkipPermissionsRun(prompt string) error

	// AutoRunArgv builds the argv for a headless, self-completing
	// autonomous run (`flow do --auto`) pinned to sessionID. This is a
	// third execution shape distinct from the two above:
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

	// Live-session detection -------------------------------------------

	// LiveSessionIDs returns the count of running processes per
	// session id. Used both for the "[live]" marker (count > 0) and
	// the duplicate-detection warning (count > 1) in `flow do`.
	// Implementations scan the process table (or equivalent) and key
	// by lowercase id. ps failures return (nil, error); empty map +
	// no error means "nothing running."
	LiveSessionIDs() (map[string]int, error)

	// Transcripts ------------------------------------------------------

	// RenderTranscript reads the harness's on-disk transcript for
	// (cwd, sessionID) and writes a normalized human-readable form
	// to w. Each impl owns both path resolution AND format decoding
	// — claude's jsonl, codex's event log, gemini's single-object
	// json all converge to the same text shape on w.
	//
	// cwd is the directory the harness session was started in (NOT
	// necessarily the task's work_dir — see tasks.session_cwd; for
	// legacy NULL rows callers fall back to work_dir). compact omits
	// tool results and thinking blocks. The whole transcript is
	// rendered — there is no time cutoff. (An earlier design scoped
	// output to entries after tasks.session_started, but that elided
	// all real work on retrospective `flow do --here` binds, where
	// session_started is stamped at bind time AFTER the conversation.)
	// Returns an error if the transcript can't be found or decoded.
	RenderTranscript(cwd, sessionID string, compact bool, w io.Writer) error

	// Skill / rules file -----------------------------------------------

	// SkillInstallPath returns where flow's skill markdown lives for
	// this harness (e.g. ~/.claude/skills/flow/SKILL.md).
	SkillInstallPath() (string, error)

	// SkillVersionPath returns the sidecar file recording which
	// flow binary version wrote the current skill content. Used by
	// the auto-upgrade gate.
	SkillVersionPath() (string, error)

	// InstallSkill writes content to SkillInstallPath, creating
	// parent dirs as needed. Idempotent — callers gate "already
	// installed" themselves.
	InstallSkill(content []byte) error

	// UninstallSkill removes the skill directory for this harness.
	UninstallSkill() error

	// Hooks ------------------------------------------------------------

	// InstallSessionStartHook idempotently registers `command` as a
	// SessionStart hook (matcher: startup|resume equivalent). Returns
	// (added=true) iff the on-disk hook config was actually modified.
	InstallSessionStartHook(command string) (added bool, err error)

	// UninstallSessionStartHook removes any SessionStart entry whose
	// inner command matches `command`.
	UninstallSessionStartHook(command string) (removed bool, err error)

	// UninstallUserPromptSubmitHook removes any stale
	// UserPromptSubmit entry matching `command`. flow used to wire
	// this hook in older releases; the cleanup is kept so upgraded
	// installs converge to a clean config.
	UninstallUserPromptSubmitHook(command string) (removed bool, err error)
}
