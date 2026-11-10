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

	"flow/internal/flowdb"
)

// Message attachments: files carried with a bus message (local or peer).
// flow copies each into $FLOW_ROOT/attachments/<message-id>/<name> before
// the row is stored and records {name,size,sha256} on the row. Files are
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

// storeAttachments copies srcs into attachments/<msgID>/ and returns their
// metadata. On any error the message's directory is removed.
func storeAttachments(msgID string, srcs []attachSrc) ([]flowdb.Attachment, error) {
	if len(srcs) == 0 {
		return nil, nil
	}
	root, err := attachmentsRoot()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, msgID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var out []flowdb.Attachment
	for _, s := range srcs {
		a, err := copyAttachment(s, filepath.Join(dir, s.name))
		if err != nil {
			os.RemoveAll(dir)
			return nil, fmt.Errorf("attachment %s: %v", s.path, err)
		}
		out = append(out, a)
	}
	return out, nil
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

// removeAttachments deletes a message's stored attachments (best-effort).
func removeAttachments(msgID string) {
	if root, err := attachmentsRoot(); err == nil && msgID != "" {
		os.RemoveAll(filepath.Join(root, msgID))
	}
}

// insertWithAttachments copies srcs for m, then inserts m; if the insert
// fails the copies are removed so no orphan directory is left behind.
func insertWithAttachments(db *sql.DB, m *flowdb.BusMessage, srcs []attachSrc) error {
	as, err := storeAttachments(m.ID, srcs)
	if err != nil {
		return err
	}
	m.Attachments = as
	if err := flowdb.InsertBusMessage(db, m); err != nil {
		if len(as) > 0 {
			removeAttachments(m.ID)
		}
		return err
	}
	return nil
}

// pruneAttachments removes attachment directories whose message row has
// rolled off the bus (retention). Best-effort, cheap: one directory read.
func pruneAttachments(db *sql.DB) {
	root, err := attachmentsRoot()
	if err != nil {
		return
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		if m, err := flowdb.GetBusMessageByID(db, e.Name()); err == nil && m == nil {
			os.RemoveAll(filepath.Join(root, e.Name()))
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
