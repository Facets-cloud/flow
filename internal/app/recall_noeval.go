//go:build !recalleval

package app

import (
	"fmt"
	"os"
)

// cmdRecallEval is a stub in the default build: the lookback eval / benchmark
// harness and its report ship only when the binary is built with
// `-tags recalleval`, so the shipped `flow` stays lean (just the recall path).
// `flow recall train --calibrate` still works in the default binary — only the
// held-out eval report is gated.
func cmdRecallEval(args []string) int {
	fmt.Fprintln(os.Stderr, "flow recall eval is not built into this binary.")
	fmt.Fprintln(os.Stderr, "rebuild with the eval harness:  go build -tags recalleval -o flow-eval .")
	fmt.Fprintln(os.Stderr, "then:  ./flow-eval recall eval")
	return 2
}
