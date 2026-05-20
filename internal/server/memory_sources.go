package server

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type memorySourceCandidate struct {
	id       string
	provider string
	scope    string
	kind     string
	label    string
	path     string
}

func (s *Server) uiAgentMemorySources(tasks []TaskView, projects []uiProject, playbooks []uiPlaybook, workdirs []uiWorkdir) []uiMemorySource {
	var candidates []memorySourceCandidate
	candidates = append(candidates, codexUserMemoryCandidates()...)
	candidates = append(candidates, codexMemoryFileCandidates(codexHomeDir())...)

	for _, workdir := range memorySourceWorkdirs(tasks, projects, playbooks, workdirs) {
		candidates = append(candidates, codexProjectMemoryCandidates(workdir)...)
		candidates = append(candidates, claudeAutoMemoryCandidates(workdir)...)
	}

	out := make([]uiMemorySource, 0, len(candidates))
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if candidate.id == "" {
			continue
		}
		if isClaudeMDPath(candidate.path) {
			continue
		}
		if seen[candidate.id] {
			candidate.id = candidate.id + "-" + memorySourceSlug(candidate.path)
			if candidate.id == "" || seen[candidate.id] {
				continue
			}
		}
		seen[candidate.id] = true
		out = append(out, buildMemorySource(candidate))
	}
	return out
}

func codexUserMemoryCandidates() []memorySourceCandidate {
	home := codexHomeDir()
	return []memorySourceCandidate{
		{
			id:       "codex-user-agents-override",
			provider: "codex",
			scope:    "user",
			kind:     "instructions",
			label:    "Codex global override instructions",
			path:     filepath.Join(home, "AGENTS.override.md"),
		},
		{
			id:       "codex-user-agents",
			provider: "codex",
			scope:    "user",
			kind:     "instructions",
			label:    "Codex global instructions",
			path:     filepath.Join(home, "AGENTS.md"),
		},
	}
}

func codexMemoryFileCandidates(home string) []memorySourceCandidate {
	root := filepath.Join(home, "memories")
	paths := markdownFilesUnder(root, 500)
	if len(paths) == 0 {
		return []memorySourceCandidate{{
			id:       "codex-user-memories",
			provider: "codex",
			scope:    "user",
			kind:     "auto-memory",
			label:    "Codex memories directory",
			path:     filepath.Join(root, "MEMORY.md"),
		}}
	}
	out := make([]memorySourceCandidate, 0, len(paths))
	for _, path := range paths {
		rel := relTo(root, path)
		out = append(out, memorySourceCandidate{
			id:       "codex-memory-" + memorySourceSlug(rel),
			provider: "codex",
			scope:    "user",
			kind:     "auto-memory",
			label:    "Codex memory " + rel,
			path:     path,
		})
	}
	return out
}

func codexProjectMemoryCandidates(workdir string) []memorySourceCandidate {
	root := repositoryRoot(workdir)
	dirs := pathChain(root, workdir)
	fallbacks := codexProjectFallbackFilenames()
	var out []memorySourceCandidate
	for _, dir := range dirs {
		if candidate, ok := firstExistingCodexProjectCandidate(root, dir, fallbacks); ok {
			out = append(out, candidate)
		}
	}
	if len(out) == 0 {
		out = append(out, memorySourceCandidate{
			id:       "codex-project-" + memorySourceSlug(workdir) + "-agents",
			provider: "codex",
			scope:    "project",
			kind:     "instructions",
			label:    "Codex project instructions",
			path:     filepath.Join(workdir, "AGENTS.md"),
		})
	}
	return out
}

func firstExistingCodexProjectCandidate(root, dir string, fallbacks []string) (memorySourceCandidate, bool) {
	names := append([]string{"AGENTS.override.md", "AGENTS.md"}, fallbacks...)
	for _, name := range names {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() && info.Size() > 0 {
			rel := relTo(root, path)
			return memorySourceCandidate{
				id:       "codex-project-" + memorySourceSlug(root) + "-" + memorySourceSlug(rel),
				provider: "codex",
				scope:    "project",
				kind:     "instructions",
				label:    "Codex project instructions " + rel,
				path:     path,
			}, true
		}
	}
	return memorySourceCandidate{}, false
}

func claudeAutoMemoryCandidates(workdir string) []memorySourceCandidate {
	memoryDir := claudeAutoMemoryDir(workdir)
	paths := markdownFilesUnder(memoryDir, 200)
	if len(paths) == 0 {
		return []memorySourceCandidate{{
			id:       "claude-auto-memory-" + memorySourceSlug(workdir),
			provider: "claude",
			scope:    "project",
			kind:     "auto-memory",
			label:    "Claude auto memory",
			path:     filepath.Join(memoryDir, "MEMORY.md"),
		}}
	}
	out := make([]memorySourceCandidate, 0, len(paths))
	for _, path := range paths {
		rel := relTo(memoryDir, path)
		out = append(out, memorySourceCandidate{
			id:       "claude-auto-memory-" + memorySourceSlug(workdir) + "-" + memorySourceSlug(rel),
			provider: "claude",
			scope:    "project",
			kind:     "auto-memory",
			label:    "Claude auto memory " + rel,
			path:     path,
		})
	}
	return out
}

func codexHomeDir() string {
	if dir := strings.TrimSpace(os.Getenv("CODEX_HOME")); dir != "" {
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".codex")
	}
	return filepath.Join("~", ".codex")
}

func claudeConfigDir() string {
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".claude")
	}
	return filepath.Join("~", ".claude")
}

func claudeAutoMemoryDir(workdir string) string {
	if dir := configuredClaudeAutoMemoryDir(); dir != "" {
		return dir
	}
	root := repositoryRoot(workdir)
	return filepath.Join(claudeConfigDir(), "projects", claudeProjectKey(root), "memory")
}

