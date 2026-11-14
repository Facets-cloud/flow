package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
)

// transcriptCwds returns the directories whose encoding claude may have
// keyed a transcript on for a session started in workDir: workDir as
// given, then workDir with symlinks resolved. Claude keys on the resolved
// path, so a work_dir that is a symlink (or sits under one, like /tmp on
// macOS) names a project directory that never exists. The unresolved path
// stays first: it is what callers and existing error messages refer to.
func transcriptCwds(workDir string) []string {
	cwds := []string{workDir}
	if resolved, err := filepath.EvalSymlinks(workDir); err == nil && resolved != workDir {
		cwds = append(cwds, resolved)
	}
	return cwds
}

// LocateSession implements harness.SessionLocator. It finds sessionID's
// transcript under any project directory and reads the directory the
// session was started in out of the transcript itself (every conversation
// record carries the cwd it was written from).
//
// The project directory name is a lossy encoding of that start directory,
// so it can't be decoded; instead a recorded cwd is accepted only when it
// encodes back to the project directory the transcript sits in. That
// rejects cwds the session merely visited later (cd, worktree) and the
// bg/worktree layout where the transcript is keyed under the parent repo:
// there the session is found but its start directory is reported unknown.
func (c *claude) LocateSession(sessionID string) (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	// No cwd hint: this is asked only once the work_dir-keyed path has
	// missed, so go straight to the search by session id.
	p, err := resolveTranscriptPath(home, "", sessionID)
	if err != nil {
		return "", false
	}
	return startCwd(p), true
}

// startCwd scans the transcript at path for the first recorded cwd that
// its project directory is keyed on. Returns "" when there is none.
func startCwd(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	projectDir := filepath.Base(filepath.Dir(path))

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	seen := map[string]bool{}
	for scanner.Scan() {
		line := scanner.Bytes()
		// Cheap pre-filter: most lines are large and many carry no cwd.
		if !bytes.Contains(line, []byte(`"cwd"`)) {
			continue
		}
		var rec struct {
			Cwd string `json:"cwd"`
		}
		if err := json.Unmarshal(line, &rec); err != nil || rec.Cwd == "" || seen[rec.Cwd] {
			continue
		}
		seen[rec.Cwd] = true
		if EncodeCwd(rec.Cwd) == projectDir {
			return rec.Cwd
		}
	}
	return ""
}
