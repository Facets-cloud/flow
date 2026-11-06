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
| v2 server plugins: `~/.config/opencode/plugins/<name>/index.js`, `export default { id, setup(ctx) }`. Hooks: `ctx.event.subscribe()` (`session.execution.started/succeeded`, whole-server), `ctx.session.hook("prompt")`, `ctx.session.hook("context", (e) => e.system.push({type:"text",text}))` (runs before every model call of the agent loop; changes are not persisted; `e.sessionID` identifies the session; may be async). **Not usable on 2.0.32** (though documented): `ctx.session.instructions` does not exist and a plugin-side `ctx.session.prompt` resolves without queueing anything. `opencode api session.prompt --param sessionID=<id> -d {"text","delivery":"steer"\|"queue"}` from a child process does work, and does not replace the TUI's forwarded env. New or rewritten plugin files load without restart, but the old module's cleanup is **not** called. | flow ships one embedded JS server plugin that bridges opencode events to the existing `flow hook …` commands, plus a separate TUI reporter (below). |
| `opencode session export <id>` prints `{info, messages[]}`; messages are `user` (`text`), `assistant` (`content[]` of `text`/`reasoning`/`tool`), `idle`, `synthetic`… | `flow transcript` renders from the export. |
| opencode loads skills from `~/.config/opencode/skills/<name>/SKILL.md` (and `~/.claude/skills`). It has a native `skill` tool. | flow installs the skill to `~/.config/opencode/skills/flow/`. |
| opencode v2 TUI plugins: a directory listed in `~/.config/opencode/cli.json` as `{"plugins": ["./<dir>"]}` (relative to `~/.config/opencode`). opencode loads `<dir>/tui.js`, whose default export is `{ id, setup(api) }`. `setup` runs inside the TUI client process and may return a cleanup function. `api.ui.router.current()` returns `{type:"session", sessionID}` when a session is on screen; `api.data.session.get(id)` returns session info including `parentID`. There is no reliable route-change event. | flow ships a tiny TUI plugin that reports the on-screen (root) session per client pid, so liveness follows the user across in-TUI session switches. |

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
| `LiveSessionIDs()` | ps scan: lines whose argv0 basename is `opencode` (pid parsed from `pid,command`); a TUI report for that pid wins over argv, otherwise the `-s`/`--session` `<ses…>` id is used; one count per line |
| `RenderTranscript` | `opencode session export <id>` → same `─── User ───` / `─── Assistant ───` / `─── Tool: x ───` / `─── Result ───` shape as codex; `compact` drops reasoning and results |
| `SkillInstallPath()` | `~/.config/opencode/skills/flow/SKILL.md` |
| `InstallSessionStartHook` | writes the embedded server plugin to `~/.config/opencode/plugins/flow/index.js` and the TUI reporter to `~/.config/opencode/flow-tui/tui.js` (each only when content differs), and adds `"./flow-tui"` to `~/.config/opencode/cli.json` (`added` = any change) |
| `UninstallSessionStartHook` | removes `~/.config/opencode/plugins/flow/` and `~/.config/opencode/flow-tui/`, and removes `"./flow-tui"` from `cli.json` (leaving the rest of the file intact) |
| other `Install*Hook` | no-op `(false, nil)` — the plugin covers them |

Model: `$FLOW_OPENCODE_MODEL` (`provider/model`, e.g.
`opencode-go/deepseek-v4.1-flash`). Unset → opencode's own default. flow has
no config file today, so env is the only knob.

Install lifecycle: the first opencode task installs the skill tree, VERSION
sidecar and plugin. After that, a plain `flow` invocation whose `Version`
differs from the recorded sidecar refreshes them — `maybeAutoUpgradeSkill`
covers every registered `SessionPreparer` harness, not just the ambient one.
A missing sidecar is an opt-out: flow never installs opencode behind a user
who never used it.

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
`//go:embed` (the TUI reporter `plugin/tui.js` is embedded the same way and
installed separately). It runs in the opencode server. State lives on
`globalThis` because the server loads one plugin instance per directory and every
instance sees every server event; the global map dedupes per session.
Each module evaluation stamps a generation on that state; listeners, scans
and hooks from an older generation retire themselves, since opencode
re-evaluates a rewritten plugin without calling the old cleanup.

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
| Inbox delivery (Claude: Monitor loop) | per flow session a listener loop: while a client for the session is live (ps: `opencode … -s <id>`), poll the NON-claiming unread list `flow inbox --json` every 3 s; when it is non-empty, claim the oldest with `flow inbox pop --json` (no `--wait`) only at that last moment, then inject it with `opencode api session.prompt` (`delivery:"steer"`, text `[flow inbox] <message json>`), retrying delivery a few times. A pop is a permanent claim (pending → delivered; there is no un-claim), so claiming any earlier loses the mail when the loop is superseded by a plugin reload or the client goes away — the earlier `pop --wait --keep-unread` loop hit exactly that in e2e. Exit 3 (task closed) stops the loop; with no live client it stops and leaves the mail pending. Started on `execution.started` and by a 15 s scan for attached `opencode -s <id>` clients (so a resumed, idle session gets one too). | works |
| PreToolUse mid-turn notice | covered by `steer` delivery (lands at the next tool-step boundary) | covered |
| Stop broadcast nudge (`flow hook stop`) | on `session.execution.succeeded`: run the hook with `{"session_id","stop_hook_active"}`; a non-empty context is queued as a prompt `[flow] …` via `opencode api session.prompt` (`delivery:"queue"`). `stop_hook_active` is true on the turn that a nudge itself started, so a nudge never re-fires on its own continuation. flow's 30 min backoff still applies. | works |
| `flow do --here` bind from inside opencode | `OPENCODE_SESSION_ID` in tool shells; the bind prepares the session (same `session.create`/`session.update` metadata write) so the plugin activates on the session's next event | works |

