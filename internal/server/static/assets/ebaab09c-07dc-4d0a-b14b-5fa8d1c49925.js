// Mission Control — mock data + formatters + icons
const { useState, useEffect, useMemo, useRef, useContext, useCallback, Fragment } = React;
const { createContext } = React;

// ──────────────────────────────────────────────────────────────
// Mock data — Mission Control for `flow`
// ──────────────────────────────────────────────────────────────

const AGENTS = [
  {
    slug: 'image-vuln-v2',
    name: 'Roll out image-vuln scanner v2 to staging',
    project: 'image-vuln',
    branch: 'image-vuln/scanner-v2',
    work_dir: '~/facets/codebases/starboard',
    provider: 'claude',
    priority: 'high',
    status: 'running',           // running, waiting, idle, stale, dead
    session_id: '7c4f8a92-91ef-4d2a-89cc-c2a31ee04891',
    started_min: 47,
    last_activity_sec: 4,
    last_action: '$ go test ./internal/scan/...',
    diff: { add: 12, rem: 3, files: 4 },
    tokens_used: 84200,
    tokens_max: 200000,
    activity: [3,4,1,5,2,3,6,2,4,7,3,5,4,8,6,2,4,7,5,3,4,5,9,12,8,6,4,3,5,7,4,5,8,12,10,6,4,5,7,9,11,14,12,8,6,4,3,5,8,10,12,15,18,12,8,5,3,4,6,8],
    tags: ['scanner','infra'],
    summary: 'Wired Trivy null-result fix into parser; running tests',
    next_step: 'Tests passing → commit + push',
  },
  {
    slug: 'mc-tui',
    name: 'Build mission-control TUI (Bubble Tea)',
    project: 'internal-tools',
    branch: 'mc-tui/sketch',
    work_dir: '~/facets/temp/mc',
    provider: 'claude',
    priority: 'medium',
    status: 'running',
    session_id: 'a3b1c7d4-2eaa-43d8-8d0c-1a98b6f7af1b',
    started_min: 132,
    last_activity_sec: 12,
    last_action: 'Edit ui/agentlist.go',
    diff: { add: 47, rem: 18, files: 6 },
    tokens_used: 64800,
    tokens_max: 200000,
    activity: [5,3,4,8,12,15,10,6,4,2,3,5,7,9,11,14,16,12,8,5,3,4,6,8,10,12,15,18,14,9,6,4,5,7,8,10,12,14,16,18,15,12,8,6,5,7,9,11,13,15,16,14,11,8,5,3,4,6,8,10],
    tags: ['dx','tui'],
    summary: 'Refactored agent list rendering; adding key bindings',
    next_step: 'Bind a/x/y/n keys to actions',
  },
  {
    slug: 'caas-cutover-eu1',
    name: 'Cut over eu1 cluster to new control plane',
    project: 'caas-exit',
    branch: 'caas-exit/eu1',
    work_dir: '~/facets/codebases/raptor',
    provider: 'codex',
    priority: 'high',
    status: 'waiting',
    session_id: 'd8e2f4a1-c3b9-4f87-a2d5-9b1e7c8a3f02',
    started_min: 18,
    last_activity_sec: 47,
    last_action: 'Bash · awaiting approval',
    waiting_for: { kind: 'tool', cmd: 'helm upgrade --install caas-cp ./charts/cp -f values-eu1.yaml --wait', why: 'Will reconcile 4 CRDs in eu1 cluster, ~12min rollout' },
    diff: { add: 0, rem: 0, files: 0 },
    tokens_used: 22100,
    tokens_max: 200000,
    activity: [2,3,5,7,4,3,2,4,5,7,9,12,10,6,4,5,7,9,11,8,5,3,4,2,1,0,0,0,0,2,4,5,7,4,2,3,5,7,9,12,15,10,6,4,2,3,5,7,9,8,6,4,3,2,1,0,0,0,0,0],
    tags: ['infra','prod'],
    summary: 'Helm upgrade staged on eu1, pending approval',
    next_step: 'Approve to start rollout',
  },
  {
    slug: 'flow-serve',
    name: 'Design flow web dashboard',
    project: null,
    branch: 'flow-serve/wireframes',
    work_dir: '~/facets/codebases/flow',
    provider: 'claude',
    priority: 'high',
    status: 'idle',
    session_id: '2f1a9b6c-4d8e-4f02-91a7-c3b5d8e9a0f1',
    started_min: 312,
    last_activity_sec: 920,
    last_action: 'Wrote DESIGN.md (919 lines)',
    diff: { add: 919, rem: 0, files: 1 },
    tokens_used: 142000,
    tokens_max: 200000,
    activity: [12,15,18,14,8,5,3,4,2,3,5,4,6,8,10,12,15,18,16,12,8,5,4,3,5,7,9,11,14,18,22,18,14,10,6,4,3,2,1,0,0,1,2,3,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0],
    tags: ['design','meta'],
    summary: 'Architecture doc done, ready for review',
    next_step: 'Awaiting human review of /DESIGN.md',
  },
  {
    slug: 'trivy-empty-findings',
    name: 'Investigate Trivy empty findings on alpine:3.19',
    project: 'image-vuln',
    branch: 'image-vuln/trivy-null',
    work_dir: '~/facets/codebases/starboard',
    provider: 'claude',
    priority: 'medium',
    status: 'stale',
    session_id: 'b6a8c1e7-7f4a-4c3b-8d92-2e0f8a4c1b5d',
    started_min: 5760,           // 4 days
    last_activity_sec: 86400 * 3 + 7200,
    last_action: 'Read internal/scan/parser.go',
    diff: { add: 2, rem: 1, files: 1 },
    tokens_used: 18400,
    tokens_max: 200000,
    activity: [10,8,6,4,3,5,7,4,2,1,0,0,0,0,0,0,0,0,1,2,1,0,0,0,0,0,0,0,0,1,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0],
    tags: ['scanner','bug'],
    summary: 'Stalled — parked when image-vuln-v2 took priority',
    next_step: 'Promote to in-progress or archive',
  },
  {
    slug: 'ntt-doctor',
    name: 'Wire up NTT health-check command',
    project: 'internal-tools',
    branch: 'ntt-doctor/probes',
    work_dir: '~/facets/codebases/ntt',
    provider: 'claude',
    priority: 'low',
    status: 'idle',
    session_id: 'e9c4d2b8-3a5f-4e1c-9b0a-6d8c2e4f7a3b',
    started_min: 96,
    last_activity_sec: 1840,
    last_action: 'cobra: added probes subcommand',
    diff: { add: 78, rem: 4, files: 3 },
    tokens_used: 31200,
    tokens_max: 200000,
    activity: [4,5,7,9,11,8,6,4,3,5,7,9,12,15,11,8,6,4,3,4,5,7,9,11,14,16,12,8,5,3,4,2,1,0,0,0,1,2,4,3,5,7,4,2,3,1,0,0,0,0,0,0,0,0,0,0,0,0,0,0],
    tags: ['ntt','dx'],
    summary: 'Probes scaffolded; awaiting review feedback',
    next_step: 'Open PR',
  },
];

