package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"flow/internal/flowdb"
)

// fileSHA hashes a file on disk.
func fileSHA(t *testing.T, p string) (string, error) {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// verifyStored asserts every file the row claims exists with a matching hash.
func verifyStored(t *testing.T, root string, m *flowdb.BusMessage) {
	t.Helper()
	for _, a := range m.Attachments {
		p := filepath.Join(root, "attachments", m.ID, a.Name)
		got, err := fileSHA(t, p)
		if err != nil {
			t.Errorf("row %s claims %s but: %v", m.ID, a.Name, err)
			continue
		}
		if got != a.SHA256 {
			t.Errorf("row %s %s sha=%s want %s", m.ID, a.Name, got, a.SHA256)
		}
	}
}

func newAttachMsg() *flowdb.BusMessage {
	return &flowdb.BusMessage{
		ID: newBusID(), CreatedAt: flowdb.NowISO(), Kind: "message",
		FromAssignee: busUser, ToAssignee: busUser, ToTaskSlug: "other", Body: "x", FYI: true,
	}
}

// #131: a send that copies its attachments while another `flow message`
// runs pruneAttachments must never lose its files. Overlapping senders
// (with attachments) and plain senders (that prune) run concurrently;
// afterwards every row with attachments has all its files intact.
func TestAttachmentsSurviveConcurrentPrune(t *testing.T) {
	root := setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "other", "sid-other")
	src := t.TempDir()
	tiny := writeFile(t, src, "tiny.txt", strings.Repeat("payload ", 512))
	srcs, err := checkAttachments([]string{tiny, tiny})
	if err != nil {
		t.Fatal(err)
	}

	const senders, perSender, pruners = 6, 40, 3
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		ids  []string
		errs []error
	)
	stop := make(chan struct{})
	var pwg sync.WaitGroup
	for i := 0; i < pruners; i++ {
		pwg.Add(1)
		go func() {
			defer pwg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					pruneAttachments(db)
				}
			}
		}()
	}
	for i := 0; i < senders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perSender; j++ {
				m := newAttachMsg()
				err := insertWithAttachments(db, m, srcs)
				mu.Lock()
				if err != nil {
					errs = append(errs, err)
				} else {
					ids = append(ids, m.ID)
				}
				mu.Unlock()
				pruneAttachments(db) // every send prunes at the end, as cmdMessage does
			}
		}()
	}
	wg.Wait()
	close(stop)
	pwg.Wait()

	for _, e := range errs {
		t.Errorf("send failed: %v", e)
	}
	lost := 0
	for _, id := range ids {
		m, err := flowdb.GetBusMessageByID(db, id)
		if err != nil || m == nil {
			t.Fatalf("row %s: %v", id, err)
		}
		if _, err := os.Stat(filepath.Join(root, "attachments", id)); err != nil {
			lost++
		}
		verifyStored(t, root, m)
	}
	if lost > 0 {
		t.Errorf("%d/%d rows lost their attachment dir", lost, len(ids))
	}
}

// #131 large-file case: one send of 4 x 24 MiB beside a loop of pruning
// plain sends must succeed with every file intact.
func TestLargeAttachmentsSurviveConcurrentPrune(t *testing.T) {
	if testing.Short() {
		t.Skip("copies ~96 MiB")
	}
	root := setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "other", "sid-other")
	src := t.TempDir()
	var paths []string
	for i := 0; i < 4; i++ {
		paths = append(paths, sparse(t, src, fmt.Sprintf("big%d.bin", i), 24<<20))
	}
	srcs, err := checkAttachments(paths)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				pruneAttachments(db)
			}
		}
	}()
	for k := 0; k < 3; k++ {
		m := newAttachMsg()
		if err := insertWithAttachments(db, m, srcs); err != nil {
			close(stop)
			<-done
			t.Fatalf("send %d: %v", k, err)
		}
		got, _ := flowdb.GetBusMessageByID(db, m.ID)
		if got == nil || len(got.Attachments) != 4 {
			t.Fatalf("row = %+v", got)
		}
		verifyStored(t, root, got)
	}
	close(stop)
	<-done
}

// pruneAttachments removes old orphaned dirs and stale staging dirs, but
// leaves young orphans (another writer may be mid-flight), fresh staging
// dirs, and dirs whose row exists.
func TestPruneAttachmentsAgeAndStaging(t *testing.T) {
	root := setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "other", "sid-other")
	src := t.TempDir()
	f := writeFile(t, src, "a.txt", "a")
	srcs, _ := checkAttachments([]string{f})
	kept := newAttachMsg()
	if err := insertWithAttachments(db, kept, srcs); err != nil {
		t.Fatal(err)
	}
	ar := filepath.Join(root, "attachments")
	mk := func(name string, age time.Duration) string {
		p := filepath.Join(ar, name)
		writeFile(t, p, "f", "x")
		old := time.Now().Add(-age)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
		return p
	}
	oldKept := filepath.Join(ar, kept.ID)
	ancient := time.Now().Add(-48 * time.Hour)
	os.Chtimes(oldKept, ancient, ancient)
	youngOrphan := mk("deadbeef", time.Minute)
	oldOrphan := mk("cafef00d", 11*time.Minute)
	freshStaging := mk(".staging-aaaa-1111", 30*time.Minute)
	staleStaging := mk(".staging-bbbb-2222", 2*time.Hour)

	pruneAttachments(db)

	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	for _, p := range []string{oldKept, youngOrphan, freshStaging} {
		if !exists(p) {
			t.Errorf("%s was pruned, want kept", filepath.Base(p))
		}
	}
	for _, p := range []string{oldOrphan, staleStaging} {
		if exists(p) {
			t.Errorf("%s survived, want pruned", filepath.Base(p))
		}
	}
}

// If the final rename fails after the row is inserted, the send errors
// and the row is removed, so no row ever claims files that aren't there;
// the staging dir is cleaned up too.
func TestAttachRenameFailureDropsRow(t *testing.T) {
	root := setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "other", "sid-other")
	f := writeFile(t, t.TempDir(), "a.txt", "a")
	srcs, _ := checkAttachments([]string{f})
	orig := renameAttachDir
	renameAttachDir = func(string, string) error { return errors.New("boom") }
	t.Cleanup(func() { renameAttachDir = orig })

	m := newAttachMsg()
	if err := insertWithAttachments(db, m, srcs); err == nil {
		t.Fatal("want error")
	}
	if got, _ := flowdb.GetBusMessageByID(db, m.ID); got != nil {
		t.Errorf("row %s left behind claiming attachments", m.ID)
	}
	ents, _ := os.ReadDir(filepath.Join(root, "attachments"))
	for _, e := range ents {
		t.Errorf("left behind: %s", e.Name())
	}
}
