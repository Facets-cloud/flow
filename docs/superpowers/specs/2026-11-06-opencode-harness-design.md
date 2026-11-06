# opencode harness — design

Status: implemented on `feat/opencode-harness`. Target: opencode **v2.0.x**
(verified against 2.0.32). v1 plugin code and v1 CLI flags are not supported.

## Goal

`flow do <ref> --harness opencode` gives an opencode session the same
lifecycle a Claude session has: bootstrap from the brief, resume by id,
`--fresh`, `--with`/`--with-file`, `--auto` headless self-completion, live
guard, tab title, `flow transcript`, `flow done` close-out sweep, and the
hook-driven context/inbox behaviour. Use case: Claude plans and reviews,
opencode (DeepSeek through the opencode Go subscription) does the bulk work.

## opencode v2 facts this design relies on (verified)

| Fact | Consequence |
|---|---|
| `opencode -s <id>` / `opencode run -s <id>` continue the session, or **create it with that id** if missing. Only rule: the id starts with `ses`. | flow pre-allocates ids locally, like Claude. No probe. |
| A shared background server (`opencode serve --service`) runs every session. TUI and `run` are clients. | Plugins and hooks run in the server process, not the tab. |
| The client's environment is forwarded into the session's tool shells, plus `OPENCODE_SESSION_ID=<id>` and `OPENCODE=1`. | `flow` commands inside opencode find their task via `OPENCODE_SESSION_ID`. `FLOW_ROOT` from the tab reaches the agent's shell. |
| TUI has no `--model`. A session created with a `model` keeps it. | Model is set at session creation (API), and with `-m` for `run`. |
| `--auto` (client flag) auto-approves anything **not explicitly denied**. Config `deny` rules still apply. `OPENCODE_CONFIG_CONTENT` is ignored on the shared server. | `--dangerously-skip-permissions` → `--auto`. Nothing weakens a project `opencode.json`. |
| `opencode api <operationId> --param k=v -d <json>` authenticates to the running server by itself. `session.create` is idempotent for an existing id; `session.update` takes `metadata`. | flow prepares sessions with `opencode api`, no HTTP client or credentials in Go. |
| v2 server plugins: `~/.config/opencode/plugins/<name>/index.js`, `export default { id, setup(ctx) }`. Hooks: `ctx.event.subscribe()` (`session.execution.started/succeeded`, whole-server), `ctx.session.hook("prompt")`, `ctx.session.hook("context", (e) => e.system.push({type:"text",text}))` (runs before every model call of the agent loop; changes are not persisted; `e.sessionID` identifies the session; may be async), `ctx.session.prompt({sessionID,text,delivery:"steer"|"queue"})`. New plugin dirs load without restart. | flow ships one embedded JS plugin that bridges opencode events to the existing `flow hook …` commands. |
| `opencode session export <id>` prints `{info, messages[]}`; messages are `user` (`text`), `assistant` (`content[]` of `text`/`reasoning`/`tool`), `idle`, `synthetic`… | `flow transcript` renders from the export. |
| opencode loads skills from `~/.config/opencode/skills/<name>/SKILL.md` (and `~/.claude/skills`). It has a native `skill` tool. | flow installs the skill to `~/.config/opencode/skills/flow/`. |

## Adapter — `internal/harness/opencode`

| Interface method | opencode behaviour |
|---|---|
| `Name()` / `Binary()` | `opencode` / `opencode` |
| `SessionIDEnvVar()` | `OPENCODE_SESSION_ID` |
| `NewSessionID()` | local mint: `ses_` + 26 random base62 chars (native shape) |
| `ValidateSessionID` | `^ses_[0-9A-Za-z]{26}$` (native and flow-minted ids) |
| `ValidateSession` | `nil` (ids are global, not cwd-keyed — same as codex) |
| `LaunchCmd(id, prompt, o)` | `opencode -s <id> [--auto] --prompt '<prompt>[\n\n<marker>\n<inject>]'` |
| `ResumeCmd(id, o)` | `opencode -s <id> [--auto] [--prompt '<marker>\n<inject>']` |
| `AutoRunArgv(id, prompt, o)` | `opencode run -s <id> [--auto] [-m <model>] <prompt[+inject]>` |
| `SkipPermissionsRun(prompt)` | `opencode run --auto [-m <model>] <prompt>` (sessionless; close-out sweep) |
| `LiveSessionIDs()` | ps scan: lines whose argv0 basename is `opencode` and that carry `-s`/`--session` `<ses…>`; one count per line |
| `RenderTranscript` | `opencode session export <id>` → same `─── User ───` / `─── Assistant ───` / `─── Tool: x ───` / `─── Result ───` shape as codex; `compact` drops reasoning and results |
| `SkillInstallPath()` | `~/.config/opencode/skills/flow/SKILL.md` |
| `InstallSessionStartHook` | writes the embedded plugin to `~/.config/opencode/plugins/flow/index.js` (only when content differs; `added` = changed) |
| `UninstallSessionStartHook` | removes `~/.config/opencode/plugins/flow/` |
| other `Install*Hook` | no-op `(false, nil)` — the plugin covers them |

Model: `$FLOW_OPENCODE_MODEL` (`provider/model`, e.g.
`opencode-go/deepseek-v4.1-flash`). Unset → opencode's own default. flow has
no config file today, so env is the only knob.

### New optional capability: `harness.SessionPreparer`

```go
type SessionMeta struct {
    TaskSlug string // flow task slug
    FlowRoot string // $FLOW_ROOT of the spawning flow ("" = default)
    FlowBin  string // absolute path of the flow binary that spawned it
    Auto     bool   // headless `flow do --auto` run
}
type SessionPreparer interface {
    PrepareSession(workDir, sessionID string, meta SessionMeta) error
}
```

