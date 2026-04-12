package app

import (
	"bufio"
	"database/sql"
	"errors"
	"flow/internal/flowdb"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// cmdWorkdir dispatches `flow workdir list|add|remove|scan`.
func cmdWorkdir(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: workdir requires a subcommand (list|add|remove|scan)")
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return cmdWorkdirList(rest)
	case "add":
		return cmdWorkdirAdd(rest)
	case "remove", "rm":
		return cmdWorkdirRemove(rest)
	case "scan":
		return cmdWorkdirScan(rest)
	default:
		fmt.Fprintf(os.Stderr, "error: unknown workdir subcommand %q\n", sub)
		return 2
	}
}

// openFlowDBForWorkdir opens the flow DB and returns (db, rc). On error, it
// prints and returns a non-nil error rc.
func openFlowDBForWorkdir() (*sql.DB, int) {
	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return nil, 1
	}
	db, err := flowdb.OpenDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: open db: %v\n", err)
		return nil, 1
	}
	return db, 0
}

// cmdWorkdirList prints all registered workdirs sorted by last_used_at
// descending (NULLs last). Each row is rendered across two output lines
// when a description is present — header first, description on a second
// indented line — so descriptions don't force the header columns wide.
func cmdWorkdirList(args []string) int {
	fs := flagSet("workdir list")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "error: workdir list takes no positional arguments")
		return 2
	}
	db, rc := openFlowDBForWorkdir()
	if rc != 0 {
		return rc
	}
	defer db.Close()

	list, err := flowdb.ListWorkdirs(db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if len(list) == 0 {
		fmt.Println("(no registered workdirs)")
		return 0
	}

	maxPath := 0
	maxName := 0
	for _, w := range list {
		if n := len(w.Path); n > maxPath {
			maxPath = n
		}
		if w.Name.Valid {
			if n := len(w.Name.String); n > maxName {
				maxName = n
			}
		}
	}
	now := time.Now()
	for _, w := range list {
		parts := []string{fmt.Sprintf("  %-*s", maxPath, w.Path)}
		if maxName > 0 {
			name := ""
			if w.Name.Valid {
				name = w.Name.String
			}
			parts = append(parts, fmt.Sprintf("%-*s", maxName, name))
		}
		if w.GitRemote.Valid && w.GitRemote.String != "" {
			parts = append(parts, "origin: "+w.GitRemote.String)
		}
		parts = append(parts, humanizeLastUsed(w.LastUsedAt, now))
		fmt.Println(strings.Join(parts, "    "))
		if w.Description.Valid && w.Description.String != "" {
			fmt.Printf("    %s\n", w.Description.String)
		}
	}
	return 0
}

// humanizeLastUsed renders last_used_at as "(used 2d ago)" / "(never used)".
func humanizeLastUsed(ts sql.NullString, now time.Time) string {
	if !ts.Valid || ts.String == "" {
		return "(never used)"
	}
	t, err := time.Parse(time.RFC3339, ts.String)
	if err != nil {
		return "(used " + ts.String + ")"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "(used just now)"
	case d < time.Hour:
		return fmt.Sprintf("(used %dm ago)", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("(used %dh ago)", int(d.Hours()))
	default:
		return fmt.Sprintf("(used %dd ago)", int(d.Hours()/24))
	}
}

// cmdWorkdirAdd registers <path> (making it absolute), auto-detects git
// remote from .git/config, and upserts. Accepts --name and --description
// anywhere in the arg list.
func cmdWorkdirAdd(args []string) int {
	name, rest, err := extractWorkdirStringFlag(args, "--name")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	description, rest, err := extractWorkdirStringFlag(rest, "--description")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	fs := flagSet("workdir add")
	fs.String("name", "", "short nickname")                  // for -h rendering only
	fs.String("description", "", "one-line free-form notes") // for -h rendering only
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "error: workdir add requires exactly one path")
		return 2
	}
	path := fs.Arg(0)
	abs, err := filepath.Abs(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	info, err := os.Stat(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if !info.IsDir() {
		fmt.Fprintf(os.Stderr, "error: %s is not a directory\n", abs)
		return 1
	}

	remote := detectGitRemote(abs)

	db, rc := openFlowDBForWorkdir()
	if rc != 0 {
		return rc
	}
	defer db.Close()

	if err := flowdb.UpsertWorkdir(db, abs, name, description, remote); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	tag := ""
	if name != "" {
		tag = fmt.Sprintf(" [name=%s]", name)
	}
	fmt.Printf("Registered workdir %s%s\n", abs, tag)
	if description != "" {
		fmt.Printf("    %s\n", description)
	}
	return 0
}

// extractWorkdirBoolFlag removes a bare boolean flag anywhere in args and
// returns (found, remaining). Named specifically to avoid collisions with
// parallel-agent helpers.
func extractWorkdirBoolFlag(args []string, flagName string) (bool, []string) {
	out := make([]string, 0, len(args))
	found := false
	for _, a := range args {
		if a == flagName {
			found = true
			continue
		}
		out = append(out, a)
	}
	return found, out
}

// extractWorkdirStringFlag pulls a `--flag val` or `--flag=val` pair out of
// args, returning (value, remaining, error). Returns ("", args, nil) if
// absent. Unique name to avoid collisions with parallel-agent helpers.
func extractWorkdirStringFlag(args []string, flagName string) (string, []string, error) {
	out := make([]string, 0, len(args))
	value := ""
	i := 0
	for i < len(args) {
		a := args[i]
		if a == flagName {
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("flag %s requires a value", flagName)
			}
			value = args[i+1]
			i += 2
			continue
		}
		if strings.HasPrefix(a, flagName+"=") {
			value = strings.TrimPrefix(a, flagName+"=")
			i++
			continue
		}
		out = append(out, a)
		i++
	}
	return value, out, nil
}

