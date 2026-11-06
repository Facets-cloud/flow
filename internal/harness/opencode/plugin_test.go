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
// runs, and the instruction/prompt calls it uses to deliver context.
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
		{"instruction put", `instructions.entry.put`},
		{"instruction remove", `instructions.entry.remove`},
		{"session start hook", `["hook", "session-start"]`},
		{"user prompt hook", `["hook", "user-prompt-submit"]`},
		{"stop hook", `["hook", "stop"]`},
		{"stop_hook_active", `stop_hook_active`},
		{"inbox pop wait", `"--wait"`},
		{"inbox pop keep-unread", `"--keep-unread"`},
		{"inbox pop json", `"--json"`},
		{"inbox ack", `["inbox", "read", id, "--json"]`},
		{"session env var", `env.OPENCODE_SESSION_ID = sessionID`},
		{"steer delivery", `delivery: "steer"`},
		{"queue delivery", `delivery: "queue"`},
	}
	for _, tc := range want {
		if !strings.Contains(src, tc.frag) {
			t.Errorf("plugin is missing %s (%q)", tc.what, tc.frag)
		}
	}
}

// TestPluginJSInboxContract pins the inbox relay's user-visible strings and
// the global state key the multi-instance dedupe relies on.
func TestPluginJSInboxContract(t *testing.T) {
	src := pluginSource(t)
	for _, frag := range []string{
		`"[flow inbox] " + out`,
		`"[flow] " + context`,
		"globalThis.__flowOpencode",
		`["inbox", "pop", "--wait", "--timeout", "300", "--keep-unread", "--json"]`,
		`stop_hook_active: active`,
		`stopSeen: new Map()`,
		`state.stopSeen ??= new Map()`,
	} {
		if !strings.Contains(src, frag) {
			t.Errorf("plugin is missing %q", frag)
		}
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
	for _, field := range []string{"meta", "started", "listeners", "nudged", "stopSeen"} {
		if !strings.Contains(src, "state."+field+" ??=") {
			t.Errorf("state.%s is not backfilled for older state objects", field)
		}
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
	// The plugin must stay dependency-free: no bare imports at all.
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "require(") {
			t.Errorf("plugin has a dependency: %q", trimmed)
		}
	}
}
