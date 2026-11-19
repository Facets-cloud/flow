// Command sqldump writes a SQLite database as replayable SQL (schema plus
// rows), like the sqlite3 CLI's .dump, without needing the CLI. It is
// dev tooling for scripts/gen-schema-fixtures.sh: the historical schema
// fixtures under internal/flowdb/testdata/schemas/ are produced with it.
//
//	go run ./scripts/sqldump <db>            # dump to stdout
//	go run ./scripts/sqldump -schema <db>    # print a normalized schema digest
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
	var execs multi
	flag.Var(&execs, "exec", "SQL file to execute against the DB before dumping (repeatable, in order)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: sqldump [-schema] [-exec file.sql ...] <db>")
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

// digest prints one line per table (sorted column names) and per index, so
// two DBs with the same logical shape print the same text regardless of
// column order or whitespace in the stored DDL.
func digest(db *sql.DB) error {
	objs, err := objects(db)
	if err != nil {
		return err
	}
	var lines []string
	for _, o := range objs {
		switch o.typ {
		case "table":
			cols, err := columns(db, o.name)
			if err != nil {
				return err
			}
			sort.Strings(cols)
			lines = append(lines, "table "+o.name+" ("+strings.Join(cols, ",")+")")
		default:
			lines = append(lines, o.typ+" "+o.name+" on "+o.tbl)
		}
	}
	sort.Strings(lines)
	fmt.Println(strings.Join(lines, "\n"))
	return nil
}

func columns(db *sql.DB, table string) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

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
