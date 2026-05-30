# UI Redesign — "Operator Console" + Vite/Preact migration

**Date:** 2026-05-30
**Branch:** `redesign/operator-console`
**Status:** Design locked (validated via interactive prototype); ready for implementation planning.

## Goal

Redesign the `flow` Mission Control web UI to be modern, fluid, human, and uncluttered, with first-class dark **and** light modes — while:

1. **Keeping all existing functionality and UI logic identical** (no behavior changes, no IA changes, no feature add/removal).
2. **Keeping the backend untouched** (Go server, all `/api/*` endpoints, data shapes).
3. **Moving to a better frontend framework/build** (off in-browser Babel) **without compromising UI performance** (it should get faster).
4. Reusing the **existing brand** (indigo `#645df6` + teal `#5eead4`, JetBrains Mono + Plus Jakarta Sans, the `flow·` mark/wordmark).

Non-negotiable: **make no mistakes** — every stage is verified against the baseline before proceeding.

## Locked visual design — "Operator Console"

Chosen over "Studio" (warm) and rejected "Aurora Glass" (slop). Interactive reference prototype:
`/.superpowers/brainstorm/86707-1780150493/content/prototype-v2.html` (gitignored; the CSS there is the visual source of truth to port).

**Aesthetic principles (anti-"AI-slop", per user):**
- Hairlines + whitespace as *structure*; retire ~80% of borders/boxes.
- "Data without boxes" — metrics render as bare type (big mono number + tracked label), not bordered stat cards.
- Monospace **only** for machine data (slugs, paths, branches, timestamps, counts, IDs, code); Plus Jakarta Sans for all human text.
- One restrained accent moment per view. No glass, no gradient blobs/glows, no gradient-filled buttons everywhere, no over-rounding, no emoji icons, no centered-hero-with-3-cards.
- Density done *well* — it's a power-user tool; don't dumb it down with empty space.
- Status as a 2px tick / small glyph + tracked-mono word, not a filled pill in its own cell.

**Component consolidation (also reduces code):** one `AgentRow` (Mission Control + Sessions), one borderless `DataTable` (Tasks/Project-detail/Trash), one `DetailHeader` (Task/Project/Playbook detail), one master-detail `Split` (Inbox/Memories/KB).

### Design tokens (the system)

Single `:root` token set (dark) + `[data-theme="light"]` override. JSX never branches on theme — it consumes `var(--*)`.

**Dark:** `--bg #0d0d10 · --surface #15151a · --surface-2 #1c1c22 · --line #1e1e25 · --line-2 #2a2a33 · --ink #e8e8ec · --ink-2 #a4a4ae · --ink-3 #76767f · --ink-4 #52525b · --indigo #645df6 · --indigo-hi #8b87f8 · --teal #5eead4 · --run #39b87a · --wait #e0a44c · --idle #6a6a74 · --stale #c08a44 · --err #d9684f`

**Light (re-tuned for contrast, not inverted):** `--bg #f7f6f3 (warm paper) · --surface #fff · --surface-2 #f1efe9 · --line #eceae3 · --line-2 #ddd9d0 · --ink #1a1a1e · --ink-2 #56544d · --ink-3 #807d74 · --ink-4 #a8a59c · --indigo #564fe0 · --teal #0f9e8a · --run #1f9d5f · --wait #b9772a · --stale #9c7430 · --err #cf3d3d`

Also tokenize: radius (6/10/14), shadow (sm/lg), an 8px spacing rhythm, type scale, transitions. Type base 14px (was 13.5), metadata 11.5–12px (was 10.5).

**Theme behavior:** follow OS on first load; manual toggle (`☀ / ◐ / ☾`) persists to `localStorage` (`flow.theme`). Replaces the hardcoded `data-theme="dark"` in the app shell.

## Current architecture (facts — must be preserved)

