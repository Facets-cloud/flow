// flow opencode TUI reporter.
//
// The server plugin (index.js) can see every session, but not which one the
// user is looking at: the TUI client's argv (`opencode -s <id>`) goes stale
// when the user switches sessions inside the TUI, and a bare `opencode`
// client that navigated to a flow session carries no id at all. This plugin
// runs inside the TUI client process and reports the on-screen session to
// ~/.local/state/flow/opencode-clients/<pid>.json every second (only when it
// changes). flow and the server plugin read those reports to decide liveness.
//
// GENERATED FILE — installed by `flow` (from the copy embedded in the flow
// binary) to ~/.config/opencode/flow-tui/tui.js, and registered in
// ~/.config/opencode/cli.json. Do not edit it here: `flow` rewrites it
// whenever the installed copy differs.
//
// A TUI plugin's default export is { id, setup(api) }; setup runs inside the
// TUI client process and returns a cleanup function. The route is polled
// because opencode has no reliable change event for it. This file must never
// throw into the TUI: every operation is wrapped.

import fs from "node:fs";
import os from "node:os";
import path from "node:path";

const REPORT_INTERVAL_MS = 1_000;

export default {
  id: "flow.tui",
  setup(api) {
    const pid = process.pid;
    const dir = path.join(os.homedir(), ".local", "state", "flow", "opencode-clients");
    const file = path.join(dir, `${pid}.json`);
    let timer = null;
    let wrote = false;
    let last = null;

    const remove = () => {
      try { fs.rmSync(file, { force: true }); } catch { /* best-effort */ }
    };

    const writeReport = (sessionID) => {
      try {
        fs.mkdirSync(dir, { recursive: true });
        const tmp = `${file}.${pid}.tmp`;
        fs.writeFileSync(tmp, JSON.stringify({ pid, sessionID, updated: Date.now() }), "utf8");
        fs.renameSync(tmp, file);
      } catch { /* never throw */ }
    };

    // rootOf walks parentID up to the root session (a child/subagent session
    // is not a task session). The seen set guards against a broken parent
    // cycle; a failed lookup keeps the id we already have.
    const rootOf = async (sessionID) => {
      const seen = new Set();
      let id = sessionID;
      while (id && !seen.has(id)) {
        seen.add(id);
        let info = null;
        try { info = await api.data.session.get(id); } catch { return id; }
        const parent = info?.parentID ?? info?.data?.parentID ?? null;
        if (typeof parent !== "string" || parent === "") return id;
        id = parent;
      }
      return id;
    };

    const tick = async () => {
      try {
        let sessionID = null;
        try {
          const route = api.ui.router.current();
          if (route && route.type === "session") {
            const sid = route.sessionID ?? route.params?.sessionID ?? null;
            if (typeof sid === "string" && sid !== "") sessionID = await rootOf(sid);
          }
        } catch {
          // Router API unavailable: write nothing, so readers fall back to
          // the client's argv instead of seeing "no session on screen".
          return;
        }
        if (wrote && sessionID === last) return;
        wrote = true;
        last = sessionID;
        writeReport(sessionID);
      } catch { /* never throw */ }
    };

    const onExit = () => remove();
    const stop = () => {
      try { if (timer) clearInterval(timer); } catch { /* never throw */ }
      timer = null;
      try { process.off("exit", onExit); } catch { /* never throw */ }
      remove();
    };

    try {
      process.on("exit", onExit);
      void tick();
      timer = setInterval(() => { void tick(); }, REPORT_INTERVAL_MS);
    } catch { /* never throw */ }

    return stop;
  },
};
