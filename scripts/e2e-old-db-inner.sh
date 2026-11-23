#!/usr/bin/env bash
# Container half of e2e-old-db.sh. Never run on the host.
set -euo pipefail
[ -f /.dockerenv ] || { echo "refusing to run outside Docker" >&2; exit 1; }
tag=$1 srcdir=/src/$2
old=/tmp/flow-old new=/tmp/flow-new
(cd "$srcdir" && go build -o $old .)
go build -o $new .
go build -o /tmp/sqldump ./scripts/sqldump
export HOME=/tmp/e2e-home FLOW_ROOT=/tmp/e2e-home/.flow
rm -rf $HOME; mkdir -p $HOME /tmp/e2e-wd/{alpha,beta,float}
step() { echo "+ $*"; "$@"; }
o() { "$old" "$@" </dev/null >/tmp/o.log 2>&1 || echo "  (old $tag: '$*' failed, ignored: $(tail -1 /tmp/o.log))"; }

echo "== populate with $tag"
o init
o add project "Alpha" --work-dir /tmp/e2e-wd/alpha --slug alpha --priority high
o add project "Beta" --work-dir /tmp/e2e-wd/beta --slug beta
for i in 1 2 3; do
  "$old" add task "Alpha task $i" --project alpha --slug a$i --tag fixture </dev/null >/dev/null 2>&1 \
    || o add task "Alpha task $i" --project alpha --slug a$i
done
o add task "Beta task" --project beta --slug b1 --priority low --due 2026-12-24
o add task "Floating" --slug f1 --work-dir /tmp/e2e-wd/float
o update task a1 --waiting "review from bob"
o update task a2 --assignee carol
o workdir add /tmp/e2e-wd/alpha --name alpha
o add playbook "Weekly review" --work-dir /tmp/e2e-wd/alpha --slug weekly --project alpha
o add owner "Repo keeper" --work-dir /tmp/e2e-wd/alpha --every 6h --slug keeper
o archive f1
# A bound, in-progress task (as `flow do` would leave it); its session sends
# mail to the human and to a peer task, and watches the project.
/tmp/sqldump -exec /dev/stdin $FLOW_ROOT/flow.db >/dev/null <<'SQL'
UPDATE tasks SET status='in-progress', session_id='aaaaaaaa-1111-4222-8333-444444444444',
  session_started='2026-11-01T10:00:00Z' WHERE slug='a3';
SQL
export CLAUDE_CODE_SESSION_ID=aaaaaaaa-1111-4222-8333-444444444444
o message user "need a decision on alpha" --options "yes|no"
o message user "fyi: beta started" --fyi
o message user/a1 "please review the alpha plan"
o watch alpha
unset CLAUDE_CODE_SESSION_ID
cp $FLOW_ROOT/flow.db /tmp/old-copy.db
before=$(/tmp/sqldump /tmp/old-copy.db | grep -c '^INSERT')
echo "old DB rows: $before"

echo "== open with the new binary"
step $new list tasks
step $new list projects
step $new show task a1 | sed -n 1,12p
step $new show project alpha | sed -n 1,6p
step $new list tasks --include-archived --status backlog
step $new list playbooks
step $new owner list
step $new workdir list
step $new human list 2>/dev/null | sed -n 1,5p || true
n0=$(/tmp/sqldump /tmp/old-copy.db | grep -c '^INSERT INTO "bus_messages"' || true)
n=$(/tmp/sqldump $FLOW_ROOT/flow.db | grep -c '^INSERT INTO "bus_messages"' || true)
[ "$n" = "$n0" ] || { echo "FAIL: bus messages $n0 before, $n after"; exit 1; }
echo "bus_messages rows: $n"
ls $FLOW_ROOT/backups/
nb=$(ls $FLOW_ROOT/backups/ | wc -l)
[ "$nb" = 1 ] || { echo "FAIL: want 1 backup, got $nb"; exit 1; }
cmp -s <(/tmp/sqldump $FLOW_ROOT/backups/* ) <(/tmp/sqldump /tmp/old-copy.db) || { echo "FAIL: backup differs from the pre-migration DB"; exit 1; }
uv=$(/tmp/sqldump $FLOW_ROOT/flow.db | sed -n 's/^PRAGMA user_version=\([0-9]*\);$/\1/p')
[ -n "$uv" ] || { echo "FAIL: user_version not set"; exit 1; }
echo "user_version=$uv"
after=$(/tmp/sqldump $FLOW_ROOT/flow.db | grep '^INSERT' | grep -vc 'schema_meta')
echo "rows before=$before after(excluding schema_meta)=$after"
[ "$after" -ge "$before" ] || { echo "FAIL: rows lost"; exit 1; }
# Per-table counts: every table the old DB had keeps exactly its rows
# (bus_listeners is dropped by the baseline by design).
q() { /tmp/sqldump -query "$1" "$2"; }
for t in $(q "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name NOT IN ('bus_listeners','schema_meta') ORDER BY name" /tmp/old-copy.db); do
  t=${t//\'/}
  c0=$(q "SELECT COUNT(*) FROM \"$t\"" /tmp/old-copy.db)
  c1=$(q "SELECT COUNT(*) FROM \"$t\"" $FLOW_ROOT/flow.db)
  [ "$c0" = "$c1" ] || { echo "FAIL: table $t: $c0 rows before, $c1 after"; exit 1; }
done
# Row identities and values survive: every column the old DB had, for the
# entity tables, compared row by row (keyed by primary key). The fixture is
# already consistent, so the baseline's data fixes must not touch these.
ident() {
  local t cols
  for t in projects tasks workdirs playbooks owners task_tags; do
    cols=$(q "SELECT group_concat('\"' || name || '\"', ',') FROM pragma_table_info('$t')" /tmp/old-copy.db)
    cols=${cols#\'}; cols=${cols%\'}
    [ -n "$cols" ] && [ "$cols" != NULL ] || continue
    echo "-- $t"
    q "SELECT $cols FROM \"$t\" ORDER BY 1, 2" "$1"
  done
}
ident /tmp/old-copy.db > /tmp/ident-before
ident $FLOW_ROOT/flow.db > /tmp/ident-after
grep -q "^'a1'" /tmp/ident-before || { echo "FAIL: identity dump is empty"; exit 1; }
diff /tmp/ident-before /tmp/ident-after || { echo "FAIL: entity rows changed by the migration"; exit 1; }
echo "per-table counts and row identities unchanged"

echo "== writes + hooks on the migrated DB"
step $new add task "New after upgrade" --project alpha --slug n1
step $new update task n1 --priority high
echo '{"session_id":"aaaaaaaa-1111-4222-8333-444444444444","prompt":"hi"}' | step $new hook user-prompt-submit | sed -n 1,3p
echo '{"session_id":"aaaaaaaa-1111-4222-8333-444444444444","tool_name":"Bash"}' | step $new hook pre-tool-use | sed -n 1,3p
nb=$(ls $FLOW_ROOT/backups/ | wc -l)
[ "$nb" = 1 ] || { echo "FAIL: later opens must not back up again (got $nb)"; exit 1; }

echo "== downgrade: the old binary still reads the migrated DB"
step $old list tasks
step $old show task n1 | sed -n 1,4p
echo "E2E OK ($tag → schema v$uv)"
