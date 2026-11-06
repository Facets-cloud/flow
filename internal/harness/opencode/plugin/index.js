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
// Plugin-side `ctx.session.prompt` is a silent no-op on opencode 2.0.32, so
// every delivery goes through `opencode api session.prompt` instead.
//
// Event -> flow call mapping:
//
//   session.execution.started    -> `flow hook session-start` (first per session)
//                                -> starts the per-session inbox listener
//   attached client (ps scan, 15 s) -> starts the inbox listener (covers resumed
//                                   sessions and plugin reloads, where no turn
//                                   ever starts)
//   session.hook("prompt")       -> `flow hook user-prompt-submit`
//   session.hook("context")      -> pushes the cached session-start and turn
//                                   context into every model call
//   session.execution.succeeded  -> `flow hook stop`
//   inbox listener               -> polls `flow inbox --json` every 3 s while a
//                                   client is attached, then claims the oldest
//                                   with `flow inbox pop --json` only right
//                                   before injecting a `[flow inbox]` steer prompt
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
//
// Reloads: `flow` rewrites this file in place, so opencode re-evaluates the
// module without ever calling the old instance's cleanup. Each evaluation gets
// a fresh `GEN`; anything an older generation started (inbox loops, the event
// subscription, the scan timer, registered hooks) sees `state.gen !== GEN` and
// exits before claiming a message, leaving the mail pending for the current
// generation's listener.
// Session-scoped caches are dropped on load because the hook output format may
// have changed with the code.

const HOOK_TIMEOUT_MS = 20_000;
const PS_TIMEOUT_MS = 5_000;
const STOP_DEDUPE_MS = 3_000; // window that collapses duplicated succeeded events
const SCAN_INTERVAL_MS = 15_000; // attachment scan period (resumed sessions, reloads)
const POLL_MS = 3_000; // inbox listener poll period while a client is attached
const INJECT_TIMEOUT_MS = 30_000; // `opencode api session.prompt` budget
const INJECT_RETRY_MS = 3_000; // pause between delivery attempts of a claimed message
const NEGATIVE_TTL_MS = 60_000; // re-check a session that had no flow metadata after this

// One instance is loaded per directory and every instance sees every server
// event, so state lives on globalThis and is deduped per session.
const state = (globalThis.__flowOpencode ??= {
  meta: new Map(), // sessionID -> flow meta | null (cached)
  metaMissAt: new Map(), // sessionID -> Date.now() of the cached "no flow meta" answer
  sessionCtx: new Map(), // sessionID -> Promise<string> of the session-start context
  turnCtx: new Map(), // sessionID -> user-prompt-submit context for the current turn
  listeners: new Map(), // sessionID -> { gen, loop } inbox listener
  nudged: new Set(), // last turn came from a stop nudge
  stopSeen: new Map(), // sessionID -> Date.now() of the last handled succeeded event
  lastScan: 0, // Date.now() of the last attachment scan (cross-instance dedupe)
  gen: "", // generation of the module evaluation that owns this state
});

// An older plugin version may already have published a state object without
// the newer fields, so backfill every one before anything reads it.
state.meta ??= new Map();
state.metaMissAt ??= new Map();
state.started ??= new Set();
state.sessionCtx ??= new Map();
state.turnCtx ??= new Map();
state.listeners ??= new Map();
state.nudged ??= new Set();
state.stopSeen ??= new Map();
state.lastScan ??= 0;
state.gen ??= "";

// Every evaluation of this module (initial load, in-place rewrite) gets a new
// generation. opencode re-evaluates without calling the previous cleanup, so
// the load itself is the only place an old generation can be retired: drop its
// listeners (their loops exit on their next alive() check), its per-session
// caches (the hook output format may have changed), and the scan timestamp so
// this generation repairs the listeners immediately instead of in 15 s.
const GEN = Date.now() + ":" + Math.random().toString(36).slice(2);
state.gen = GEN;
for (const [sessionID, entry] of state.listeners) {
  if (entry?.gen !== GEN) state.listeners.delete(sessionID);
}
state.sessionCtx.clear();
state.meta.clear();
state.metaMissAt.clear();
state.lastScan = 0;

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

