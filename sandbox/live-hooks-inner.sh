#!/bin/sh
# Runs inside the flow-sandbox container (see live-hooks.sh).
set -u
if [ "${1:-}" != phase2 ]; then
cd /src && go build -buildvcs=false -o /usr/local/bin/flow-real . || exit 1
# Shim: log every hook invocation (argv, event, bytes of stdout) then run
# the real binary. Lets the matrix count which handler did work.
cat > /usr/local/bin/flow <<'SH'
#!/bin/sh
in=$(cat 2>/dev/null || true)
case "$1" in hook|inbox)
  out=$(printf '%s' "$in" | flow-real "$@"); rc=$?
  ev=$(printf '%s' "$in" | jq -r '.hook_event_name // "-"' 2>/dev/null)
  printf '%s\t%s\trc=%s\tout=%s\n' "$ev" "$*" "$rc" "${#out}" >> /tmp/hooks.log
  printf '%s' "$out"; exit $rc ;;
esac
printf '%s' "$in" | exec flow-real "$@"
SH
chmod +x /usr/local/bin/flow
: > /tmp/hooks.log; chmod 666 /tmp/hooks.log
# Claude refuses --dangerously-skip-permissions as root: phase 2 as `dev`.
exec runuser -u dev -- env HOME=/home/dev FLOW_ROOT=/home/dev/.flow sh "$0" phase2
fi
cd "$HOME"
flow init >/dev/null 2>&1 || { echo "flow init failed"; exit 1; }
echo "== after flow init: settings.json exists? $(test -e ~/.claude/settings.json && echo YES || echo no)"
ls -a ~/.claude/skills/flow ~/.claude/skills/flow/.claude-plugin ~/.claude/skills/flow/hooks
claude --version
mkdir -p ~/work && cd ~/work
run() { # label, args...
  label=$1; shift
  : > /tmp/hooks.log
  timeout 180 claude "$@" > /tmp/out.txt 2>&1; echo "-- $label: claude rc=$? out: $(head -c 300 /tmp/out.txt | tr '\n' ' ')"
  sleep 1; sort /tmp/hooks.log | uniq -c
}
sid=$(cat /proc/sys/kernel/random/uuid)
P='Run the shell command `echo hello` with the Bash tool, then reply DONE.'
run "plugin only: claude -p" -p --session-id "$sid" --dangerously-skip-permissions "$P"
run "plugin only: claude -p --resume" -p --resume "$sid" --dangerously-skip-permissions "$P"
claude plugin list 2>&1 | head -20
# Legacy: a pre-plugin settings.json with the old flow entries.
cat > ~/.claude/settings.json <<'JSON'
{"hooks":{
 "SessionStart":[{"matcher":"startup|resume|compact|fork","hooks":[{"type":"command","command":"flow hook session-start"}]},
   {"matcher":"startup|resume|compact|fork","hooks":[{"type":"command","command":"flow inbox wait --rewake","asyncRewake":true,"timeout":86400,"rewakeMessage":"flow bus:"}]}],
 "UserPromptSubmit":[{"hooks":[{"type":"command","command":"flow hook user-prompt-submit"}]}],
 "PreToolUse":[{"matcher":"Bash|Edit|Write|Read|Glob|Grep|Agent|WebFetch|WebSearch","hooks":[{"type":"command","command":"flow hook pre-tool-use"}]}],
 "Stop":[{"hooks":[{"type":"command","command":"flow hook stop"}]},
   {"hooks":[{"type":"command","command":"flow inbox wait --rewake","asyncRewake":true,"timeout":86400,"rewakeMessage":"flow bus:"}]}]}}
JSON
sid2=$(cat /proc/sys/kernel/random/uuid)
run "legacy+plugin: claude -p" -p --session-id "$sid2" --dangerously-skip-permissions "$P"
echo "markers: $(ls $FLOW_ROOT/hooks/plugin 2>/dev/null | tr '\n' ' ')"
