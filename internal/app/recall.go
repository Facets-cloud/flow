package app

import (
	"encoding/json"
	"flow/internal/match"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
)

// cmdRecall implements `flow recall` — the user-facing lookback: given the
// current task (or a slug, or free text, or a mid-session prompt), surface the
// handful of past tasks most likely to be relevant, each as a one-liner with a
// strong/possible confidence label. It wraps the shipped layered pipeline
// (internal/match) read-only; the only writes are the on-disk index cache and,
// for `recall train --calibrate`, the per-user confidence fit, both under
// $FLOW_ROOT/match/.
func cmdRecall(args []string) int {
	setRecallMemoryLimit()
	if len(args) > 0 {
		switch args[0] {
		case "train":
			return cmdRecallTrain(args[1:])
		case "eval":
			return cmdRecallEval(args[1:])
		case "-h", "--help", "help":
			printRecallUsage()
			return 0
		}
	}
	return cmdRecallLookup(args)
}

func printRecallUsage() {
	fmt.Println(`flow recall — related past work, from your own flow history

  flow recall                              related past tasks for the BOUND task (reverse lookup)
  flow recall <task-slug>                  ... for that task (arg exactly matches an existing slug)
  flow recall "<free text>"                free-text lookback (arg is not a slug)
  flow recall [<slug>] --prompt "<text>"   in-session: rank against a prompt, with the task brief as context
  flow recall train [--calibrate]          rebuild the index; --calibrate refits the confidence model on your links

  Flags (lookup): [--k N] [--min P] [--project X] [--include-children] [--include-self] [--all] [--text] [--json]
    --k N                show at most N matches (default 2)
    --min P              confidence cut: show a match only if p >= P (0 = plain top k)
    --project X          restrict candidates to a project slug or slug prefix
    --include-children   keep the task's own children / playbook runs (dropped by default)
    --include-self       keep the bound task itself (a bound session excludes it from every mode by default)
    --all                rank every task, not only those created before this one
    --text               force free-text mode even if the argument matches a slug
    --json               machine-readable output (slug, status, name, summary, label, confidence, score)`)
}

// recallMemoryFloor is the default soft memory limit for recall/train. The
// index build (hydrating transcripts + an LSA SVD) is the only memory-heavy
// path; a soft limit keeps a cold build on a large history from ballooning.
// Overridable by the GOMEMLIMIT environment variable, which the Go runtime
// reads at startup — if it is set we leave the runtime's value alone.
const recallMemoryFloor = 400 << 20 // 400 MiB

func setRecallMemoryLimit() {
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(recallMemoryFloor)
	}
}

// recallCalibPath is where `recall train --calibrate` persists the per-user
// confidence fit, next to the index cache.
func recallCalibPath() (string, error) {
	dir, err := recallDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "recall-calib.json"), nil
}

// loadRecallCalib returns the persisted per-user confidence model, or nil when
// none has been fit (the shipped RelatedConf default is then used).
func loadRecallCalib() *match.ConfModel {
	path, err := recallCalibPath()
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var c match.ConfModel
	if err := json.Unmarshal(b, &c); err != nil || len(c.W) == 0 {
		return nil
	}
	return &c
}

