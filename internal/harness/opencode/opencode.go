// Package opencode implements harness.Harness for the opencode CLI.
package opencode

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"flow/internal/harness"
	"flow/internal/spawner"
)

var (
	// PSRunner runs `ps -axo pid,command`. Tests replace it with a fixture.
	PSRunner = runPS

	// APIRunner runs `opencode api <args...>` against the running server.
	// Tests replace it to record and stub protocol operations.
	APIRunner = runAPI

	// RunCmd runs argv with stdout/stderr discarded. Used by
	// SkipPermissionsRun; tests replace it to capture the argv.
	RunCmd = runCmd

	// RandReader is the entropy source for NewSessionID.
	RandReader io.Reader = rand.Reader
)

//go:embed plugin/index.js
var pluginJS []byte

// apiTimeout bounds each `opencode api` invocation. A hung server would
// otherwise block `flow do` indefinitely.
const apiTimeout = 30 * time.Second

func New() harness.Harness { return &opencode{} }

type opencode struct{}

var _ harness.SessionPreparer = (*opencode)(nil)

func (o *opencode) Name() harness.Name      { return harness.NameOpencode }
func (o *opencode) Binary() string          { return "opencode" }
func (o *opencode) SessionIDEnvVar() string { return "OPENCODE_SESSION_ID" }

// opencode session ids are `ses_` followed by 26 base62 characters.
var sessionIDRe = regexp.MustCompile(`^ses_[0-9A-Za-z]{26}$`)

const sessionIDAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// NewSessionID mints a native-shaped id locally: flow pre-allocates the
// id and opencode creates the session with it (no probe needed).
func (o *opencode) NewSessionID() (string, error) {
	buf := make([]byte, 0, 26)
	// Rejection sampling keeps the distribution uniform: only bytes
	// below 248 (62*4) map cleanly onto the 62-char alphabet.
	scratch := make([]byte, 32)
	for len(buf) < 26 {
		if _, err := io.ReadFull(RandReader, scratch); err != nil {
			return "", fmt.Errorf("mint opencode session id: %w", err)
		}
		for _, b := range scratch {
			if b >= 248 {
				continue
			}
			buf = append(buf, sessionIDAlphabet[int(b)%len(sessionIDAlphabet)])
			if len(buf) == 26 {
				break
			}
		}
	}
	return "ses_" + string(buf), nil
}

func (o *opencode) ValidateSessionID(s string) error {
	if !sessionIDRe.MatchString(s) {
		return fmt.Errorf("not a valid opencode session id (ses_ + 26 base62 chars): %q", s)
	}
	return nil
}

// opencode session ids are global, not cwd-keyed (same as codex).
func (o *opencode) ValidateSession(workDir, sessionID string) error { return nil }

// model returns the configured opencode model as provider/model, or ""
// when unset (opencode's own default).
func model() string { return strings.TrimSpace(os.Getenv("FLOW_OPENCODE_MODEL")) }

func (o *opencode) LaunchCmd(sessionID, prompt string, opts harness.LaunchOpts) string {
	if opts.Inject != "" {
		prompt += "\n\n" + harness.InjectionMarker + "\n" + opts.Inject
	}
	cmd := "opencode -s " + sessionID
	if opts.SkipPermissions {
		cmd += " --auto"
	}
	return cmd + " --prompt " + spawner.ShellQuote(prompt)
}

func (o *opencode) ResumeCmd(sessionID string, opts harness.LaunchOpts) string {
	cmd := "opencode -s " + sessionID
	if opts.SkipPermissions {
		cmd += " --auto"
	}
	if opts.Inject != "" {
		cmd += " --prompt " + spawner.ShellQuote(harness.InjectionMarker+"\n"+opts.Inject)
	}
	return cmd
}

func (o *opencode) SkipPermissionsRun(prompt string) error {
	argv := []string{"opencode", "run", "--auto"}
	if m := model(); m != "" {
		argv = append(argv, "-m", m)
	}
	return RunCmd(append(argv, prompt))
}

func (o *opencode) AutoRunArgv(sessionID, prompt string, opts harness.LaunchOpts) []string {
	if opts.Inject != "" {
		prompt += "\n\n" + harness.InjectionMarker + "\n" + opts.Inject
	}
	argv := []string{"opencode", "run", "-s", sessionID}
	if opts.SkipPermissions {
		argv = append(argv, "--auto")
	}
	if m := model(); m != "" {
		argv = append(argv, "-m", m)
	}
	return append(argv, prompt)
}

var liveSessionRe = regexp.MustCompile(`(?:^|\s)(?:-s|--session)(?:\s+|=)(ses_[0-9A-Za-z]{26})\b`)

// LiveSessionIDs scans the process table. Only lines whose argv0 basename
// is exactly "opencode" count — a `timeout … opencode` wrapper must not
// look like a live session. Ids are base62 (case-sensitive), so they are
// keyed as-is rather than lowercased.
func (o *opencode) LiveSessionIDs() (map[string]int, error) {
	out, err := PSRunner()
	if err != nil {
		return nil, err
	}
	live := map[string]int{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if filepath.Base(fields[1]) != "opencode" {
			continue
		}
		seen := map[string]bool{}
		for _, m := range liveSessionRe.FindAllStringSubmatch(line, -1) {
			id := m[1]
			if !seen[id] {
				live[id]++
				seen[id] = true
			}
		}
	}
	return live, nil
}