opencode's impl, with a 30 s timeout per call:
1. `opencode api session.create -d {"id","location":{"directory":workDir},"title":slug,"model"?,"metadata":{"flow":meta}}` — idempotent, also avoids the observed first-`run`-in-a-new-dir hang.
2. `opencode api session.update --param sessionID=<id> -d {"metadata":{…existing,"flow":meta}}` so resumed and `--here`-bound sessions get metadata too.

`cmdDo` calls it right after the status-flip commit, on bootstrap AND resume,
for both the tab and `--auto` paths. Before it, flow ensures the harness
install (skill tree if the version sidecar differs + the plugin via
`InstallSessionStartHook`). Failure on bootstrap rolls back exactly like a
spawn failure (`session_id=NULL, status=backlog`). The rollback SQL is
factored into one helper shared by the three sites.

## Hooks parity — the flow opencode plugin

One file, `internal/harness/opencode/plugin/index.js`, embedded with
`//go:embed`. It runs in the opencode server. State lives on `globalThis`
because the server loads one plugin instance per directory and every
instance sees every server event; the global map dedupes per session.

Session gating: the plugin acts only on sessions whose
`metadata.flow.taskSlug` is set (`ctx.session.get`). Every other opencode
session (for example ad-hoc DeepSeek runs) gets zero flow calls.
`flowBin`/`flowRoot` come from the metadata, so a test binary with an isolated
`FLOW_ROOT` drives its own sessions. Every `flow` child runs with
`OPENCODE_SESSION_ID=<id>`, `FLOW_ROOT` from the metadata, `FLOW_AUTO_RUN=1` if
`meta.auto`, and `CLAUDE_CODE_SESSION_ID`/`CODEX_THREAD_ID` removed.

| Claude hook | opencode plugin equivalent | Status |
|---|---|---|
| SessionStart (`flow hook session-start`) | on the first `session.execution.started` per session per server lifetime: run the hook with stdin `{"session_id","source":"startup"}`, cache `additionalContext` as a promise per session (`sessionCtx`), and push it as `<flow-session>…</flow-session>` into `e.system` in `session.hook("context")` | works |
| UserPromptSubmit (`flow hook user-prompt-submit`) | `session.hook("prompt")`: run the hook with `{"session_id","prompt"}`; store the output per session (`turnCtx`, deleted when empty) and push it as `<flow-turn>…</flow-turn>` into `e.system` in `session.hook("context")`, which fires before every model call | works |
| Inbox delivery (Claude: Monitor loop) | per flow session a listener loop: `flow inbox pop --wait --timeout 300 --keep-unread --json` → if a client for the session is live (ps: `opencode … -s <id>`) inject with `session.prompt({delivery:"steer", text:"[flow inbox] …"})`, then ack with `flow inbox read <id>`; if no client is live, stop the loop and leave the mail unread. Exit 3 (task closed) stops it. Restarted on the next `execution.started`. | works |
| PreToolUse mid-turn notice | covered by `steer` delivery (lands at the next tool-step boundary) | covered |
| Stop broadcast nudge (`flow hook stop`) | on `session.execution.succeeded`: run the hook with `{"session_id","stop_hook_active"}`; a non-empty context is queued as a prompt `[flow] …`. `stop_hook_active` is true on the turn that a nudge itself started, so a nudge never re-fires on its own continuation. flow's 30 min backoff still applies. | works |
| `flow do --here` bind from inside opencode | `OPENCODE_SESSION_ID` in tool shells | works; plugin gets metadata on the next `flow do` |

Flow-side changes needed for parity:
- `busSessionStartContext`: for a task whose harness is `opencode`, replace
  the "arm a Monitor" listener text with "flow's opencode plugin delivers
  messages into this conversation as `[flow inbox]` prompts; do not start a
  listener". Claude/codex text unchanged.
- `isHumanPrompt`: treat `[flow inbox]` and `[flow]` prefixed prompts as
  non-human (add to `flowPromptPrefixes`).

## Permissions and precedence

- `--dangerously-skip-permissions` / `--auto` runs → opencode `--auto`.
  opencode applies it only to rules that are not explicitly `deny`. A project
  `opencode.json` deny-list (like this repo's local one) keeps blocking.
- flow never sets session `permissions`, never writes `opencode.json`, and
  never uses `OPENCODE_CONFIG_CONTENT`.
- opencode precedence (low → high): global `~/.config/opencode/opencode.json`,
  project `opencode.json` files (farthest → nearest), `.opencode/opencode.json`,
  system dir, managed settings. Session-level rules are enforced by the
  server on top.
- Without the flag, opencode prompts in the TUI as usual. A prompt injected
  by the plugin into a session with no client attached would block on an
  `ask` rule, which is why the inbox listener only injects while a client is
  live.

## Live guard and focus

`LiveSessionIDs` drives the existing guard. Focus: kitty's
`windowIDForHarnessSession` also matches `-s|--session <ses…>` so `flow do`
on a live opencode task switches to its tab. Other backends fall through to
the existing "running elsewhere — pass --force" error.

## Known limits

- The TUI's argv id goes stale if the user switches sessions inside the TUI.
- Server restart: listeners restart on the session's next turn; until then
  pending mail is still surfaced by the session-start context.
- The skill text is Claude-flavoured (AskUserQuestion, Monitor). opencode
  maps the skill tool natively; question/monitor wording is advisory.
- Owners (`flow owner tick`) are not wired for opencode.