Flow-side changes needed for parity:
- `busSessionStartContext`: for a task whose harness is `opencode`, replace
  the "arm a Monitor" listener text with "flow's opencode plugin delivers
  messages into this conversation as `[flow inbox]` prompts; do not start a
  listener". Claude/codex text unchanged.
- `isHumanPrompt`: treat `[flow inbox]` and `[flow]` prefixed prompts as
  non-human (add to `flowPromptPrefixes`).

## TUI reporter and liveness

The TUI client's argv (`opencode -s <id>`) is a snapshot from launch: it goes
stale when the user switches sessions inside the TUI, and a bare `opencode`
that navigated to a flow session carries no id at all. flow therefore ships a
second, TUI-side plugin, `internal/harness/opencode/plugin/tui.js` (embedded as
`tuiJS`), installed to `~/.config/opencode/flow-tui/tui.js` and registered in
`~/.config/opencode/cli.json` as `"./flow-tui"`.

`setup(api)` runs in the TUI client process. Every second it reads
`api.ui.router.current()`; when the route is `{type:"session", sessionID}` it
walks `parentID` via `api.data.session.get` to the root session (loop-guarded;
a child/subagent session resolves to its task session). When the resolved
value changes it writes
`~/.local/state/flow/opencode-clients/<process.pid>.json` =
`{"pid", "sessionID": <root or null>, "updated": <ms>}` via mkdir -p,
tmp-file + rename. `sessionID: null` means the client is on a non-session
screen. Cleanup and `process.on("exit")` delete the file. The reporter never
throws, so a broken route or lookup can never take down the TUI.

Liveness rule, per opencode client process with pid P (Go `LiveSessionIDs` and
the server plugin's `attachedSessions`/`clientLive` share it):

1. Find clients in `ps -axo pid,command` whose argv0 basename is `opencode`.
2. If `~/.local/state/flow/opencode-clients/P.json` exists and parses, it is
   authoritative: a non-null `sessionID` is that client's session and argv is
   ignored; a null `sessionID` means the client has no session on screen.
3. Otherwise fall back to the argv `-s`/`--session` id.
4. Reports whose pid is not an opencode client in the current ps snapshot are
   stale and ignored (Go deletes them best-effort).

A missing reports directory is not an error — it just means argv-only
behaviour (the old rule). In the server plugin, `clientLive` keeps failing
open when `ps` itself fails; `attachedSessions` keeps failing closed (empty).

If `cli.json` exists but is not valid JSON, install leaves it untouched and
does not fail: the server plugin still works, only the TUI reporter is not
registered.

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

## Terminal spawn: long commands (kitty)

kitty's backend types the command into the new tab with `kitty @ send-text`,
which can land before the shell's line editor is up, in the tty canonical
buffer that macOS caps at 1024 bytes. A bootstrap line longer than that was
cut mid-quote (found in e2e). Lines over 900 bytes are now written to a
self-deleting script and only ` bash '<path>'` is typed; shorter commands
are sent exactly as before.

## Live guard and focus

`LiveSessionIDs` drives the existing guard, using the TUI report when one
exists (so a session switch inside the TUI is followed) and argv otherwise.
Focus: kitty's `windowIDForHarnessSession` also matches `-s|--session <ses…>`
so `flow do` on a live opencode task switches to its tab. Other backends fall
through to the existing "running elsewhere — pass --force" error.

## Known limits

- Server restart: listeners restart on the session's next turn; until then
  pending mail is still surfaced by the session-start context.
- The skill text is Claude-flavoured (AskUserQuestion, Monitor). opencode
  maps the skill tool natively; question/monitor wording is advisory.
