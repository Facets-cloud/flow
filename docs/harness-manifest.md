# Harness manifests: teaching flow a new coding agent

A **harness manifest** is a TOML file that tells flow how to drive a
coding agent — how to launch it, resume it, find its transcript, install
flow's skill, and wire flow's hooks. Adding an agent is a file, not a
release.

```
~/.flow/harnesses/<name>.toml     # or $FLOW_ROOT/harnesses/
```

```bash
flow harness validate ./mine.toml   # structural check, before installing
cp mine.toml ~/.flow/harnesses/
flow harness list                   # confirm it loaded
flow harness show <name>            # capabilities + rendered sample commands
flow skill install --harness <name> # install the skill + wire hooks
```

Working examples live in [`examples/harnesses/`](../examples/harnesses/).
Copy the closest one and edit.

---

## If you are an agent writing a manifest

Do not guess flags. A wrong flag passes `flow harness validate` — which
checks *structure*, not whether a flag exists — and then fails at spawn
time, or worse, silently does nothing. Establish each fact first:

```bash
AGENT=<binary>

$AGENT --version
$AGENT --help                     # subcommands, global flags
$AGENT <sub> --help               # per-subcommand flags (resume, exec, …)

# Does it publish a session id into the environment?  (decides session_env)
strings "$(command -v $AGENT)" | grep -oE '[A-Z][A-Z0-9_]{2,}_SESSION[A-Z0-9_]*' | sort -u

# Where do transcripts live, and is the name derivable from a session id?
ls ~/.<agent>            2>/dev/null
find ~/.<agent> -name '*.jsonl' 2>/dev/null | head -3

# What are the top-level keys of one transcript record?  (fills transcript.map)
find ~/.<agent> -name '*.jsonl' | head -1 | xargs -I{} sh -c 'head -1 {} | python3 -m json.tool' | head -40

# Hooks and instructions files
ls ~/.<agent>/*.json ~/.<agent>/*.toml 2>/dev/null
```

Record what you could **not** establish as a comment in the manifest.
An honest `# UNVERIFIED` beats a plausible invention — see the research
checklist in [`TEMPLATE.toml`](../examples/harnesses/TEMPLATE.toml).

---

## Required fields

Only four things are required. Everything else is a capability you
declare if the agent has it.

```toml
schema = 1
name   = "agent"          # the id stored in tasks.harness; must be unique
binary = "agent"          # the executable, used for process matching

[session]
strategy = "uuid7"        # uuid4 | uuid7 | exec-capture
validate = '^[0-9a-f-]{36}$'   # REQUIRED — see "Why validate is required"

[launch]
argv = ["agent", "--session", "{{.SessionID}}", "{{.Prompt}}"]

[liveness]
probe = "none"            # ps | exec | none

[vocab]
product = "Agent"         # human-facing name used in flow's own output
```

### `session_env` — optional, and often absent

```toml
session_env = "CLAUDE_CODE_SESSION_ID"
```

The environment variable the agent exports **inside** a session. Flow
reads it to answer "which task is THIS session bound to".

Some agents publish nothing. Omit the field when that is true; the
manifest can still work if the agent accepts Flow's chosen session id at
launch and resume. It cannot support `flow do --here` or hook reverse
lookup, and `flow harness show` says so.

This is separate from **session ownership**. Flow allocates and stores an
id before launching. If an agent instead mints its own id and offers no
launch flag for Flow's id, the current contract cannot represent it: a
UUID strategy would bind Flow to a session the agent never created.
Codex and OMP currently fall into that category, so no shipped manifest
claims support for them.

### Why `validate` is required

Flow shell-quotes `{{.SessionID}}` like every other runtime data value.
`validate` is still required: ids recovered from environment variables,
process listings, and external capture commands must match the harness's
actual identity format before Flow stores or uses them. Matching must
cover the **entire** string; substring matches are rejected.

---

## Capability tables

Omit a table and flow reports that capability as absent, in the agent's
own terms, instead of failing at the moment you need it.

| Table | Grants | Omitted means |
|---|---|---|
| `[resume]` | continuing a session by id | `flow do` on a bound task refuses rather than stranding the transcript |
| `[headless]` | `flow done` close-out sweep, `flow do --auto`, `$FLOW_TERM=bg` | those commands report the gap |
| `[transcript]` | `flow transcript` | "keeps no readable transcript" |
| `[skills]` | `flow skill install` | "no skill install location" |
| `[hooks]` | SessionStart / UserPromptSubmit context | "no hook mechanism" |

### `[launch]`, `[resume]`

```toml
[launch]
argv            = ["agent", "--cwd", "{{.WorkDir}}", "{{.Prompt}}"]
permission_flag = ["--allow-all"]
prelude         = "ulimit -n 65536"   # optional shell prefix
```

