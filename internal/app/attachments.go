package app

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"flow/internal/flowdb"
)

// Message attachments: files carried with a bus message (local or peer).
// flow copies each into $FLOW_ROOT/attachments/<message-id>/<name>
// (staged, then moved into place once the row is stored) and records {name,size,sha256} on the row. Files are
// data — flow never opens, runs or interprets them.
//
//	flow message <to> "<note>" --attach a.txt --attach b/a.txt
//	   └─► attachments/<id>/a.txt, attachments/<id>/a-2.txt  ─► row
//	flow peer deliver ... --attach <path>   (connector, inbound) — same

const (
	attachFileMax  = 25 << 20  // bytes per file
	attachTotalMax = 100 << 20 // bytes per message
	attachCountMax = 20        // files per message
)

// attachSrc is one validated source file and the name it will be stored as.
type attachSrc struct {
	path, name string
	size       int64
}

// checkAttachments validates --attach paths against the caps and picks
// collision-free stored names. Only regular files are accepted (no
// directories, symlinks, devices). Nothing is copied yet.
func checkAttachments(paths []string) ([]attachSrc, error) {
	if len(paths) > attachCountMax {
		return nil, fmt.Errorf("%d attachments (max %d per message)", len(paths), attachCountMax)
	}
	var out []attachSrc
	var total int64
	used := map[string]bool{}
	for _, p := range paths {
		fi, err := os.Lstat(p)
		if err != nil {
			return nil, fmt.Errorf("attachment %s: %v", p, err)
		}
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("attachment %s is not a regular file (directories and symlinks are refused)", p)
		}
		if fi.Size() > attachFileMax {
			return nil, fmt.Errorf("attachment %s is %d bytes (max %d MiB per file)", p, fi.Size(), attachFileMax>>20)
		}
		total += fi.Size()
		if total > attachTotalMax {
			return nil, fmt.Errorf("attachments total more than %d MiB per message", attachTotalMax>>20)
		}
		out = append(out, attachSrc{path: p, name: uniqueAttachName(filepath.Base(p), used), size: fi.Size()})
	}
	return out, nil
}

// uniqueAttachName returns base, or base-2, base-3 ... (before the
// extension) when an earlier attachment of this message took it.
func uniqueAttachName(base string, used map[string]bool) string {
	name := base
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if stem == "" { // dotfile like ".env": treat the whole name as the stem
		stem, ext = base, ""
	}
	for n := 2; used[name]; n++ {
		name = fmt.Sprintf("%s-%d%s", stem, n, ext)
	}
	used[name] = true
	return name
}

func attachmentsRoot() (string, error) {
	root, err := flowRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "attachments"), nil
}

// attachmentPath is the absolute path of a stored attachment.
func attachmentPath(msgID, name string) string {
	dir, err := attachmentsRoot()
	if err != nil {
		return name
	}
	if abs, err := filepath.Abs(filepath.Join(dir, msgID, name)); err == nil {
		return abs
	}
	return filepath.Join(dir, msgID, name)
}

// Attachment storage is stage → insert → rename so a concurrent
// pruneAttachments (run at the end of every `flow message`) can never
// delete files of a send that is still in flight (#131):
//
//	copy  → attachments/.staging-<id>-<rand>/   (prune skips dot-dirs)
//	insert row
//	rename staging → attachments/<id>/          (row exists: prune keeps it)
//
// If the insert fails the staging dir is removed. If the rename fails
// after the insert, the row is deleted and the send errors, so a row
// never claims files that aren't there.

const (
	attachStagingPrefix = ".staging-"
	// pruneOrphanMinAge: an orphaned attachments/<id>/ younger than this is
	// left alone — belt and braces for any other writer mid-flight.
	pruneOrphanMinAge = 10 * time.Minute
	// pruneStagingMaxAge: staging dirs older than this belong to crashed
	// sends and are removed.
	pruneStagingMaxAge = time.Hour
)

// renameAttachDir moves a staging dir into place; a var so tests can fail it.
var renameAttachDir = os.Rename

