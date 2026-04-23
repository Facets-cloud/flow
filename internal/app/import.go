package app

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"flow/internal/flowdb"
)

func cmdImport(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: import requires a bundle file path")
		return 2
	}
	bundlePath := args[0]
	fs := flagSet("import")
	force := fs.Bool("force", false, "overwrite existing slugs on conflict")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := flowdb.OpenDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: open db: %v\n", err)
		return 1
	}
	defer db.Close()

	root, err := flowRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	home, _ := os.UserHomeDir()

	if err := importBundle(db, root, bundlePath, home, *force); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func importBundle(db *sql.DB, root, bundlePath, home string, force bool) error {
	files, err := readTarFiles(bundlePath)
	if err != nil {
		return err
	}

	mfData, ok := files["manifest.json"]
	if !ok {
		return fmt.Errorf("bundle missing manifest.json")
	}
	var mf bundleManifest
	if err := json.Unmarshal(mfData, &mf); err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}

	switch mf.Type {
	case "task":
		return importTaskBundle(db, root, files, home, force)
	case "project":
		return importProjectBundle(db, root, files, home, force)
	case "all":
		return importAllBundle(db, root, files, home, force)
	}
	return fmt.Errorf("unknown bundle type %q", mf.Type)
}

// ---------- task bundle import ----------

func importTaskBundle(db *sql.DB, root string, files map[string][]byte, home string, force bool) error {
	taskData, ok := files["task.json"]
	if !ok {
		return fmt.Errorf("bundle missing task.json")
	}
	var bt bundledTask
	if err := json.Unmarshal(taskData, &bt); err != nil {
		return fmt.Errorf("parse task.json: %w", err)
	}

	if err := upsertTask(db, bt, bt.Slug, home, force); err != nil {
		return err
	}

	taskRoot := filepath.Join(root, "tasks", bt.Slug)
	skip := map[string]bool{"manifest.json": true, "task.json": true}
	if err := writeFiles(files, "", taskRoot, skip); err != nil {
		return err
	}

	fmt.Printf("imported task %q\n", bt.Slug)
	printWorkDirWarning(bt.WorkDir, home, bt.Slug)
	return nil
}

// ---------- project bundle import ----------

func importProjectBundle(db *sql.DB, root string, files map[string][]byte, home string, force bool) error {
	projData, ok := files["project.json"]
	if !ok {
		return fmt.Errorf("bundle missing project.json")
	}
	var bp bundledProject
	if err := json.Unmarshal(projData, &bp); err != nil {
		return fmt.Errorf("parse project.json: %w", err)
	}

	if err := upsertProject(db, bp, home, force); err != nil {
		return err
	}

	// Write project-level files (brief.md, updates/) — skip tasks/ subtree.
	projRoot := filepath.Join(root, "projects", bp.Slug)
	skip := map[string]bool{"manifest.json": true, "project.json": true}
	for name, data := range files {
		if skip[name] || strings.HasPrefix(name, "tasks/") {
			continue
		}
		dst := filepath.Join(projRoot, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return err
		}
	}

	// Import each task nested under tasks/.
	taskGroups := groupByPrefix(files, "tasks/")
	for taskSlug, tFiles := range taskGroups {
		taskData, ok := tFiles["task.json"]
		if !ok {
			continue
		}
		var bt bundledTask
		if err := json.Unmarshal(taskData, &bt); err != nil {
			return fmt.Errorf("parse tasks/%s/task.json: %w", taskSlug, err)
		}
		if err := upsertTask(db, bt, bt.Slug, home, force); err != nil {
			return err
		}
		taskRoot := filepath.Join(root, "tasks", bt.Slug)
		if err := writeFiles(tFiles, "", taskRoot, map[string]bool{"task.json": true}); err != nil {
			return err
		}
		printWorkDirWarning(bt.WorkDir, home, bt.Slug)
	}

	fmt.Printf("imported project %q with %d tasks\n", bp.Slug, len(taskGroups))
	printWorkDirWarning(bp.WorkDir, home, "project:"+bp.Slug)
	return nil
}

// ---------- all bundle import ----------

func importAllBundle(db *sql.DB, root string, files map[string][]byte, home string, force bool) error {
	projGroups := groupByPrefix(files, "projects/")
	floatGroups := groupByPrefix(files, "floating-tasks/")

	importedProj, importedTask := 0, 0

	for projSlug, pFiles := range projGroups {
		projData, ok := pFiles["project.json"]
		if !ok {
			continue
		}
		var bp bundledProject
		if err := json.Unmarshal(projData, &bp); err != nil {
			return fmt.Errorf("parse projects/%s/project.json: %w", projSlug, err)
		}
		if err := upsertProject(db, bp, home, force); err != nil {
			return err
		}
		projRoot := filepath.Join(root, "projects", bp.Slug)
		for name, data := range pFiles {
			if name == "project.json" || strings.HasPrefix(name, "tasks/") {
				continue
			}
			dst := filepath.Join(projRoot, filepath.FromSlash(name))
			os.MkdirAll(filepath.Dir(dst), 0o755)
			os.WriteFile(dst, data, 0o644)
		}
		importedProj++

		taskGroups := groupByPrefix(pFiles, "tasks/")
		for _, tFiles := range taskGroups {
			taskData, ok := tFiles["task.json"]
			if !ok {
				continue
			}
			var bt bundledTask
			if err := json.Unmarshal(taskData, &bt); err != nil {
				return err
			}
			if err := upsertTask(db, bt, bt.Slug, home, force); err != nil {
				return err
			}
			taskRoot := filepath.Join(root, "tasks", bt.Slug)
			writeFiles(tFiles, "", taskRoot, map[string]bool{"task.json": true})
			importedTask++
		}
	}

	for _, tFiles := range floatGroups {
		taskData, ok := tFiles["task.json"]
		if !ok {
			continue
		}
		var bt bundledTask
		if err := json.Unmarshal(taskData, &bt); err != nil {
			return err
		}
		if err := upsertTask(db, bt, bt.Slug, home, force); err != nil {
			return err
		}
		taskRoot := filepath.Join(root, "tasks", bt.Slug)
		writeFiles(tFiles, "", taskRoot, map[string]bool{"task.json": true})
		importedTask++
	}

	// KB files.
	for name, data := range files {
		if !strings.HasPrefix(name, "kb/") {
			continue
		}
		dst := filepath.Join(root, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(dst), 0o755)
		os.WriteFile(dst, data, 0o644)
	}

	fmt.Printf("imported %d projects, %d tasks\n", importedProj, importedTask)
	return nil
}

