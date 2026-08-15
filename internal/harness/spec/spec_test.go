package spec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/harness"
)

// minimalManifest is a valid baseline; tests mutate one thing at a time
// so a failure names exactly which rule fired.
const minimalManifest = `
schema      = 1
name        = "demo"
binary      = "demo"
session_env = "DEMO_SESSION_ID"

[session]
strategy = "uuid4"
validate = '^[a-z0-9-]+$'

[launch]
argv = ["demo", "--id", "{{.SessionID}}", "{{.Prompt}}"]

[liveness]
probe = "ps"
match = '--id ([a-z0-9-]+)'

[vocab]
product = "Demo"
`

func TestDecodeAcceptsMinimalManifest(t *testing.T) {
	s, err := Decode([]byte(minimalManifest), "minimal.toml")
	if err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	a, err := New(s)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Absent tables must surface as absent capabilities, not as a
	// harness that claims to do everything and fails at call time.
	if a.Resume() != nil {
		t.Error("Resume() should be nil when [resume] is absent")
	}
	if a.Headless() != nil {
		t.Error("Headless() should be nil when [headless] is absent")
	}
	if a.Transcript() != nil {
		t.Error("Transcript() should be nil when [transcript] is absent")
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(string) string
		wantSub string
	}{
		{
			"unknown key",
			func(m string) string { return m + "\nnonsense_key = 1\n" },
			"unknown key",
		},
		{
			"wrong schema",
			func(m string) string { return strings.Replace(m, "schema      = 1", "schema      = 99", 1) },
			"schema 1",
		},
		{
			"missing session.validate",
			func(m string) string { return strings.Replace(m, "validate = '^[a-z0-9-]+$'", "", 1) },
			"session.validate is required",
		},
		{
			"invalid session.validate regexp",
			func(m string) string { return strings.Replace(m, "'^[a-z0-9-]+$'", "'^[unclosed'", 1) },
			"not a valid regexp",
		},
		{
			"liveness.match with two capture groups",
			func(m string) string { return strings.Replace(m, "'--id ([a-z0-9-]+)'", "'--(id) ([a-z0-9-]+)'", 1) },
			"exactly 1 capturing group",
		},
		{
			"unknown session strategy",
			func(m string) string { return strings.Replace(m, `strategy = "uuid4"`, `strategy = "magic"`, 1) },
			"session.strategy",
		},
		{
			"malformed template",
			func(m string) string { return strings.Replace(m, "{{.Prompt}}", "{{.Prompt", 1) },
			"launch.argv",
		},
		{
			"unknown template variable",
			func(m string) string { return strings.Replace(m, "{{.Prompt}}", "{{.Nonexistent}}", 1) },
			"", // parses fine; caught at execution — see TestUnknownVariableFailsLoudly
		},
		{
			"missing vocab.product",
			func(m string) string { return strings.Replace(m, `product = "Demo"`, "", 1) },
			"vocab.product is required",
		},
		{
			"verify_cwd without transcript",
			func(m string) string {
				return strings.Replace(m, `strategy = "uuid4"`, "strategy = \"uuid4\"\nverify_cwd = true", 1)
			},
			"needs a [transcript] table",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantSub == "" {
				t.Skip("validated at execution time, not load time")
			}
			_, err := Decode([]byte(tc.mutate(minimalManifest)), "test.toml")
			if err == nil {
				t.Fatalf("expected rejection, got none")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error missing %q:\n%v", tc.wantSub, err)
			}
		})
	}
}

// TestUnknownVariableFailsLoudly pins that a typo'd variable produces
// an error rather than an empty string. A silently-empty argument is
// the worst failure mode a manifest can have: the command runs, and
// misbehaves.
func TestUnknownVariableFailsLoudly(t *testing.T) {
	_, err := expandOne("{{.Nonexistent}}", Vars{})
	if err == nil {
		t.Fatal("unknown template variable expanded silently; it must error")
	}
}

// TestShellQuotingRoundTrip is the real proof that rendered commands are
// safe: the command is executed by an actual shell and the argument it
// receives must be byte-identical to what went in.
//
// Asserting on the rendered string would only prove it looks right.
// Only running it proves a prompt containing $(...) is inert.
func TestShellQuotingRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh available")
	}
	// printf %s echoes exactly one argument, so stdout IS the argument
	// the shell parsed out of the rendered command line.
	manifest := strings.Replace(minimalManifest,
		`argv = ["demo", "--id", "{{.SessionID}}", "{{.Prompt}}"]`,
		`argv = ["printf", "%s", "{{.Prompt}}"]`, 1)
	s, err := Decode([]byte(manifest), "quoting.toml")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	a, err := New(s)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	hostile := []string{
		"plain",
		"don't stop",
		`say "hello"`,
		"run $(echo pwned) now",
		"run `echo pwned` now",
		"semi; echo pwned",
		"pipe | echo pwned",
		"amp && echo pwned",
		"line one\nline two",
		"tab\there",
		"dollar $HOME sign",
		"glob * and ? here",
		"emoji 🚀 and — dash",
	}
	for _, prompt := range hostile {
		t.Run(prompt, func(t *testing.T) {
			cmdline := a.LaunchCmd("abc-123", prompt, harness.LaunchOpts{})
			out, err := exec.Command("sh", "-c", cmdline).Output()
			if err != nil {
				t.Fatalf("running %q: %v", cmdline, err)
			}
			if string(out) != prompt {
				t.Errorf("prompt mangled by the shell\n  sent: %q\n  got:  %q\n  cmd:  %s", prompt, string(out), cmdline)
			}
		})
	}
}