`permission_flag` is appended when the caller asks to skip tool
approvals. `prelude` is prepended with `&&`.

### `[headless]`

```toml
[headless]
run_argv  = ["agent", "run", "--cwd", "{{.WorkDir}}", "{{.Prompt}}"]
auto_argv = ["agent", "run", "--session", "{{.SessionID}}", "--cwd", "{{.WorkDir}}", "{{.Prompt}}"]
```

`run_argv` powers `flow done`'s close-out sweep (stdout discarded; a
failing run's stderr tail rides back in flow's warning, so whatever the
agent prints there is the diagnosis a user gets). `auto_argv` powers
`flow do --auto` and keeps a transcript. Declaring `[headless]` also
makes `$FLOW_TERM=bg` work: flow supervises a detached run itself.

**Set the agent's run limits explicitly.** A headless mode usually ships
defaults tuned for a one-shot question — a turn cap, a time budget — and
hitting one is a non-zero exit, which flow can only report as a failed
sweep or a dead auto run. praxis is the worked example: `prx run`
defaults to `-max-turns 25` and exits 1 with `agent: max turns reached`,
where its TUI treats the same stop as soft, and `0` is coerced back to
25 rather than meaning unbounded. Read the agent's `--help` for these
defaults and state them, sized per shape: a close-out sweep is bounded
work, an auto run executes a whole task.

### `[liveness]`

```toml
[liveness]
probe = "ps"                                  # scan the process table
match = 'agent\s+resume\s+([0-9a-f-]{36})'    # EXACTLY one capture group: the id
```

`probe = "exec"` runs `argv` and matches against its output — for agents
with a registry command. `probe = "none"` never reports a live session.

### `[transcript]`

```toml
[transcript]
path   = "{{.Home}}/.claude/projects/{{encodeCwd .Cwd}}/{{.SessionID}}.jsonl"
format = "jsonl"

[transcript.map]
role       = "message.role"
text       = "message.content[].text"    # REQUIRED
tool_block = "tool_use"                  # the "type" VALUE marking a tool call
tool_name  = "message.content[].name"
```

Map values are dotted paths into each JSON record. A `[]` suffix
iterates an array, so `message.content[].text` collects every block's
text. A path that does not resolve yields nothing — records are
heterogeneous by nature, and a tool-call block having no `.text` is
normal, not an error.

**Globs.** Some agents name transcripts by creation time, which no
template can derive. Use `*` and flow resolves it, taking the newest
match:

```toml
path = "{{.Home}}/.agent/sessions/*/run-*-{{.SessionID}}.jsonl"
```

### `[skills]`

```toml
[skills]
discovery = "native"                        # native | pointer
dir       = "{{.Home}}/.claude/skills/flow"
owns_dir  = true                            # false ⇒ uninstall must not delete it
require_frontmatter = ["name", "description"]
```

- **`native`** — the agent already scans `dir`. Write the tree, done.
- **`pointer`** — the agent has no skill mechanism. Flow writes the tree
  *and* injects a marker-delimited block into an instructions file it
  does read:

```toml
[skills.pointer]
file    = "{{.Home}}/.agent/AGENTS.md"
comment = "html"                 # html | hash | slash
block   = """
# flow
When the user asks about tasks or projects, read {{.SkillPath}} and follow it.
{{.HookDirective}}
"""
```

The block is written between `<!-- flow:managed:start -->` and
`<!-- flow:managed:end -->`. Everything around a valid pair is preserved,
updates replace only that region, and uninstall removes exactly it. If an
owned marker is unmatched, duplicated, or out of order, install/uninstall
returns a repair-required error without modifying the file.

Two extra variables are available inside `block`: `{{.SkillPath}}` (the
installed `SKILL.md`) and `{{.HookDirective}}` (the self-invocation
instructions, empty unless the `instruction-directive` strategy is on).

> **Point at the agent's OWN directory.** Several agents also scan a
> sibling's (praxis reads `~/.claude/skills` as a fallback) but rank
> their own first and resolve **first-name-wins**. Installing into the
> sibling's tree gets silently shadowed by anything already in the
> native one.

### `[hooks]`

Flow needs two moments: **SessionStart** (inject the bound task's
context) and **UserPromptSubmit** (re-anchor on drift). Declare every
strategy that applies; `flow skill install` performs all of them.

```toml
[hooks]
strategies = ["config-patch"]
```

| Strategy | Covers flow-spawned | Covers ad-hoc | Guarantee |
|---|---|---|---|
| `config-patch` | yes | yes | deterministic |
| `prompt-prelude` | yes | no | deterministic |
| `instruction-directive` | yes | yes | best-effort — the agent must comply |