const DEAD_AGENT = {
  slug: 'pr128-fixes',
  name: 'Address review comments on PR 128',
  project: 'image-vuln',
  branch: 'image-vuln/pr128-fixes',
  work_dir: '~/facets/codebases/starboard',
  provider: 'claude',
  priority: 'medium',
  status: 'dead',
  session_id: '4f8c1a2e-5d9b-4a3c-b8e7-1f2d6a9c3e8b',
  started_min: 4320,
  last_activity_sec: 86400 * 2,
  last_action: 'session exited code 137',
  exit_reason: 'OOM (Claude killed by macOS, 11.4 GB RSS)',
  diff: { add: 23, rem: 5, files: 2 },
  tokens_used: 198000,
  tokens_max: 200000,
  activity: [3,5,7,4,2,1,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0],
  tags: ['scanner'],
  summary: 'Crashed mid-edit on parser_test.go',
  next_step: 'Inspect logs or restart',
};

const BACKLOG = [
  { slug: 'caas-cutover-us2', name: 'Cut over us2 cluster', project: 'caas-exit', priority: 'high', due: 'in 8d' },
  { slug: 'oauth-budget', name: 'Add OAuth to budgeting app', project: '(floating)', priority: 'medium', tags: ['#auth','#personal'] },
  { slug: 'flow-tmux', name: 'Evaluate tmux backend for flow', project: '(floating)', priority: 'medium' },
  { slug: 'digest-collision', name: 'Investigate digest collision report', project: 'image-vuln', priority: 'low', waiting_on: '@karthik repro' },
  { slug: 'cdn-edge-fail', name: 'CDN edge failures > 1% in apac', project: 'caas-exit', priority: 'medium' },
  { slug: 'flow-serve-build', name: 'Build flow serve from DESIGN.md', project: '(floating)', priority: 'high' },
];

