#!/usr/bin/env bash
# Container half of the drift fixtures: each scripts/schema-drift/<name>.sql
# names a base release fixture on its first line (`-- base: <tag>`) and is
# applied on top of it; the result is dumped to testdata/schemas/drift-<name>.sql.
# Never run on the host (see gen-schema-fixtures.sh).
set -euo pipefail
[ -f /.dockerenv ] || { echo "refusing to run outside Docker" >&2; exit 1; }
out=/src/internal/flowdb/testdata/schemas
go build -o /tmp/sqldump ./scripts/sqldump
rm -f "$out"/drift-*.sql "$out"/.drift-*.tmp
for f in scripts/schema-drift/*.sql; do
  name=$(basename "$f" .sql)
  base=$(sed -n '1s/^-- base: *//p' "$f")
  [ -f "$out/$base.sql" ] || { echo "$f: base fixture $base.sql missing" >&2; exit 1; }
  db=$(mktemp -u /tmp/drift-XXXXXX.db)
  /tmp/sqldump -exec "$out/$base.sql" -exec "$f" "$db" > "$out/.drift-$name.tmp"
  mv "$out/.drift-$name.tmp" "$out/drift-$name.sql"
  rm -f "$db"
  echo "drift-$name.sql (base $base)"
done
