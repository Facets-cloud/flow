package flowdb

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Guardrail: schema DDL and the column probes live only in the migrator.
// Anything else that creates/alters/drops tables or indexes, or touches
// user_version, bypasses versioning (and its backup + single transaction).
// Add a Migration in migrate.go instead.
func TestNoSchemaWritesOutsideMigrations(t *testing.T) {
	allowed := map[string]bool{
		filepath.Join("flowdb", "migrate.go"):  true,
		filepath.Join("flowdb", "baseline.go"): true,
	}
	re := regexp.MustCompile(`(?i)\b(ALTER\s+TABLE|CREATE\s+(UNIQUE\s+)?(TABLE|INDEX)|DROP\s+(TABLE|INDEX)|PRAGMA\s+user_version|PRAGMA\s+foreign_keys)\b|columnExists\(`)
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
		for i, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if re.MatchString(line) {
				bad = append(bad, fmt.Sprintf("%s:%d: %s", rel, i+1, strings.TrimSpace(line)))
			}
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
