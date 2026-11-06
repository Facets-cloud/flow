package opencode

import (
	"strings"
	"testing"
)

// pluginSource is the embedded plugin as text. A compile-time embed guard
// matters here: the placeholder that shipped before this test was a
// one-liner, and every assertion below would fail loudly against it.
func pluginSource(t *testing.T) string {
	t.Helper()
	src := string(pluginJS)
	if len(src) < 2000 {
		t.Fatalf("embedded plugin is %d bytes — still the placeholder?", len(src))
	}
	return src
}

// TestPluginJSBridgesEveryHook pins the plugin's hook parity contract: the
// id, the opencode v2 event names it subscribes to, the flow commands it
// runs, and the hook calls it uses to deliver context.
func TestPluginJSBridgesEveryHook(t *testing.T) {
	src := pluginSource(t)

	want := []struct {
		what string
		frag string
	}{
		{"plugin id", `id: "flow"`},
		{"started event", `"session.execution.started"`},
		{"succeeded event", `"session.execution.succeeded"`},
		{"prompt hook", `session.hook("prompt"`},
		{"context hook", `session.hook("context"`},
		{"context push", `e.system.push`},
		{"context guard", `Array.isArray(e?.system)`},
		{"session start hook", `["hook", "session-start"]`},
		{"user prompt hook", `["hook", "user-prompt-submit"]`},
		{"stop hook", `["hook", "stop"]`},
		{"stop_hook_active", `stop_hook_active`},
		{"inbox list", `["inbox", "--json"]`},
		{"inbox pop", `["inbox", "pop", "--json"]`},
		{"session env var", `env.OPENCODE_SESSION_ID = sessionID`},
		{"inject cli", `"session.prompt"`},
		{"inject param", `"--param"`},
		{"inject helper", `injectPrompt`},
		{"steer delivery", `injectPrompt(sessionID, "[flow inbox] " + out, "steer")`},
		{"queue delivery", `injectPrompt(sessionID, "[flow] " + context, "queue")`},
	}
	for _, tc := range want {
		if !strings.Contains(src, tc.frag) {
			t.Errorf("plugin is missing %s (%q)", tc.what, tc.frag)
		}
	}
}

// TestPluginJSContextDelivery pins how flow context reaches the model: the
// session-start and turn context are cached per session and pushed into the
// context hook's system blocks. opencode v2 has no `session.instructions`,
// so any use of `instructions.entry` is a silent no-op.
func TestPluginJSContextDelivery(t *testing.T) {
	src := pluginSource(t)

	for _, frag := range []string{
		`<flow-session>`,
		`<flow-turn>`,
		`sessionCtx: new Map()`,
		`turnCtx: new Map()`,
		`state.sessionCtx ??= new Map()`,
		`state.turnCtx ??= new Map()`,
		`state.sessionCtx.has(sessionID)`,
		`state.sessionCtx.set(sessionID, started)`,
		`state.turnCtx.set(sessionID, context)`,
		`state.turnCtx.delete(sessionID)`,
	} {
		if !strings.Contains(src, frag) {
			t.Errorf("plugin is missing %q", frag)
		}
	}
	if strings.Contains(src, "instructions.entry") {
		t.Error("plugin still uses instructions.entry, which opencode v2 does not expose")
	}
}

// TestPluginJSInboxContract pins the inbox relay's user-visible strings and
// the global state key the multi-instance dedupe relies on. The relay must
// poll the non-claiming unread list and claim with a plain pop only right
// before injecting: a pop is a permanent claim with no un-claim, so the
// previous `--wait --keep-unread` loop lost mail that was claimed by a
// generation which then retired (or a client that went away).
func TestPluginJSInboxContract(t *testing.T) {
	src := pluginSource(t)
	for _, frag := range []string{
		`"[flow inbox] " + out`,
		`"[flow] " + context`,
		"globalThis.__flowOpencode",
		`["inbox", "--json"]`,
		`["inbox", "pop", "--json"]`,
		`stop_hook_active: active`,
		`stopSeen: new Map()`,
		`state.stopSeen ??= new Map()`,
	} {
		if !strings.Contains(src, frag) {
			t.Errorf("plugin is missing %q", frag)
		}
	}
	for _, frag := range []string{
		`--keep-unread`,
		`"--wait"`,
		`["inbox", "read"`,
		`const delivered = new Set()`,
	} {
		if strings.Contains(src, frag) {
			t.Errorf("plugin still contains the retired claim-early path %q", frag)
		}
	}
}