An agent with no hook system reaches near-parity by combining the last
two. `instruction-directive` is delivered inside the `[skills.pointer]`
block, so it requires `discovery = "pointer"`.

```toml
[hooks.config_patch]
file = "{{.Home}}/.claude/settings.json"     # JSON only

[hooks.config_patch.events.SessionStart]
pointer = "/hooks/SessionStart"              # slash path to the ARRAY of entries
entry   = '{"matcher":"startup|resume","hooks":[{"type":"command","command":"{{.Command}}"}]}'

[hooks.config_patch.events.UserPromptSubmit]
pointer = "/hooks/UserPromptSubmit"
entry   = '{"hooks":[{"type":"command","command":"{{.Command}}"}]}'
```

`entry` is a **template**, not a fixed struct, because entry shapes
differ wildly — Claude wants a nested object, while Praxis accepts a
bare `"{{.Command}}"` string. It is rendered and parsed before the file
is touched, so malformed JSON fails without corrupting the config.
Missing intermediate objects are created; other keys are preserved;
uninstall removes only flow's entries and prunes what it emptied.

### `[vocab]`

The agent-facing dialect, so prompts read correctly everywhere.

```toml
[vocab]
product      = "Claude"            # REQUIRED — used in flow's own output
context_file = "CLAUDE.md"         # ambient instructions file it reads
ask_tool     = "AskUserQuestion"   # interactive-choice tool; "" if none
skill_hint   = "via the Skill tool"
```

Leave `ask_tool` empty when the agent has none — prompts then fall back
to plain prose instead of naming a tool that does not exist.

---

## Templates

### Three contexts, three rules

| Where | Quoting | Notes |
|---|---|---|
| `[launch]`/`[resume]` `argv` | selective shell-quoting, joined with spaces | handed to a terminal |
| `[headless]`/`[liveness]` `argv` | none | passed to exec, so quotes and `$(…)` are inert |
| `path`, `dir`, `file` | none | filesystem paths |

### Variables

| Variable | Meaning | Quoted in shell context |
|---|---|---|
| `{{.SessionID}}` | the session id | no — `validate` makes it safe |
| `{{.Prompt}}` | the prompt | **yes** |
| `{{.Inject}}` | `flow do --with` text | **yes** |
| `{{.WorkDir}}`, `{{.Cwd}}` | the task's work dir | **yes** |
| `{{.Home}}` | home directory | **yes** |
| `{{.InjectionMarker}}` | marker announcing injected text | no |
| `{{.Name}}`, `{{.Binary}}` | from this manifest | no |

An argv element is quoted **iff its template text mentions a data
variable**. Literal tokens stay bare, which is what keeps generated
commands byte-identical to hand-written ones.

Functions: `{{encodeCwd .Cwd}}` (path → `-`-separated, for cwd-keyed
layouts) and `{{envOr "VAR" .Home "/.agent"}}` (env var with a fallback).

A misspelled variable is an **error**, not an empty string.

### Optional arguments

An element is dropped when it expands to empty **only if it consists
entirely of conditionals**:

```toml
"{{if .Inject}}{{.InjectionMarker}}\n{{.Inject}}{{end}}"   # dropped when no injection
"{{.Prompt}}"                                              # kept as '' when empty
```

That distinction matters: dropping an empty positional argument would
shift every argument after it.

### TOML quoting

Use `'single quotes'` for regexes (no escape processing, backslashes
reach the engine intact) and `"double quotes"` for templates that need
`\n`.

---

## Precedence and failure

- `~/.flow/harnesses/*.toml` **shadows** a built-in of the same name.
  Deliberate — retuning claude's flags needs no rebuild — and reported
  by `flow harness list`.
- A malformed manifest disables **only itself**. Other harnesses keep
  working; the error appears in `flow harness list`.
- Unknown keys are rejected at load, so `sesion_env` fails loudly
  instead of silently defaulting.

## Before you ship a manifest

1. `flow harness validate ./x.toml` — structure, regexes, templates.
2. `flow harness show <name>` — read the **rendered sample commands**
   and check the quoting is what the agent expects.
3. `flow skill install --harness <name>` — then confirm on disk that the
   skill and hook entries landed where the agent will look.
4. Confirm the agent actually *resolves* the skill, not merely that the
   file exists. "It's in the right directory" and "the agent loads it"
   are different claims — shadowing and frontmatter parsing sit between
   them.

> **Frontmatter warning.** Not every agent parses real YAML. Some read
> a line-based subset, where `description: |` yields the literal `"|"`
> and the continuation is dropped — the skill loads, reports itself
> invocable, and arrives with a useless one-character description.
> Keep frontmatter values on a single line.
