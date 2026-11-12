package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"flow/internal/flowdb"
)

// cmdAuto is `flow auto "<name>" --brief <file|->`: the one-shot a driver
// session uses to hand a deterministic subtask to a headless child.
//
//	flow auto "<name>" --brief b.md
//	  ├─ refuse if called from inside an auto run (no grandchildren)
//	  ├─ refuse if the brief is missing or blank (an empty auto is useless)
//	  ├─ flow add task      (parent + project inheritance + auto-watch)
//	  ├─ write brief.md     (replaces the stub)
//	  └─ flow do --auto     (detached headless run)
func cmdAuto(args []string) int {
	name, flagArgs, missing := splitPositional(args)
	fs := flagSet("auto")
	slugFlag := fs.String("slug", "", "short slug (default: auto-generated from name)")
	briefFlag := fs.String("brief", "", "brief file for the child (required; - reads stdin)")
	priority := fs.String("priority", "medium", "high|medium|low")
	workDir := fs.String("work-dir", "", "work directory (default: inherited like flow add task)")
	mkdir := fs.Bool("mkdir", false, "same as flow add task --mkdir")
	harnessName := fs.String("harness", "", "harness for the child session (one of: "+registeredHarnessNames()+")")
	if err := fs.Parse(flagArgs); err != nil {
		return flagParseRC(err)
	}
	if missing {
		fmt.Fprintln(os.Stderr, "error: auto requires a task name: flow auto \"<name>\" --brief <file|->")
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "error: unexpected argument %q (quote the task name)\n", fs.Arg(0))
		return 2
	}
	if rc := refuseInCodexSandbox("flow auto"); rc != 0 {
		return rc
	}

	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := flowdb.OpenDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	bound, _ := currentSessionTask(db)
	if isAutoRunSession(bound) {
		fmt.Fprintln(os.Stderr, "error: flow auto refused inside an auto run: an auto child may not spawn a grandchild. "+
			"Do the work yourself, or report that it needs splitting so the driver decides.")
		return 1
	}

	if *briefFlag == "" {
		fmt.Fprintln(os.Stderr, "error: auto requires --brief <file> (or --brief - for stdin): an auto child with an empty brief has nothing to do")
		return 2
	}
	brief, err := readAutoBrief(*briefFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: read brief: %v\n", err)
		return 1
	}
	if strings.TrimSpace(brief) == "" {
		fmt.Fprintln(os.Stderr, "error: brief is empty: write the goal, Done when, and gates for the child")
		return 2
	}
	if !isValidPriority(*priority) {
		fmt.Fprintf(os.Stderr, "error: priority must be high|medium|low, got %q\n", *priority)
		return 2
	}

	slug := *slugFlag
	if slug == "" {
		base, err := Slugify(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 2
		}
		if slug, err = uniqueSlug(db, "tasks", base); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
	} else if _, err := flowdb.GetTask(db, slug); err == nil {
		fmt.Fprintf(os.Stderr, "error: task %q already exists; pick another --slug\n", slug)
		return 1
	}
	// addTask and cmdDo open their own handles.
	db.Close()

	addArgs := []string{name, "--slug", slug, "--priority", *priority}
	if *workDir != "" {
		addArgs = append(addArgs, "--work-dir", *workDir)
	}
	if *mkdir {
		addArgs = append(addArgs, "--mkdir")
	}
	if rc := addTask(addArgs); rc != 0 {
		return rc
	}

	root, err := flowRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	briefPath := filepath.Join(root, "tasks", slug, "brief.md")
	if err := os.WriteFile(briefPath, []byte(brief), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "error: write brief: %v\n", err)
		return 1
	}

	doArgs := []string{slug, "--auto"}
	if *harnessName != "" {
		doArgs = append(doArgs, "--harness", *harnessName)
	}
	if rc := cmdDo(doArgs); rc != 0 {
		fmt.Fprintf(os.Stderr, "error: task %q was created but its auto run did not launch; retry with: flow do --auto %s\n", slug, slug)
		return rc
	}
	fmt.Printf("spawning auto child: %s (brief: %s)\n", slug, briefPath)
	return 0
}

// readAutoBrief returns the brief text from a file path, or stdin for "-".
func readAutoBrief(src string) (string, error) {
	if src == "-" {
		b, err := io.ReadAll(os.Stdin)
		return string(b), err
	}
	b, err := os.ReadFile(src)
	return string(b), err
}
