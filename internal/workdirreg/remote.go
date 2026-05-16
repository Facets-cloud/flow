package workdirreg

import (
	"bufio"
	"database/sql"
	"flow/internal/flowdb"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var gitRemoteURLRE = regexp.MustCompile(`^\s*url\s*=\s*(.+?)\s*$`)

// Register records a workdir and captures its current origin remote when one
// exists. Existing names/descriptions are preserved unless non-empty values are
// supplied by the caller.
func Register(db *sql.DB, path, name, description string) error {
	return flowdb.UpsertWorkdir(db, path, name, description, DetectGitRemote(path))
}

// Touch records recent use for a workdir and refreshes git_remote if the path
// currently exposes an origin remote.
func Touch(db *sql.DB, path string) error {
	now := flowdb.NowISO()
	remote := DetectGitRemote(path)
	var (
		res sql.Result
		err error
	)
	if remote != "" {
		res, err = db.Exec(`UPDATE workdirs SET last_used_at = ?, git_remote = ? WHERE path = ?`, now, remote, path)
	} else {
		res, err = db.Exec(`UPDATE workdirs SET last_used_at = ? WHERE path = ?`, now, path)
	}
	if err != nil {
		return err
	}
	if changed, err := res.RowsAffected(); err == nil && changed == 0 {
		return flowdb.UpsertWorkdir(db, path, "", "", remote)
	}
	return nil
}

// SyncGitRemotes is a one-shot backfill/update for registered workdirs. It
// never clears stored remotes; it only writes a remote that is currently
// discoverable from the local git checkout.
func SyncGitRemotes(db *sql.DB) (int, error) {
	workdirs, err := flowdb.ListWorkdirs(db)
	if err != nil {
		return 0, err
	}
	updated := 0
	for _, w := range workdirs {
		remote := DetectGitRemote(w.Path)
		if remote == "" {
			continue
		}
		if w.GitRemote.Valid && w.GitRemote.String == remote {
			continue
		}
		if _, err := db.Exec(`UPDATE workdirs SET git_remote = ? WHERE path = ?`, remote, w.Path); err != nil {
			return updated, fmt.Errorf("sync workdir remote %s: %w", w.Path, err)
		}
		updated++
	}
	return updated, nil
}

// DetectGitRemote reads <path>/.git/config and extracts the origin remote URL.
// It handles .git being either a directory or a git-worktree pointer file.
func DetectGitRemote(path string) string {
	configPath := resolveGitConfigPath(path)
	if configPath == "" {
		return ""
	}
	f, err := os.Open(configPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	inOrigin := false
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			inOrigin = trimmed == `[remote "origin"]`
			continue
		}
		if !inOrigin {
			continue
		}
		if m := gitRemoteURLRE.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return ""
}

func resolveGitConfigPath(path string) string {
	gitPath := filepath.Join(path, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return ""
	}
	if info.IsDir() {
		return filepath.Join(gitPath, "config")
	}
	data, err := os.ReadFile(gitPath)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, "gitdir:") {
		return ""
	}
	target := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if !filepath.IsAbs(target) {
		target = filepath.Join(path, target)
	}
	return filepath.Join(target, "config")
}