// cmdRecallLookup handles the three query shapes (bound task, slug, free text)
// plus --prompt in-session mode.
func cmdRecallLookup(args []string) int {
	pos, flagArgs := splitRelatedArgs(args)
	fs := flagSet("recall")
	k := fs.Int("k", match.RelatedCap, "show at most this many matches")
	minP := fs.Float64("min", match.RelatedMinP, "confidence cut: show a match only if p >= this (0 = plain top k)")
	all := fs.Bool("all", false, "rank every task, not only those created before this one")
	project := fs.String("project", "", "restrict candidates to a project slug or slug prefix")
	inclChildren := fs.Bool("include-children", false, "keep the task's own children / playbook runs")
	inclSelf := fs.Bool("include-self", false, "keep the bound task itself in the results (by default a bound session never recalls the task it is in)")
	prompt := fs.String("prompt", "", "in-session mode: rank against this prompt, with the task brief as context")
	forceText := fs.Bool("text", false, "force free-text mode even if the argument matches a slug")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if *k <= 0 {
		printRecallUsage()
		return 2
	}
	// A multi-word positional (several argv tokens, or one with spaces) is text
	// by definition — a slug has no spaces.
	arg := strings.Join(pos, " ")
	multiWord := len(pos) > 1 || strings.ContainsAny(arg, " \t")

	// Build the index (cold first run prints a one-line notice on stderr).
	cachePath, _ := lookbackCachePath()
	firstRun := cachePath == "" || !matchFileExists(cachePath)
	if firstRun {
		fmt.Fprintln(os.Stderr, "building index… (first run; subsequent calls are fast)")
	}
	cacheCh := make(chan *lookbackCache, 1)
	go func() { cacheCh <- loadRawCache() }()
	rows, err := matchScan()
	if err != nil {
		return fail(err)
	}
	tasks, space, model, cards, _ := hydrateCachedWith(rows, <-cacheCh)

	known := make(map[string]bool, len(tasks))
	for i := range tasks {
		known[tasks[i].Slug] = true
	}

	// The task bound to THIS session (if any). A bound session never recalls
	// the task it is sitting in, in ANY mode, unless --include-self; an unbound
	// session excludes nothing. The error is non-fatal here — unbound is fine.
	boundSlug, boundErr := boundTaskSlugRO()
	excludeBound := ""
	if !*inclSelf {
		excludeBound = boundSlug
	}

	// Decide the mode.
	promptMode := *prompt != ""
	freeText := false
	slug := ""
	switch {
	case promptMode:
		// context task: the positional slug, else the bound task.
		if arg != "" && !multiWord && known[arg] {
			slug = arg
		} else if arg != "" {
			fmt.Fprintf(os.Stderr, "error: --prompt takes a known task slug or nothing (got %q)\n", arg)
			return 2
		} else if boundSlug == "" {
			fmt.Fprintf(os.Stderr, "error: --prompt needs a bound task or a slug argument, and %v\n", boundErr)
			return 2
		} else {
			slug = boundSlug
		}
	case arg != "":
		if !multiWord && !*forceText && known[arg] {
			slug = arg // exact slug match → task mode
		} else {
			freeText = true // not a slug (or forced) → free text
		}
	default:
		if boundSlug == "" {
			fmt.Fprintf(os.Stderr, "error: no task slug given and %v\n", boundErr)
			return 2
		}
		slug = boundSlug
	}

	calib := loadRecallCalib()
	var rel match.RelatedResult
	switch {
	case promptMode:
		// Prompt mode keeps its own RelatedPromptConf; the per-user calib is a
		// brief-fit model, so it is not applied here.
		rel = match.RelatedQuery(tasks, match.RelatedQueryOptions{
			Prompt: *prompt, Self: slug, ContextWeight: match.RelatedContextWeight,
			K: *k, MinP: *minP, ExcludeChildren: !*inclChildren, ExcludeBound: excludeBound, Project: *project,
			Space: space, Model: model, Cards: cards})
	case freeText:
		rel = match.RelatedQuery(tasks, match.RelatedQueryOptions{
			Prompt: arg, K: *k, MinP: *minP, ExcludeBound: excludeBound, Project: *project,
			Space: space, Model: model, Cards: cards, Conf: calib})
	default:
		rel = match.RelatedWith(tasks, slug, match.RelatedOptions{
			K: *k, MinP: *minP, PastOnly: !*all, ExcludeChildren: !*inclChildren, ExcludeBound: excludeBound, Project: *project,
			Space: space, Model: model, Cards: cards, Conf: calib})
	}
	return printRecallResult(rel, slug, *asJSON)
}

// recallMatch is one match in the stable JSON schema.
type recallMatch struct {
	Slug       string  `json:"slug"`
	Status     string  `json:"status"`
	Name       string  `json:"name"`
	Summary    string  `json:"summary"`
	Label      string  `json:"label"`
	Confidence float64 `json:"confidence"`
	Score      float64 `json:"score"`
}

// recallJSON is the stable machine-readable schema for `flow recall --json`.
type recallJSON struct {
	Task       string        `json:"task,omitempty"`
	Mode       string        `json:"mode"`
	Scorer     string        `json:"scorer"`
	Candidates int           `json:"candidates"`
	MinP       float64       `json:"min_p"`
	Project    string        `json:"project,omitempty"`
	Abstain    bool          `json:"abstain,omitempty"`
	Matches    []recallMatch `json:"matches"`
}