// ---------- DB upsert helpers ----------

func upsertTask(db *sql.DB, bt bundledTask, targetSlug, home string, force bool) error {
	var exists int
	db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE slug = ?`, targetSlug).Scan(&exists)
	if exists > 0 && !force {
		return fmt.Errorf("task %q already exists; use --force to overwrite", targetSlug)
	}

	now := flowdb.NowISO()
	workDir := expandHome(bt.WorkDir, home)

	project := sql.NullString{}
	if bt.ProjectSlug != "" {
		project = sql.NullString{String: bt.ProjectSlug, Valid: true}
	}
	waitingOn := sql.NullString{}
	if bt.WaitingOn != "" {
		waitingOn = sql.NullString{String: bt.WaitingOn, Valid: true}
	}
	dueDate := sql.NullString{}
	if bt.DueDate != "" {
		dueDate = sql.NullString{String: bt.DueDate, Valid: true}
	}
	statusChangedAt := sql.NullString{}
	if bt.StatusChangedAt != "" {
		statusChangedAt = sql.NullString{String: bt.StatusChangedAt, Valid: true}
	}
	archivedAt := sql.NullString{}
	if bt.ArchivedAt != "" {
		archivedAt = sql.NullString{String: bt.ArchivedAt, Valid: true}
	}

	_, err := db.Exec(`INSERT OR REPLACE INTO tasks
		(slug, name, project_slug, status, priority, work_dir, waiting_on, due_date,
		 status_changed_at, created_at, updated_at, archived_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		targetSlug, bt.Name, project, bt.Status, bt.Priority, workDir,
		waitingOn, dueDate, statusChangedAt, bt.CreatedAt, now, archivedAt,
	)
	return err
}

func upsertProject(db *sql.DB, bp bundledProject, home string, force bool) error {
	var exists int
	db.QueryRow(`SELECT COUNT(*) FROM projects WHERE slug = ?`, bp.Slug).Scan(&exists)
	if exists > 0 && !force {
		return fmt.Errorf("project %q already exists; use --force to overwrite", bp.Slug)
	}
	now := flowdb.NowISO()
	archivedAt := sql.NullString{}
	if bp.ArchivedAt != "" {
		archivedAt = sql.NullString{String: bp.ArchivedAt, Valid: true}
	}
	workDir := expandHome(bp.WorkDir, home)
	_, err := db.Exec(`INSERT OR REPLACE INTO projects
		(slug, name, status, priority, work_dir, created_at, updated_at, archived_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		bp.Slug, bp.Name, bp.Status, bp.Priority, workDir, bp.CreatedAt, now, archivedAt,
	)
	return err
}

// ---------- file-write helpers ----------

// writeFiles writes files map entries (except skip entries) to dstRoot.
// Tar paths are forward-slash; filepath.FromSlash converts for the OS.
func writeFiles(files map[string][]byte, _ string, dstRoot string, skip map[string]bool) error {
	for name, data := range files {
		if skip[name] {
			continue
		}
		dst := filepath.Join(dstRoot, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// groupByPrefix extracts sub-maps from files keyed by a path prefix.
// e.g. prefix="tasks/", entry "tasks/task-a/task.json" → sub-slug="task-a", key="task.json".
func groupByPrefix(files map[string][]byte, prefix string) map[string]map[string][]byte {
	groups := map[string]map[string][]byte{}
	for name, data := range files {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		rest := strings.TrimPrefix(name, prefix)
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) < 2 {
			continue
		}
		sub, rel := parts[0], parts[1]
		if groups[sub] == nil {
			groups[sub] = map[string][]byte{}
		}
		groups[sub][rel] = data
	}
	return groups
}

// printWorkDirWarning warns if work_dir still has <HOME> after expansion
// (meaning expandHome couldn't resolve it).
func printWorkDirWarning(workDir, home, label string) {
	expanded := expandHome(workDir, home)
	if strings.Contains(expanded, "<HOME>") {
		fmt.Fprintf(os.Stderr, "  ⚠ work_dir for %s may need updating: %s\n    run: flow update task %s --work-dir <path>\n", label, expanded, label)
	}
}
