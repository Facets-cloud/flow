package app

import (
	"regexp"
	"testing"
)

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewUUIDFormat(t *testing.T) {
	for i := 0; i < 50; i++ {
		id, err := newUUID()
		if err != nil {
			t.Fatalf("newUUID: %v", err)
		}
		if !uuidRe.MatchString(id) {
			t.Errorf("newUUID returned %q, does not match UUID v4 format", id)
		}
	}
}

func TestNewUUIDUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id, err := newUUID()
		if err != nil {
			t.Fatal(err)
		}
		if seen[id] {
			t.Fatalf("duplicate UUID after %d: %s", i, id)
		}
		seen[id] = true
	}
}

// TestEncodeCwdForClaude pins the empirical rule derived from
// ~/.claude/projects/* vs. the original cwd recorded in each dir's
// *.jsonl. `/`, `.`, and `_` each map to `-`; everything else is
// unchanged. If a new sample surfaces that needs a different rule, add
// the observed pair here before touching EncodeCwdForClaude.
func TestEncodeCwdForClaude(t *testing.T) {
	cases := []struct {
		cwd, want string
	}{
		// Plain path — only slashes transform.
		{"/Users/alice/code/myapp", "-Users-alice-code-myapp"},
		// Dotfile segment: `.flow` becomes `-flow`, producing a double
		// dash after `alice`.
		{"/Users/alice/.flow/tasks/add-oauth/workspace",
			"-Users-alice--flow-tasks-add-oauth-workspace"},
		// Underscores in a path segment also transform — observed on
		// Terraform module paths with numeric prefixes.
		{"/Users/alice/monorepo/tf/modules/1_input_instance/application_gcp",
			"-Users-alice-monorepo-tf-modules-1-input-instance-application-gcp"},
		// Underscore-prefix dir — seen in some workspace trees;
		// `/_default` becomes `--default`.
		{"/Users/alice/.workspaces/instances/default/projects/abc/def/_default",
			"-Users-alice--workspaces-instances-default-projects-abc-def--default"},
		// Hyphens, digits, and mixed case pass through unchanged.
		{"/Users/alice/Downloads/my-charts-45dae5e1171f",
			"-Users-alice-Downloads-my-charts-45dae5e1171f"},
	}
	for _, tc := range cases {
		if got := EncodeCwdForClaude(tc.cwd); got != tc.want {
			t.Errorf("EncodeCwdForClaude(%q) = %q, want %q", tc.cwd, got, tc.want)
		}
	}
}