// TestPluginJSInjectsViaCLI guards the delivery path: plugin-side
// `ctx.session.prompt` resolves without queueing anything on opencode 2.0.32,
// so every injection must go through `opencode api session.prompt`, and no
// plugin call may fall back to the silent no-op.
func TestPluginJSInjectsViaCLI(t *testing.T) {
	src := pluginSource(t)

	for _, frag := range []string{
		`"session.prompt"`,
		`"--param"`,
		`injectPrompt`,
		`async function injectPrompt(sessionID, text, delivery)`,
		`opencodeBin`,
		`JSON.stringify({ text, delivery })`,
		`data?.id`,
	} {
		if !strings.Contains(src, frag) {
			t.Errorf("plugin is missing %q", frag)
		}
	}
	if strings.Contains(src, "ctx.session.prompt(") {
		t.Error("plugin still calls ctx.session.prompt, a silent no-op on opencode 2.0.32")
	}
}

// TestPluginJSStopDedupeIsSynchronous guards the stop-broadcast dedupe: with
// one plugin instance per directory, every instance sees the same succeeded
// event, so `stopSeen` must be consulted and updated before the first await
// or sibling instances all run `flow hook stop` and queue N nudges.
func TestPluginJSStopDedupeIsSynchronous(t *testing.T) {
	src := pluginSource(t)

	const marker = "async function onExecutionSucceeded"
	start := strings.Index(src, marker)
	if start < 0 {
		t.Fatalf("plugin has no %q", marker)
	}
	body := src[start+len(marker):]
	preAwait, _, ok := strings.Cut(body, "await")
	if !ok {
		t.Fatalf("onExecutionSucceeded has no await — unexpected shape")
	}
	for _, frag := range []string{"state.stopSeen.get(sessionID)", "state.stopSeen.set(sessionID", "STOP_DEDUPE_MS"} {
		if !strings.Contains(preAwait, frag) {
			t.Errorf("stop dedupe is not synchronous: %q appears after the first await", frag)
		}
	}
	// The backfill keeps an older globalThis state object (no stopSeen) usable.
	for _, field := range []string{"meta", "started", "sessionCtx", "turnCtx", "listeners", "nudged", "stopSeen"} {
		if !strings.Contains(src, "state."+field+" ??=") {
			t.Errorf("state.%s is not backfilled for older state objects", field)
		}
	}
}

// TestPluginJSAttachedClientScan guards the repair path for sessions that were
// resumed without a prompt or whose listener died with a plugin reload: a
// periodic ps scan must find attached clients and re-arm their inbox listener,
// deduped across plugin instances by `lastScan`.
func TestPluginJSAttachedClientScan(t *testing.T) {
	src := pluginSource(t)
	for _, frag := range []string{
		"attachedSessions",
		"setInterval",
		"clearInterval",
		"lastScan",
		"SCAN_INTERVAL_MS",
	} {
		if !strings.Contains(src, frag) {
			t.Errorf("plugin is missing %q", frag)
		}
	}
}

// TestPluginJSReloadGeneration guards hot-reload safety. `flow` rewrites the
// plugin file in place, so opencode re-evaluates the module but never calls the
// previous instance's cleanup: the old inbox loop, event subscription and hooks
// keep running. Each evaluation must therefore claim a new generation, retire
// other generations' listeners, and gate every entry point on it — an old loop
// that reaches the claim or the inject would otherwise steal a message through
// stale code.
func TestPluginJSReloadGeneration(t *testing.T) {
	src := pluginSource(t)

	// opencode evaluates the module once per directory instance, so a
	// per-evaluation generation would make each newly booted directory retire
	// every other directory's context and prompt hooks.
	if strings.Contains(src, "Math.random") {
		t.Error("generation must identify the code, not the module evaluation")
	}

	for _, frag := range []string{
		`const GEN = sourceGeneration()`,
		`createHash("sha256")`,
		`if (state.gen !== GEN) {`, // only changed code retires other instances
		`state.gen = GEN`,
		`if (state.gen !== GEN) return`,
		`if (state.gen !== GEN) break`,
		`state.gen !== GEN`,    // also guards the event loop body and the scan
		`entry?.gen !== GEN`,   // load-time retirement of other generations' listeners
		`existing.gen === GEN`, // ensureListener treats a foreign entry as absent
		`const alive = () => !stopping.value && state.gen === GEN`,
		`alive()`,
		`state.listeners.get(sessionID)?.gen === GEN`,
		`state.listeners.get(id)?.gen === GEN`,
		`state.listeners.set(sessionID, { gen: GEN, loop })`,
		`state.sessionCtx.clear()`,
		`state.meta.clear()`,
		`state.lastScan = 0`,
	} {
		if !strings.Contains(src, frag) {
			t.Errorf("plugin is missing reload guard %q", frag)
		}
	}

	// The old listener must bail before claiming: being superseded while it
	// polls the unread list or pops must not claim a message the current
	// generation would then never see. Once a pop has claimed a message there
	// is no un-claim, so delivery is attempted even when superseded.
	for _, frag := range []string{
		"while (alive()) {",
		"if (!alive()) return; // superseded: no claim was made",
		"if (!(await clientLive(sessionID)) || !alive()) return;",
		`log("inbox message claimed but not delivered:", sessionID, id)`,
	} {
		if !strings.Contains(src, frag) {
			t.Errorf("listener loop is missing reload check %q", frag)
		}
	}

	// Cleanup must not evict the entry a newer generation already installed.
	at := strings.Index(src, "} finally {")
	if at < 0 {
		t.Fatal("listener has no finally block")
	}
	if !strings.Contains(src[at:], `if (state.listeners.get(sessionID)?.gen === GEN) state.listeners.delete(sessionID);`) {
		t.Error("listener finally deletes the map entry without checking the generation")
	}
}