const DONE_AGENTS = [];
const DONE_TASKS = [];

const PROJECTS_MC = [
  { slug: 'caas-exit', name: 'Migrate CaaS clusters off the legacy control plane', priority: 'high', tasks: { total: 7, in_progress: 1, backlog: 5, done: 1 } },
  { slug: 'image-vuln', name: 'Container image vulnerability remediation', priority: 'high', tasks: { total: 9, in_progress: 1, backlog: 2, done: 6 } },
  { slug: 'internal-tools', name: 'Personal developer-experience tooling', priority: 'medium', tasks: { total: 5, in_progress: 2, backlog: 1, done: 2 } },
];

const PLAYBOOKS_MC = [
  { slug: 'release-debug', name: 'Investigate a release failure', project: 'image-vuln', runs_week: 4, last_min: 132, spark: [0,1,2,0,1,0,0,1,2,1,0,1,0,0,0,2,1,0,1,0,1,0,0,1,2,1,0,1,0,1] },
  { slug: 'weekly-review', name: 'Friday weekly retrospective', project: null, runs_week: 1, last_min: 60 * 24 * 7, spark: [0,0,0,0,0,1,0,0,0,0,0,0,1,0,0,0,0,0,0,1,0,0,0,0,0,1,0,0,0,1] },
];

const KB_FILES = [
  {
    name: 'user.md',
    preview: '2026-04-29 — prefers terse confirmations',
    count: 22,
    entries: [
      { d: '2026-04-29', t: 'prefers terse confirmations over verbose explanations' },
      { d: '2026-04-21', t: 'works in iTerm with split panes; never in Terminal.app' },
      { d: '2026-04-12', t: 'monorepo is at ~/facets/codebases/raptor' },
      { d: '2026-04-03', t: 'reads slack notifications during deep work hours; do not summon' },
      { d: '2026-03-22', t: 'commits with conventional-commits format; squash-merges PRs' },
      { d: '2026-03-12', t: 'prefers async communication over meetings' },
      { d: '2026-02-28', t: 'codes in dark mode at all times' },
      { d: '2026-02-18', t: 'preferred editor: VS Code w/ vim bindings' },
      { d: '2026-02-04', t: 'sometimes uses Codex for one-off scripts; mostly Claude' },
    ],
  },
  {
    name: 'org.md',
    preview: '2026-04-15 — Facets infra team owns CaaS',
    count: 14,
    entries: [
      { d: '2026-04-15', t: 'Facets infra team owns CaaS migrations; ping @karthik for approvals' },
      { d: '2026-04-02', t: 'Friday afternoons are reserved for weekly review; no deploys' },
      { d: '2026-03-19', t: 'On-call rotations live in PagerDuty; no flow integration yet' },
      { d: '2026-02-20', t: 'Decisions over $5k spend require finance approval' },
      { d: '2026-02-04', t: 'Our customers are mostly mid-market SaaS' },
    ],
  },
  {
    name: 'products.md',
    preview: '2026-04-25 — Praxis AI = customer-facing AI',
    count: 18,
    entries: [
      { d: '2026-04-25', t: 'Praxis AI is the customer-facing AI surface; lives in apps/praxis' },
      { d: '2026-04-10', t: 'Starboard is the image-scanner; Trivy + Grype hybrid' },
      { d: '2026-03-15', t: 'NTT is the network-troubleshooting tool; shells through SSM' },
      { d: '2026-02-28', t: 'Kairos is the scheduler; cron-replacement for Facets ops' },
    ],
  },
  {
    name: 'processes.md',
    preview: '2026-04-22 — PR template requires test plan',
    count: 9,
    entries: [
      { d: '2026-04-22', t: 'PR template requires test plan + screenshots' },
      { d: '2026-04-08', t: 'Release notes go to #releases on Slack' },
      { d: '2026-03-30', t: 'Friday weekly review uses the weekly-review playbook' },
      { d: '2026-03-04', t: 'CaaS migrations require eu1 → us2 → ap1 ordering' },
    ],
  },
  {
    name: 'business.md',
    preview: '2026-04-18 — Q2 OKR: ship image-vuln v2',
    count: 11,
    entries: [
      { d: '2026-04-18', t: 'Q2 OKR: ship image-vuln v2 to all customers by June' },
      { d: '2026-04-03', t: 'Pricing tiers: starter ($0), team ($49/seat), platform (custom)' },
      { d: '2026-03-20', t: 'Top 3 customers by ARR are scoped on /private channels' },
      { d: '2026-02-12', t: 'Renewals run on a Feb/Aug cadence' },
    ],
  },
];