// opencodeBin is the argv0 of the inject CLI: the server's own binary when it
// runs as the compiled opencode executable, otherwise `opencode` from PATH.
function opencodeBin() {
  const base = (process.execPath || "").split("/").pop();
  return base === "opencode" ? process.execPath : "opencode";
}

// injectPrompt delivers text into a live session via the opencode CLI.
// Plugin-side `ctx.session.prompt` resolves without queueing anything on
// opencode 2.0.32, so the CLI is the only path that actually runs the turn.
// It returns true only when the CLI exited 0 and printed the queued message
// (`{ data: { id: "msg_…" } }`); it logs and returns false otherwise, so the
// caller can leave the mail unread. It never throws.
async function injectPrompt(sessionID, text, delivery) {
  try {
    const argv = [
      opencodeBin(),
      "api",
      "session.prompt",
      "--param",
      "sessionID=" + sessionID,
      "-d",
      JSON.stringify({ text, delivery }),
    ];
    const options = { env: { ...process.env }, stdin: "ignore", stdout: "pipe", stderr: "ignore" };
    const res = await spawnCollect(argv, options, INJECT_TIMEOUT_MS);
    if (res.code !== 0) {
      log("inject failed:", sessionID, "exit", res.code, (res.stdout || "").trim());
      return false;
    }
    let id = "";
    try { id = JSON.parse(res.stdout)?.data?.id || ""; } catch { /* not JSON */ }
    if (typeof id !== "string" || id === "") {
      log("inject failed:", sessionID, "unexpected output", (res.stdout || "").trim());
      return false;
    }
    return true;
  } catch (err) {
    log("inject failed:", sessionID, err);
    return false;
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

// flowMeta reads the flow metadata prepared on the session. Positive
// answers stay cached for the plugin's lifetime; a "no flow metadata"
// answer is cached only for NEGATIVE_TTL_MS, because a session can be bound
// later (`flow do --here` from inside it) and must then activate the plugin
// without a server restart. Without the TTL a non-flow session would stay
// dead for the whole server lifetime.
async function flowMeta(ctx, sessionID) {
  if (state.meta.has(sessionID)) return state.meta.get(sessionID);
  const missedAt = state.metaMissAt.get(sessionID);
  if (missedAt !== undefined && Date.now() - missedAt < NEGATIVE_TTL_MS) return null;
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
  if (meta) {
    state.meta.set(sessionID, meta);
    state.metaMissAt.delete(sessionID);
    return meta;
  }
  state.metaMissAt.set(sessionID, Date.now());
  return null;
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

// ensureListener starts the per-session inbox relay once. It polls the
// NON-claiming unread list (`flow inbox --json`) while a client is attached
// and claims a message with `flow inbox pop` only at the last moment, right
// before injecting it: a pop is a permanent claim (pending -> delivered, no
// un-claim), so claiming any earlier loses the mail if this loop is
// superseded by a reload or the client goes away. An entry left by another
// generation is not "started" — its loop is already exiting without
// claiming — so the current generation replaces it.
function ensureListener(meta, sessionID, stopping) {
  const existing = state.listeners.get(sessionID);
  if (existing && existing.gen === GEN) return;
  // alive() gates every point where this generation could still act on the
  // world: after a reload the loop must exit before claiming, so the mail
  // stays pending for the generation that now owns the session.
  const alive = () => !stopping.value && state.gen === GEN;
  const loop = (async () => {
    try {
      while (alive()) {
        if (!(await clientLive(sessionID))) return; // no client: mail stays pending
        const listed = await runFlow(meta, sessionID, ["inbox", "--json"]);
        if (!alive()) return; // superseded: no claim was made
        if (listed.code !== 0) { await sleep(5_000); continue; }
        let pending = [];
        try { pending = JSON.parse(listed.stdout || "[]"); } catch { /* not JSON */ }
        if (!Array.isArray(pending) || pending.length === 0) { await sleep(POLL_MS); continue; }
        if (!(await clientLive(sessionID)) || !alive()) return;
        // Claim the oldest message. From the next line on the message is
        // claimed, so it must always be delivered — even if superseded.
        const popped = await runFlow(meta, sessionID, ["inbox", "pop", "--json"]);
        if (popped.code === 3) return; // task closed: stop listening
        if (popped.code !== 0) { await sleep(POLL_MS); continue; } // raced / empty
        const out = (popped.stdout || "").trim();
        let id = "";
        try { id = JSON.parse(out)?.id || ""; } catch { /* not JSON — no message */ }
        if (!id) { await sleep(POLL_MS); continue; }
        let ok = await injectPrompt(sessionID, "[flow inbox] " + out, "steer");
        for (let i = 0; !ok && i < 5; i++) {
          await sleep(INJECT_RETRY_MS);
          ok = await injectPrompt(sessionID, "[flow inbox] " + out, "steer");
        }
        if (!ok) log("inbox message claimed but not delivered:", sessionID, id);
      }
    } catch (err) {
      log("inbox listener stopped:", sessionID, err);
    } finally {
      // Only retire our own entry: a newer generation may already own the slot.
      if (state.listeners.get(sessionID)?.gen === GEN) state.listeners.delete(sessionID);
    }
  })();
  state.listeners.set(sessionID, { gen: GEN, loop });
}

async function onExecutionStarted(ctx, sessionID, stopping) {
  const meta = await flowMeta(ctx, sessionID);
  if (!meta) return;
  if (state.gen !== GEN) return; // a reload landed while session.get was in flight
  void sessionStartContext(ctx, meta, sessionID);
  ensureListener(meta, sessionID, stopping);
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
  const ok = await injectPrompt(sessionID, "[flow] " + context, "queue");
  if (!ok) return; // not delivered — keep the session unmarked so the next stop retries
  state.nudged.add(sessionID);
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
        if (state.gen !== GEN) return; // a newer generation owns the session
        try { await onPrompt(ctx, event); } catch (err) { log("prompt hook failed:", err); }
      });
    } catch (err) {
      log("prompt hook registration failed:", err);
    }

    // The context hook runs before every model call of the agent loop. The
    // pushed text is not persisted between calls, so push on every fire.
    try {
      await ctx.session.hook("context", async (e) => {
        if (state.gen !== GEN) return; // a reload replaced this hook's code
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

    // Subscribe in the background so setup returns promptly. The loop also
    // exits on its own when a reload supersedes this generation, because
    // opencode never calls the previous instance's cleanup.
    void (async () => {
      try {
        for await (const event of ctx.event.subscribe({ signal: subscription.signal })) {
          if (state.gen !== GEN) break; // superseded by a newer module evaluation
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
      if (state.gen !== GEN) { clearInterval(scanTimer); return; } // retired by a reload
      try {
        const now = Date.now();
        if (now - (state.lastScan || 0) < SCAN_INTERVAL_MS - 1_000) return;
        state.lastScan = now;
        const ids = await attachedSessions();
        for (const id of ids) {
          if (state.gen !== GEN) return; // superseded mid-scan
          if (state.listeners.get(id)?.gen === GEN) continue;
          // flowMeta caches a negative answer for NEGATIVE_TTL_MS, so
          // unrelated opencode sessions cost at most one session.get per
          // minute and a later `flow do --here` bind is picked up.
          const meta = await flowMeta(ctx, id);
          if (!meta) continue;
          void sessionStartContext(ctx, meta, id);
          ensureListener(meta, id, stopping);
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
