# Make the harness layer pluggable: add a coding agent with a file, not a release

**Date:** 2026-08-15
**Status:** P1, P2a and P2b implemented; P3–P4 designed. See "Implementation notes" at the end for where reality diverged from this document.

## Summary

Today a new coding harness (codex, gemini, praxis, …) cannot be integrated
without editing Go source and cutting a flow release. This spec makes a
harness a **declarative manifest** — `~/.flow/harnesses/<name>.toml` — that
flow loads at runtime, with a per-operation `exec` escape hatch for anything
the schema can't express.

Three things must work for a manifest-defined harness, not merely degrade:
**skills**, **hooks**, and **background sessions**. `flow skill install`
against a dropped-in manifest wires all three.

The claude adapter's behavior is unchanged. It stays a native Go adapter and
additionally ships an embedded `claude.toml` that a differential test pins to
byte-equality with the native adapter — the manifest path is proven against
the hardest real harness instead of rotting as a second-class citizen.

## Why this isn't already done

The seam exists and is good. `internal/harness/harness.go` defines a
20-method `Harness` interface plus an optional `BackgroundLauncher`, and all
**60 call sites in `internal/app` already route through it** — every method
is exercised, none dead. This is not a dependency-inversion job.

Four things block a second harness.

### 1. Registration is compile-time

`internal/app/harness.go:18` hardcodes the registry, and five more sites fall
back to `claude.New()` (`harness.go:51,72,121,134`, plus `harnessByName`'s
empty-name branch).

### 2. Four structural bypasses skip the interface entirely

| Site | Leak | Consequence |
|---|---|---|
| `internal/app/hook.go:125` | `os.Getenv("CLAUDE_CODE_SESSION_ID")` | The **entire** hook reverse-lookup. A non-claude task is structurally invisible to SessionStart/UserPromptSubmit. |
| `internal/app/auto.go:134` | `strings.HasPrefix(kv, "CLAUDE_CODE_SESSION_ID=")` | A non-claude `--auto` run leaks the parent's session-id var into the detached child. |
| `internal/app/stats.go:62`, `internal/stats/report.go:13,44,76` | direct `import "flow/internal/harness/claude"`, field `ClaudeProjects`, hardcoded `<encoded-cwd>/<sid>.jsonl`; `internal/stats/scan.go:152,162` switches on literal tool names `"Bash"`/`"Read"` | `flow stats` structurally excludes every non-claude session. |
| `internal/iterm/iterm.go:114-120`, `internal/terminal/terminal.go:199-201`, `internal/kitty/kitty.go:167-169`, `internal/zellij/zellij.go:136-138` | claude's literal `--session-id`/`--resume` flag spellings, duplicated 4× | `FocusSession` silently returns "not found" for any other harness. |

Plus `internal/iterm/iterm.go:62-66` prepends `ulimit -n 65536` to **every**
spawned command — a claude-motivated workaround `SpawnTab` has no parameter
to gate on — and `internal/spawner/spawner.go:140-151` has no `FocusSession`
case for `BackendWarp` or `BackendGhostty`, which silently fall through to
iTerm.

### 3. The interface is all-or-nothing

Twenty required methods. A harness with no skills directory and no hook
system must stub eight of them with errors, and `internal/app` has no way to
ask "can this harness do hooks?" before calling.

### 4. The agent-facing vocabulary is hardcoded in prompts

| Literal | Sites |
|---|---|
| `"Read CLAUDE.md in your work_dir"` | `do.go:766,784`, `auto.go:315,320`, `hook.go:79` |
| `"via the Skill tool"` | `do.go:763,780`, `done.go:149`, `auto.go:312`, `hook.go:70,177`, `owner_tick.go:509,531` |
| `"AskUserQuestion"` | `do.go:787,802,806,807`, `auto.go:317`, `hook.go:179`, `owner_tick.go:503` |

`references/templates.md:35-36` bakes `CLAUDE.md` into every persisted
`brief.md`, so the idiom leaks into user data, not just prompts. Roughly 19
further user-facing strings say "Claude session" unconditionally.