const WORKDIRS = [
  { path: '~/facets/codebases/flow', name: 'flow', remote: 'git@github.com:Facets-cloud/flow', used_min: 60, tasks: 3, untouched: false },
  { path: '~/facets/codebases/raptor', name: 'raptor', remote: 'git@github.com:Facets-cloud/raptor', used_min: 60, tasks: 2, untouched: false },
  { path: '~/facets/codebases/starboard', name: 'starboard', remote: 'git@github.com:Facets-cloud/starboard', used_min: 47, tasks: 3, untouched: false },
  { path: '~/facets/codebases/ntt', name: 'ntt', remote: 'git@github.com:Facets-cloud/ntt', used_min: 60 * 13, tasks: 1, untouched: false },
  { path: '~/facets/codebases/kairos', name: 'kairos', remote: 'git@github.com:Facets-cloud/kairos', used_min: 60 * 24 * 4, tasks: 0, untouched: false },
  { path: '~/facets/temp/mc', name: 'mc (sketch)', remote: null, used_min: 60 * 24 * 2, tasks: 1, untouched: false },
  { path: '~/old/facets/spike-2025-12', name: 'spike-2025-12', remote: null, used_min: 60 * 24 * 47, tasks: 0, untouched: true },
];

// Sample transcript for session detail
const SAMPLE_TRANSCRIPT = [
  { type: 'user', text: 'The image-vuln scanner is returning empty findings on alpine:3.19 even though Trivy reports CVE-2024-2961. Can you check the parser?' },
  { type: 'assistant', text: "I'll read the parser and see how it handles Trivy's output." },
  { type: 'tool_use', tool: 'Read', input: 'internal/scan/parser.go', lines: 142 },
  { type: 'tool_result', tool: 'Read', summary: 'parser.go · 142 lines · Go', preview: '12: type TrivyResult struct {\n13:   Results []TrivyFinding `json:"Results"`\n14: }\n...\n42: if len(r.Results) == 0 {\n43:   return nil, errors.New("scan failed: no results")\n44: }' },
  { type: 'thinking', text: 'The parser treats Results==nil as a failure. But Trivy emits `null` (not `[]`) when the scan succeeded with zero findings. So zero-vuln alpine images get falsely reported as scan failures upstream.' },
  { type: 'assistant', text: 'Found it. Line 42 treats `Results == nil` as a scan failure, but Trivy returns `null` (not `[]`) when there are zero findings. Empty findings != scan failure. I\'ll fix the check and add a regression test.' },
  { type: 'tool_use', tool: 'Edit', input: 'internal/scan/parser.go · line 42-44' },
  { type: 'tool_result', tool: 'Edit', summary: 'parser.go · -2 / +5', preview: '-if len(r.Results) == 0 {\n-  return nil, errors.New("scan failed: no results")\n-}\n+// nil Results means Trivy returned null — treat as zero findings, not failure.\n+findings := []Finding{}\n+for _, tr := range r.Results {\n+  findings = append(findings, tr.toCommon())\n+}' },
  { type: 'tool_use', tool: 'Write', input: 'internal/scan/parser_test.go (new)' },
  { type: 'tool_result', tool: 'Write', summary: 'parser_test.go · 28 lines (new)' },
  { type: 'tool_use', tool: 'Bash', input: '$ go test ./internal/scan/... -run Empty -v' },
  { type: 'tool_result', tool: 'Bash', summary: 'PASS · 0.094s', preview: '=== RUN   TestParseTrivyEmptyResults\n--- PASS: TestParseTrivyEmptyResults (0.00s)\nok  facets/scan  0.094s' },
  { type: 'assistant', text: 'Test passes. Want me to commit and push, or wait?' },
];

