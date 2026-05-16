package agents

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"flow/internal/flowdb"
)

const (
	ProviderClaude = "claude"
	ProviderCodex  = "codex"
)

var anyUUIDRe = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

type CodexSessionCandidate struct {
	ID        string
	Path      string
	CWD       string
	Timestamp time.Time
	ModTime   time.Time
}

func CaptureCodexSessionForTask(db *sql.DB, taskSlug, workDir, startedAt string) (string, error) {
	started := ParseTimeLoose(startedAt)
	if started.IsZero() {
		return "", fmt.Errorf("parse session_started %q", startedAt)
	}
	return CaptureCodexSessionForTaskSince(db, taskSlug, workDir, started)
}

func CaptureCodexSessionForTaskSince(db *sql.DB, taskSlug, workDir string, started time.Time) (string, error) {
	candidate, err := FindCodexSessionForTask(taskSlug, workDir, started)
	if err != nil || candidate.ID == "" {
		return "", err
	}
	now := flowdb.NowISO()
	res, err := db.Exec(
		`UPDATE tasks
		 SET session_provider = 'codex',
		     session_id = ?,
		     session_started = COALESCE(session_started, ?),
		     updated_at = ?
		 WHERE slug = ?
		   AND session_provider = 'codex'
		   AND (session_id IS NULL OR session_id = '' OR session_id = ?)`,
		candidate.ID, started.Format(time.RFC3339), now, taskSlug, candidate.ID,
	)
	if err != nil {
		return "", fmt.Errorf("update codex session_id: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return "", nil
	}
	return candidate.ID, nil
}

func FindCodexSessionForTask(taskSlug, workDir string, started time.Time) (CodexSessionCandidate, error) {
	var best CodexSessionCandidate
	err := WalkCodexSessionFiles(func(path string, info os.FileInfo) error {
		if info.ModTime().Before(started.Add(-5 * time.Second)) {
			return nil
		}
		meta, err := ReadCodexSessionMeta(path)
		if err != nil || meta.ID == "" {
			return nil
		}
		if meta.CWD != "" && cleanPath(meta.CWD) != cleanPath(workDir) {
			return nil
		}
		if !CodexSessionFileMentions(path, taskSlug) {
			return nil
		}
		meta.Path = path
		meta.ModTime = info.ModTime()
		if meta.Timestamp.IsZero() {
			meta.Timestamp = info.ModTime()
		}
		if best.ID == "" || meta.Timestamp.After(best.Timestamp) || meta.ModTime.After(best.ModTime) {
			best = meta
		}
		return nil
	})
	return best, err
}

func FindCodexSessionPathByID(sessionID string) (string, error) {
	var found string
	err := WalkCodexSessionFiles(func(path string, info os.FileInfo) error {
		base := filepath.Base(path)
		if strings.Contains(base, sessionID) {
			found = path
			return filepath.SkipAll
		}
		meta, err := ReadCodexSessionMeta(path)
		if err == nil && strings.EqualFold(meta.ID, sessionID) {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil && !errors.Is(err, filepath.SkipAll) {
		return "", err
	}
	if found == "" {
		return "", os.ErrNotExist
	}
	return found, nil
}

func WalkCodexSessionFiles(fn func(path string, info os.FileInfo) error) error {
	for _, home := range CodexHomeDirs() {
		for _, sub := range []string{"sessions", "archived_sessions"} {
			root := filepath.Join(home, sub)
			if _, err := os.Stat(root); err != nil {
				continue
			}
			err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
					return nil
				}
				info, err := d.Info()
				if err != nil {
					return nil
				}
				return fn(path, info)
			})
			if errors.Is(err, filepath.SkipAll) {
				return err
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func CodexHomeDir() (string, error) {
	if codexHome := os.Getenv("CODEX_HOME"); codexHome != "" {
		return codexHome, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	return filepath.Join(home, ".codex"), nil
}

func CodexHomeDirs() []string {
	var dirs []string
	seen := map[string]bool{}
	add := func(path string) {
		if path == "" {
			return
		}
		clean := cleanPath(path)
		if !seen[clean] {
			seen[clean] = true
			dirs = append(dirs, clean)
		}
	}
	add(os.Getenv("CODEX_HOME"))
	if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, ".codex"))
		add(filepath.Join(home, ".Codex"))
	}
	return dirs
}

func ReadCodexSessionMeta(path string) (CodexSessionCandidate, error) {
	f, err := os.Open(path)
	if err != nil {
		return CodexSessionCandidate{}, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !scanner.Scan() {
		return CodexSessionCandidate{}, scanner.Err()
	}
	line := scanner.Bytes()
	var modern struct {
		Type    string `json:"type"`
		Payload struct {
			ID        string `json:"id"`
			Timestamp string `json:"timestamp"`
			CWD       string `json:"cwd"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(line, &modern); err == nil && modern.Type == "session_meta" && modern.Payload.ID != "" {
		return CodexSessionCandidate{
			ID:        modern.Payload.ID,
			CWD:       modern.Payload.CWD,
			Timestamp: ParseTimeLoose(modern.Payload.Timestamp),
		}, nil
	}
	var legacy struct {
		ID        string `json:"id"`
		Timestamp string `json:"timestamp"`
	}
	if err := json.Unmarshal(line, &legacy); err == nil && legacy.ID != "" {
		return CodexSessionCandidate{
			ID:        legacy.ID,
			Timestamp: ParseTimeLoose(legacy.Timestamp),
		}, nil
	}
	if match := anyUUIDRe.FindString(filepath.Base(path)); match != "" {
		return CodexSessionCandidate{ID: match}, nil
	}
	return CodexSessionCandidate{}, errors.New("no codex session id found")
}

func CodexSessionFileMentions(path, taskSlug string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	const max = 1024 * 1024
	scanner := bufio.NewScanner(io.LimitReader(f, max))
	scanner.Buffer(make([]byte, 0, 64*1024), max)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), taskSlug) {
			return true
		}
	}
	return false
}

func ParseTimeLoose(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t
		}
	}
	return time.Time{}
}

func cleanPath(path string) string {
	if path == "" {
		return ""
	}
	if expanded, err := filepath.Abs(path); err == nil {
		return filepath.Clean(expanded)
	}
	return filepath.Clean(path)
}