// stageAttachments copies srcs into a fresh staging dir under the
// attachments root and returns the dir and the files' metadata. On any
// error the staging dir is removed.
func stageAttachments(msgID string, srcs []attachSrc) (string, []flowdb.Attachment, error) {
	root, err := attachmentsRoot()
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp(root, attachStagingPrefix+msgID+"-")
	if err != nil {
		return "", nil, err
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	var out []flowdb.Attachment
	for _, s := range srcs {
		a, err := copyAttachment(s, filepath.Join(dir, s.name))
		if err != nil {
			os.RemoveAll(dir)
			return "", nil, fmt.Errorf("attachment %s: %v", s.path, err)
		}
		out = append(out, a)
	}
	return dir, out, nil
}

func copyAttachment(s attachSrc, dst string) (flowdb.Attachment, error) {
	in, err := os.Open(s.path)
	if err != nil {
		return flowdb.Attachment{}, err
	}
	defer in.Close()
	// The copy is data: never executable, never overwrites.
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return flowdb.Attachment{}, err
	}
	h := sha256.New()
	// Read at most one byte past the cap so a file grown since the check
	// is refused rather than copied unbounded.
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(in, attachFileMax+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return flowdb.Attachment{}, err
	}
	if n > attachFileMax {
		return flowdb.Attachment{}, fmt.Errorf("grew past %d MiB while copying", attachFileMax>>20)
	}
	return flowdb.Attachment{Name: s.name, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

// insertWithAttachments stages srcs, inserts m, then moves the staged
// files into attachments/<m.ID>/ (see the stage → insert → rename note
// above). Without attachments it is a plain insert.
func insertWithAttachments(db *sql.DB, m *flowdb.BusMessage, srcs []attachSrc) error {
	if len(srcs) == 0 {
		return flowdb.InsertBusMessage(db, m)
	}
	staging, as, err := stageAttachments(m.ID, srcs)
	if err != nil {
		return err
	}
	m.Attachments = as
	if err := flowdb.InsertBusMessage(db, m); err != nil {
		os.RemoveAll(staging)
		return err
	}
	root, err := attachmentsRoot()
	if err == nil {
		err = renameAttachDir(staging, filepath.Join(root, m.ID))
	}
	if err != nil {
		os.RemoveAll(staging)
		if derr := flowdb.DeleteBusMessage(db, m.ID); derr != nil {
			return fmt.Errorf("store attachments: %v (and removing the row failed: %v)", err, derr)
		}
		return fmt.Errorf("store attachments: %v", err)
	}
	return nil
}

// pruneAttachments removes attachment directories whose message row has
// rolled off the bus (retention), and staging dirs left by crashed sends.
// It never touches a staging dir younger than pruneStagingMaxAge nor an
// orphaned dir younger than pruneOrphanMinAge, so in-flight sends are
// safe. Best-effort, cheap: one directory read.
func pruneAttachments(db *sql.DB) {
	root, err := attachmentsRoot()
	if err != nil {
		return
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return
	}
	now := time.Now()
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue // vanished (renamed/removed) under us
		}
		age := now.Sub(fi.ModTime())
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			if strings.HasPrefix(name, attachStagingPrefix) && age > pruneStagingMaxAge {
				os.RemoveAll(filepath.Join(root, name))
			}
			continue
		}
		if age < pruneOrphanMinAge {
			continue
		}
		if m, err := flowdb.GetBusMessageByID(db, name); err == nil && m == nil {
			os.RemoveAll(filepath.Join(root, name))
		}
	}
}

// attachmentJSON is the machine-readable rendering of one attachment.
type attachmentJSON struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Path   string `json:"path"`
}

// attachmentsJSON renders m's attachments with absolute paths; an empty
// (non-nil) slice when there are none.
func attachmentsJSON(m *flowdb.BusMessage) []attachmentJSON {
	out := []attachmentJSON{}
	for _, a := range m.Attachments {
		out = append(out, attachmentJSON{Name: a.Name, Size: a.Size, SHA256: a.SHA256, Path: attachmentPath(m.ID, a.Name)})
	}
	return out
}

// attachmentLines renders one indented "attachment: <path> (<size>)" line
// per file, for the text views.
func attachmentLines(m *flowdb.BusMessage) string {
	var b strings.Builder
	for _, a := range m.Attachments {
		fmt.Fprintf(&b, "        attachment: %s (%d bytes)\n", attachmentPath(m.ID, a.Name), a.Size)
	}
	return b.String()
}