This axis is the one that decides whether genericity *works*: ship
`"read CLAUDE.md via the Skill tool and confirm with AskUserQuestion"` to
praxis and it is syntactically fine, semantically nonsense — praxis's tools
are named `bash`, `read`, `ask` (`tool/bash.go:129`, `tool/read.go:50`,
`tool/external.go:53`) and its context file is `AGENTS.md`.

## Approach

Four models were considered:

| | Model | Verdict |
|---|---|---|
| A | Go plugin registry (self-registering packages, capability interfaces) | Makes contribution cheap, not zero. Still a rebuild per harness. |
| B | Pure declarative manifest | Zero code change, but an under-expressive DSL becomes a trap that forces a code change anyway. |
| C | External `flow-harness-<name>` exec protocol | Infinitely general; a subprocess per call on hot paths (`flow list`) and a distribution burden. |
| **D** | **Manifest + per-operation `exec` escape hatch** | **Chosen.** Declarative for the normal case, never a dead end for the weird one. |

Every one of the 20 interface methods was checked against D and is
expressible as templates + regexes + a small strategy enum + a
config-file-patching primitive.

## Architecture

```
internal/app                  unchanged — 60 call sites keep using the interface
   │
internal/harness              core contract + capability accessors
   │
internal/harness/registry     user toml  ›  embedded spec  ›  native Go
   │                    │
   │                    └── internal/harness/claude   (native, behavior unchanged)
   │
internal/harness/spec         one adapter that interprets a HarnessSpec
                              per-operation `exec = [...]` escape hatch
```

### Capability split

The 20-method interface conflates three concerns with different genericity
profiles: process control (trivially declarative), state discovery (mixed),
and environment integration (where harnesses genuinely differ).

**Required core (8):** `Name`, `Binary`, `SessionIDEnvVar`, `NewSessionID`,
`ValidateSessionID`, `ValidateSession`, `LaunchCmd`, `LiveSessionIDs`.
`ValidateSession` may return nil unconditionally; `LiveSessionIDs` may return
an empty map.

**Optional — accessors returning a nil interface when unsupported:**

| Accessor | Wraps |
|---|---|
| `Resume() Resumer` | `ResumeCmd` |
| `Headless() HeadlessRunner` | `SkipPermissionsRun`, `AutoRunArgv` |
| `Transcript() TranscriptSource` | `RenderTranscript` + new `Events()` |
| `Skills() SkillInstaller` | the 4 skill methods |
| `Hooks() HookWirer` | the 4 install/uninstall methods + new `Encode(event, ctx)` |
| `Background() BackgroundLauncher` | existing 3, converted from type-assertion |
| `Vocab() Vocabulary` | new; always non-nil, defaults applied |

Nil-accessor rather than a `Caps()` bitset because a single concrete
spec-driven type cannot conditionally satisfy an interface in Go — but it can
return nil.

### Registry precedence

1. `~/.flow/harnesses/*.toml` — user
2. embedded built-in specs — shipped in the binary
3. native Go adapters — `claude` today

Higher shadows lower, with a warning on the wire when a user spec shadows a
native adapter. This lets a user retune claude's launch flags without a
rebuild. A malformed spec disables **only that harness**, named in the error;
it never breaks flow.

### Templating and quoting

All command construction is **argv arrays**, never shell strings. Each
element is expanded independently with `text/template` over a fixed variable
set — `.SessionID .Prompt .Cwd .WorkDir .Home .Name .Binary .Inject
.SkipPermissions` — and shell-quoting happens once, at the
`spawner.SpawnTab` boundary, via `spawner.ShellQuote`.

This replaces today's split personality where `LaunchCmd` returns a
`fmt.Sprintf`'d shell string (`claude.go` `LaunchCmd`) while `AutoRunArgv`
returns argv. Quoting becomes structural instead of textual.

Template helpers: `envOr <VAR> <fallback...>` and `json` (for embedding a
string as a JSON value).

## Behavior: the three capabilities that must work

### Skills — two mechanisms, one corpus

