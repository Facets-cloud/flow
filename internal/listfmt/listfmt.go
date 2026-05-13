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
	"strings"
	"text/tabwriter"

	"github.com/mattn/go-isatty"
)

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

// Wrap returns text decorated with the given ANSI code. The decoration is
// bracketed by tabwriter.Escape (0xff) markers so the color bytes are excluded
// from column-width calculations. The Table renderer strips the markers
// before emitting output.
func (p Painter) Wrap(text, code string) string {
	if !p.Enabled || code == "" {
		return text
	}
	return "\xff" + code + "\xff" + text + "\xff" + Reset + "\xff"
}

// Table is a simple column-aligned renderer backed by text/tabwriter.
type Table struct {
	Headers []string
	Rows    [][]string
}

// Render writes the table to w. Columns are tab-separated on input and
// auto-padded to align on output. tabwriter.StripEscape is set so 0xff
// markers (used by Painter.Wrap to hide ANSI codes from width math) are
// removed from the output stream.
func (t *Table) Render(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.StripEscape)
	if len(t.Headers) > 0 {
		if _, err := fmt.Fprintln(tw, strings.Join(t.Headers, "\t")); err != nil {
			return err
		}
	}
	for _, row := range t.Rows {
		if _, err := fmt.Fprintln(tw, strings.Join(row, "\t")); err != nil {
			return err
		}
	}
	return tw.Flush()
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
