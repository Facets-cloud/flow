package managedblock

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newBlock(t *testing.T, name string) Block {
	t.Helper()
	return Block{
		Path:    filepath.Join(t.TempDir(), "AGENTS.md"),
		Name:    name,
		Comment: HTML,
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestApplyCreatesFile(t *testing.T) {
	b := newBlock(t, "flow:managed")
	changed, err := b.Apply("hello")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("creating a file should report changed")
	}
	got := read(t, b.Path)
	if !strings.Contains(got, "<!-- flow:managed:start -->") ||
		!strings.Contains(got, "<!-- flow:managed:end -->") ||
		!strings.Contains(got, "hello") {
		t.Errorf("unexpected content:\n%s", got)
	}
}

// TestApplyPreservesSurroundingContent is the property that matters
// most: this file belongs to the user, and flow is a guest in it.
func TestApplyPreservesSurroundingContent(t *testing.T) {
	b := newBlock(t, "flow:managed")
	original := "# My notes\n\nkeep me\n\n<!-- other-tool:start -->\nnot ours\n<!-- other-tool:end -->\n\ntrailing prose\n"
	if err := os.WriteFile(b.Path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Apply("flow content"); err != nil {
		t.Fatal(err)
	}
	got := read(t, b.Path)
	for _, want := range []string{"# My notes", "keep me", "<!-- other-tool:start -->", "not ours", "trailing prose", "flow content"} {
		if !strings.Contains(got, want) {
			t.Errorf("lost %q:\n%s", want, got)
		}
	}

	// A second tool's identical marker pattern must survive an update
	// and a removal of ours.
	if _, err := b.Apply("flow content v2"); err != nil {
		t.Fatal(err)
	}
	got = read(t, b.Path)
	if strings.Contains(got, "flow content\n") && !strings.Contains(got, "flow content v2") {
		t.Errorf("update did not replace the old body:\n%s", got)
	}
	if !strings.Contains(got, "not ours") {
		t.Errorf("another tool's block was damaged:\n%s", got)
	}
	if _, err := b.Remove(); err != nil {
		t.Fatal(err)
	}
	got = read(t, b.Path)
	if strings.Contains(got, "flow:managed") {
		t.Errorf("remove left our markers behind:\n%s", got)
	}
	for _, want := range []string{"# My notes", "keep me", "not ours", "trailing prose"} {
		if !strings.Contains(got, want) {
			t.Errorf("remove damaged %q:\n%s", want, got)
		}
	}
}

// TestApplyIsIdempotent: re-running an install must not report a change
// or churn the file, or `flow skill install` would look like it did
// something every single time.
func TestApplyIsIdempotent(t *testing.T) {
	b := newBlock(t, "flow:managed")
	if _, err := b.Apply("stable body"); err != nil {
		t.Fatal(err)
	}
	first := read(t, b.Path)

	changed, err := b.Apply("stable body")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("re-applying identical content reported a change")
	}
	if got := read(t, b.Path); got != first {
		t.Errorf("re-apply rewrote the file:\n%q\nvs\n%q", got, first)
	}
}

func TestApplyUpdatesInPlace(t *testing.T) {
	b := newBlock(t, "flow:managed")
	if _, err := b.Apply("v1"); err != nil {
		t.Fatal(err)
	}
	changed, err := b.Apply("v2")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("changing the body should report a change")
	}
	got := read(t, b.Path)
	if strings.Contains(got, "v1") {
		t.Errorf("old body survived:\n%s", got)
	}
	if n := strings.Count(got, "flow:managed:start"); n != 1 {
		t.Errorf("expected exactly 1 start marker, found %d:\n%s", n, got)
	}
}

// TestRemoveDeletesFlowOnlyFile: if flow created the file and flow's
// region is all that is in it, uninstall should leave no litter.
func TestRemoveDeletesFlowOnlyFile(t *testing.T) {
	b := newBlock(t, "flow:managed")
	if _, err := b.Apply("only ours"); err != nil {
		t.Fatal(err)
	}
	removed, err := b.Remove()
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Error("Remove reported nothing removed")
	}
	if _, err := os.Stat(b.Path); !os.IsNotExist(err) {
		t.Errorf("file flow created and solely owned was left behind: %v", err)
	}
}

func TestRemoveOnAbsentFileIsNoOp(t *testing.T) {
	b := newBlock(t, "flow:managed")
	removed, err := b.Remove()
	if err != nil {
		t.Fatalf("removing from a nonexistent file errored: %v", err)
	}
	if removed {
		t.Error("reported a removal from a file that does not exist")
	}
}

// TestTruncatedBlockIsRepaired: a hand-edited file with a start marker
// and no end must not cause flow to eat the rest of the file.
func TestTruncatedBlockIsRepaired(t *testing.T) {
	b := newBlock(t, "flow:managed")
	broken := "before\n\n<!-- flow:managed:start -->\nhalf a block\n\nafter\n"
	if err := os.WriteFile(b.Path, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Apply("repaired"); err != nil {
		t.Fatal(err)
	}
	got := read(t, b.Path)
	if !strings.Contains(got, "before") || !strings.Contains(got, "after") {
		t.Errorf("a truncated marker caused content loss:\n%s", got)
	}
	if !strings.Contains(got, "repaired") {
		t.Errorf("new content not written:\n%s", got)
	}
}

// TestStrayEndMarkerBeforeStart guards the inverted-range case: an end
// marker appearing before the start must not delete everything between.
func TestStrayEndMarkerBeforeStart(t *testing.T) {
	b := newBlock(t, "flow:managed")
	tricky := "<!-- flow:managed:end -->\nprecious\n<!-- flow:managed:start -->\nbody\n<!-- flow:managed:end -->\n"
	if err := os.WriteFile(b.Path, []byte(tricky), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Apply("new body"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, b.Path); !strings.Contains(got, "precious") {
		t.Errorf("content between a stray end marker and the real start was eaten:\n%s", got)
	}
}

func TestCommentSyntaxes(t *testing.T) {
	cases := []struct {
		comment Comment
		want    string
	}{
		{HTML, "<!-- flow:managed:start -->"},
		{Hash, "# flow:managed:start"},
		{Slash, "// flow:managed:start"},
	}
	for _, tc := range cases {
		t.Run(string(tc.comment), func(t *testing.T) {
			b := Block{
				Path:    filepath.Join(t.TempDir(), "config"),
				Name:    "flow:managed",
				Comment: tc.comment,
			}
			if _, err := b.Apply("x"); err != nil {
				t.Fatal(err)
			}
			if got := read(t, b.Path); !strings.Contains(got, tc.want) {
				t.Errorf("want marker %q:\n%s", tc.want, got)
			}
			if !b.Present() {
				t.Error("Present() false right after Apply")
			}
		})
	}
}

func TestValidRejectsUnknownComment(t *testing.T) {
	if Valid(Comment("emoji")) {
		t.Error("unknown comment syntax accepted")
	}
	for _, c := range []Comment{HTML, Hash, Slash} {
		if !Valid(c) {
			t.Errorf("%s should be valid", c)
		}
	}
}
