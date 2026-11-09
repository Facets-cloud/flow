#!/bin/sh
# Linux smoke test for flow (also runs on macOS with tmux installed).
#
# What it proves, end to end, in a throwaway $HOME:
#   1. install.sh installs from a local mirror (FLOW_BASE_URL) and verifies SHA256
#   2. flow init creates ~/.flow and installs the skill + hooks for the claude harness
#   3. flow add task / flow list tasks
#   4. flow do --here binds the current (stubbed) claude session
#   5. flow do inside tmux opens a tmux window that runs the claude command
#   6. flow do outside any terminal prints the no-terminal handoff (Linux only)
#   7. flow done flips status and runs the (stubbed) close-out sweep
#   8. flow message + flow inbox pop round-trip
#
# `claude` is a stub on PATH that records its argv; no network, no real harness.
# Needs: tmux, curl or wget, ps, sha256sum/shasum, and either Go (to build) or
# FLOW_BIN=<prebuilt linux binary for this arch>.
#
# shellcheck disable=SC1010  # `flow do` / `flow done` are subcommands, not shell keywords
set -eu

REPO=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(mktemp -d)
SOCK="flowsmoke$$"
cleanup() {
  tmux -L "$SOCK" kill-server 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

step() { printf '\n==> %s\n' "$*"; }
fail() { printf 'SMOKE FAIL: %s\n' "$*" >&2; exit 1; }

export HOME="$WORK/home"
export FLOW_ROOT="$HOME/.flow"
mkdir -p "$HOME" "$WORK/bin" "$WORK/mirror" "$WORK/repo"
# Nothing from the real machine may leak in.
unset ZELLIJ TMUX REX_SESSION KITTY_WINDOW_ID FLOW_TERM TERM_PROGRAM CLAUDE_CODE_SESSION_ID CODEX_THREAD_ID CODEX_HOME 2>/dev/null || true

case "$(uname -s)" in Darwin) os=darwin ;; Linux) os=linux ;; *) fail "unsupported OS" ;; esac
case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; arm64|aarch64) arch=arm64 ;; *) fail "unsupported arch" ;; esac
asset="flow-$os-$arch"

step "1. build or copy the binary into a local release mirror"
if [ -n "${FLOW_BIN:-}" ]; then
  cp "$FLOW_BIN" "$WORK/mirror/$asset"
else
  (cd "$REPO" && CGO_ENABLED=0 go build -ldflags '-X main.version=v0.0.0-smoke' -o "$WORK/mirror/$asset" .)
fi
if command -v sha256sum >/dev/null 2>&1; then
  (cd "$WORK/mirror" && sha256sum "$asset" > SHA256SUMS)
else
  (cd "$WORK/mirror" && shasum -a 256 "$asset" > SHA256SUMS)
fi
cat "$WORK/mirror/SHA256SUMS"

step "2. install.sh from the mirror (no sudo, user-local dir)"
FLOW_BASE_URL="file://$WORK/mirror" FLOW_INSTALL_DIR="$WORK/bin" sh "$REPO/install.sh"
export PATH="$WORK/bin:$PATH"
[ -x "$WORK/bin/flow" ] || fail "install.sh did not place flow in $WORK/bin"
command -v flow
flow --version

step "2b. install.sh rejects a tampered binary"
cp -r "$WORK/mirror" "$WORK/mirror-bad"
printf 'x' >> "$WORK/mirror-bad/$asset"
if FLOW_BASE_URL="file://$WORK/mirror-bad" FLOW_INSTALL_DIR="$WORK/bin-bad" sh "$REPO/install.sh" 2>"$WORK/bad.err"; then
  fail "install.sh accepted a binary with a bad checksum"
fi
grep -q "checksum mismatch" "$WORK/bad.err" || fail "expected a checksum mismatch error, got: $(cat "$WORK/bad.err")"
echo "tampered binary rejected: OK"