```toml
[skills]
discovery = "native"        # native | pointer
dir       = "{{.Home}}/.claude/skills/flow"
version   = "{{.Home}}/.claude/skills/flow/VERSION"
owns_dir  = true
require_frontmatter = ["name", "description"]
```

- **`native`** — the harness auto-discovers a skills directory. Write the
  tree, done. This is today's claude behavior, unchanged.
- **`pointer`** — write the tree, then inject a marker-delimited **managed
  block** into the harness's instructions file:

```toml
[skills.pointer]
file    = "{{.Home}}/.codex/AGENTS.md"
comment = "html"                        # html | hash | slash
block   = """
# flow
When the user asks about tasks, projects, playbooks, or "what should I work
on", read {{.SkillPath}} and follow it.
{{.HookDirective}}
"""
```

The managed-block writer is one primitive — `{file, marker-name,
comment-syntax, body}` → replace between markers, or append when absent.
Idempotent, uninstallable, preserves surrounding content.

This is not speculative. `~/.codex/AGENTS.md` on the author's machine already
contains a hand-written `<!-- flow:managed:start -->` block pointing at a
hand-maintained `~/.codex/skills/flow/`, and `grep flow:managed internal/`
returns zero hits — flow has no idea it exists. The same marker-block pattern
appears in `~/.gemini/GEMINI.md` and `~/.config/opencode/AGENTS.md` for a
different tool. The mechanism is proven and already being maintained by hand.

Two safety fields fall out of real harnesses:

- **`owns_dir = false`** — a manifest may legitimately point at a
  directory another harness owns. Praxis, for instance, scans
  `~/.claude/skills` and `~/.codex/skills` as lower-ranked fallbacks
  (`systemprompt/discovery.go:114`), so a user with several harnesses
  could choose to keep one shared copy. Uninstalling the borrower must
  then **not** delete the tree. (Praxis's own default is its own
  directory — see the correction below.)
- **`require_frontmatter`** — praxis rejects a `SKILL.md` with no
  `description:` outright (`skill/skill.go:118-120`). Checked pre-flight by
  `flow harness validate`, not discovered at runtime.

Critically, **one corpus**: the embedded `internal/app/skill/` tree installs
for every harness. The divergent hand-maintained forks stop existing.

### Hooks — a strategy ladder, all wired by `flow skill install`

Flow needs two events: SessionStart (inject bound-task context or the unbound
hint) and UserPromptSubmit (re-anchor drift). Three delivery strategies,
declared as an ordered list; **`flow skill install` runs every declared
strategy**.

| Strategy | flow-spawned | ad-hoc session | drift re-anchor | guarantee |
|---|---|---|---|---|
| `config-patch` | yes | yes | yes | deterministic |
| `prompt-prelude` | yes | no | at spawn only | deterministic |
| `instruction-directive` | yes | yes | yes | best-effort — agent must comply |

- **`config-patch`** — patch the harness's own hook config. Generalized as
  `{file, format: json|toml|yaml, pointer, entry-template, match-path}`.
- **`prompt-prelude`** — flow injects the hook context directly into the
  launch/resume prompt. Flow already builds that prompt, so this is nearly
  free and covers 100% of flow-spawned sessions.
- **`instruction-directive`** — the managed block carries a standing
  instruction to self-invoke `flow hook session-start` and re-check on drift.

A harness with no hook system combining the last two reaches near-parity:
deterministic on the spawn path (the common case), best-effort elsewhere.
`instruction-directive` is labelled best-effort deliberately — it is the same
mechanism the hand-written `~/.codex/AGENTS.md` block relies on today.

The **response envelope is data, not code**:

```toml
[hooks.response]
envelope = '{"hookSpecificOutput":{"hookEventName":"{{.Event}}","additionalContext":{{.Context|json}}}}'
```

This currently lives hardcoded in `internal/app/hook.go:206-219`
(`emitHookContext`). Praxis accepts **the identical shape**
(`praxis/native_command_hooks.go:884-913`) and deliberately aliases Claude
Code's event names — `SessionStart`→`session_start` (`:253`),
`UserPromptSubmit`→`user_prompt_submit` (`:249`) — so one envelope serves two
harnesses while a third can differ. That is exactly why it belongs in data.