const SAMPLE_TERMINAL = [];

// Claude session — the real TUI shape: welcome banner, todo-list,
// streaming "thinking" indicator, tool-use call-outs, status footer.
const CLAUDE_TERMINAL = {
  banner: [
    { c: 'banner', text: '╭───────────────────────────────────────────────────────────╮' },
    { c: 'banner', text: '│  ✻ v1.0.42 · sonnet-4.5 · ~/facets/codebases/starboard  │' },
    { c: 'banner', text: '│     session: 7af3-9b21 · resumed · /help for shortcuts     │' },
    { c: 'banner', text: '╰───────────────────────────────────────────────────────────╯' },
    { c: 'space', text: '' },
  ],
  feed: [
    { c: 'user', text: '> Investigate why image-vuln scanner v2 returns no findings on alpine:3.19.\n  Trivy says null but we treat it as an error. Fix and add a regression test.' },
    { c: 'space', text: '' },
    { c: 'todo-head', text: '  ⏺ Update Todos' },
    { c: 'todo-done', text: '     ☒ Reproduce the empty-findings bug locally' },
    { c: 'todo-done', text: '     ☒ Read internal/scan/parser.go and find the null branch' },
    { c: 'todo-active', text: '     ◐ Patch parser to treat null Results as zero findings' },
    { c: 'todo', text: '     ☐ Add regression test in parser_test.go' },
    { c: 'todo', text: '     ☐ Run full scan suite to verify no other tests regress' },
    { c: 'space', text: '' },
    { c: 'tool', text: '  ⏺ Bash(go test ./internal/scan/... -run Empty -v)' },
    { c: 'tool-out', text: '    └─ === RUN   TestParseTrivyEmptyResults' },
    { c: 'tool-out', text: '       --- PASS: TestParseTrivyEmptyResults (0.00s)' },
    { c: 'tool-out', text: '       PASS' },
    { c: 'tool-out', text: '       ok  facets/scan  0.094s' },
    { c: 'space', text: '' },
    { c: 'assistant', text: '⏺ The fix lands. Trivy returns `Results: null` for images with no\n  layers it can scan, and we were treating that as a scan failure. Now\n  we emit an empty findings array. Adding the regression test next so\n  this doesn\u0027t come back.' },
  ],
  appends: [
    { c: 'space', text: '' },
    { c: 'tool', text: '  ⏺ Edit(internal/scan/parser_test.go)' },
    { c: 'tool-out', text: '    └─ +13 lines · created file' },
    { c: 'space', text: '' },
    { c: 'tool', text: '  ⏺ Bash(go test ./internal/scan/... -v 2>&1 | tail -20)' },
    { c: 'tool-out', text: '    └─ === RUN   TestParseTrivyAlpine' },
    { c: 'tool-out', text: '       --- PASS: TestParseTrivyAlpine (0.01s)' },
    { c: 'tool-out', text: '       === RUN   TestParseTrivyEmptyResults' },
    { c: 'tool-out', text: '       --- PASS: TestParseTrivyEmptyResults (0.00s)' },
    { c: 'tool-out', text: '       PASS' },
    { c: 'tool-out', text: '       ok  facets/scan  0.118s' },
    { c: 'space', text: '' },
    { c: 'todo-head', text: '  ⏺ Update Todos' },
    { c: 'todo-done', text: '     ☒ Reproduce the empty-findings bug locally' },
    { c: 'todo-done', text: '     ☒ Read internal/scan/parser.go and find the null branch' },
    { c: 'todo-done', text: '     ☒ Patch parser to treat null Results as zero findings' },
    { c: 'todo-done', text: '     ☒ Add regression test in parser_test.go' },
    { c: 'todo-active', text: '     ◐ Run full scan suite to verify no other tests regress' },
  ],
  footer: [
    { c: 'space', text: '' },
    { c: 'thinking', text: '✻ Thinking… (32s)' },
    { c: 'space', text: '' },
    { c: 'prompt-box', text: '╭─────────────────────────────────────────────────────────────────────╮' },
    { c: 'prompt-box', text: '│ >                                                                   │' },
    { c: 'prompt-box', text: '╰─────────────────────────────────────────────────────────────────────╯' },
    { c: 'status', text: '  ⏵⏵ auto-accept edits on (shift+tab to toggle)         ⎿ 24.3k tokens · esc to interrupt' },
  ],
};