// TestPluginJSNeverThrows is a coarse guard on the never-crash-the-server
// rule: every hook entry point is wrapped, and setup returns a cleanup.
func TestPluginJSNeverThrows(t *testing.T) {
	src := pluginSource(t)
	for _, frag := range []string{
		"try {",
		`console.error("[flow-plugin]", ...args)`,
		"return cleanup;",
		"subscription.abort()",
	} {
		if !strings.Contains(src, frag) {
			t.Errorf("plugin is missing %q", frag)
		}
	}
	// The plugin must stay dependency-free: only built-in `node:` imports.
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "import ") {
			if !strings.Contains(trimmed, `"node:`) {
				t.Errorf("plugin imports a non-builtin dependency: %q", trimmed)
			}
			continue
		}
		if strings.HasPrefix(trimmed, "require(") {
			t.Errorf("plugin has a CommonJS dependency: %q", trimmed)
		}
	}
}

// TestPluginJSFollowsTUIReports pins the liveness rule shared with the Go
// adapter: a TUI reporter file for a client pid wins over its (stale) argv id.
func TestPluginJSFollowsTUIReports(t *testing.T) {
	src := pluginSource(t)
	for _, frag := range []string{
		"opencode-clients",
		`"ps", "-axo", "pid,command"`,
		"clientReport",
		"clientSessionIDs",
		"fs.readFileSync",
	} {
		if !strings.Contains(src, frag) {
			t.Errorf("server plugin is missing TUI-report fragment %q", frag)
		}
	}
}

// TestTuiPluginReporter pins the embedded TUI plugin: it polls the route,
// reports the root session to the per-pid file, and cleans up on exit.
func TestTuiPluginReporter(t *testing.T) {
	src := string(tuiJS)
	if len(src) < 500 {
		t.Fatalf("embedded TUI plugin is %d bytes — missing?", len(src))
	}
	for _, frag := range []string{
		`id: "flow.tui"`,
		"ui.router.current",
		"opencode-clients",
		"process.pid",
		"parentID",
		"setInterval",
		`process.on("exit"`,
	} {
		if !strings.Contains(src, frag) {
			t.Errorf("TUI plugin is missing %q", frag)
		}
	}
}

// TestPluginJSNegativeMetaCacheExpires guards the `flow do --here` activation
// path: a session that had no flow metadata when the plugin first looked can
// be bound later, so the negative answer must expire instead of being cached
// for the whole server lifetime.
func TestPluginJSNegativeMetaCacheExpires(t *testing.T) {
	src := pluginSource(t)
	for _, frag := range []string{
		"NEGATIVE_TTL_MS",
		"state.metaMissAt",
		"state.metaMissAt ??= new Map()",
		"state.metaMissAt.clear()",
		"Date.now() - missedAt < NEGATIVE_TTL_MS",
		"state.metaMissAt.set(sessionID, Date.now())",
		"state.metaMissAt.delete(sessionID)",
	} {
		if !strings.Contains(src, frag) {
			t.Errorf("plugin is missing negative-cache guard %q", frag)
		}
	}
	// A positive result still stays cached unconditionally.
	if !strings.Contains(src, "if (state.meta.has(sessionID)) return state.meta.get(sessionID);") {
		t.Error("plugin stopped short-circuiting positive metadata cache hits")
	}
}
