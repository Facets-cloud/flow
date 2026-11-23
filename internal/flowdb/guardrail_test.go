package flowdb

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var schemaWriteRE = regexp.MustCompile(`(?i)\b(ALTER\s+TABLE|CREATE\s+(UNIQUE\s+)?(TABLE|INDEX)|DROP\s+(TABLE|INDEX)|PRAGMA\s+user_version|PRAGMA\s+foreign_keys)\b|columnExists\(`)

// Guardrail: schema DDL and the column probes live only in the migrator.
// Anything else that creates/alters/drops tables or indexes, or touches
// user_version, bypasses versioning (and its backup + single transaction).
// Add a Migration in migrate.go instead.
func TestNoSchemaWritesOutsideMigrations(t *testing.T) {
	allowed := map[string]bool{
		filepath.Join("flowdb", "migrate.go"):  true,
		filepath.Join("flowdb", "baseline.go"): true,
	}
	re := schemaWriteRE
	root := ".." // internal/
	var bad []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if allowed[rel] {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, h := range schemaWriteHits(re, b) {
			bad = append(bad, rel+":"+h)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Errorf("schema changes outside internal/flowdb/{migrate,baseline}.go — add a Migration instead:\n%s",
			strings.Join(bad, "\n"))
	}
}

// schemaWriteHits returns "line: match" for every guardrail match in src.
// // comment lines are blanked first (offsets and newlines kept), then the
// whole source is matched, so a statement split across lines of a raw
// string literal is still caught.
func schemaWriteHits(re *regexp.Regexp, b []byte) []string {
	src := append([]byte(nil), b...)
	off := 0
	for _, line := range strings.SplitAfter(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			for k := off; k < off+len(line); k++ {
				if src[k] != '\n' {
					src[k] = ' '
				}
			}
		}
		off += len(line)
	}
	var out []string
	for _, loc := range re.FindAllIndex(src, -1) {
		n := 1 + bytes.Count(src[:loc[0]], []byte("\n"))
		out = append(out, fmt.Sprintf("%d: %s", n, strings.Join(strings.Fields(string(src[loc[0]:loc[1]])), " ")))
	}
	return out
}

func TestGuardrailScan(t *testing.T) {
	re := schemaWriteRE
	src := "package x\n// CREATE TABLE in a comment is fine\nvar a = `CREATE\n  TABLE stray (id INTEGER)`\nvar b = \"ALTER TABLE t ADD c\"\n"
	got := schemaWriteHits(re, []byte(src))
	want := []string{"3: CREATE TABLE", "5: ALTER TABLE"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("hits = %q, want %q", got, want)
	}
}