// cmdWorkdirRemove deletes a workdir row. Does not touch the filesystem
// and does not null out tasks.work_dir.
func cmdWorkdirRemove(args []string) int {
	fs := flagSet("workdir remove")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "error: workdir remove requires exactly one path")
		return 2
	}
	abs, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, rc := openFlowDBForWorkdir()
	if rc != 0 {
		return rc
	}
	defer db.Close()
	if _, err := db.Exec("DELETE FROM workdirs WHERE path = ?", abs); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("Unregistered workdir %s\n", abs)
	return 0
}

// cmdWorkdirScan walks a root (or ~/code and ~/work by default) up to 3
// levels deep, finds .git directories, and either prints candidates or
// upserts them with --add.
func cmdWorkdirScan(args []string) int {
	// --add may appear anywhere; extract it manually.
	add, rest := extractWorkdirBoolFlag(args, "--add")
	fs := flagSet("workdir scan")
	fs.Bool("add", false, "register discovered candidates") // for -h only
	if err := fs.Parse(rest); err != nil {
		return 2
	}

	var roots []string
	if fs.NArg() == 1 {
		abs, err := filepath.Abs(fs.Arg(0))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		roots = []string{abs}
	} else if fs.NArg() > 1 {
		fmt.Fprintln(os.Stderr, "error: workdir scan takes at most one root")
		return 2
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		for _, d := range []string{"code", "work"} {
			p := filepath.Join(home, d)
			if info, err := os.Stat(p); err == nil && info.IsDir() {
				roots = append(roots, p)
			}
		}
		if len(roots) == 0 {
			fmt.Println("(no default scan roots exist: tried ~/code, ~/work)")
			return 0
		}
	}

	db, rc := openFlowDBForWorkdir()
	if rc != 0 {
		return rc
	}
	defer db.Close()

	var candidates []string
	seen := map[string]bool{}
	for _, root := range roots {
		found, err := scanForGitRepos(root, 3)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: scan %s: %v\n", root, err)
			continue
		}
		for _, p := range found {
			if !seen[p] {
				seen[p] = true
				candidates = append(candidates, p)
			}
		}
	}
	sort.Strings(candidates)

	if len(candidates) == 0 {
		fmt.Println("(no .git repos found)")
		return 0
	}

	addedCount := 0
	for _, path := range candidates {
		remote := detectGitRemote(path)
		existing, err := flowdb.GetWorkdir(db, path)
		known := err == nil && existing != nil
		if !known && !errors.Is(err, sql.ErrNoRows) && err != nil {
			// Non-fatal: log and treat as new.
			fmt.Fprintf(os.Stderr, "warning: query %s: %v\n", path, err)
		}

		if add {
			if known {
				continue
			}
			name := filepath.Base(path)
			if err := flowdb.UpsertWorkdir(db, path, name, "", remote); err != nil {
				fmt.Fprintf(os.Stderr, "warning: upsert %s: %v\n", path, err)
				continue
			}
			addedCount++
			if remote != "" {
				fmt.Printf("Added %s (origin: %s)\n", path, remote)
			} else {
				fmt.Printf("Added %s\n", path)
			}
		} else {
			marker := "[new]"
			if known {
				marker = "[known]"
			}
			if remote != "" {
				fmt.Printf("%s %s    origin: %s\n", marker, path, remote)
			} else {
				fmt.Printf("%s %s\n", marker, path)
			}
		}
	}
	if add {
		fmt.Printf("Added %d workdirs\n", addedCount)
	}
	return 0
}

// scanForGitRepos walks root up to maxDepth levels, recording parent
// directories of any `.git` subdirectory it finds. maxDepth of 3 means
// root/a/b/c/.git is the deepest match. Does not recurse into .git itself
// or into discovered repos (once found, we stop descending into that tree).
func scanForGitRepos(root string, maxDepth int) ([]string, error) {
	var found []string
	rootDepth := strings.Count(strings.TrimRight(root, string(os.PathSeparator)), string(os.PathSeparator))

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Permission denied etc — keep walking other branches.
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		// Depth check.
		depth := strings.Count(strings.TrimRight(path, string(os.PathSeparator)), string(os.PathSeparator)) - rootDepth
		if depth > maxDepth {
			return filepath.SkipDir
		}
		// Check if this dir contains .git (file or dir).
		gitPath := filepath.Join(path, ".git")
		if info, err := os.Stat(gitPath); err == nil && (info.IsDir() || info.Mode().IsRegular()) {
			found = append(found, path)
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

// gitRemoteRE matches the url = <value> line under the origin section of a
// .git/config. The whole [remote "origin"] block lookup is done manually
// below; this regex only pulls the URL value out of a given line.
var gitRemoteURLRE = regexp.MustCompile(`^\s*url\s*=\s*(.+?)\s*$`)

// detectGitRemote reads <path>/.git/config (if present) and extracts the
// origin remote URL. Returns "" on any error or if the section isn't
// present. Handles `.git` being either a directory (normal repo) or a
// file (git worktree — we follow the gitdir pointer).
func detectGitRemote(path string) string {
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
			// Section header.
			inOrigin = (trimmed == `[remote "origin"]`)
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

// resolveGitConfigPath returns the absolute path to the git config for a
// repo rooted at path, or "" if none exists. Handles both the common case
// (.git is a directory) and the git-worktree case (.git is a file with
// `gitdir: <path>`).
func resolveGitConfigPath(path string) string {
	gitPath := filepath.Join(path, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return ""
	}
	if info.IsDir() {
		return filepath.Join(gitPath, "config")
	}
	// Worktree: .git is a file of the form "gitdir: <relative-or-absolute>".
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
