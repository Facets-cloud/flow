package spec_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/harness/spec"
)

// TestShippedExamplesAreValid keeps examples/harnesses/ honest.
//
// A published example that no longer parses is worse than no example:
// a user copies it, `flow harness validate` rejects it, and the first
// impression of the whole manifest system is that it is broken. Since
// the examples are the primary documentation of the schema, a schema
// change that invalidates one must fail the build here.
func TestShippedExamplesAreValid(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "examples", "harnesses")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	var checked int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			path := filepath.Join(dir, e.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			s, err := spec.Decode(data, path)
			if err != nil {
				t.Fatalf("shipped example does not validate:\n%v", err)
			}
			if _, err := spec.New(s); err != nil {
				t.Fatalf("shipped example does not build an adapter: %v", err)
			}
			// TEMPLATE.toml is the skeleton users copy; it must be a
			// working manifest, not a sketch.
			if s.Name == "" {
				t.Error("manifest declares no name")
			}
		})
		checked++
	}
	if checked == 0 {
		t.Fatal("no example manifests found — the examples directory moved or emptied")
	}
	// The template plus at least the verified set.
	if checked < 4 {
		t.Errorf("only %d examples found; expected TEMPLATE.toml plus the verified manifests", checked)
	}
}

// TestExamplesDeclareDistinctNames: two examples sharing a name would
// make whichever loads second a silent no-op.
func TestExamplesDeclareDistinctNames(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "examples", "harnesses")
	entries, _ := os.ReadDir(dir)
	seen := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		s, err := spec.Decode(data, path)
		if err != nil {
			continue // reported by TestShippedExamplesAreValid
		}
		if prev, dup := seen[s.Name]; dup {
			t.Errorf("harness name %q declared by both %s and %s", s.Name, prev, e.Name())
		}
		seen[s.Name] = e.Name()
	}
}