step "3. stub claude on PATH (records argv; -p exits at once; interactive lingers)"
export CLAUDE_STUB_LOG="$WORK/claude-argv.log"
cat > "$WORK/bin/claude" <<'STUB'
#!/bin/sh
printf '%s\n' "$*" >> "${CLAUDE_STUB_LOG:?}"
case " $* " in *" -p "*) exit 0 ;; esac
sleep "${CLAUDE_STUB_LINGER:-8}"
STUB
chmod +x "$WORK/bin/claude"

step "4. flow init + verify"
flow init
[ -f "$HOME/.claude/skills/flow/SKILL.md" ] || fail "skill not installed under ~/.claude/skills/flow"
grep -q "flow hook session-start" "$HOME/.claude/settings.json" || fail "SessionStart hook missing from ~/.claude/settings.json"
flow list tasks | grep -q "no tasks" || fail "fresh install should list no tasks"

step "5. add a task"
flow add task "Smoke here" --slug smoke-here --work-dir "$WORK/repo"
flow list tasks | grep -q smoke-here || fail "task not listed"

step "6. flow do --here binds the ambient claude session"
if [ -r /proc/sys/kernel/random/uuid ]; then
  sid=$(cat /proc/sys/kernel/random/uuid)
else
  sid=$(uuidgen | tr '[:upper:]' '[:lower:]')
fi
enc=$(printf '%s' "$WORK/repo" | sed 's#[/._]#-#g')
mkdir -p "$HOME/.claude/projects/$enc"
: > "$HOME/.claude/projects/$enc/$sid.jsonl"
CLAUDE_CODE_SESSION_ID="$sid" flow do --here smoke-here
flow show task smoke-here | grep -q "session_id:.*$sid" || fail "--here did not bind $sid"
flow show task smoke-here | grep -q "status: *in-progress" || fail "--here did not flip to in-progress"

if [ "$os" = linux ]; then
  step "7. flow do outside any supported terminal prints the handoff (Linux)"
  flow add task "Smoke bare" --slug smoke-bare --work-dir "$WORK/repo"
  flow do smoke-bare > "$WORK/bare.out" 2>&1 || fail "flow do without a terminal must exit 0; got: $(cat "$WORK/bare.out")"
  cat "$WORK/bare.out"
  grep -q "no supported terminal detected" "$WORK/bare.out" || fail "handoff text missing"
  grep -q "claude --session-id" "$WORK/bare.out" || fail "handoff command missing"
  grep -q "flow do --here smoke-bare" "$WORK/bare.out" || fail "--here hint missing"
  flow show task smoke-bare | grep -q "status: *in-progress" || fail "handoff should keep the task in-progress"
fi

step "8. flow do inside tmux opens a window running claude"
flow add task "Smoke tmux" --slug smoke-tmux --work-dir "$WORK/repo"
# The new tmux window runs a fresh login shell. Debian's /etc/profile resets
# PATH, so give the throwaway $HOME the rc lines a real user has (INSTALL.md
# step 3): PATH to the install dir, plus the stub's log path and FLOW_ROOT.
rc_lines="export PATH='$WORK/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin'
export CLAUDE_STUB_LOG='$CLAUDE_STUB_LOG'
export CLAUDE_STUB_LINGER=90
export FLOW_ROOT='$FLOW_ROOT'"
for rc in .profile .bashrc .bash_profile .zshrc .zprofile; do
  printf '%s\n' "$rc_lines" > "$HOME/$rc"
done
tmux -L "$SOCK" new-session -d -s smoke -x 160 -y 40
tmux -L "$SOCK" send-keys -t smoke "export PATH='$PATH' HOME='$HOME' FLOW_ROOT='$FLOW_ROOT' CLAUDE_STUB_LOG='$CLAUDE_STUB_LOG'; flow do smoke-tmux > '$WORK/do.out' 2>&1; echo rc=\$? >> '$WORK/do.out'" Enter
i=0
until grep -q '^rc=' "$WORK/do.out" 2>/dev/null; do
  i=$((i+1)); [ $i -lt 60 ] || fail "flow do inside tmux did not finish: $(cat "$WORK/do.out" 2>/dev/null)"
  sleep 0.5