### Background — spawn and registry are separable

```toml
[background]
spawn    = "supervised"     # native | supervised
registry = "native"         # native | flow
```

| | detached run | status/pid | logs | live two-way attach | resume |
|---|---|---|---|---|---|
| `spawn = native` (claude `--bg`) | yes | yes | yes | yes | yes |
| `spawn = supervised` | yes (`Setsid`) | yes (DB) | yes (log file) | **no** | via `flow do` in a tab |

`supervised` is not new machinery. `internal/app/auto.go` already runs a
detached supervisor: `SysProcAttr{Setsid: true}` (`auto.go:94`),
`Process.Release()` (`:101`), `launchAutoRun` (`:147`), `reconcileAutoRun`
(`:193`), with pid/status/log in `tasks.auto_run_*`
(`internal/flowdb/db.go:61-65`). This is extracted into
`internal/harness/supervisor` and shared by `--auto` and `$FLOW_TERM=bg`.
The `auto_run_*` columns are reused verbatim with widened semantics — **no
DDL migration**.

The split matters because praxis needs one of each: it has **no** `-bg` flag,
but it **does** have a machine-readable registry, `prx run agents -json`
(`cli/native/agents.go:86`) returning `AgentSessionInfo`
(`praxis/commands_agent_sessions.go:68-90`). A single `mode` field could not
express that.

The honest limit: supervised mode gives detached execution, status, logs and
resume — **not** live mid-conversation reply injection into a detached
headless process. `internal/app/do.go:542`'s hardcoded "(only claude does
today)" becomes registry-derived and precise about which harness offers which.

### Vocabulary

```toml
[vocab]
product      = "Praxis"
context_file = "AGENTS.md"
ask_tool     = "ask"
skill_hint   = "load the flow skill — praxis discovers it from ~/.claude/skills"
```

Prompt builders become templates over a `.V` namespace:

| Today | Becomes |
|---|---|
| `"Read CLAUDE.md in your work_dir"` | `Read {{.V.ContextFile}} in your work_dir` |
| `"via the Skill tool"` | `{{.V.SkillHint}}` |
| `"AskUserQuestion"` | `{{if .V.AskTool}}Use {{.V.AskTool}}{{else}}Ask the user directly and wait for an answer{{end}}` |

The `else` branch is load-bearing: genericity must degrade to prose, never to
a dangling tool name. Same treatment for `references/templates.md:35-36`, so
the idiom stops leaking into persisted `brief.md` files.

## Reference manifest: praxis

Fully grounded in `github.com/Facets-cloud/praxis-harness`, whose own
`AGENTS.md:134-140` already pins `Options.SessionID`, `PRAXIS_SESSION_ID`, the
`settings.json` hooks map, the skills search order, and `-model` resolution as
stable contracts **for flow**.