func configuredClaudeAutoMemoryDir() string {
	settingsPath := filepath.Join(claudeConfigDir(), "settings.json")
	body, err := os.ReadFile(settingsPath)
	if err != nil {
		return ""
	}
	var settings struct {
		AutoMemoryDirectory string `json:"autoMemoryDirectory"`
	}
	if err := json.Unmarshal(body, &settings); err != nil {
		return ""
	}
	dir := strings.TrimSpace(settings.AutoMemoryDirectory)
	if dir == "" {
		return ""
	}
	if strings.HasPrefix(dir, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(dir, "~/"))
		}
	}
	if filepath.IsAbs(dir) {
		return dir
	}
	return ""
}

func claudeProjectKey(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	path = filepath.Clean(path)
	return strings.ReplaceAll(filepath.ToSlash(path), "/", "-")
}

func codexProjectFallbackFilenames() []string {
	configPath := filepath.Join(codexHomeDir(), "config.toml")
	body, err := os.ReadFile(configPath)
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "project_doc_fallback_filenames") {
			continue
		}
		start := strings.Index(line, "[")
		end := strings.LastIndex(line, "]")
		if start < 0 || end <= start {
			return nil
		}
		var out []string
		for _, raw := range strings.Split(line[start+1:end], ",") {
			name := strings.Trim(strings.TrimSpace(raw), `"'`)
			if name != "" && filepath.Base(name) == name {
				out = append(out, name)
			}
		}
		return out
	}
	return nil
}

func memorySourceWorkdirs(tasks []TaskView, projects []uiProject, playbooks []uiPlaybook, workdirs []uiWorkdir) []string {
	seen := map[string]bool{}
	var out []string
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		path = filepath.Clean(path)
		if seen[path] {
			return
		}
		seen[path] = true
		out = append(out, path)
	}
	for _, task := range tasks {
		add(task.WorkDir)
	}
	for _, project := range projects {
		add(project.WorkDir)
	}
	for _, playbook := range playbooks {
		add(playbook.WorkDir)
	}
	for _, workdir := range workdirs {
		add(workdir.Path)
	}
	sort.Strings(out)
	return out
}

func repositoryRoot(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	path = filepath.Clean(path)
	for dir := path; ; dir = filepath.Dir(dir) {
		gitPath := filepath.Join(dir, ".git")
		if _, err := os.Stat(gitPath); err == nil {
			if root := worktreeMainRoot(dir, gitPath); root != "" {
				return root
			}
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return path
		}
	}
}

func worktreeMainRoot(worktreeRoot, gitPath string) string {
	info, err := os.Stat(gitPath)
	if err != nil || info.IsDir() {
		return ""
	}
	body, err := os.ReadFile(gitPath)
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(body))
	if !strings.HasPrefix(text, "gitdir:") {
		return ""
	}
	gitDir := strings.TrimSpace(strings.TrimPrefix(text, "gitdir:"))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Clean(filepath.Join(worktreeRoot, gitDir))
	}
	parts := strings.Split(filepath.ToSlash(gitDir), "/.git/worktrees/")
	if len(parts) != 2 {
		return ""
	}
	return filepath.FromSlash(parts[0])
}

func pathChain(root, leaf string) []string {
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	if abs, err := filepath.Abs(leaf); err == nil {
		leaf = abs
	}
	root = filepath.Clean(root)
	leaf = filepath.Clean(leaf)
	rel, err := filepath.Rel(root, leaf)
	if err != nil || strings.HasPrefix(rel, "..") {
		return []string{leaf}
	}
	out := []string{root}
	if rel == "." {
		return out
	}
	dir := root
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "" || part == "." {
			continue
		}
		dir = filepath.Join(dir, part)
		out = append(out, dir)
	}
	return out
}

func markdownFilesUnder(root string, limit int) []string {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil
	}
	var out []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && shouldSkipMemoryDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(d.Name()), ".md") {
			out = append(out, path)
		}
		if limit > 0 && len(out) >= limit {
			return filepath.SkipAll
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func shouldSkipMemoryDir(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", ".codex", ".agents", "node_modules", "vendor", "dist", "build":
		return true
	default:
		return false
	}
}

func isClaudeMDPath(path string) bool {
	return strings.EqualFold(filepath.Base(path), "CLAUDE.md")
}

func relTo(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.Base(path)
	}
	return filepath.ToSlash(rel)
}

func memorySourceSlug(path string) string {
	path = strings.TrimSuffix(filepath.ToSlash(path), ".md")
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(path) {
		allowed := r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		if allowed {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func buildMemorySource(candidate memorySourceCandidate) uiMemorySource {
	src := uiMemorySource{
		ID:       candidate.id,
		Provider: candidate.provider,
		Scope:    candidate.scope,
		Kind:     candidate.kind,
		Label:    candidate.label,
		Path:     candidate.path,
		Status:   "missing",
	}
	info, err := os.Stat(candidate.path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			src.Status = "unavailable"
			src.Error = err.Error()
		}
		return src
	}
	if info.IsDir() {
		src.Status = "unavailable"
		src.Error = "path is a directory"
		return src
	}
	src.MTime = info.ModTime().Format(time.RFC3339)
	src.Size = info.Size()
	src.Format = "text"
	if strings.EqualFold(filepath.Ext(candidate.path), ".md") {
		src.Format = "markdown"
	}
	body, err := os.ReadFile(candidate.path)
	if err != nil {
		src.Status = "unavailable"
		src.Error = err.Error()
		return src
	}
	src.Status = "available"
	src.Available = true
	src.Content = string(body)
	return src
}
