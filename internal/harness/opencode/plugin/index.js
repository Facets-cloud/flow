// flow opencode plugin (v2 server plugin).
//
// Bridges opencode server events to the `flow hook …` commands so an
// opencode session gets the same lifecycle a Claude Code session has:
// bootstrap context, per-prompt drift anchors, the inbox relay, and the
// stop-broadcast nudge. Everything is best-effort: failures are logged
// and swallowed, never thrown into the server; sessions without flow
// metadata (`metadata.flow.taskSlug`) are never touched.
//
// GENERATED FILE — installed by `flow` (from the copy embedded in the
// flow binary) to ~/.config/opencode/plugins/flow/index.js. Do not edit
// it here: `flow` rewrites it whenever the installed copy differs.
//
// Event -> flow call mapping:
//
//   session.execution.started    -> `flow hook session-start` (first per session)
//                                -> `flow inbox pop --wait --timeout 300 --keep-unread --json`
//   attached client (ps scan, 15 s) -> `flow inbox pop …` (covers resumed sessions
//                                   and plugin reloads, where no turn ever starts)
//   session.hook("prompt")       -> `flow hook user-prompt-submit`
//   session.hook("context")      -> pushes the cached session-start and turn
//                                   context into every model call
//   session.execution.succeeded  -> `flow hook stop`
//   inbox message popped         -> a `[flow inbox]` steer prompt, then `flow inbox read <id> --json`
//
// One plugin instance is loaded per directory, so each event reaches every
// instance. The per-turn entry points therefore dedupe synchronously on
// shared globalThis state before doing any work: started events via
// `sessionCtx`/`listeners`, succeeded events via `stopSeen` (a short window),
// so one turn end runs `flow hook stop` and queues at most one `[flow]` nudge.
// The 15 s attachment scan is deduped the same way via `lastScan`, so one
// instance per interval walks the attached sessions and repairs a missing
// listener. `session.hook("context")` only fires in the instance for the
// session's directory, so it pushes unconditionally (context is not persisted
// between model calls).

const HOOK_TIMEOUT_MS = 20_000;
const LISTENER_TIMEOUT_MS = 330_000;
const PS_TIMEOUT_MS = 5_000;
const STOP_DEDUPE_MS = 3_000; // window that collapses duplicated succeeded events
const SCAN_INTERVAL_MS = 15_000; // attachment scan period (resumed sessions, reloads)

// One instance is loaded per directory and every instance sees every server
// event, so state lives on globalThis and is deduped per session.
const state = (globalThis.__flowOpencode ??= {
  meta: new Map(), // sessionID -> flow meta | null (cached)
  sessionCtx: new Map(), // sessionID -> Promise<string> of the session-start context
  turnCtx: new Map(), // sessionID -> user-prompt-submit context for the current turn
  listeners: new Map(), // sessionID -> inbox listener promise
  nudged: new Set(), // last turn came from a stop nudge
  stopSeen: new Map(), // sessionID -> Date.now() of the last handled succeeded event
  lastScan: 0, // Date.now() of the last attachment scan (cross-instance dedupe)
});

// An older plugin version may already have published a state object without
// the newer fields, so backfill every one before anything reads it.
state.meta ??= new Map();
state.started ??= new Set();
state.sessionCtx ??= new Map();
state.turnCtx ??= new Map();
state.listeners ??= new Map();
state.nudged ??= new Set();
state.stopSeen ??= new Map();
state.lastScan ??= 0;