func printRecallResult(rel match.RelatedResult, slug string, asJSON bool) int {
	if asJSON {
		out := recallJSON{Task: rel.Task, Mode: rel.Mode, Scorer: rel.Scorer,
			Candidates: rel.Candidates, MinP: rel.MinP, Project: rel.Project,
			Abstain: rel.Abstain, Matches: []recallMatch{}}
		for _, h := range rel.Top {
			out.Matches = append(out.Matches, recallMatch{
				Slug: h.Slug, Status: h.Status, Name: h.Name, Summary: h.Summary,
				Label: h.Label, Confidence: h.P, Score: h.Score})
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return 0
	}
	var head string
	switch rel.Mode {
	case "query":
		head = "related to query"
	case "prompt":
		head = fmt.Sprintf("related to prompt (in %s)", slug)
	default:
		head = "related to " + slug
	}
	scope := fmt.Sprintf("%d candidates", rel.Candidates)
	if rel.Project != "" {
		scope += ", project=" + rel.Project
	}
	fmt.Printf("%s (%s over %s, confidence ≥ %.2f):\n", head, rel.Scorer, scope, rel.MinP)
	if rel.Abstain {
		fmt.Println("  no confident matches (prompt too generic — needs a distinctive term or an identifier)")
		return 0
	}
	if len(rel.Top) == 0 {
		fmt.Println("  no confident matches")
	}
	for i, c := range rel.Top {
		fmt.Printf("  %d. [%s] %-8s %s — %s\n", i+1, c.Label, c.Status, c.Slug, c.Name)
		if c.Summary != "" {
			fmt.Printf("        %s\n", c.Summary)
		}
	}
	return 0
}

// cmdRecallTrain rebuilds the index from scratch. With --calibrate it also
// refits the per-user confidence model on the user's own links and persists
// it; without enough history it leaves the shipped default in place.
func cmdRecallTrain(args []string) int {
	fs := flagSet("recall train")
	calibrate := fs.Bool("calibrate", false, "also refit the confidence model on your own links")
	seed := fs.Uint64("seed", match.DefaultSeed, "tie-break seed for calibration ranking")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rows, err := matchScan()
	if err != nil {
		return fail(err)
	}
	fmt.Fprintln(os.Stderr, "building index…")
	tasks := hydrateAll(rows)
	space := match.FitRelatedSpace(tasks)
	if err := writeCache(&lookbackCache{Tasks: slimAll(tasks), Space: space,
		Model: match.BuildRelatedIndex(tasks), Cards: match.BuildRelatedCards(tasks), BasisN: len(tasks)}); err != nil {
		return fail(err)
	}
	fmt.Printf("index built: %d tasks\n", len(tasks))

	if *calibrate {
		conf, n, ok := match.CalibrateRelated(tasks, *seed)
		if !ok {
			fmt.Fprintf(os.Stderr,
				"calibration skipped: only %d queries have strong truth (mention/parent/family/playbook); need at least %d. Using the shipped default coefficients.\n",
				n, match.MinCalibQueries)
			return 0
		}
		path, err := recallCalibPath()
		if err != nil {
			return fail(err)
		}
		b, _ := json.MarshalIndent(conf, "", "  ")
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			return fail(err)
		}
		fmt.Printf("confidence model recalibrated on %d queries: %s\n", n, path)
	}
	return 0
}

// cmdRecallEval runs the held-out lookback evaluation on the user's own
// history. Dev-only (kept out of the main usage): it is how the pipeline was
// measured and how `train --calibrate` is validated.
func cmdRecallEval(args []string) int {
	fs := flagSet("recall eval")
	asJSON := fs.Bool("json", false, "machine-readable report")
	seed := fs.Uint64("seed", match.DefaultSeed, "negative-sampling seed")
	exclude := fs.String("exclude", "", "comma-separated slug globs to drop from train+eval")
	boot := fs.Int("boot", 1000, "bootstrap resamples for the 95% CIs")
	nex := fs.Int("examples", 5, "example queries to list")
	jsonOut := fs.String("json-out", "", "also write the JSON report to this file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rows, err := matchScan()
	if err != nil {
		return fail(err)
	}
	start := time.Now()
	tasks := hydrateAll(rows)
	tasks, excluded := excludeTasksSplit(tasks, append(append([]string(nil), lookbackExcludes...), splitGlobs(*exclude)...))
	rep := match.RunLookback(tasks, match.LookbackOptions{Weights: match.DefaultWeights, Seed: *seed,
		PromptsN: 3, Boot: *boot, Examples: *nex, StripSlugs: excluded})
	b, _ := json.MarshalIndent(rep, "", "  ")
	if *jsonOut != "" {
		if err := os.WriteFile(*jsonOut, append(b, '\n'), 0o644); err != nil {
			return fail(err)
		}
	}
	if *asJSON {
		fmt.Println(string(b))
		return 0
	}
	match.WriteLookbackReport(os.Stdout, rep)
	fmt.Printf("\n(eval took %s)\n", time.Since(start).Round(time.Millisecond))
	return 0
}

// splitRelatedArgs separates positional arguments from flags, handling the
// `--flag value` form (flag values are not positionals). Boolean flags take no
// value. Mirrors the hand-split used across the match CLI so a free-text
// positional can sit before or after flags.
func splitRelatedArgs(args []string) (pos, flagArgs []string) {
	boolFlag := map[string]bool{"json": true, "all": true, "include-children": true, "include-self": true, "text": true, "calibrate": true}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			flagArgs = append(flagArgs, a)
			bare := strings.TrimLeft(a, "-")
			if !strings.Contains(a, "=") && !boolFlag[bare] && i+1 < len(args) {
				flagArgs = append(flagArgs, args[i+1])
				i++
			}
			continue
		}
		pos = append(pos, a)
	}
	return pos, flagArgs
}