// Codex CLI — different chrome: ChatGPT-style turn markers, no todo list.
const CODEX_TERMINAL = {
  banner: [
    { c: 'banner', text: '◇ codex · gpt-5-codex · workdir: ~/facets/codebases/raptor' },
    { c: 'banner', text: '◇ session 4d18-c702 · waiting for user input (1 ask pending)' },
    { c: 'space', text: '' },
  ],
  feed: [
    { c: 'user', text: 'user ▸ cut over the eu1 cluster to the new control plane. dry-run first.' },
    { c: 'space', text: '' },
    { c: 'assistant', text: 'codex ▸ Reading manifests in deploy/eu1/ and computing the diff against\n        the new control-plane chart. I will pause before any apply.' },
    { c: 'tool', text: '  ▸ shell: helm diff upgrade caas-eu1 ./charts/cp-v2 --values eu1.yaml' },
    { c: 'tool-out', text: '    - 12 manifests changed' },
    { c: 'tool-out', text: '    - 3 manifests added (PodMonitor, NetworkPolicy x2)' },
    { c: 'tool-out', text: '    - 1 manifest removed (legacy Service)' },
    { c: 'space', text: '' },
    { c: 'approval', text: '┌─ Approval required ────────────────────────────────────────────────┐' },
    { c: 'approval', text: '│  Action:  kubectl apply -f /tmp/codex-eu1.yaml --context=caas-eu1   │' },
    { c: 'approval', text: '│  Scope:   ✻ destructive · cluster: caas-eu1 (production)            │' },
    { c: 'approval', text: '│           [a] approve   [d] deny   [m] modify   [s] skip            │' },
    { c: 'approval', text: '└─────────────────────────────────────────────────────────────────────┘' },
  ],
  appends: [
    { c: 'space', text: '' },
    { c: 'dim', text: '  ⏸ awaiting your decision · idle 8m' },
    { c: 'dim', text: '  ⏸ awaiting your decision · idle 8m 3s' },
    { c: 'dim', text: '  ⏸ awaiting your decision · idle 8m 6s' },
  ],
  footer: [
    { c: 'space', text: '' },
    { c: 'status', text: '◇ codex · waiting on user · type a/d/m/s or paste new instructions' },
  ],
};

const TERMINAL_SAMPLES = { claude: CLAUDE_TERMINAL, codex: CODEX_TERMINAL };

const SAMPLE_DIFF_FILES = [
  {
    name: 'internal/scan/parser.go',
    add: 5, rem: 2,
    hunks: [
      {
        header: '@@ -39,5 +39,8 @@ func ParseTrivyOutput(data []byte) (*ScanResult, error) {',
        lines: [
          { type: 'ctx', n: 39, code: '  if err := json.Unmarshal(data, &r); err != nil {' },
          { type: 'ctx', n: 40, code: '    return nil, fmt.Errorf("parse trivy: %w", err)' },
          { type: 'ctx', n: 41, code: '  }' },
          { type: 'rem', n: 42, code: '  if len(r.Results) == 0 {' },
          { type: 'rem', n: 43, code: '    return nil, errors.New("scan failed: no results")' },
          { type: 'rem', n: 44, code: '  }' },
          { type: 'add', n: 42, code: '  // nil Results means Trivy returned null — treat as' },
          { type: 'add', n: 43, code: '  // zero findings, not a scan failure.' },
          { type: 'add', n: 44, code: '  findings := []Finding{}' },
          { type: 'add', n: 45, code: '  for _, tr := range r.Results {' },
          { type: 'add', n: 46, code: '    findings = append(findings, tr.toCommon())' },
          { type: 'ctx', n: 47, code: '  }' },
          { type: 'ctx', n: 48, code: '  return &ScanResult{Findings: findings}, nil' },
          { type: 'ctx', n: 49, code: '}' },
        ]
      }
    ]
  },
  {
    name: 'internal/scan/parser_test.go',
    add: 13, rem: 0,
    hunks: [
      {
        header: '@@ -0,0 +1,28 @@',
        lines: [
          { type: 'add', n: 1, code: 'package scan' },
          { type: 'add', n: 2, code: '' },
          { type: 'add', n: 3, code: 'import (' },
          { type: 'add', n: 4, code: '  "testing"' },
          { type: 'add', n: 5, code: '  "github.com/stretchr/testify/require"' },
          { type: 'add', n: 6, code: ')' },
          { type: 'add', n: 7, code: '' },
          { type: 'add', n: 8, code: 'func TestParseTrivyEmptyResults(t *testing.T) {' },
          { type: 'add', n: 9, code: '  data := []byte(`{"Results": null, "ArtifactName": "alpine:3.19"}`)' },
          { type: 'add', n: 10, code: '  r, err := ParseTrivyOutput(data)' },
          { type: 'add', n: 11, code: '  require.NoError(t, err)' },
          { type: 'add', n: 12, code: '  require.Empty(t, r.Findings)' },
          { type: 'add', n: 13, code: '}' },
        ]
      }
    ]
  },
  { name: 'CHANGELOG.md', add: 2, rem: 0 },
  { name: 'internal/scan/types.go', add: 0, rem: 1 },
];