- **Embed/serve:** `internal/server/server.go` → `//go:embed all:static` embeds `internal/server/static/`. Handler serves `static/<path>` with SPA fallback to `index.html`. The committed `static/` dir is what ships in the binary.
- **Data:** `/api/ui-data.js` emits `window.FLOW_BOOTSTRAP = <JSON>` (handler `handleUIDataJS`, builder `ui_data.go`). Loaded before app code; merged into `window.MC` via `Object.assign`. Live updates via `/api/events` (SSE), `/api/inbox` (fetch), `/api/actions` (POST). **None of this changes.**
- **Runtime today:** React 18 **dev** builds + react-dom dev + `@babel/standalone` + lucide v1.14 + xterm(+fit,+unicode11) loaded as `<script>`s; then 3 `type="text/babel"` files + an inline `type="text/babel"` block transpiled in the browser on every load.
- **Module coupling (the port's crux):** classic-script shared global scope. Layering:
  - `ebaab09c` (508 ln): React-hook re-exports, ClockContext (`ClockCtx`/`ClockProvider`), formatters (`formatAge`, `formatActivity`, `fmtTokens`, `shortUUID`), fallback mock data; assigns `window.MC`; merges `window.FLOW_BOOTSTRAP`.
  - `dfbb0627` (851 ln): primitives — `Icon` (reads `window.lucide.icons`), `FlowMark/FlowLogo/FlowLoader`, `Dot`, `StatusPill`, `TaskStatePill`, `PriorityPill`, `AgentChip`, `ProviderMark`, `BranchChip`, `PixelIndicator`, `Sparkline`, `ActivityHeatmap`, `AgentTile`, `TranscriptView`, `DependencyBadges`, `FocusDrawer`, `SkeletonRows`.
  - `c906f42d` (5258 ln): 15 screens (`MissionControl`, `SessionsGrid`, `SessionDetail`, `CompletedSessionView`, `TasksList`, `TaskDetail`, `ProjectsList`, `ProjectDetail`, `PlaybooksList`, `PlaybookDetail`, `TrashView`, `KBView`, `MemorySourcesView`, `WorkdirsView`, `InboxView`) + 6 overlays (`CommandPalette`, `QRModal`, `ConfirmModal`, `ShortcutsOverlay`, `CreateFlowModal`, `CreateProjectModal`) → `window.MC_SCREENS`.
  - inline block: `App`, `Root`, `NotificationsBell`, routing (`routeFromLocation`/`pathForRoute`, history API), data refresh, SSE wiring, mounts `ReactDOM.createRoot(#root).render(<Root/>)`.
- **External globals:** `window.lucide.icons[...]`, `Terminal`/`FitAddon`/`Unicode11Addon` (xterm, used imperatively in session views), `window.FLOW_BOOTSTRAP`. localStorage: `flow.gitDiffOpen`, `flow.artifactsOpen` (+ new `flow.theme`). Custom events: `flow-terminal-restart`, `flow:toast`, `flow:ui-data:refresh`. React 18 `createRoot`; `<>` fragments used.

## Framework decision

**esbuild ahead-of-time build + Preact via a global compat shim; transpile the existing files in place, preserving their global-scope structure (no ES-module rewrite). React-prod is a one-line fallback. No TypeScript now.**

Key enabler: the app references `React`/`ReactDOM` as **globals** (`React.createElement`, hooks destructured from `React`, `ReactDOM.createRoot`) and never `import`s them. So a tiny bundled shim (`src/runtime.js`) sets `window.React = preact/compat` and `window.ReactDOM = {…compat, createRoot}` **before** the app scripts run; the existing `.js` files are transpiled JSX→JS by esbuild (`jsxFactory: React.createElement`, `jsxFragment: React.Fragment`) and minified, but keep their current shared global scope (no imports/exports added). This deletes `@babel/standalone` and swaps dev-React for prod-Preact with **zero logic restructuring** — the safest possible route to "keep logic identical".

Rationale: app uses only core hooks (`useState`×115, `useEffect`×37, `useMemo`, `useRef`, `createContext`, `useCallback`, one `createElement`) — no `Suspense`/`lazy`/`forwardRef`/portals — all in Preact/compat. Removes BOTH perf taxes (in-browser Babel transpile + dev-mode React); prod Preact runtime is ~3KB vs dev React-DOM ~1MB+. If Preact regresses anything, the shim points at prod `react`/`react-dom` instead (one-line) — still a big win. esbuild chosen over a full Vite module port because the global-scope-preserving transform avoids touching 6,700 lines of logic (Vite's bundler assumes ES modules → would force that risky rewrite). TypeScript deferred (mid-port type churn fights "no mistakes").

Rejected: full Vite ES-module split (unnecessary 6,700-line rewrite risk); Svelte/Solid rewrite (hand-porting all logic → behavior drift).

## Migration architecture

- **Source layout:** new `internal/server/ui/` build project: `package.json` (dev-deps `esbuild` + `preact`), `build.mjs` (the build script), `src/runtime.js` (Preact→global-React/ReactDOM shim), `src/main.jsx` (the inline `index.html` block, extracted verbatim). The three existing app files are transpiled in place from `static/assets` (not moved during S1) → new outputs; they're moved into `src/` and the originals deleted only at S5 cleanup. Token/component CSS lives in `src/styles/app.css` (ported from the prototype) and is emitted to `static/assets`.
- **build.mjs outputs (committed `static/assets`):** `app.runtime.js` = esbuild **bundle** of `src/runtime.js` (`format:'iife'`, minified) — imports `preact/compat`, sets `window.React`/`window.ReactDOM` (incl. a `createRoot` that calls preact `render`). `app.data.js`/`app.primitives.js`/`app.screens.js`/`app.main.js` = esbuild **transform** (no bundling, no format wrapping → global scope preserved) of the 4 source files, JSX-lowered + minified. `app.css` = the design system. Babel-standalone + dev React/React-DOM scripts are removed from `index.html`.
- **index.html (hand-edited, then committed):** load order = `app.runtime.js` (classic, first) → lucide → xterm(+addons) → `/api/ui-data.js` → `app.data.js` → `app.primitives.js` → `app.screens.js` → `app.main.js`; `<link rel="stylesheet" href="/assets/app.css">`. All classic scripts (deterministic in-order execution). Integrity attrs dropped (same-origin embedded assets).
- **Embed unchanged:** `//go:embed all:static` still picks up the committed `static/`. **Plain `go build` keeps working without Node** (the built assets are committed, exactly like today). Only regenerating the UI needs Node.
- **Makefile:** add a standalone `make ui` (`cd internal/server/ui && pnpm install && node build.mjs`). `make build`/`go build` stay **Node-free** (use committed assets) — `make ui` is run only after editing UI source, exactly mirroring the repo's existing "static is committed" model. `.gitignore` adds `internal/server/ui/node_modules` only (built `static/assets` stays committed).
- **Data/bootstrap:** `/api/ui-data.js` (→ `window.FLOW_BOOTSTRAP`) still loads before the app scripts; the existing merge into `window.MC` is untouched. SSE/fetch/actions unchanged.
- **xterm + lucide:** both stay external `<script>`s (imperative `Terminal` global + `window.lucide.icons` usage preserved verbatim) — zero risk; optional npm migration is a later pass.

## Staged build sequence (each stage has a verification gate)

- **S0 — Baseline (DONE):** branch created; `make build` green; `go test ./...` all pass. Reference for parity.
- **S1 — Build pipeline + Preact, logic-identical (NO visual change):** scaffold `internal/server/ui`; write `runtime.js` shim + `build.mjs`; extract the inline `index.html` block verbatim into `src/main.jsx`; transpile the 4 app files unchanged; emit `app.*.js`; rewrite `index.html` script tags (drop Babel + dev-React, add `app.runtime.js` + transpiled files in order). **Gate:** app boots on Preact, all 14 views render, routing/back/deep-links, data load + SSE refresh, terminal attach, all 6 modals, command palette, notifications — behave exactly as `static/` on `main`. Visuals byte-for-byte unchanged (CSS not yet touched). `make build` + `go test ./...` green; verify in a real browser (Playwright).
- **S2 — Fallback check:** confirm Preact parity; if any regression found, point the shim at prod `react`/`react-dom` (one-line) and re-verify — still a valid win. Record bundle-size + load-time delta vs baseline.
- **S3 — Operator Console restyle:** replace the design tokens + component CSS with the ported prototype system; add `[data-theme="light"]` + follow-system toggle (remove hardcoded dark); update component markup/classNames to the new language (AgentRow, DataTable, DetailHeader, Split, metrics-without-boxes, hairlines) **without changing props, state, handlers, or data flow**. **Gate:** per-view visual review in dark + light against the prototype; every interaction still works; no console errors.
- **S4 — Performance validation:** confirm no in-browser Babel; production Preact; measure bundle size + first-paint/TTI vs baseline (must be ≥ as fast, expected much faster); check theme switch has no layout shift; responsive at narrow widths.
- **S5 — Finalize:** `go test ./...` green; `make build` green from clean; manual end-to-end pass; Makefile/.gitignore/docs updated; commit on branch.

## Out of scope

Backend/API changes; data-shape changes; information-architecture or navigation changes; new features; TypeScript adoption; removing functionality. Skill file (`internal/app/skill/SKILL.md`) interactions unchanged.

## Parity / verification checklist (run at S1, S3, S5)

Per view (14) + overlays (6): renders, all controls respond, data binds from `FLOW_BOOTSTRAP`/SSE, navigation + breadcrumb + history back/forward, deep-link routes resolve, terminal (xterm) attaches & streams, git-diff/artifacts panels + their `localStorage` persistence, notifications bell + inbox counts, theme persists across reload + follows OS on first load, no console errors, `go test ./...` green, `make build` green without manual steps.
