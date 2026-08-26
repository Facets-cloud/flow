package spec

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"flow/internal/harness"
)

// Adapter turns a validated *Spec into a harness.Harness.
//
// One concrete type implements every capability, and the accessors
// decide at runtime which of them this particular manifest actually
// has. That is why they return an untyped nil rather than a typed nil
// pointer: a typed nil in an interface is non-nil, and callers gate on
// `if x := h.Skills(); x != nil`.
type Adapter struct {
	spec *Spec

	// Compiled once at construction — LiveSessionIDs runs on every
	// `flow list`, and recompiling per call would be pure waste.
	sessionRe *regexp.Regexp
	liveRe    *regexp.Regexp
	captureRe *regexp.Regexp
}

// PSRunner is the process-table probe, swappable in tests.
var PSRunner = func() ([]byte, error) {
	return exec.Command("ps", "-axo", "pid,command").Output()
}

// ExecRunner runs a manifest-declared helper command, swappable in tests.
var ExecRunner = func(argv []string) ([]byte, error) {
	if err := validateExpandedArgv("exec command", argv); err != nil {
		return nil, err
	}
	return exec.Command(argv[0], argv[1:]...).Output()
}

// StatFn is the on-disk existence check, swappable in tests.
var StatFn = func(path string) error {
	_, err := os.Stat(path)
	return err
}

// New compiles a validated spec into a usable adapter. Validate must
// have passed already (Decode does it), so a regexp failing to compile
// here is a programming error in the loader, not user input.
func New(s *Spec) (*Adapter, error) {
	a := &Adapter{spec: s}
	var err error
	if a.sessionRe, err = regexp.Compile(s.Session.Validate); err != nil {
		return nil, fmt.Errorf("%s: session.validate: %w", s.Source, err)
	}
	if s.Liveness.Match != "" {
		if a.liveRe, err = regexp.Compile(s.Liveness.Match); err != nil {
			return nil, fmt.Errorf("%s: liveness.match: %w", s.Source, err)
		}
	}
	if s.Session.Capture != "" {
		if a.captureRe, err = regexp.Compile(s.Session.Capture); err != nil {
			return nil, fmt.Errorf("%s: session.capture: %w", s.Source, err)
		}
	}
	return a, nil
}

// Spec exposes the underlying manifest, for `flow harness show`.
func (a *Adapter) Spec() *Spec { return a.spec }

// ---------- identity ----------

func (a *Adapter) Name() harness.Name      { return harness.Name(a.spec.Name) }
func (a *Adapter) Binary() string          { return a.spec.Binary }
func (a *Adapter) SessionIDEnvVar() string { return a.spec.SessionEnv }

func (a *Adapter) Vocab() harness.Vocabulary {
	return harness.Vocabulary{
		Product:     a.spec.Vocab.Product,
		ContextFile: a.spec.Vocab.ContextFile,
		AskTool:     a.spec.Vocab.AskTool,
		SkillHint:   a.spec.Vocab.SkillHint,
	}
}

// vars builds the template environment. Home is resolved once per call
// rather than cached, so a test that swaps $HOME sees the change.
func (a *Adapter) vars() Vars {
	home, _ := os.UserHomeDir()
	return Vars{
		InjectionMarker: harness.InjectionMarker,
		Name:            a.spec.Name,
		Binary:          a.spec.Binary,
		Home:            home,
	}
}

// varsFor layers the per-launch options onto the base environment.
//
// WorkDir and Cwd are both populated from opts.WorkDir: a manifest may
// legitimately think of it either way (an explicit `-cwd` flag, or the
// directory a transcript is keyed to), and having one of them silently
// empty is precisely the trap this exists to close.
func (a *Adapter) varsFor(opts harness.LaunchOpts) Vars {
	v := a.vars()
	v.Inject = opts.Inject
	v.WorkDir = opts.WorkDir
	v.Cwd = opts.WorkDir
	return v
}

// ---------- session allocation ----------

func (a *Adapter) NewSessionID() (string, error) {
	switch a.spec.Session.Strategy {
	case "uuid4":
		return newUUID4()
	case "uuid7":
		return newUUID7()
	case "exec-capture":
		return a.captureSessionID()
	}
	// Unreachable: Validate rejects any other strategy.
	return "", fmt.Errorf("harness %s: unknown session strategy %q", a.spec.Name, a.spec.Session.Strategy)
}

