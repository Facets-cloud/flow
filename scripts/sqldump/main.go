// Command sqldump writes a SQLite database as replayable SQL (schema plus
// rows), like the sqlite3 CLI's .dump, without needing the CLI. It is
// dev tooling for scripts/gen-schema-fixtures.sh: the historical schema
// fixtures under internal/flowdb/testdata/schemas/ are produced with it.
//
//	go run ./scripts/sqldump <db>            # dump to stdout
//	go run ./scripts/sqldump -schema <db>    # print a normalized schema digest
//	go run ./scripts/sqldump -query 'SELECT …' <db>  # print result rows, tab-separated
package main

import (
	"database/sql"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

func main() {
	schemaOnly := flag.Bool("schema", false, "print a normalized schema digest instead of a dump")
	query := flag.String("query", "", "run this SELECT and print its rows (tab-separated) instead of a dump")
	var execs multi
	flag.Var(&execs, "exec", "SQL file to execute against the DB before dumping (repeatable, in order)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: sqldump [-schema | -query SQL] [-exec file.sql ...] <db>")
		os.Exit(2)
	}
	db, err := sql.Open("sqlite", "file:"+flag.Arg(0))
	if err != nil {
		fatal(err)
	}
	defer db.Close()
	for _, f := range execs {
		b, err := os.ReadFile(f)
		if err != nil {
			fatal(err)
		}
		if _, err := db.Exec(string(b)); err != nil {
			fatal(fmt.Errorf("exec %s: %w", f, err))
		}
	}
	if *query != "" {
		if err := printQuery(db, *query); err != nil {
			fatal(err)
		}
		return
	}
	if *schemaOnly {
		if err := digest(db); err != nil {
			fatal(err)
		}
		return
	}
	if err := dump(db); err != nil {
		fatal(err)
	}
}

// printQuery prints each result row as tab-separated literals (NULL, 'text',
// numbers), one row per line.
func printQuery(db *sql.DB, q string) error {
	rows, err := db.Query(q)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		lits := make([]string, len(vals))
		for i, v := range vals {
			lits[i] = literal(v)
		}
		fmt.Println(strings.Join(lits, "\t"))
	}
	return rows.Err()
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "sqldump:", err)
	os.Exit(1)
}

type obj struct{ typ, name, tbl, sql string }