```toml
schema      = 1
name        = "praxis"
binary      = "prx"                    # .goreleaser.yaml:21-23
session_env = "PRAXIS_SESSION_ID"      # praxis/native_lifecycle.go:84

[session]
strategy   = "uuid7"                   # session/session.go:211-218
validate   = '^[^/\\]+$'               # session/layout.go:20-22 — path-safe, not UUID-shaped
verify_cwd = false                     # transcripts are id-keyed; a cwd check is meaningless

[launch]                                                    # TUI face
argv            = ["prx","chat","-session-id","{{.SessionID}}","-cwd","{{.WorkDir}}","-prompt","{{.Prompt}}"]
permission_flag = ["-permission-mode","yolo"]               # praxis/options.go:27

[resume]
argv = ["prx","chat","-resume","{{.SessionID}}","-cwd","{{.WorkDir}}"]   # tui/run.go:600

[headless]                                                  # cmd/prx/main.go:310
# `prx run -prompt <string>` takes the prompt directly. An earlier draft
# of this document claimed praxis needed stdin delivery and therefore
# could not do headless — that was a misreading of -prompt-file, which
# is a SIBLING flag that overrides -prompt, not the only way in.
run_argv  = ["prx","run","-permission-mode","yolo","-cwd","{{.WorkDir}}","-prompt","{{.Prompt}}"]
auto_argv = ["prx","run","-session","{{.SessionID}}","-permission-mode","yolo",
             "-cwd","{{.WorkDir}}","-prompt","{{.Prompt}}"]

[transcript]
path   = "{{envOr \"PRAXIS_CODING_AGENT_DIR\" .Home \"/.praxis/agent\"}}/sessions/{{.SessionID}}/session.jsonl"
format = "jsonl"                                            # session/layout.go:26
[transcript.map]
header_cwd = "cwd"                                          # recover cwd from the header record
role       = "message.role"                                 # ai/types.go:87-96
text       = "message.content[].text"
tool_block = "toolCall"                                     # ai/types.go:56 — camelCase discriminator
tool_name  = "message.content[].name"
tool_arg   = "message.content[].arguments.command"
[transcript.map.usage]                                      # ai/types.go:63-83
input      = "message.usage.input"
output     = "message.usage.output"
cache_read = "message.usage.cacheRead"
cache_creation = "message.usage.cacheWrite"
[transcript.tools]
shell = ["bash"]                                            # tool/bash.go:129
read  = ["read"]                                            # tool/read.go:50

[skills]
# praxis has its OWN skills dir at <AgentDir>/skills, ranked FIRST among
# the user dirs it scans; ~/.claude/skills and ~/.codex/skills are
# lower-ranked compatibility fallbacks (systemprompt/discovery.go:114).
# The loader is first-name-wins, so installing into claude's tree would
# be SHADOWED by anything already sitting in praxis's own.
discovery = "native"
dir       = "{{envOr \"PRAXIS_CODING_AGENT_DIR\" .Home \"/.praxis/agent\"}}/skills/flow"
require_frontmatter = ["name","description"]                # skill/skill.go:118-120

[hooks]
strategies = ["config-patch"]
[hooks.config_patch]
# Same AgentDir root as the skills and session dirs (settings.go:153-172).
# Global layer: the project layer is workspace-trust gated (:44-51).
file = "{{envOr \"PRAXIS_CODING_AGENT_DIR\" .Home \"/.praxis/agent\"}}/settings.json"
[hooks.config_patch.events.SessionStart]
pointer = "/hooks/SessionStart"                             # aliased → session_start (:253)
entry   = '"{{.Command}}"'                                  # bare-string form accepted (:630-654)
[hooks.config_patch.events.UserPromptSubmit]
pointer = "/hooks/UserPromptSubmit"                         # aliased → user_prompt_submit (:249)
entry   = '"{{.Command}}"'
[hooks.response]
envelope = '{"hookSpecificOutput":{"hookEventName":"{{.Event}}","additionalContext":{{.Context|json}}}}'

[background]
spawn    = "supervised"                                     # no -bg flag exists on either face
registry = "native"
[background.registry]
argv = ["prx","run","agents","-json"]                       # cli/native/agents.go:86
[background.registry.map]                                   # praxis/commands_agent_sessions.go:68-90
session_id = "sessionId"
short_id   = "id"
name       = "name"
cwd        = "cwd"
status     = "status"
state      = "state"

[vocab]
product      = "Praxis"
context_file = "AGENTS.md"                                  # projectcontext.go:138 — prio 10, first
ask_tool     = "ask"                                        # tool/external.go:53
skill_hint   = "load the flow skill (praxis discovers it under its agent dir)"
```

Two traps this manifest absorbs for free, both worth knowing before
implementing:

- **The two faces spell the same flag differently.** `-session-id` (TUI,
  `tui/run.go:566`) vs `-session` (headless, `cli/native/main.go:227`); resume
  is `-resume` on the TUI only. Separate argv templates per operation handle
  this without a special case.
- **Transcripts are id-keyed, not cwd-keyed.** `~/.praxis/agent/sessions/`
  also contains cwd-encoded directories, but **no code in praxis-harness
  produces them** — a different program shares that root. Decoding directory
  names would find zero transcripts. Recover cwd from the header's `cwd`
  field.

## Code changes

### P1 — De-leak (no behavior change, no new features)