// captureSessionID runs a pre-launch allocation command and pulls the id
// out of its output. The command must create or reserve the same session
// that launch/resume will use; this is not post-launch discovery.
func (a *Adapter) captureSessionID() (string, error) {
	argv, err := ExpandArgv(a.spec.Session.Argv, a.vars())
	if err != nil {
		return "", fmt.Errorf("harness %s: session.argv: %w", a.spec.Name, err)
	}
	if err := validateExpandedArgv("session.argv", argv); err != nil {
		return "", fmt.Errorf("harness %s: %w", a.spec.Name, err)
	}
	out, err := ExecRunner(argv)
	if err != nil {
		return "", fmt.Errorf("harness %s: minting a session id via %q failed: %w",
			a.spec.Name, strings.Join(argv, " "), err)
	}
	m := a.captureRe.FindSubmatch(out)
	if len(m) < 2 {
		return "", fmt.Errorf("harness %s: session.capture found no session id in the output of %q",
			a.spec.Name, strings.Join(argv, " "))
	}
	id := string(m[1])
	if err := a.ValidateSessionID(id); err != nil {
		return "", fmt.Errorf("harness %s: captured id is not valid per session.validate: %w", a.spec.Name, err)
	}
	return id, nil
}

func (a *Adapter) ValidateSessionID(s string) error {
	match := a.sessionRe.FindStringIndex(s)
	if match == nil || match[0] != 0 || match[1] != len(s) {
		return fmt.Errorf("not a valid %s session id: %q", a.spec.Name, s)
	}
	return nil
}

// ValidateSession checks that the transcript for (workDir, sessionID)
// is where a later resume would look for it.
//
// Only meaningful for cwd-keyed layouts, which is why it is gated on
// session.verify_cwd: a harness that keys transcripts by id alone has
// nothing to disagree about, and returns nil.
func (a *Adapter) ValidateSession(workDir, sessionID string) error {
	if !a.spec.Session.VerifyCwd || a.spec.Transcript == nil {
		return nil
	}
	path, err := a.transcriptPath(workDir, sessionID)
	if err != nil {
		return err
	}
	if err := StatFn(path); err != nil {
		return fmt.Errorf("no %s transcript at %s", a.spec.Name, path)
	}
	return nil
}

func (a *Adapter) transcriptPath(cwd, sessionID string) (string, error) {
	v := a.vars()
	v.Cwd = cwd
	v.WorkDir = cwd
	v.SessionID = sessionID
	// ResolvePath, not ExpandPath: transcript layouts that embed a
	// creation timestamp are only reachable through a glob.
	return ResolvePath(a.spec.Transcript.Path, v)
}

// ---------- launching ----------

// LaunchCmd renders [launch].argv as a shell command line.
//
// Errors cannot be returned through harness.Harness's signature, so an
// expansion failure degrades to the empty string. That is not silent:
// every caller treats an empty command as a spawn failure, and Decode
// already parsed every template, so reaching here requires a template
// that parses but cannot execute.
func (a *Adapter) LaunchCmd(sessionID, prompt string, opts harness.LaunchOpts) string {
	v := a.varsFor(opts)
	v.SessionID = sessionID
	v.Prompt = prompt

	argv := a.spec.Launch.Argv
	if opts.SkipPermissions {
		argv = append(append([]string{}, argv...), a.spec.Launch.PermissionFlag...)
	}
	cmd, err := ExpandShell(argv, a.spec.Launch.Prelude, v)
	if err != nil {
		return ""
	}
	return cmd
}

// ResumeCmd renders [resume].argv. Only reachable via Resume(), which
// is nil unless the table exists.
func (a *Adapter) ResumeCmd(sessionID string, opts harness.LaunchOpts) string {
	v := a.varsFor(opts)
	v.SessionID = sessionID

	argv := a.spec.Resume.Argv
	if opts.SkipPermissions {
		argv = append(append([]string{}, argv...), a.spec.Resume.PermissionFlag...)
	}
	cmd, err := ExpandShell(argv, a.spec.Resume.Prelude, v)
	if err != nil {
		return ""
	}
	return cmd
}

// ---------- headless ----------

