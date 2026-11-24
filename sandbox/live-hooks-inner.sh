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
  sum=$(printf '%s' "$in" | sha256sum | cut -c1-10)
  printf '%s\t%s\tsrc=%s\tstdin=%s\trc=%s\tout=%s\n' "$ev" "$*" "${FLOW_HOOK_SRC:-legacy}" "$sum" "$rc" "${#out}" >> /tmp/hooks.log
  printf '%s' "$out"; exit $rc ;;
esac
printf '%s' "$in" | exec flow-real "$@"
SH
chmod +x /usr/local/bin/flow
: > /tmp/hooks.log; chmod 666 /tmp/hooks.log
# Claude refuses --dangerously-skip-permissions as root: phase 2 as `dev`.
# A fresh FLOW_ROOT per run; the dev user's settings.json is restored after.
exec runuser -u dev -- env HOME=/home/dev FLOW_ROOT=/tmp/flow-live-$$ FLOW_NO_AUTO_UPGRADE= sh "$0" phase2
fi
cd "$HOME"
[ -e ~/.claude/settings.json ] && cp ~/.claude/settings.json /tmp/settings.json.bak
trap 'if [ -e /tmp/settings.json.bak ]; then cp /tmp/settings.json.bak ~/.claude/settings.json; else rm -f ~/.claude/settings.json; fi; rm -rf ~/work/.claude' EXIT
rm -f ~/.claude/settings.json
flow init >/dev/null 2>&1 || { echo "flow init failed"; exit 1; }
echo "== after flow init: settings.json exists? $(test -e ~/.claude/settings.json && echo YES || echo no)"
ls -a ~/.claude/skills/flow/.claude-plugin ~/.claude/skills/flow/hooks
claude --version
mkdir -p ~/work && cd ~/work
run() { # label, args...
  label=$1; shift
  : > /tmp/hooks.log
  timeout 180 claude "$@" > /tmp/out.txt 2>&1; echo "-- $label: claude rc=$? out: $(head -c 200 /tmp/out.txt | tr '\n' ' ')"
  sleep 2; cut -f1-3,6 /tmp/hooks.log | sort | uniq -c
}
legacy() { # the pre-plugin settings.json an older flow wrote
cat > ~/.claude/settings.json <<'JSON'
{"model":"sonnet","hooks":{
 "SessionStart":[{"matcher":"startup|resume|compact|fork","hooks":[{"type":"command","command":"flow hook session-start"}]},
   {"matcher":"startup|resume|compact|fork","hooks":[{"type":"command","command":"flow inbox wait --rewake","asyncRewake":true,"timeout":86400,"rewakeMessage":"flow bus:"}]}],
 "UserPromptSubmit":[{"hooks":[{"type":"command","command":"flow hook user-prompt-submit"}]}],
 "PreToolUse":[{"matcher":"Bash|Edit|Write|Read|Glob|Grep|Agent|WebFetch|WebSearch","hooks":[{"type":"command","command":"flow hook pre-tool-use"}]}],
 "Stop":[{"hooks":[{"type":"command","command":"flow hook stop"}]},
   {"hooks":[{"type":"command","command":"flow inbox wait --rewake","asyncRewake":true,"timeout":86400,"rewakeMessage":"flow bus:"}]}]},
 "alwaysThinkingEnabled":false}
JSON
}
left() { n=$(grep -c '"flow hook\|"flow inbox' ~/.claude/settings.json 2>/dev/null); echo "${n:-0}"; }
# --all: a session that surfaces the human's mail marks it seen.
fyis() { flow inbox --as user --all 2>/dev/null | grep -c 'hooks moved to the flow plugin'; }
uuid() { cat /proc/sys/kernel/random/uuid; }
P='Run the shell command `echo hello` with the Bash tool, then reply DONE.'

echo; echo "== M1 plugin only (fresh install)"
sid=$(uuid)
run "M1 claude -p" -p --session-id "$sid" --dangerously-skip-permissions "$P"
run "M1 claude -p --resume" -p --resume "$sid" --dangerously-skip-permissions "$P"

echo; echo "== M2 plugin NOT loading (disabled in project .claude/settings.local.json), legacy entries present"
legacy
rm -rf "$FLOW_ROOT/hooks/plugin"
mkdir -p ~/work/.claude && echo '{"enabledPlugins":{"flow@skills-dir":false}}' > ~/work/.claude/settings.local.json
run "M2 claude -p" -p --session-id "$(uuid)" --dangerously-skip-permissions "$P"
FLOW_NO_AUTO_UPGRADE= flow list tasks >/dev/null 2>&1; flow skill update --harness claude >/dev/null 2>&1
echo "M2 result: legacy entries left=$(left) (want 6) fyis=$(fyis) (want 0) plugin markers=$(ls $FLOW_ROOT/hooks/plugin 2>/dev/null | wc -l) (want 0)"
rm -rf ~/work/.claude

echo; echo "== M3 plugin loads but ANOTHER flow-visible session is live"
other=$(uuid)
sh -c 'sleep 600' claude --session-id "$other" & fake=$!
sleep 1
run "M3 claude -p" -p --session-id "$(uuid)" --dangerously-skip-permissions "$P"
echo "M3 result: legacy entries left=$(left) (want 6) fyis=$(fyis) (want 0)"
kill $fake 2>/dev/null

echo; echo "== M4 plugin loads, no other session live -> strip once + FYI"
run "M4 claude -p" -p --session-id "$(uuid)" --dangerously-skip-permissions "$P"
echo "M4 result: legacy entries left=$(left) (want 0) fyis=$(fyis) (want 1) strip stamp=$(test -e $FLOW_ROOT/hooks/legacy-stripped && echo yes || echo no)"
echo "M4 settings.json now:"; cat ~/.claude/settings.json
run "M4 next session (plugin only)" -p --session-id "$(uuid)" --dangerously-skip-permissions "$P"
echo "M4 after next session: fyis=$(fyis) (want 1)"
