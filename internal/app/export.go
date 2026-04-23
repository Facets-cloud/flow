package app

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"flow/internal/flowdb"
)

// ---------- bundle types ----------

type bundleManifest struct {
	Type       string `json:"type"`
	Version    string `json:"version"`
	ExportedAt string `json:"exported_at"`
	Slug       string `json:"slug,omitempty"`
}

type bundledTask struct {
	Slug            string `json:"slug"`
	Name            string `json:"name"`
	ProjectSlug     string `json:"project_slug,omitempty"`
	Status          string `json:"status"`
	Priority        string `json:"priority"`
	WorkDir         string `json:"work_dir"`
	WaitingOn       string `json:"waiting_on,omitempty"`
	DueDate         string `json:"due_date,omitempty"`
	StatusChangedAt string `json:"status_changed_at,omitempty"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
	ArchivedAt      string `json:"archived_at,omitempty"`
}

type bundledProject struct {
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Priority   string `json:"priority"`
	WorkDir    string `json:"work_dir"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
	ArchivedAt string `json:"archived_at,omitempty"`
}

// ---------- home-dir masking ----------

func maskHome(p, home string) string {
	if home != "" && strings.HasPrefix(p, home) {
		return "<HOME>" + p[len(home):]
	}
	return p
}

func expandHome(p, home string) string {
	if strings.HasPrefix(p, "<HOME>") {
		return home + p[len("<HOME>"):]
	}
	return p
}

// ---------- DB → bundle conversions ----------

func taskFromDB(t *flowdb.Task, home string) bundledTask {
	return bundledTask{
		Slug:            t.Slug,
		Name:            t.Name,
		ProjectSlug:     t.ProjectSlug.String,
		Status:          t.Status,
		Priority:        t.Priority,
		WorkDir:         maskHome(t.WorkDir, home),
		WaitingOn:       t.WaitingOn.String,
		DueDate:         t.DueDate.String,
		StatusChangedAt: t.StatusChangedAt.String,
		CreatedAt:       t.CreatedAt,
		UpdatedAt:       t.UpdatedAt,
		ArchivedAt:      t.ArchivedAt.String,
	}
}

func projectFromDB(p *flowdb.Project, home string) bundledProject {
	return bundledProject{
		Slug:       p.Slug,
		Name:       p.Name,
		Status:     p.Status,
		Priority:   p.Priority,
		WorkDir:    maskHome(p.WorkDir, home),
		CreatedAt:  p.CreatedAt,
		UpdatedAt:  p.UpdatedAt,
		ArchivedAt: p.ArchivedAt.String,
	}
}

// ---------- tar write helpers ----------

func addBytesToTar(tw *tar.Writer, name string, data []byte) error {
	hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func addFileToTar(tw *tar.Writer, srcPath, tarName string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	return addBytesToTar(tw, tarName, data)
}

// addUpdatesTar writes all *.md files from updatesDir into tw under tarPrefix/updates/.
func addUpdatesTar(tw *tar.Writer, updatesDir, tarPrefix string) error {
	entries, _ := os.ReadDir(updatesDir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		src := filepath.Join(updatesDir, e.Name())
		dst := path.Join(tarPrefix, "updates", e.Name())
		if err := addFileToTar(tw, src, dst); err != nil {
			return fmt.Errorf("add update %s: %w", e.Name(), err)
		}
	}
	return nil
}

func marshalJSON(v any) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}

// newTarFile creates the output file and returns (outPath, tarWriter, cleanup, error).
// cleanup(&ok) must be deferred: it closes writers and removes the file if ok==false.
func newTarFile(outDir, filename string) (string, *tar.Writer, func(ok *bool), error) {
	outPath, err := filepath.Abs(filepath.Join(outDir, filename))
	if err != nil {
		return "", nil, nil, err
	}
	f, err := os.Create(outPath)
	if err != nil {
		return "", nil, nil, fmt.Errorf("create bundle: %w", err)
	}
	tw := tar.NewWriter(f)
	cleanup := func(ok *bool) {
		tw.Close()
		f.Close()
		if !*ok {
			os.Remove(outPath)
		}
	}
	return outPath, tw, cleanup, nil
}

// readTarFiles reads all entries of a tar into a name→data map.
// Used by import.go (same package).
func readTarFiles(tarPath string) (map[string][]byte, error) {
	f, err := os.Open(tarPath)
	if err != nil {
		return nil, fmt.Errorf("open bundle: %w", err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	files := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar: %w", err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		files[hdr.Name] = data
	}
	return files, nil
}

// ---------- dispatcher ----------

func cmdExport(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: export requires 'task', 'project', or 'all'")
		return 2
	}
	switch args[0] {
	case "task":
		return exportTaskCmd(args[1:])
	case "project":
		return exportProjectCmd(args[1:])
	case "all":
		return exportAllCmd(args[1:])
	}
	fmt.Fprintf(os.Stderr, "error: unknown export subcommand %q (want task|project|all)\n", args[0])
	return 2
}

// ---------- flow export task ----------

func exportTaskCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: export task requires a slug")
		return 2
	}
	slug := args[0]
	fs := flagSet("export task")
	outDir := fs.String("output", ".", "directory to write bundle")
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

	task, err := flowdb.GetTask(db, slug)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: task %q not found\n", slug)
		return 1
	}
	root, err := flowRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	home, _ := os.UserHomeDir()

	outPath, err := writeTaskBundle(task, root, *outDir, home)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Println(outPath)
	return 0
}

func writeTaskBundle(task *flowdb.Task, root, outDir, home string) (string, error) {
	filename := fmt.Sprintf("flow-task-%s-%s.tar", task.Slug, time.Now().Format("20060102"))
	ok := false
	outPath, tw, cleanup, err := newTarFile(outDir, filename)
	if err != nil {
		return "", err
	}
	defer cleanup(&ok)

	mf := bundleManifest{Type: "task", Version: "1", ExportedAt: time.Now().Format(time.RFC3339), Slug: task.Slug}
	b, err := marshalJSON(mf)
	if err != nil {
		return "", fmt.Errorf("marshal manifest: %w", err)
	}
	if err := addBytesToTar(tw, "manifest.json", b); err != nil {
		return "", fmt.Errorf("write manifest: %w", err)
	}

	bt := taskFromDB(task, home)
	b, err = marshalJSON(bt)
	if err != nil {
		return "", fmt.Errorf("marshal task: %w", err)
	}
	if err := addBytesToTar(tw, "task.json", b); err != nil {
		return "", fmt.Errorf("write task: %w", err)
	}

	briefPath := filepath.Join(root, "tasks", task.Slug, "brief.md")
	if err := addFileToTar(tw, briefPath, "brief.md"); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("brief.md: %w", err)
	}

	updatesDir := filepath.Join(root, "tasks", task.Slug, "updates")
	if err := addUpdatesTar(tw, updatesDir, "."); err != nil {
		return "", err
	}

	ok = true
	return outPath, nil
}

// Stubs for Tasks 2 and 3 — replaced in subsequent tasks.
func exportProjectCmd(_ []string) int {
	fmt.Fprintln(os.Stderr, "error: export project not yet implemented")
	return 1
}

func exportAllCmd(_ []string) int {
	fmt.Fprintln(os.Stderr, "error: export all not yet implemented")
	return 1
}