func (a *Adapter) SkipPermissionsRun(prompt string, opts harness.LaunchOpts) error {
	if len(a.spec.Headless.RunArgv) == 0 {
		return fmt.Errorf("harness %s declares no headless run_argv", a.spec.Name)
	}
	v := a.varsFor(opts)
	v.Prompt = prompt
	argv, err := ExpandArgv(a.spec.Headless.RunArgv, v)
	if err != nil {
		return fmt.Errorf("harness %s: headless.run_argv: %w", a.spec.Name, err)
	}
	if err := validateExpandedArgv("headless.run_argv", argv); err != nil {
		return fmt.Errorf("harness %s: %w", a.spec.Name, err)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = opts.WorkDir
	// Keep stderr: a manifest can only be debugged from what the
	// harness itself says when the run fails.
	return harness.RunCapturingStderr(cmd)
}

func (a *Adapter) AutoRunArgv(sessionID, prompt string, opts harness.LaunchOpts) []string {
	v := a.varsFor(opts)
	v.SessionID = sessionID
	v.Prompt = withInjection(prompt, harness.InjectionMarker, opts.Inject)

	argv := a.spec.Headless.AutoArgv
	if opts.SkipPermissions {
		argv = append(append([]string{}, argv...), a.spec.Launch.PermissionFlag...)
	}
	out, err := ExpandArgv(argv, v)
	if err != nil {
		return nil
	}
	if err := validateExpandedArgv("headless.auto_argv", out); err != nil {
		return nil
	}
	return out
}

// ---------- liveness ----------

// LiveSessionIDs counts running processes per session id.
//
// A row is counted at most once per id even when argv mentions the id
// twice (some shells echo the command back), which is why the inner
// dedupe set exists.
func (a *Adapter) LiveSessionIDs() (map[string]int, error) {
	live := make(map[string]int)
	switch a.spec.Liveness.Probe {
	case "none":
		return live, nil
	case "ps":
		out, err := PSRunner()
		if err != nil {
			return nil, fmt.Errorf("ps: %w", err)
		}
		for _, line := range strings.Split(string(out), "\n") {
			// The row must mention the binary. Bare names and
			// fully-qualified paths both occur in practice.
			if !strings.Contains(line, a.spec.Binary) {
				continue
			}
			a.countIDs(line, live)
		}
		return live, nil
	case "exec":
		argv, err := ExpandArgv(a.spec.Liveness.Argv, a.vars())
		if err != nil {
			return nil, fmt.Errorf("harness %s: liveness.argv: %w", a.spec.Name, err)
		}
		if err := validateExpandedArgv("liveness.argv", argv); err != nil {
			return nil, fmt.Errorf("harness %s: %w", a.spec.Name, err)
		}
		out, err := ExecRunner(argv)
		if err != nil {
			return nil, fmt.Errorf("harness %s: %q: %w", a.spec.Name, strings.Join(argv, " "), err)
		}
		a.countIDs(string(out), live)
		return live, nil
	}
	return live, nil
}

func (a *Adapter) countIDs(text string, live map[string]int) {
	seen := map[string]bool{}
	for _, m := range a.liveRe.FindAllStringSubmatch(text, -1) {
		if len(m) < 2 {
			continue
		}
		id := strings.ToLower(m[1])
		if seen[id] {
			continue
		}
		seen[id] = true
		live[id]++
	}
}

// ---------- capability accessors ----------
//
// Returning a literal nil (not a typed nil pointer) is load-bearing:
// callers test `!= nil`, and a typed nil wrapped in an interface would
// pass that test and then panic.

func (a *Adapter) Resume() harness.Resumer {
	if a.spec.Resume == nil {
		return nil
	}
	return a
}

func (a *Adapter) Headless() harness.HeadlessRunner {
	if a.spec.Headless == nil {
		return nil
	}
	return a
}

func (a *Adapter) Transcript() harness.TranscriptSource {
	if a.spec.Transcript == nil {
		return nil
	}
	return a
}

// Skills and Hooks live in skills.go and hooks.go beside the rest of
// their implementation. Background is not described by the schema yet,
// so a spec-driven harness honestly reports it has none.
func (a *Adapter) Background() harness.BackgroundLauncher { return nil }

// Compile-time proof that the receiver satisfies every capability it
// can hand out, so a dropped method fails the build instead of
// surfacing as a nil accessor at runtime.
var (
	_ harness.Harness          = (*Adapter)(nil)
	_ harness.Resumer          = (*Adapter)(nil)
	_ harness.HeadlessRunner   = (*Adapter)(nil)
	_ harness.TranscriptSource = (*Adapter)(nil)
)

// ---------- session id generation ----------

func newUUID4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return formatUUID(b), nil
}

// newUUID7 produces a time-ordered v7 UUID: 48 bits of Unix
// milliseconds followed by random bits. Sorting by id sorts by
// creation time, which is why harnesses that name session directories
// after the id prefer it.
func newUUID7() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	ms := uint64(time.Now().UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	b[6] = (b[6] & 0x0f) | 0x70 // version 7
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return formatUUID(b), nil
}

func formatUUID(b [16]byte) string {
	var dst [36]byte
	hex.Encode(dst[0:8], b[0:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], b[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], b[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], b[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:36], b[10:16])
	return string(dst[:])
}