const MONITOR = {
  notifications: [],
  events: [],
  rules: [],
  sources: [
    { id: 'github', label: 'gh CLI', status: 'not synced' },
    { id: 'slack', label: 'slack CLI', status: 'not synced' },
  ],
  unread: 0,
  approvals: 0,
  last_sync: '',
};
const ACTIVITY_HEATMAP = [];

// ──────────────────────────────────────────────────────────────
// Formatters
// ──────────────────────────────────────────────────────────────

function formatAge(minutes) {
  if (minutes == null) return '—';
  if (minutes < 1) return 'just now';
  if (minutes < 60) return `${Math.floor(minutes)}m`;
  const h = minutes / 60;
  if (h < 24) {
    const hh = Math.floor(h);
    const m = Math.floor(minutes - hh * 60);
    return m > 0 && hh < 6 ? `${hh}h ${m}m` : `${hh}h`;
  }
  return `${Math.floor(h / 24)}d`;
}

function formatActivity(seconds) {
  if (seconds < 60) return `${Math.floor(seconds)}s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
  return `${Math.floor(seconds / 86400)}d ago`;
}

function fmtTokens(n) {
  if (n >= 1000) return `${(n / 1000).toFixed(0)}k`;
  return `${n}`;
}

function shortUUID(uuid) {
  if (!uuid) return '—';
  return uuid.slice(0, 8) + '…' + uuid.slice(-4);
}

function rerenderIcons() {
  // Icons are rendered as React-owned SVG nodes. External DOM rewrites can
  // crash React removal/reconciliation.
}

// ──────────────────────────────────────────────────────────────
// Clock context — drives all live ticks
// ──────────────────────────────────────────────────────────────
const ClockCtx = createContext(0);

const ClockProvider = ({ children }) => {
  const [t, setT] = useState(0);
  useEffect(() => {
    const id = setInterval(() => setT(x => x + 1), 1000);
    return () => clearInterval(id);
  }, []);
  return <ClockCtx.Provider value={t}>{children}</ClockCtx.Provider>;
};

const TRASH = { tasks: [], projects: [], playbooks: [], total: 0 };
const AGENT_MEMORY_SOURCES = [];

// Expose
window.MC = window.MC || {};
Object.assign(window.MC, {
  AGENTS, DEAD_AGENT, DONE_AGENTS, BACKLOG, DONE_TASKS, KB_FILES, AGENT_MEMORY_SOURCES, WORKDIRS, PLAYBOOKS_MC, PROJECTS_MC, ACTIVITY_HEATMAP, MONITOR, TRASH,
  SAMPLE_TRANSCRIPT, SAMPLE_TERMINAL, TERMINAL_SAMPLES, SAMPLE_DIFF_FILES,
  formatAge, formatActivity, fmtTokens, shortUUID, rerenderIcons,
  ClockProvider, ClockCtx,
});

if (window.FLOW_BOOTSTRAP) {
  Object.assign(window.MC, window.FLOW_BOOTSTRAP);
}
