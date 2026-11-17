package app

import (
	"flow/internal/flowdb"
	"flow/internal/harness"
	"fmt"
	"os"
	"path/filepath"
)

// cmdTranscriptBackfill implements `flow transcript backfill [--all|<slug>]
// [--dry-run]`. For every task with a session id whose harness transcript
// still exists it writes or refreshes the flow copy. Idempotent (each copy
// is rebuilt from scratch). --dry-run reports what it would write without
// touching disk. Reports per-task and total record/byte counts.
func cmdTranscriptBackfill(args []string) int {
	fs := flagSet("transcript backfill")
	all := fs.Bool("all", false, "backfill every task (default when no slug is given)")
	dryRun := fs.Bool("dry-run", false, "report what would be written without writing")
	// Allow a leading positional slug before flags.
	ref := ""
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		ref = args[0]
		args = args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if ref == "" && fs.NArg() > 0 {
		ref = fs.Arg(0)
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

	var tasks []*flowdb.Task
	if ref != "" && !*all {
		t, err := resolveTaskRef(db, ref)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		tasks = []*flowdb.Task{t}
	} else {
		tasks, err = flowdb.ListTasks(db, flowdb.TaskFilter{IncludeArchived: true})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: list tasks: %v\n", err)
			return 1
		}
	}

	var totalRecords int
	var totalBytes int64
	var copied, skipped int
	for _, t := range tasks {
		if !t.SessionID.Valid || t.SessionID.String == "" {
			continue
		}
		if !transcriptsCopyEnabled(t.Slug) {
			skipped++
			continue
		}
		recs, bytes, status, err := backfillOne(t, *dryRun)
		switch {
		case err != nil:
			fmt.Printf("  %-32s error: %v\n", t.Slug, err)
		case status != "":
			// nothing captured (no source, no normalizer); stay quiet unless verbose
		default:
			copied++
			totalRecords += recs
			totalBytes += bytes
			verb := "wrote"
			if *dryRun {
				verb = "would write"
			}
			fmt.Printf("  %-32s %s %d records (%d source bytes)\n", t.Slug, verb, recs, bytes)
		}
	}

	verb := "Backfilled"
	if *dryRun {
		verb = "Would backfill"
	}
	fmt.Printf("%s %d task(s): %d records, %d source bytes", verb, copied, totalRecords, totalBytes)
	if skipped > 0 {
		fmt.Printf(" (%d skipped — copy disabled)", skipped)
	}
	fmt.Println()
	return 0
}

// backfillOne force-rebuilds (or, in dry-run, measures) the flow copy for a
// single task. status != "" means nothing to do (no source / no support).
func backfillOne(t *flowdb.Task, dryRun bool) (records int, bytes int64, status string, err error) {
	sid := t.SessionID.String
	hName := taskHarnessName(t)
	h, err := harnessForTask(t)
	if err != nil {
		return 0, 0, "", err
	}
	pather, ok := h.(harness.TranscriptPather)
	if !ok {
		return 0, 0, "unsupported", nil
	}
	norm, ok := h.(harness.TranscriptNormalizer)
	if !ok {
		return 0, 0, "unsupported", nil
	}
	sourcePath, err := pather.TranscriptPath(t.WorkDir, sid)
	if err != nil {
		return 0, 0, "no-source", nil // harness transcript gone — can't backfill
	}
	fi, err := os.Stat(sourcePath)
	if err != nil {
		return 0, 0, "no-source", nil
	}
	size, inode := fi.Size(), fileInode(fi)

	if dryRun {
		f, err := os.Open(sourcePath)
		if err != nil {
			return 0, 0, "", err
		}
		defer f.Close()
		var recs []harness.NormalizedRecord
		if sn, ok := norm.(harness.StreamNormalizer); ok {
			recs, err = sn.NormalizeStream(f)
		} else {
			recs, err = normalizeByLine(norm, f)
		}
		if err != nil {
			return 0, 0, "", err
		}
		return len(recs), size, "", nil
	}

	copyPath, statePath, err := flowCopyPaths(t.Slug, hName, sid)
	if err != nil {
		return 0, 0, "", err
	}
	if err := os.MkdirAll(filepath.Dir(copyPath), 0o755); err != nil {
		return 0, 0, "", err
	}
	n, err := rebuildFlowCopy(norm, hName, sid, sourcePath, copyPath, statePath, size, inode)
	return n, size, "", err
}

// cmdTranscriptPurge implements `flow transcript purge [<slug>|--all]`. It
// deletes flow's transcript copies (and their offset sidecars) for one task
// or all tasks. The harness's own transcripts are never touched.
func cmdTranscriptPurge(args []string) int {
	fs := flagSet("transcript purge")
	all := fs.Bool("all", false, "purge copies for every task")
	ref := ""
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		ref = args[0]
		args = args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if ref == "" && fs.NArg() > 0 {
		ref = fs.Arg(0)
	}
	if ref == "" && !*all {
		fmt.Fprintln(os.Stderr, "usage: flow transcript purge <slug> | --all")
		return 2
	}

	if ref != "" && !*all {
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
		t, err := resolveTaskRef(db, ref)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		n, err := purgeTranscriptsFor(t.Slug)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Printf("purged %d file(s) for %s\n", n, t.Slug)
		return 0
	}

	// --all: walk every task directory under $FLOW_ROOT/tasks.
	root, err := flowRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	entries, err := os.ReadDir(filepath.Join(root, "tasks"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	var total, dirs int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n, err := purgeTranscriptsFor(e.Name())
		if err != nil {
			fmt.Printf("  %-32s error: %v\n", e.Name(), err)
			continue
		}
		if n > 0 {
			dirs++
			total += n
		}
	}
	fmt.Printf("purged %d file(s) across %d task(s)\n", total, dirs)
	return 0
}

// purgeTranscriptsFor removes a task's transcripts/ directory and returns
// how many files it held. Missing directory → (0, nil).
func purgeTranscriptsFor(slug string) (int, error) {
	dir, err := taskTranscriptsDir(slug)
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	n := len(entries)
	if err := os.RemoveAll(dir); err != nil {
		return 0, err
	}
	return n, nil
}