func objects(db *sql.DB) ([]obj, error) {
	rows, err := db.Query(`SELECT type, name, tbl_name, sql FROM sqlite_master
		WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []obj
	for rows.Next() {
		var o obj
		if err := rows.Scan(&o.typ, &o.name, &o.tbl, &o.sql); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// digest prints a normalized description of the schema: per table its
// column definitions (type, notnull, default, pk), foreign keys (with
// referential actions), CHECK constraints and indexes (uniqueness, partial,
// key columns), plus any views/triggers. Each set is sorted, so two DBs
// with the same logical shape print the same text regardless of column
// order or whitespace in the stored DDL, while a changed default,
// constraint or index definition prints differently.
func digest(db *sql.DB) error {
	objs, err := objects(db)
	if err != nil {
		return err
	}
	var lines []string
	for _, o := range objs {
		switch o.typ {
		case "table":
			tl, err := tableDigest(db, o)
			if err != nil {
				return err
			}
			lines = append(lines, tl...)
		case "index":
			// Covered per table via pragma_index_list (which also sees
			// the sqlite_autoindex_* behind UNIQUE/PRIMARY KEY constraints).
		default:
			lines = append(lines, o.typ+" "+o.name+" on "+o.tbl+": "+normSQL(o.sql))
		}
	}
	sort.Strings(lines)
	fmt.Println(strings.Join(lines, "\n"))
	return nil
}

func tableDigest(db *sql.DB, o obj) ([]string, error) {
	t := o.name
	var out []string
	cols, err := queryLines(db, `SELECT 'table `+t+` col ' || name || ' ' || type || ' nn=' || "notnull" ||
		' dflt=' || COALESCE(dflt_value,'') || ' pk=' || pk FROM pragma_table_info(?)`, t)
	if err != nil {
		return nil, err
	}
	out = append(out, cols...)
	fks, err := queryLines(db, `SELECT 'table `+t+` fk ' || "from" || ' -> ' || "table" || '(' || COALESCE("to",'') ||
		') on_update=' || on_update || ' on_delete=' || on_delete || ' match=' || "match" FROM pragma_foreign_key_list(?)`, t)
	if err != nil {
		return nil, err
	}
	out = append(out, fks...)
	idx, err := queryLines(db, `SELECT il.name || char(9) || il."unique" || char(9) || il.partial || char(9) || il.origin ||
		char(9) || COALESCE(m.sql,'') FROM pragma_index_list(?) il LEFT JOIN sqlite_master m ON m.name = il.name`, t)
	if err != nil {
		return nil, err
	}
	for _, l := range idx {
		f := strings.SplitN(l, "\t", 5)
		keys, err := queryLines(db, `SELECT COALESCE(name, '<expr>') FROM pragma_index_info(?) ORDER BY seqno`, f[0])
		if err != nil {
			return nil, err
		}
		name := f[0]
		if strings.HasPrefix(name, "sqlite_autoindex_") {
			name = "<auto>" // numbering depends on constraint order
		}
		line := fmt.Sprintf("table %s index %s unique=%s partial=%s origin=%s (%s)",
			t, name, f[1], f[2], f[3], strings.Join(keys, ","))
		if f[2] == "1" {
			if i := strings.Index(strings.ToUpper(f[4]), " WHERE "); i >= 0 {
				line += " where " + normSQL(f[4][i+7:])
			}
		}
		out = append(out, line)
	}
	for _, c := range checkClauses(o.sql) {
		out = append(out, "table "+t+" check "+c)
	}
	return out, nil
}

func queryLines(db *sql.DB, q string, args ...any) ([]string, error) {
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// checkClauses extracts every CHECK(...) clause (balanced parens) from a
// CREATE TABLE statement, whitespace-normalized.
func checkClauses(ddl string) []string {
	var out []string
	up := strings.ToUpper(ddl)
	for i := 0; ; {
		j := strings.Index(up[i:], "CHECK")
		if j < 0 {
			break
		}
		k := i + j + len("CHECK")
		for k < len(ddl) && (ddl[k] == ' ' || ddl[k] == '\t' || ddl[k] == '\n' || ddl[k] == '\r') {
			k++
		}
		if k >= len(ddl) || ddl[k] != '(' {
			i = k
			continue
		}
		depth, e := 0, k
		for ; e < len(ddl); e++ {
			if ddl[e] == '(' {
				depth++
			} else if ddl[e] == ')' {
				if depth--; depth == 0 {
					break
				}
			}
		}
		if e >= len(ddl) {
			break
		}
		out = append(out, normSQL(ddl[k:e+1]))
		i = e + 1
	}
	sort.Strings(out)
	return out
}

func normSQL(s string) string { return strings.Join(strings.Fields(s), " ") }

func dump(db *sql.DB) error {
	objs, err := objects(db)
	if err != nil {
		return err
	}
	w := os.Stdout
	fmt.Fprintln(w, "PRAGMA foreign_keys=OFF;")
	fmt.Fprintln(w, "BEGIN TRANSACTION;")
	for _, o := range objs {
		if o.typ != "table" {
			continue
		}
		fmt.Fprintln(w, o.sql+";")
		if err := dumpRows(db, w, o.name); err != nil {
			return err
		}
	}
	for _, o := range objs {
		if o.typ == "table" {
			continue
		}
		fmt.Fprintln(w, o.sql+";")
	}
	var uv int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&uv); err != nil {
		return err
	}
	if uv != 0 {
		fmt.Fprintf(w, "PRAGMA user_version=%d;\n", uv)
	}
	fmt.Fprintln(w, "COMMIT;")
	return nil
}

func dumpRows(db *sql.DB, w *os.File, table string) error {
	rows, err := db.Query(`SELECT * FROM "` + table + `" ORDER BY rowid`)
	if err != nil {
		// WITHOUT ROWID tables have no rowid; fall back to natural order.
		rows, err = db.Query(`SELECT * FROM "` + table + `"`)
		if err != nil {
			return err
		}
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		lits := make([]string, len(vals))
		for i, v := range vals {
			lits[i] = literal(v)
		}
		fmt.Fprintf(w, "INSERT INTO \"%s\" VALUES(%s);\n", table, strings.Join(lits, ","))
	}
	return rows.Err()
}

func literal(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case []byte:
		return "X'" + hex.EncodeToString(x) + "'"
	case string:
		return "'" + strings.ReplaceAll(x, "'", "''") + "'"
	case bool:
		if x {
			return "1"
		}
		return "0"
	default:
		return "'" + strings.ReplaceAll(fmt.Sprint(x), "'", "''") + "'"
	}
}
