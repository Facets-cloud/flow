// Package harness abstracts the agent CLI (Claude Code, Codex, Gemini, …)
// that flow drives behind a per-task session.
//
// The interface intentionally accommodates two binding models:
//
//   - Pre-allocating harnesses (Claude Code today). PrepareSpawn mints a
//     session UUID up front; flow claims it in the DB before spawning so
//     the session's transcript file lands at a deterministic path. The
//     status flip to in-progress and the session_id write are atomic.
//
//   - Self-allocating harnesses (Codex, Gemini). PrepareSpawn returns "";
//     the harness mints its own id at startup. HookEnvForSpawn injects a
//     correlator (e.g. FLOW_TASK=<slug>) so the SessionStart hook can
//     learn the runtime id and bind it back to the right task. The
//     status flip is deferred to the hook handler.
//
// The single LaunchCmd/ResumeCmd/HeadlessRun shape works for both
// because the data — sessionID is empty when not pre-allocated — drives
// the branching in flow's caller code, never a per-harness switch.
package harness

// Name is the short identifier persisted on tasks.harness and used to
// look up an implementation via ByName.
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
	// SkipApprovals asks the harness to run without per-tool approval
	// prompts. Each impl picks its own flag.
	SkipApprovals bool

	// Inject is the first-user-message text to wrap with
	// InjectionMarker and feed to the spawned session.
	Inject string
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
	// (e.g. "CLAUDE_CODE_SESSION_ID").
	SessionIDEnvVar() string

	// Session allocation -----------------------------------------------

	// PrepareSpawn returns a session id to claim BEFORE spawning, or
	// "" if the harness mints its own at startup. flow's caller
	// branches on the empty-string return — pre-alloc'd harnesses get
	// an immediate status flip; self-allocating ones defer to the
	// SessionStart hook.
	PrepareSpawn() (sessionID string, err error)

	// ValidateSessionID rejects strings that can't be a session id for
	// this harness. Used by `flow do --here` to gate the env-var-
	// supplied id before writing it to the DB.
	ValidateSessionID(s string) error

	// Launching --------------------------------------------------------

	// LaunchCmd builds the shell command to start a fresh session.
	// sessionID is whatever PrepareSpawn returned (may be "").
	// The returned string is fed verbatim to spawner.SpawnTab.
	LaunchCmd(sessionID, prompt string, opts LaunchOpts) string

	// ResumeCmd builds the shell command to continue an existing
	// session by id. opts.Inject (if any) is appended as the first
	// turn after resume.
	ResumeCmd(sessionID string, opts LaunchOpts) string

	// HookEnvForSpawn returns env vars to inject into the spawned
	// process so the SessionStart hook can correlate the runtime
	// session id to the task being launched. Returns nil for pre-
	// allocating harnesses (no correlation needed — the id was
	// already written to the DB before spawn).
	HookEnvForSpawn(taskSlug string) map[string]string

	// HeadlessRun executes a non-interactive prompt against the
	// harness (used by `flow done`'s close-out sweep). Stdout/stderr
	// are discarded; only the exit code matters.
	HeadlessRun(prompt string) error

	// Live-session detection -------------------------------------------

	// LiveSessionIDs returns the count of running processes per
	// session id. Used both for the "[live]" marker (count > 0) and
	// the duplicate-detection warning (count > 1) in `flow do`.
	// Implementations scan the process table (or equivalent) and key
	// by lowercase id. ps failures return (nil, error); empty map +
	// no error means "nothing running."
	LiveSessionIDs() (map[string]int, error)

	// Transcripts ------------------------------------------------------

	// TranscriptPath returns the absolute path on disk where the
	// harness records the session's transcript. Used by
	// `flow transcript` when called directly with a task ref. The
	// hook-stdin path provides this dynamically; harnesses that don't
	// expose a stable on-disk path may return an error.
	TranscriptPath(workDir, sessionID string) (string, error)

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