| File | Change |
|---|---|
| `internal/harness/harness.go` | Split into required core + capability accessors; add `Vocabulary`, `TranscriptSource.Events`, `HookWirer.Encode`. |
| `internal/harness/claude/*.go` | Implement the accessors. **No behavioral change.** |
| `internal/app/harness.go:18` | `allHarnesses()` delegates to `registry.All()`; the five `claude.New()` fallbacks become `registry.Default()`. |
| `internal/app/hook.go:125` | `os.Getenv(h.SessionIDEnvVar())` via `ambientHarness()`. |
| `internal/app/auto.go:134` | Strip `h.SessionIDEnvVar()+"="`. |
| `internal/app/do.go:542,632,661,695` | Derive harness/registry names instead of literal `claude agents`. |
| ~19 user-facing strings | Interpolate `h.Vocab().Product` / `h.Binary()`. |

### P2a — Spec engine

New `internal/harness/spec`: schema structs + TOML decode, `text/template`
argv expansion, `spawner.ShellQuote` at the boundary, per-operation `exec`
escape hatch, jsonl decoder driven by `[transcript.map]`.
New `internal/harness/registry`: discovery, precedence, validation.
Embedded `claude.toml` + the differential test below.
New commands: `flow harness list | show <name> | validate <file>`.

### P2b — Integration

New `internal/harness/managedblock`: marker-delimited block writer.
New `internal/harness/supervisor`: extracted from `internal/app/auto.go:94,101,147,193`.
`internal/harness/spec` implements `Skills()` (`native`/`pointer`, `owns_dir`,
`require_frontmatter`), `Hooks()` (the three strategies, config-patch over
json/toml/yaml), `Background()` (`spawn` × `registry`).
`internal/app/skill.go` installs for **every** registered harness, with
`--harness <name>` to narrow.

### P3 — Dialect

Prompt builders (`do.go:760-814`, `done.go:128-191`, `auto.go:300+`,
`hook.go`, `owner_tick.go`) become `text/template` over `.V`.
`internal/app/skill/SKILL.md` and `references/*.md` de-idiomized;
`references/templates.md:35-36` stops baking `CLAUDE.md` into `brief.md`.

### P4 — Analytics and spawn polish

`internal/stats` consumes `TranscriptSource.Events()`; `BuildOpts.ClaudeProjects`
and the `harness/claude` import are deleted; `scan.go:152,162` switches on
normalized `"shell"`/`"read"`. Delete `FileRollup.First` (populated, never read).
`FocusSession` takes the harness's `liveness.match` instead of four duplicated
regexes; `iterm.go:62-66`'s `ulimit` becomes `launch.prelude`; add the missing
`BackendWarp`/`BackendGhostty` cases at `spawner.go:140-151`.

## Tests

| Test | Pins |
|---|---|
| `TestClaudeSpecMatchesNativeAdapter` | For a matrix of `(sessionID, prompt, opts)`, the spec adapter's `LaunchCmd`/`ResumeCmd`/`AutoRunArgv`/transcript path/skill paths/hook JSON **equal** the native adapter's. This is what keeps the DSL honest. |
| `TestAppLayerIsHarnessAgnostic` | A `fixture` harness spec drives the `internal/app` command surface. Today every app test runs against claude — which is precisely why four bypasses survived. |
| `TestSpecTemplateQuoting` | Prompts containing `'`, `"`, `$(…)`, backticks, newlines survive argv expansion → shell-quote → parse. |
| `TestManagedBlockRoundtrip` | Insert / update / remove preserves surrounding file content; idempotent; tolerates a missing file and a hand-edited block. |
| `TestHookStrategyLadder` | Each strategy installs and uninstalls independently; `flow skill install` runs all declared ones. |
| `TestSpecValidationRejects` | Malformed spec disables only that harness, named in the error; flow still runs. |
| `TestSupervisedBackgroundLifecycle` | Spawn → status → log → reconcile against a fake headless binary. |
| `TestRegistryPrecedence` | user toml shadows embedded shadows native, with a warning. |

