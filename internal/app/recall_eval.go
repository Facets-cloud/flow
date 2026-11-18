//go:build recalleval

package app

import (
	"encoding/json"
	"flow/internal/match"
	"fmt"
	"os"
	"time"
)

// cmdRecallEval runs the held-out lookback evaluation on the user's own
// history. Dev-only: it is how the pipeline was measured and how `train
// --calibrate` is validated. It ships only in a binary built with
// `-tags recalleval` so the default `flow` carries just the recall path.
//
//	go build -tags recalleval -o flow-eval .
//	./flow-eval recall eval
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
