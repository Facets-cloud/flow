// Package listfmt is the shared output renderer used by every `flow list`
// subcommand. It wraps text/tabwriter for table output, adds JSON and TSV
// emitters, and provides ANSI color helpers that disable themselves when
// stdout is not a TTY.
package listfmt

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-isatty"
)

// ansiSGR matches ANSI SGR (Select Graphic Rendition) escape sequences —
// the family of codes used for color/bold/dim/etc. We strip these to
// compute the *visible* width of a cell, which is what tabular alignment
// must care about. Go's text/tabwriter has no built-in way to exempt
// inline ANSI from its width math (despite what the Escape-character
// docs suggest for tab-bearing cells), so we render manually.
var ansiSGR = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// visibleWidth returns the rune count of s after stripping ANSI SGR
// escape sequences. This is the width tabular renderers should pad to.
func visibleWidth(s string) int {
	return utf8.RuneCountInString(ansiSGR.ReplaceAllString(s, ""))
}

// Format selects how a list result is serialized to the output stream.
type Format int

const (
	FormatTable Format = iota
	FormatJSON
	FormatTSV
)

// ParseFormat converts a user-supplied string into a Format. Empty string and
// "table" both yield FormatTable; "json" and "tsv" are the other accepted
// values. Comparison is case-insensitive and ignores surrounding whitespace.
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "table":
		return FormatTable, nil
	case "json":
		return FormatJSON, nil
	case "tsv":
		return FormatTSV, nil
	}
	return 0, fmt.Errorf("invalid format %q (want table|json|tsv)", s)
}

// ANSI color codes used by the color-aware renderers below.
const (
	Reset  = "\x1b[0m"
	Bold   = "\x1b[1m"
	Dim    = "\x1b[2m"
	Red    = "\x1b[31m"
	Green  = "\x1b[32m"
	Yellow = "\x1b[33m"
	Blue   = "\x1b[34m"
	Cyan   = "\x1b[36m"
	Gray   = "\x1b[90m"
)

// ColorEnabled reports whether ANSI color codes should be emitted to w.
// Color is disabled when forceOff is true, when the NO_COLOR env var is set
// (per https://no-color.org), or when w is not a TTY.
func ColorEnabled(w io.Writer, forceOff bool) bool {
	if forceOff {
		return false
	}
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd())
}

// Painter applies ANSI color when Enabled, otherwise returns text unchanged.
type Painter struct {
	Enabled bool
}

// Wrap returns text decorated with the given ANSI code. When color is
// disabled or code is empty, the input is returned unchanged.
func (p Painter) Wrap(text, code string) string {
	if !p.Enabled || code == "" {
		return text
	}
	return code + text + Reset
}

// Table is a column-aligned renderer. Columns auto-size to the widest
// *visible* cell — ANSI escape codes are ignored for width purposes, so a
// row with colored cells aligns identically to a row of plain text.
type Table struct {
	Headers []string
	Rows    [][]string
	// ColumnGap is the number of spaces inserted between adjacent columns
	// after the widest cell. Zero falls back to 2.
	ColumnGap int
}

// Render writes the table to w. Trailing whitespace is trimmed from every
// emitted line so an empty last cell doesn't litter output with spaces.
func (t *Table) Render(w io.Writer) error {
	gap := t.ColumnGap
	if gap <= 0 {
		gap = 2
	}

	allRows := t.Rows
	if len(t.Headers) > 0 {
		allRows = append([][]string{t.Headers}, t.Rows...)
	}
	if len(allRows) == 0 {
		return nil
	}

	ncols := 0
	for _, r := range allRows {
		if len(r) > ncols {
			ncols = len(r)
		}
	}
	widths := make([]int, ncols)
	for _, r := range allRows {
		for i, cell := range r {
			if vw := visibleWidth(cell); vw > widths[i] {
				widths[i] = vw
			}
		}
	}

	for _, r := range allRows {
		var sb strings.Builder
		for i := 0; i < ncols; i++ {
			cell := ""
			if i < len(r) {
				cell = r[i]
			}
			sb.WriteString(cell)
			if i < ncols-1 {
				pad := widths[i] - visibleWidth(cell) + gap
				if pad > 0 {
					sb.WriteString(strings.Repeat(" ", pad))
				}
			}
		}
		line := strings.TrimRight(sb.String(), " ")
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}

// RenderJSON encodes v as indented JSON. Callers should pass a slice of
// concrete structs (or maps) — json.Marshal handles key ordering for structs
// based on field declaration order, giving a stable output.
func RenderJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// RenderTSV emits a tab-separated values stream with a header row.
func RenderTSV(w io.Writer, headers []string, rows [][]string) error {
	if _, err := fmt.Fprintln(w, strings.Join(headers, "\t")); err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := fmt.Fprintln(w, strings.Join(r, "\t")); err != nil {
			return err
		}
	}
	return nil
}

// Truncate shortens s to maxRunes runes, appending an ellipsis when the
// string was actually shortened. maxRunes <= 0 disables truncation.
func Truncate(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	if maxRunes == 1 {
		return "…"
	}
	return string(runes[:maxRunes-1]) + "…"
}