Existing claude tests must pass unmodified. That is the regression gate for
"keep the claude logic the same".

## Compatibility

- `tasks.harness` NULL/empty → `claude`, unchanged (`internal/flowdb/db.go:159-163,323-334`).
- The `~/.claude/settings.json` hook command strings are unchanged — altering
  them orphans existing installs (flagged in `CLAUDE.md`).
- `hookMatcher = "startup|resume"` preserved verbatim.
- `auto_run_*` columns reused; no DDL migration.
- `EncodeCwd` stops being claude-private and becomes the named
  `path_encoding = "claude-dash"` transcript strategy.

## Implementation notes

Recorded as P1 and P2a landed, so the next phase starts from what is
true rather than from what was planned.

### Corrections this document owes

**`session.validate` is required, not optional.** Runtime session ids are
shell-quoted, but ids recovered from environment variables, process
listings, and external capture commands are still untrusted identity.
The regexp must match the entire id (substring matches are rejected),
and a manifest without it is rejected at load.

**Quoting is per-variable, not per-boundary.** `spawner.ShellQuote`
always wraps. An argv element is quoted iff its *template text*
references a data-class variable (`.SessionID`, `.Prompt`, `.Inject`,
`.WorkDir`, `.Cwd`, `.Home`). Literal tokens stay bare. Deciding from
template text rather than the expansion is deliberate — after expansion
the provenance is gone. The native Claude adapter quotes the same values,
so the differential test remains byte-identical.

**Empty has two meanings.** The differential test caught this on its
first run. `{{if .Inject}}…{{end}}` expanding to empty means "this
optional argument is absent" → drop the element. `{{.Prompt}}` expanding
to empty means "the prompt is the empty string" → claude still emits
`''`, holding the positional slot. The rule: an element is optional iff
its template consists *entirely* of conditional blocks
(`isPurelyConditional`). Dropping an empty positional argument shifts
every later argument left.

**`LaunchOpts` gained `WorkDir`.** `Harness.LaunchCmd` had no workdir
parameter — the spawner `cd`s separately — so a `{{.WorkDir}}` template
would have silently expanded to nothing forever. Launch, resume, auto,
owner ticks, and close-out sweeps now carry the task/owner workdir. Native
and manifest headless runners also set the child process directory.

**`claude.toml` lives in `internal/harness/spec/testdata/`, not embedded.**
The native adapter wins registry precedence, so an embedded copy could
never run in production — it would be dead weight that drifts. Its job
is to fail the build when the schema stops being able to express a real
harness, and a testdata file does that job exactly as well.

**Self-minted post-launch ids are not representable yet.** Flow allocates
and stores a session id before spawning. UUID strategies therefore require
`launch.argv` to pass that exact id to the agent; `exec-capture` must be a
pre-launch allocation command that creates/reserves the same session.
Codex and OMP mint only after interactive launch and accept no caller id
there, so their earlier example manifests bound Flow to nonexistent
sessions. Those examples were removed rather than shipping launch-only
configurations that fail resume, transcript, liveness, and completion.

### P2b corrections

**`[background]` was designed away, not deferred.** The spec proposed
`spawn = native|supervised` plus `registry = native|flow`. Reading
`internal/app/auto.go` showed the supervised half is **derivable rather
than declarable**: `--auto` already does detached execution with
`Setsid`, cwd, a run log, pid and status reconciliation, and a harness
with no native background cannot accept a mid-conversation reply
anyway. So any harness with a `[headless]` table can already be run
detached, and `$FLOW_TERM=bg` now falls back to that path instead of
hard-erroring. The only thing a manifest genuinely could not express is
*native* background — and no manifest-defined harness has it, so
adding that schema would have been speculative. `[background]` is not
in the schema and nothing is missing because of it.

**`OwnsSkillDir()` and `Strategies()` were added to the interface.**
Both exist because the app layer was otherwise forced to lie:

- Without `OwnsSkillDir`, uninstalling praxis printed "uninstalled flow
  skill from …" while correctly deleting nothing, because its manifest
  points at claude's tree.
