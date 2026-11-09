# Install flow — instructions for a coding agent

You are a coding agent (Claude Code, Codex, or similar). The user pasted
something like **"Install flow from https://github.com/Facets-cloud/flow"**.
Follow the steps below in order, run each command, and finish with the
report in step 6. Do not skip verification. Nothing here needs `sudo`;
if a step would, stop and ask first.

flow is a single static binary plus a `~/.flow` data directory and an
agent skill. Supported: **macOS** and **Linux**, arm64 and x86_64.
Windows is not supported yet.

## 1. Detect the platform

```sh
uname -s && uname -m
```

`Darwin` → macOS. `Linux` → Linux. Anything else: stop and tell the user
there is no build for it. `arm64`/`aarch64` and `x86_64`/`amd64` are fine.

## 2. Install the binary

```sh
curl -fsSL https://raw.githubusercontent.com/Facets-cloud/flow/main/install.sh | sh
```

- Installs to `~/.local/bin/flow` (no `sudo`), verifies the download
  against the release `SHA256SUMS`, and on macOS clears the Gatekeeper
  quarantine attribute itself. Re-running it later upgrades in place.
- Knobs, only if the user asked: `FLOW_VERSION=vX.Y.Z` pins a release;
  `FLOW_INSTALL_DIR=/some/dir` changes the target (a root-owned dir needs
  `sudo` — say so and let the user run it).
- No `curl`? The script also works with `wget`. If neither exists, ask
  before installing one (`apt-get install -y curl` needs `sudo`).
- From source (needs Go 1.25+): `git clone https://github.com/Facets-cloud/flow && cd flow && make install`.

## 3. Make sure `flow` is on PATH

```sh
command -v flow || echo "flow is not on PATH"
```

If it is not on PATH, append this line to the user's shell rc file —
`~/.zshrc` (macOS default), `~/.bashrc` (most Linux), or
`~/.config/fish/config.fish` — unless the line is already there:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

Tell the user which file you edited. For the rest of these steps use the
full path `~/.local/bin/flow` if the current shell has not picked it up.

## 4. Initialise

```sh
flow init
```

Creates `~/.flow` (SQLite db, `kb/` files) and installs the flow skill and
hooks for every harness it detects (`~/.claude`, `~/.codex`). Safe to
re-run. It prints which harnesses got the skill — keep that for the report.

## 5. Verify

```sh
flow --version
flow list tasks
```

Expect a version string and `(no tasks)`. If `flow --version` fails, go
back to step 3.

## 6. Report

Tell the user, in a few lines: the version installed and where the binary
is; whether you edited a shell rc file (which one); which harnesses got the
skill (from `flow init`); and that the skill loads in the **next** harness
session. Suggest a first step such as `flow add task "<something they are
working on>"`.

## Platform notes

- **macOS:** tabs for `flow do` open in iTerm2, Terminal.app, Warp,
  Ghostty, kitty, zellij or tmux. Terminal.app needs Accessibility for
  the keystroke; flow's error says exactly what to grant.
- **Linux:** tabs open inside tmux, zellij or kitty (start your shell in
  one of them). Outside those, `flow do <task>` prints the exact command
  to run and offers `flow do --here` (bind the session you are in) or
  `flow do --auto` (run headless). Scheduled runs use systemd timers or
  cron instead of launchd. The native macOS dialog connector is not
  available on Linux; other connectors are.
- `xattr -d com.apple.quarantine` is macOS only and already handled by
  install.sh; never run it on Linux.

## Uninstall

```sh
flow skill uninstall && rm -f ~/.local/bin/flow
```

`~/.flow` holds the user's data; remove it only if they ask.
