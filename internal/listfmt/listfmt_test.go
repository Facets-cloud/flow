package listfmt

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseFormat(t *testing.T) {
	cases := []struct {
		in   string
		want Format
		err  bool
	}{
		{"", FormatTable, false},
		{"table", FormatTable, false},
		{"TABLE", FormatTable, false},
		{" json ", FormatJSON, false},
		{"tsv", FormatTSV, false},
		{"yaml", 0, true},
	}
	for _, c := range cases {
		got, err := ParseFormat(c.in)
		if c.err {
			if err == nil {
				t.Errorf("ParseFormat(%q): want error, got nil", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseFormat(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseFormat(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		s    string
		max  int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello", 4, "hel…"},
		{"hello", 1, "…"},
		{"hello", 0, "hello"},
		{"héllo", 4, "hél…"},
	}
	for _, c := range cases {
		got := Truncate(c.s, c.max)
		if got != c.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", c.s, c.max, got, c.want)
		}
	}
}

// TestColorEnabled_NonTTY verifies that a bytes.Buffer (non-TTY writer)
// always returns false, regardless of NO_COLOR. This is the only branch
// we can hermetically test without a pty.
func TestColorEnabled_NonTTY(t *testing.T) {
	var buf bytes.Buffer
	if ColorEnabled(&buf, false) {
		t.Error("ColorEnabled on bytes.Buffer: want false, got true")
	}
}

func TestColorEnabled_ForceOff(t *testing.T) {
	var buf bytes.Buffer
	if ColorEnabled(&buf, true) {
		t.Error("ColorEnabled(forceOff=true): want false")
	}
}

func TestPainter_Disabled(t *testing.T) {
	p := Painter{Enabled: false}
	got := p.Wrap("hello", Red)
	if got != "hello" {
		t.Errorf("disabled Painter.Wrap = %q, want %q", got, "hello")
	}
}

func TestPainter_Enabled(t *testing.T) {
	p := Painter{Enabled: true}
	got := p.Wrap("hello", Red)
	// Wrapped output contains the ANSI code, the text, and the reset, all
	// bracketed by 0xff markers.
	if !strings.Contains(got, Red) || !strings.Contains(got, "hello") || !strings.Contains(got, Reset) {
		t.Errorf("enabled Painter.Wrap missing parts: %q", got)
	}
	if !strings.HasPrefix(got, "\xff") || !strings.HasSuffix(got, "\xff") {
		t.Errorf("enabled Painter.Wrap missing 0xff markers: %q", got)
	}
}

// TestTable_AlignedColumns verifies that tabwriter aligns columns to the
// widest cell, regardless of input width — the primary value-add over the
// hand-rolled Sprintf padding we're replacing.
func TestTable_AlignedColumns(t *testing.T) {
	tab := &Table{
		Headers: []string{"A", "B"},
		Rows: [][]string{
			{"short", "x"},
			{"a-much-longer-cell", "y"},
			{"mid", "z"},
		},
	}
	var buf bytes.Buffer
	if err := tab.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines, got %d: %q", len(lines), buf.String())
	}
	// The "B" column should start at the same column index in every row,
	// because tabwriter pads the first column to its widest cell.
	bIdx := -1
	for i, line := range lines {
		idx := strings.LastIndexAny(line, "xyzB")
		if i == 0 {
			bIdx = idx
			continue
		}
		if idx != bIdx {
			t.Errorf("line %d: B-column at %d, want %d (line=%q)", i, idx, bIdx, line)
		}
	}
}

// TestTable_StripsEscape verifies the StripEscape flag is honored — 0xff
// bytes from Painter.Wrap must not appear in the output stream.
func TestTable_StripsEscape(t *testing.T) {
	p := Painter{Enabled: true}
	tab := &Table{
		Headers: []string{"name"},
		Rows: [][]string{
			{p.Wrap("hello", Red)},
		},
	}
	var buf bytes.Buffer
	if err := tab.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(buf.String(), "\xff") {
		t.Errorf("output contains 0xff bytes: %q", buf.String())
	}
	if !strings.Contains(buf.String(), Red) {
		t.Errorf("output missing ANSI code: %q", buf.String())
	}
}

func TestRenderJSON(t *testing.T) {
	type row struct {
		Slug   string `json:"slug"`
		Status string `json:"status"`
	}
	rows := []row{{"a", "in-progress"}, {"b", "backlog"}}
	var buf bytes.Buffer
	if err := RenderJSON(&buf, rows); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"slug": "a"`) || !strings.Contains(out, `"status": "backlog"`) {
		t.Errorf("RenderJSON output unexpected: %s", out)
	}
}

func TestRenderTSV(t *testing.T) {
	var buf bytes.Buffer
	err := RenderTSV(&buf, []string{"slug", "status"}, [][]string{
		{"a", "in-progress"},
		{"b", "backlog"},
	})
	if err != nil {
		t.Fatalf("RenderTSV: %v", err)
	}
	want := "slug\tstatus\na\tin-progress\nb\tbacklog\n"
	if buf.String() != want {
		t.Errorf("RenderTSV =\n%q\nwant\n%q", buf.String(), want)
	}
}