- Without `Strategies`, a pointer-discovery harness reported
  "SessionStart hook already installed" — `added=false` is ambiguous
  between "already registered" and "this harness registers nothing
  here". Reporting the wrong one tells a user their hooks are wired
  when they are not.

**Praxis's skill directory: an error worth recording.** This document
first claimed praxis should install into `~/.claude/skills/flow` with
`owns_dir = false`, on the strength of praxis scanning that path. That
was wrong in a way that would have failed silently.

`DefaultSkillDirs` (`systemprompt/discovery.go:108-123`) ranks user
directories `<AgentDir>/skills` → `~/.pi/agent/skills` →
`~/.omp/agent/skills` → `~/.claude/skills` → `~/.codex/skills`, and the
loader is **first-name-wins**. Praxis's own `~/.praxis/agent/skills`
therefore beats claude's tree. Installing into claude's would have let
any pre-existing `~/.praxis/agent/skills/flow` — exactly the
hand-maintained fork this work exists to retire — shadow flow's install
forever, with no error anywhere. `AgentDir` is
`$PRAXIS_CODING_AGENT_DIR` or `~/.praxis/agent` (`settings.go:153-161`),
so both the skills path and the hooks path derive from it.

**Shared skill directories still needed a per-run ledger.** Two
manifests may name the same directory (the `owns_dir` case above), and
the second harness in a sweep would otherwise trip the `--force` guard
against flow's own output from a second earlier. `flow skill install`
tracks what this run wrote: the guard still protects a pre-existing
user file, and hooks are still wired per-harness because they live in
different config files.

**The hook entry is a template, and that is load-bearing.** Claude
wants `{"matcher":…,"hooks":[{"type":"command","command":…}]}`; praxis
accepts a bare `"command"` string. Both are produced by the same engine
from their own manifests, verified against a real settings.json.

### Deferred, with the reason

| Item | Why not yet |
|---|---|
| `TranscriptSource.Events` | No consumer until the analytics phase; adding it now is an unimplemented method on every adapter. |
| `HookWirer.Encode` | The response envelope is still hardcoded in `internal/app/hook.go`. It becomes data when a harness needs a different shape — praxis accepts claude's verbatim, so nothing forces it yet. |
| `prompt_delivery = "stdin"` | No longer needed for praxis — `prx run -prompt <string>` takes the prompt directly, so `[headless]` is expressible as ordinary argv. The key stays absent until a harness appears that genuinely only reads stdin. |
| `[transcript.map].timestamp`, `.tools` | Only the analytics phase reads them. Every key in the schema today does something. |
| `config_patch.format` (toml/yaml) | Both harnesses flow has met use JSON. Adding the key is non-breaking when a TOML-config harness appears. |

### What shipped

- `internal/harness` — 8 required methods + 6 nil-when-unsupported
  capability accessors + `Vocabulary`.
- `internal/harness/registry` — manifest discovery, precedence
  (user toml › native), per-manifest error isolation, `Reload` for tests.
- `internal/harness/spec` — schema + strict decode, three-context
  template engine, adapter, jsonl transcript decoder.
- `prompt-prelude` — SessionStart context is applied to fresh launch and
  auto prompts, and carried through `.Inject` on resume.
- `flow harness list | show <name> | validate <file>`.
- Dependency added: `github.com/BurntSushi/toml` v1.6.0 (pure Go, no
  CGO), chosen over stdlib JSON because manifests are regexp-dense and
  because `MetaData.Undecoded()` gives strict unknown-key rejection for
  free.
- 819 tests pass, up from 577 before this work.

## Out of scope (YAGNI)

- Rewriting claude as a manifest and deleting the native adapter. The
  differential test makes that a safe, separate, later decision.
- A harness marketplace, remote spec fetching, or spec versioning beyond the
  `schema = 1` field.
- Live two-way attach for `spawn = "supervised"`. Detached execution, status,
  logs and resume only.
- Per-project harness manifests. User-level `~/.flow/harnesses/` only.
- Translating the skill corpus per harness beyond vocabulary substitution.
- `praxis-official` as a separate integration — it is a second manifest
  differing in `binary` and paths, requiring no code.
