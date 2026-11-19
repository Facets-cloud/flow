#!/usr/bin/env bash
# Container half of gen-schema-fixtures.sh. Expects /src/.fixture-build/<tag>/
# source trees and /src/.fixture-build/TAGS. Never run on the host.
set -uo pipefail
[ -f /.dockerenv ] || { echo "refusing to run outside Docker" >&2; exit 1; }
out=/src/internal/flowdb/testdata/schemas
mkdir -p "$out"
# FIXTURE_TAGS (space-separated) regenerates only those tags' fixtures.
[ -n "${FIXTURE_TAGS:-}" ] || rm -f "$out"/v*.sql
go build -o /tmp/sqldump ./scripts/sqldump
seen=/tmp/seen-digests; : > "$seen"
for t in ${FIXTURE_TAGS:-$(cat /src/.fixture-build/TAGS)}; do
  src=/src/.fixture-build/$t
  bin=/tmp/flow-$t
  if ! (cd "$src" && go build -o "$bin" . ) >/tmp/build-$t.log 2>&1; then
    echo "$t: build failed (skipped)"; tail -3 /tmp/build-$t.log; continue
  fi
  export FLOW_ROOT=/tmp/fr-$t HOME=/tmp/home-$t
  rm -rf "$FLOW_ROOT" "$HOME"; mkdir -p "$HOME" /tmp/wd/alpha /tmp/wd/float
  f() { "$bin" "$@" </dev/null >/dev/null 2>&1; }
  f init
  f add project "Alpha" --work-dir /tmp/wd/alpha --slug alpha --priority high || f add project "Alpha" --work-dir /tmp/wd/alpha
  f add task "Task one" --project alpha --slug t1 --priority high --tag fixture \
    || f add task "Task one" --project alpha --slug t1 \
    || f add task "Task one" --project alpha
  f add task "Floating" --slug t2 --work-dir /tmp/wd/float || f add task "Floating" --work-dir /tmp/wd/float
  f update task t1 --waiting "bob"
  f update task t2 --due-date 2026-12-01
  f workdir add /tmp/wd/alpha --name alpha
  f add playbook "Weekly" --work-dir /tmp/wd/alpha --slug weekly
  f add owner "Keeper" --work-dir /tmp/wd/alpha --every 1h --slug keeper
  f archive t2
  db="$FLOW_ROOT/flow.db"
  [ -f "$db" ] || { echo "$t: no flow.db (skipped)"; continue; }
  # Bus-era tags refuse mail from an unbound shell: bind t1 to a session
  # (as `flow do` would) and send from it.
  sid=aaaaaaaa-1111-4222-8333-444444444444
  if /tmp/sqldump -exec /dev/stdin "$db" >/dev/null 2>&1 <<SQL
UPDATE tasks SET status='in-progress', session_id='$sid', session_started='2026-11-01T10:00:00Z' WHERE slug='t1';
SQL
  then
    CLAUDE_CODE_SESSION_ID=$sid f message user "hello from fixture"
    CLAUDE_CODE_SESSION_ID=$sid f message user/t2 "peer mail from fixture"
    CLAUDE_CODE_SESSION_ID=$sid f watch alpha
  fi
  d=$(/tmp/sqldump -schema "$db" | sha1sum | cut -c1-12)
  if grep -q "$d" "$seen"; then echo "$t: same schema as $(grep "$d" "$seen" | cut -d' ' -f2)"; continue; fi
  echo "$d $t" >> "$seen"
  /tmp/sqldump "$db" > "$out/$t.sql"
  echo "$t: wrote $(basename "$out/$t.sql") (schema $d)"
done