function log(...args) {
  try { console.error("[flow-plugin]", ...args); } catch { /* never throw out of logging */ }
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

// spawnCollect runs argv and returns { code, stdout } — never throws.
async function spawnCollect(argv, options, timeoutMs) {
  const failed = { code: -1, stdout: "" }; // shared failure/timeout result
  let proc;
  try {
    proc = Bun.spawn(argv, options);
  } catch (err) {
    log("spawn failed:", argv[0], err);
    return failed;
  }
  const read = (async () => {
    const stdout = proc.stdout ? await new Response(proc.stdout).text() : "";
    return { code: await proc.exited, stdout };
  })().catch((err) => {
    log("read output failed:", argv[0], err);
    return failed;
  });
  let timer;
  const timedOut = new Promise((resolve) => {
    timer = setTimeout(() => {
      try { proc.kill(); } catch (err) { log("kill failed:", argv[0], err); }
      resolve(failed);
    }, timeoutMs);
  });
  try {
    return await Promise.race([read, timedOut]);
  } finally {
    clearTimeout(timer);
  }
}

// runFlow runs the flow binary for a session with the flow env contract.
async function runFlow(meta, sessionID, args, stdinObj, timeoutMs = HOOK_TIMEOUT_MS) {
  const env = { ...process.env };
  for (const key of ["CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID", "FLOW_ROOT"]) delete env[key];
  delete env.FLOW_AUTO_RUN;
  env.OPENCODE_SESSION_ID = sessionID;
  if (meta.flowRoot) env.FLOW_ROOT = meta.flowRoot;
  if (meta.auto) env.FLOW_AUTO_RUN = "1";
  const options = { env, stdin: "ignore", stdout: "pipe", stderr: "ignore" };
  if (stdinObj !== undefined) options.stdin = new Blob([JSON.stringify(stdinObj)], { type: "application/json" });
  return spawnCollect([meta.flowBin || "flow", ...args], options, timeoutMs);
}

// flowMeta reads (and caches) the flow metadata prepared on the session.
async function flowMeta(ctx, sessionID) {
  if (state.meta.has(sessionID)) return state.meta.get(sessionID);
  let flow;
  try {
    const info = await ctx.session.get({ sessionID });
    flow = info?.metadata?.flow ?? info?.data?.metadata?.flow;
  } catch (err) {
    log("session.get failed:", sessionID, err);
    return null; // transient: do not cache, retry on the next event
  }
  const slug = flow?.taskSlug;
  const meta = typeof slug === "string" && slug !== ""
    ? { taskSlug: slug, flowRoot: flow.flowRoot || "", flowBin: flow.flowBin || "", auto: flow.auto === true }
    : null;
  state.meta.set(sessionID, meta);
  return meta;
}

// hookContext pulls additionalContext out of a `flow hook` JSON response.
function hookContext(stdout) {
  try {
    const context = JSON.parse(stdout)?.hookSpecificOutput?.additionalContext;
    return typeof context === "string" ? context.trim() : "";
  } catch { return ""; }
}

// sessionStartContext runs `flow hook session-start` once per session and
// caches the promise so concurrent callers share one run. The promise is
// stored synchronously, before the first await, so sibling plugin instances
// cannot start a second run. It never rejects: failures resolve to "".
function sessionStartContext(ctx, meta, sessionID) {
  if (state.sessionCtx.has(sessionID)) return state.sessionCtx.get(sessionID);
  const started = (async () => {
    try {
      const payload = { session_id: sessionID, source: "startup" };
      const out = await runFlow(meta, sessionID, ["hook", "session-start"], payload);
      return hookContext(out.stdout);
    } catch (err) {
      log("session-start hook failed:", sessionID, err);
      return "";
    }
  })();
  state.sessionCtx.set(sessionID, started);
  return started;
}

// psCommands runs one `ps -axo command` and returns its output lines, or null
// when ps itself failed. Callers decide what a failure means: the live guard
// fails open, the attachment scan fails closed.
async function psCommands() {
  const ps = await spawnCollect(["ps", "-axo", "command"], { stdout: "pipe", stderr: "ignore" }, PS_TIMEOUT_MS);
  if (ps.code !== 0) return null;
  return ps.stdout.split("\n");
}

// opencodeClients keeps only the ps lines whose argv0 is the opencode binary,
// so unrelated processes that happen to carry a session id are ignored.
function opencodeClients(lines) {
  return (lines || []).filter((line) => {
    const argv0 = line.trim().split(/\s+/)[0] || "";
    return argv0.split("/").pop() === "opencode";
  });
}

// clientLive is flow's live-guard ps check: is an opencode client attached?
// Errors fail open, like the rest of flow's live checks.
async function clientLive(sessionID) {
  const lines = await psCommands();
  if (lines === null) return true;
  const re = new RegExp(`(?:^|\\s)(?:-s|--session)(?:\\s+|=)${sessionID}(?=\\s|$)`);
  return opencodeClients(lines).some((line) => re.test(line));
}

// attachedSessions is the set of session ids carried by live opencode clients
// (`opencode -s ses_…` / `--session ses_…`). It is what keeps the inbox relay
// alive for a session that was resumed without a prompt, or whose listener
// died with a plugin reload: no turn starts, so no started event fires.
// A ps failure yields an empty set — this scan is a repair path, not a gate.
async function attachedSessions() {
  const ids = new Set();
  const lines = await psCommands();
  const re = /(?:^|\s)(?:-s|--session)(?:\s+|=)(ses_[0-9A-Za-z]{26})(?=\s|$)/g;
  for (const line of opencodeClients(lines)) {
    for (const match of line.matchAll(re)) ids.add(match[1]);
  }
  return ids;
}

// ensureListener starts the per-session inbox relay once: mail is steered
// into the conversation while a client is live, then acked; with no live
// client it stays unread for the next session.
function ensureListener(ctx, meta, sessionID, stopping) {
  if (state.listeners.has(sessionID)) return;
  const delivered = new Set(); // injected but not acked yet
  const loop = (async () => {
    try {
      while (!stopping.value) {
        if (!(await clientLive(sessionID))) return;
        const popArgs = ["inbox", "pop", "--wait", "--timeout", "300", "--keep-unread", "--json"];
        const popped = await runFlow(meta, sessionID, popArgs, undefined, LISTENER_TIMEOUT_MS);
        if (stopping.value) return;
        if (popped.code === 3) return; // task closed
        if (popped.code !== 0) { await sleep(5_000); continue; }
        const out = (popped.stdout || "").trim();
        if (!out) continue; // wait timed out, re-arm
        let id = "";
        try { id = JSON.parse(out)?.id || ""; } catch { /* not JSON — no message */ }
        if (!id) continue;
        if (delivered.has(id)) { await sleep(2_000); continue; } // ack failed earlier
        if (!(await clientLive(sessionID))) return; // leave the mail unread
        await ctx.session.prompt({ sessionID, text: "[flow inbox] " + out, delivery: "steer" });
        delivered.add(id);
        await runFlow(meta, sessionID, ["inbox", "read", id, "--json"]);
      }
    } catch (err) {
      log("inbox listener stopped:", sessionID, err);
    } finally {
      state.listeners.delete(sessionID);
    }
  })();
  state.listeners.set(sessionID, loop);
}

async function onExecutionStarted(ctx, sessionID, stopping) {
  const meta = await flowMeta(ctx, sessionID);
  if (!meta) return;
  void sessionStartContext(ctx, meta, sessionID);
  ensureListener(ctx, meta, sessionID, stopping);
}

async function onPrompt(ctx, event) {
  const sessionID = event?.sessionID;
  if (!sessionID) return;
  const meta = await flowMeta(ctx, sessionID);
  if (!meta) return;
  const payload = { session_id: sessionID, prompt: event?.prompt?.text ?? "" };
  const submitted = await runFlow(meta, sessionID, ["hook", "user-prompt-submit"], payload);
  const context = hookContext(submitted.stdout);
  if (context) state.turnCtx.set(sessionID, context);
  else state.turnCtx.delete(sessionID);
}

// onExecutionSucceeded is the Stop hook: a nudge it produces is queued as
// the next prompt, and the turn that prompt starts is marked so the same
// nudge cannot fire again on its own continuation.
//
// The dedupe must stay ahead of the first `await`: every loaded instance
// receives the same succeeded event, and an await yields to the sibling
// instances (which share this global state) before the mark is recorded —
// that is exactly how N instances end up running `flow hook stop` N times.
async function onExecutionSucceeded(ctx, sessionID) {
  const now = Date.now();
  const seen = state.stopSeen.get(sessionID);
  if (seen !== undefined && now - seen < STOP_DEDUPE_MS) return;
  state.stopSeen.set(sessionID, now);

  const meta = await flowMeta(ctx, sessionID);
  if (!meta) return;
  const active = state.nudged.has(sessionID);
  state.nudged.delete(sessionID);
  const payload = { session_id: sessionID, stop_hook_active: active };
  const stopped = await runFlow(meta, sessionID, ["hook", "stop"], payload);
  const context = hookContext(stopped.stdout);
  if (!context) return;
  state.nudged.add(sessionID);
  await ctx.session.prompt({ sessionID, text: "[flow] " + context, delivery: "queue" });
}

function eventSessionID(e) {
  return e?.data?.sessionID ?? e?.properties?.sessionID ?? e?.data?.id ?? null;
}

export default {
  id: "flow",
  async setup(ctx) {
    const stopping = { value: false };
    const subscription = new AbortController();
    let scanTimer = null;
    const cleanup = () => {
      stopping.value = true;
      try { subscription.abort(); } catch (err) { log("subscription abort failed:", err); }
      clearInterval(scanTimer);
    };

    try {
      await ctx.session.hook("prompt", async (event) => {
        try { await onPrompt(ctx, event); } catch (err) { log("prompt hook failed:", err); }
      });
    } catch (err) {
      log("prompt hook registration failed:", err);
    }

    // The context hook runs before every model call of the agent loop. The
    // pushed text is not persisted between calls, so push on every fire.
    try {
      await ctx.session.hook("context", async (e) => {
        try {
          const sid = e?.sessionID;
          if (!sid) return;
          const meta = await flowMeta(ctx, sid);
          if (!meta) return;
          if (!Array.isArray(e?.system)) return;
          const start = await sessionStartContext(ctx, meta, sid);
          if (start) e.system.push({ type: "text", text: "<flow-session>\n" + start + "\n</flow-session>" });
          const turn = state.turnCtx.get(sid);
          if (turn) e.system.push({ type: "text", text: "<flow-turn>\n" + turn + "\n</flow-turn>" });
        } catch (err) { log("context hook failed:", err); }
      });
    } catch (err) {
      log("context hook registration failed:", err);
    }

    // Subscribe in the background so setup returns promptly.
    void (async () => {
      try {
        for await (const event of ctx.event.subscribe({ signal: subscription.signal })) {
          try {
            const sessionID = eventSessionID(event);
            if (!sessionID) continue;
            const type = event?.type;
            if (type === "session.execution.started") await onExecutionStarted(ctx, sessionID, stopping);
            if (type === "session.execution.succeeded") await onExecutionSucceeded(ctx, sessionID);
          } catch (err) {
            log("event failed:", event?.type, err);
          }
        }
      } catch (err) {
        if (!stopping.value) log("event subscription failed:", err);
      }
    })();

    // Attached-client scan: a resumed session (`opencode -s <id>` with no
    // prompt) and a plugin reload both leave no listener behind, because no
    // turn ever starts. The scan repairs that from the process table. The
    // `lastScan` check must stay ahead of the first await so sibling plugin
    // instances share one ps scan per interval.
    const scan = async () => {
      try {
        const now = Date.now();
        if (now - (state.lastScan || 0) < SCAN_INTERVAL_MS - 1_000) return;
        state.lastScan = now;
        const ids = await attachedSessions();
        for (const id of ids) {
          if (stopping.value) return;
          if (state.listeners.has(id)) continue;
          // flowMeta caches null for non-flow sessions, so unrelated
          // opencode sessions cost one session.get each, once.
          const meta = await flowMeta(ctx, id);
          if (!meta) continue;
          void sessionStartContext(ctx, meta, id);
          ensureListener(ctx, meta, id, stopping);
        }
      } catch (err) {
        log("attachment scan failed:", err);
      }
    };
    scanTimer = setInterval(() => { void scan(); }, SCAN_INTERVAL_MS);
    void scan();

    return cleanup;
  },
};