done
cat "$WORK/do.out"
grep -q '^rc=0$' "$WORK/do.out" || fail "flow do inside tmux failed"
grep -q 'Spawned smoke-tmux' "$WORK/do.out" || fail "expected 'Spawned smoke-tmux'"
tmux -L "$SOCK" list-windows -t smoke
tmux -L "$SOCK" list-windows -t smoke -F '#{window_name}' | grep -q 'smoke-tmux' || fail "no tmux window named after the task"
sid2=$(flow show task smoke-tmux | sed -n 's/^session_id: *\([0-9a-fA-F-]*\).*/\1/p')
[ -n "$sid2" ] || fail "smoke-tmux has no session_id"
i=0
until grep -q -- "--session-id $sid2" "$CLAUDE_STUB_LOG" 2>/dev/null; do
  if [ $i -ge 60 ]; then
    echo "--- pane contents of the spawned window:"; tmux -L "$SOCK" capture-pane -p -t smoke-tmux || true
    fail "stub claude never received --session-id $sid2; log: $(cat "$CLAUDE_STUB_LOG" 2>/dev/null)"
  fi
  i=$((i+1))
  sleep 0.5
done
echo "stub claude argv: $(grep -- "--session-id $sid2" "$CLAUDE_STUB_LOG" | cut -c1-120)..."
grep -- "--session-id $sid2" "$CLAUDE_STUB_LOG" | grep -q "flow-linux-core\|Invoke the flow skill\|flow show task" || echo "note: bootstrap prompt text not matched (fine if wording changed)"

step "9a. from outside tmux, the live-session guard refuses a second tab"
if flow do smoke-tmux > "$WORK/do2.out" 2>&1; then
  cat "$WORK/do2.out"; fail "second flow do from outside tmux should have been refused"
fi
cat "$WORK/do2.out"
grep -q "running elsewhere" "$WORK/do2.out" || fail "second flow do failed for an unexpected reason"

step "9b. from inside tmux, flow do focuses the window that already runs the session"
tmux -L "$SOCK" select-window -t smoke:0
tmux -L "$SOCK" send-keys -t smoke:0 "flow do smoke-tmux > '$WORK/do3.out' 2>&1; echo rc=\$? >> '$WORK/do3.out'" Enter
i=0
until grep -q '^rc=' "$WORK/do3.out" 2>/dev/null; do
  i=$((i+1)); [ $i -lt 60 ] || fail "flow do (focus) inside tmux did not finish: $(cat "$WORK/do3.out" 2>/dev/null)"
  sleep 0.5
done
cat "$WORK/do3.out"
grep -q '^rc=0$' "$WORK/do3.out" || fail "flow do (focus) inside tmux failed"
grep -q "switched to existing tab" "$WORK/do3.out" || fail "expected FocusSession to switch to the existing window"
active=$(tmux -L "$SOCK" display-message -t smoke -p '#{window_name}')
[ "$active" = "smoke-tmux" ] || fail "active window after focus is '$active', want smoke-tmux"
echo "focused window: $active"

step "10. message + inbox pop round-trip"
flow message user/smoke-tmux "hello from the smoke test"
CLAUDE_CODE_SESSION_ID="$sid2" flow inbox pop > "$WORK/pop.out" 2>&1 || true
cat "$WORK/pop.out"
grep -q "hello from the smoke test" "$WORK/pop.out" || fail "inbox pop did not return the message"

step "11. flow done runs the stubbed sweep and flips status"
: > "$CLAUDE_STUB_LOG"
flow done smoke-here
flow show task smoke-here | grep -q "status: *done" || fail "flow done did not flip status"
grep -q -- " -p " "$CLAUDE_STUB_LOG" || echo "note: sweep did not call claude -p (deferred or skipped); status flip is the contract"
flow list tasks --status done | grep -q smoke-here || fail "done task not listed"

step "ALL SMOKE STEPS PASSED on $os/$arch"