// TestOptionalElementSemantics pins the distinction the differential
// test uncovered: a purely-conditional element disappears when empty,
// but an unconditional one holds its positional slot.
func TestOptionalElementSemantics(t *testing.T) {
	cases := []struct {
		name string
		tmpl string
		want bool
	}{
		{"pure literal", "claude", false},
		{"bare value", "{{.Prompt}}", false},
		{"value plus conditional", "{{.Prompt}}{{if .Inject}}x{{end}}", false},
		{"pure conditional", "{{if .Inject}}{{.Inject}}{{end}}", true},
		{"conditional with literal inside", "{{if .Inject}}--flag {{.Inject}}{{end}}", true},
		{"nested conditional", "{{if .Inject}}{{if .Prompt}}x{{end}}{{end}}", true},
		{"conditional then literal", "{{if .Inject}}x{{end}}tail", false},
		{"with else", "{{if .Inject}}a{{else}}b{{end}}", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPurelyConditional(tc.tmpl); got != tc.want {
				t.Errorf("isPurelyConditional(%q) = %v, want %v", tc.tmpl, got, tc.want)
			}
		})
	}
}

// TestEmptyPromptKeepsItsSlot is the behavioural counterpart: an empty
// prompt must still be passed, or every later argument shifts left.
func TestEmptyPromptKeepsItsSlot(t *testing.T) {
	s, err := Decode([]byte(minimalManifest), "t.toml")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	a, _ := New(s)
	got := a.LaunchCmd("abc-123", "", harness.LaunchOpts{})
	if !strings.HasSuffix(got, "''") {
		t.Errorf("empty prompt dropped instead of quoted as an empty argument: %q", got)
	}
}

const transcriptManifest = `
schema      = 1
name        = "demo"
binary      = "demo"
session_env = "DEMO_SESSION_ID"

[session]
strategy = "uuid4"
validate = '^[a-z0-9-]+$'

[launch]
argv = ["demo", "{{.Prompt}}"]

[liveness]
probe = "none"

[transcript]
path   = "{{.Cwd}}/{{.SessionID}}.jsonl"
format = "jsonl"

[transcript.map]
role       = "message.role"
text       = "message.content[].text"
tool_block = "toolCall"
tool_name  = "message.content[].name"

[vocab]
product = "Demo"
`

func TestRenderTranscript(t *testing.T) {
	dir := t.TempDir()
	// Heterogeneous on purpose: mixed block types, a record with no
	// text at all, and a malformed trailing line as a live transcript
	// being appended to would have.
	lines := []string{
		`{"message":{"role":"user","content":[{"type":"text","text":"fix the bug"}]}}`,
		`{"message":{"role":"assistant","content":[{"type":"text","text":"looking now"},{"type":"toolCall","name":"bash"}]}}`,
		`{"message":{"role":"assistant","content":[{"type":"toolCall","name":"read"}]}}`,
		`{"unrelated":"record"}`,
		`{"message":{"role":"assis`,
	}
	path := filepath.Join(dir, "abc-123.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Decode([]byte(transcriptManifest), "t.toml")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	a, _ := New(s)

	var sb strings.Builder
	if err := a.Transcript().RenderTranscript(dir, "abc-123", false, &sb); err != nil {
		t.Fatalf("RenderTranscript: %v", err)
	}
	got := sb.String()
	for _, want := range []string{"─── User ───", "fix the bug", "─── Assistant ───", "looking now", "tool: bash", "tool: read"} {
		if !strings.Contains(got, want) {
			t.Errorf("transcript missing %q:\n%s", want, got)
		}
	}

	// compact drops tool lines but keeps the conversation.
	var compact strings.Builder
	if err := a.Transcript().RenderTranscript(dir, "abc-123", true, &compact); err != nil {
		t.Fatalf("RenderTranscript compact: %v", err)
	}
	if strings.Contains(compact.String(), "tool:") {
		t.Errorf("compact mode still rendered tool calls:\n%s", compact.String())
	}
	if !strings.Contains(compact.String(), "fix the bug") {
		t.Errorf("compact mode dropped conversation:\n%s", compact.String())
	}
}

// TestRenderTranscriptReportsMismatchedMap turns the commonest manifest
// authoring mistake — paths that do not match the file's real shape —
// into an explicit message instead of silent empty output.
func TestRenderTranscriptReportsMismatchedMap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "abc-123.jsonl")
	if err := os.WriteFile(path, []byte(`{"totally":"different"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := Decode([]byte(transcriptManifest), "t.toml")
	a, _ := New(s)
	err := a.Transcript().RenderTranscript(dir, "abc-123", false, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "transcript.map") {
		t.Errorf("want an error naming [transcript.map], got %v", err)
	}
}
