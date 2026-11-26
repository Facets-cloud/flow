package app

import (
	"io/fs"
	"strings"
	"testing"
)

// driver.md gives a concrete inline-vs-delegate test instead of the old
// "how to split is your call" escape hatch, so drivers actually spawn
// children for multi-part work.
func TestDriverMDHasConcreteDelegationTest(t *testing.T) {
	b, err := fs.ReadFile(skillFiles(), "references/driver.md")
	if err != nil {
		t.Fatal(err)
	}
	// Collapse wrapping so the assertion is independent of line breaks.
	got := strings.Join(strings.Fields(string(b)), " ")
	want := "Inline only if it is one coherent step you can finish in ~15 minutes, " +
		"or glue/verification. Anything with separable parts (API / UI / infra / " +
		"tests / research) or longer than ~15 minutes goes to `flow auto` children; " +
		"you keep the decisions and verify their output."
	if !strings.Contains(got, want) {
		t.Errorf("driver.md missing the concrete delegation test:\n%s", want)
	}
	for _, old := range []string{"How to split is your call", "small work may stay inline"} {
		if strings.Contains(got, old) {
			t.Errorf("driver.md still carries the old escape hatch %q", old)
		}
	}
}
