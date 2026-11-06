package opencode

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

// inOrder fails unless every want appears in out in the given order.
func inOrder(t *testing.T, out string, want ...string) {
	t.Helper()
	pos := 0
	for _, w := range want {
		i := strings.Index(out[pos:], w)
		if i < 0 {
			t.Fatalf("%q not found in order after byte %d\n--- output ---\n%s", w, pos, out)
		}
		pos += i + len(w)
	}
}

func renderFixture(t *testing.T, compact bool) string {
	t.Helper()
	data, err := os.ReadFile("testdata/export.json")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := RenderExport(data, compact, &buf); err != nil {
		t.Fatalf("RenderExport: %v", err)
	}
	return buf.String()
}

func TestRenderExportFixture(t *testing.T) {
	out := renderFixture(t, false)

	if strings.HasPrefix(out, "\n") {
		t.Errorf("output starts with a blank line:\n%q", out)
	}
	inOrder(t, out,
		"─── User ───",
		`Run bash: echo "P=[$FLOW_PROBE]`,
		"─── Thinking ───",
		"─── Tool: shell ───",
		`"command"`,
		"─── Result ───",
		"P=[from-plugin] F=[plug1]",
		"─── Assistant ───",
		"The secret word is **BANANA**.",
	)
	if strings.Contains(out, "idle") || strings.Contains(out, "succeeded") {
		t.Errorf("session metadata leaked into output:\n%s", out)
	}
}

func TestRenderExportFixtureCompact(t *testing.T) {
	out := renderFixture(t, true)

	inOrder(t, out,
		"─── User ───",
		`Run bash: echo "P=[$FLOW_PROBE]`,
		"─── Tool: shell ───",
		`"command"`,
		"─── Assistant ───",
		"The secret word is **BANANA**.",
	)
	for _, absent := range []string{"Thinking", "Result"} {
		if strings.Contains(out, absent) {
			t.Errorf("compact output should omit %q:\n%s", absent, out)
		}
	}
}

func TestRenderExportInline(t *testing.T) {
	tests := []struct {
		name      string
		json      string
		compact   bool
		want      []string
		absent    []string
		wantErr   bool
		wantEmpty bool
	}{
		{
			name: "unquoted user text untouched",
			json: `{"messages":[{"type":"user","text":"hello world"}]}`,
			want: []string{"─── User ───\nhello world\n"},
		},
		{
			name: "quoted user text unwrapped",
			json: `{"messages":[{"type":"user","text":"\"hi there\""}]}`,
			want: []string{"─── User ───\nhi there\n"},
		},
		{
			name:   "unparsable quotes stripped",
			json:   `{"messages":[{"type":"user","text":"\"bad \\q escape\""}]}`,
			want:   []string{`bad \q escape`},
			absent: []string{`"bad`},
		},
		{
			name: "tool error without content",
			json: `{"messages":[{"type":"assistant","content":[` +
				`{"type":"tool","name":"bash","state":{"status":"error","error":"boom"}}]}]}`,
			want: []string{"─── Tool: bash ───", "─── Result ───", "boom"},
		},
		{
			name:    "tool error compact has no result",
			json:    `{"messages":[{"type":"assistant","content":[{"type":"tool","name":"bash","state":{"status":"error","error":"boom"}}]}]}`,
			compact: true,
			want:    []string{"─── Tool: bash ───"},
			absent:  []string{"Result", "boom"},
		},
		{
			name: "unknown message types skipped",
			json: `{"messages":[{"type":"idle"},{"type":"system","text":"x"},` +
				`{"type":"compaction","text":"x"},{"type":"agent-switched"},{"type":"mystery","text":"x"}]}`,
			wantEmpty: true,
		},
		{
			name: "empty text blocks skipped",
			json: `{"messages":[{"type":"synthetic","text":""},` +
				`{"type":"assistant","content":[{"type":"text","text":""},{"type":"reasoning","text":""}]}]}`,
			wantEmpty: true,
		},
		{
			name: "missing fields do not panic",
			json: `{"info":{},"messages":[{"type":"user"},{"type":"assistant","content":[` +
				`{"type":"tool"},{"type":"tool","name":"x","state":null},{"type":"bogus"},{}]}]}`,
			want: []string{"─── User ───", "─── Tool:  ───", "─── Tool: x ───"},
		},
		{
			name:      "empty messages",
			json:      `{"messages":[]}`,
			wantEmpty: true,
		},
		{
			name:    "invalid json",
			json:    `{"messages":`,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := RenderExport([]byte(tc.json), tc.compact, &buf)
			if tc.wantErr {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("RenderExport: %v", err)
			}
			out := buf.String()
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("missing %q in:\n%s", w, out)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(out, a) {
					t.Errorf("unexpected %q in:\n%s", a, out)
				}
			}
			if tc.wantEmpty && out != "" {
				t.Errorf("output = %q, want empty", out)
			}
		})
	}
}

func TestRenderTranscriptUsesExporter(t *testing.T) {
	orig := ExportRunner
	t.Cleanup(func() { ExportRunner = orig })

	fixture, err := os.ReadFile("testdata/export.json")
	if err != nil {
		t.Fatal(err)
	}
	var gotID string
	ExportRunner = func(sessionID string) ([]byte, error) {
		gotID = sessionID
		return fixture, nil
	}

	var buf bytes.Buffer
	sid := sidOf("A")
	if err := New().RenderTranscript("/tmp", sid, false, &buf); err != nil {
		t.Fatalf("RenderTranscript: %v", err)
	}
	if gotID != sid {
		t.Errorf("ExportRunner called with %q, want %q", gotID, sid)
	}
	inOrder(t, buf.String(), "─── User ───", "─── Tool: shell ───", "The secret word is **BANANA**.")
}

func TestRenderTranscriptExportError(t *testing.T) {
	orig := ExportRunner
	t.Cleanup(func() { ExportRunner = orig })
	ExportRunner = func(string) ([]byte, error) {
		return nil, errors.New("no such session")
	}

	sid := sidOf("B")
	var buf bytes.Buffer
	err := New().RenderTranscript("/tmp", sid, false, &buf)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "opencode session export "+sid) {
		t.Errorf("error %q does not name the export command", err)
	}
	if !strings.Contains(err.Error(), "no such session") {
		t.Errorf("error %q does not wrap the cause", err)
	}
	if buf.Len() != 0 {
		t.Errorf("wrote output on error: %q", buf.String())
	}
}