func (o *opencode) SkillInstallPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	return filepath.Join(home, ".config", "opencode", "skills", "flow", "SKILL.md"), nil
}

func (o *opencode) SkillVersionPath() (string, error) {
	p, err := o.SkillInstallPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(p), "VERSION"), nil
}

func (o *opencode) InstallSkill(files fs.FS) error {
	p, err := o.SkillInstallPath()
	if err != nil {
		return err
	}
	base := filepath.Dir(p)
	return fs.WalkDir(files, ".", func(rel string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		content, err := fs.ReadFile(files, rel)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", rel, err)
		}
		target := filepath.Join(base, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(target), err)
		}
		return os.WriteFile(target, content, 0o644)
	})
}

func (o *opencode) UninstallSkill() error {
	p, err := o.SkillInstallPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Dir(p)); os.IsNotExist(err) {
		return nil
	}
	return os.RemoveAll(filepath.Dir(p))
}

func pluginPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	return filepath.Join(home, ".config", "opencode", "plugins", "flow", "index.js"), nil
}

// InstallSessionStartHook writes the embedded flow plugin. opencode has
// no hook config to edit — the plugin itself bridges every event — so the
// command argument is ignored and "added" means the plugin file changed.
func (o *opencode) InstallSessionStartHook(command string) (bool, error) {
	path, err := pluginPath()
	if err != nil {
		return false, err
	}
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, pluginJS) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, pluginJS, 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

func (o *opencode) UninstallSessionStartHook(command string) (bool, error) {
	path, err := pluginPath()
	if err != nil {
		return false, err
	}
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return false, nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return false, err
	}
	return true, nil
}

// Every other hook is covered by the flow opencode plugin.
func (o *opencode) InstallUserPromptSubmitHook(command string) (bool, error)   { return false, nil }
func (o *opencode) UninstallUserPromptSubmitHook(command string) (bool, error) { return false, nil }
func (o *opencode) InstallPostToolUseHook(command string) (bool, error)        { return false, nil }
func (o *opencode) UninstallPostToolUseHook(command string) (bool, error)      { return false, nil }
func (o *opencode) InstallPreToolUseHook(command string) (bool, error)         { return false, nil }
func (o *opencode) UninstallPreToolUseHook(command string) (bool, error)       { return false, nil }
func (o *opencode) InstallStopHook(command string) (bool, error)               { return false, nil }
func (o *opencode) UninstallStopHook(command string) (bool, error)             { return false, nil }

// PrepareSession creates the session server-side (idempotent) with the
// model and flow metadata, then merges the flow metadata into whatever
// the session already carries. Called on bootstrap and resume.
func (o *opencode) PrepareSession(workDir, sessionID string, meta harness.SessionMeta) error {
	body := map[string]any{
		"id":       sessionID,
		"title":    meta.TaskSlug,
		"location": map[string]any{"directory": workDir},
		"metadata": map[string]any{"flow": meta},
	}
	if m := model(); m != "" {
		provider, id, ok := parseModel(m)
		if !ok {
			return fmt.Errorf("FLOW_OPENCODE_MODEL must be provider/model (got %q)", m)
		}
		body["model"] = map[string]any{"providerID": provider, "id": id}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode opencode session.create: %w", err)
	}
	if _, err := APIRunner("session.create", "-d", string(payload)); err != nil {
		return fmt.Errorf("opencode session.create: %w", err)
	}

	getOut, err := APIRunner("session.get", "--param", "sessionID="+sessionID)
	if err != nil {
		return fmt.Errorf("opencode session.get: %w", err)
	}
	existing, err := parseSessionMetadata(getOut)
	if err != nil {
		return fmt.Errorf("opencode session.get: %w", err)
	}
	merged := make(map[string]any, len(existing)+1)
	for k, v := range existing {
		merged[k] = v
	}
	merged["flow"] = meta
	update, err := json.Marshal(map[string]any{"metadata": merged})
	if err != nil {
		return fmt.Errorf("encode opencode session.update: %w", err)
	}
	if _, err := APIRunner("session.update", "--param", "sessionID="+sessionID, "-d", string(update)); err != nil {
		return fmt.Errorf("opencode session.update: %w", err)
	}
	return nil
}

// parseSessionMetadata accepts either a bare session object or a
// {"data": {...}} envelope, and tolerates a missing/null metadata map.
func parseSessionMetadata(out []byte) (map[string]any, error) {
	var top map[string]any
	if err := json.Unmarshal(out, &top); err != nil {
		return nil, err
	}
	if data, ok := top["data"].(map[string]any); ok {
		top = data
	}
	meta, _ := top["metadata"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	return meta, nil
}

// parseModel splits provider/model on the FIRST "/" and drops any
// "#variant" suffix from the model part.
func parseModel(s string) (provider, id string, ok bool) {
	i := strings.Index(s, "/")
	if i <= 0 || i == len(s)-1 {
		return "", "", false
	}
	provider = s[:i]
	id = s[i+1:]
	if j := strings.Index(id, "#"); j >= 0 {
		id = id[:j]
	}
	if id == "" {
		return "", "", false
	}
	return provider, id, true
}

func runPS() ([]byte, error) { return exec.Command("ps", "-axo", "pid,command").Output() }

func runAPI(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "opencode", append([]string{"api"}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if s := strings.TrimSpace(stderr.String()); s != "" {
			return nil, fmt.Errorf("%w: %s", err, s)
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

func runCmd(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("empty command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}
