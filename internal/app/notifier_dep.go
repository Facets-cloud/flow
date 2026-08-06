package app

import (
	"flow/internal/notify"
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// brewInstallRunner runs `brew install <formula>`, streaming brew's
// output to the user's terminal so a multi-second install isn't a silent
// hang. Overridable for tests, which must never shell out to brew.
var brewInstallRunner = func(formula string) error {
	cmd := exec.Command("brew", "install", formula)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// lookPathRunner resolves a binary on $PATH. Overridable for tests.
var lookPathRunner = exec.LookPath

// ensureNotifierInstalled installs terminal-notifier when it's missing
// and Homebrew is available.
//
// Why flow installs it rather than just asking: notification banners are
// only genuinely useful if clicking one jumps to the tab that's asking,
// and that click action requires a signed .app bundle with its own
// bundle identifier. osascript's `display notification` cannot carry
// one. terminal-notifier is the smallest dependency that provides it.
//
// This is a SOFT dependency in every direction:
//   - No brew, no install (flow does not vendor a package manager).
//   - Not macOS, no install (this whole feature is macOS-only).
//   - A brew failure warns and returns; it never fails the caller.
//   - At notification time, internal/notify re-checks and degrades to a
//     non-clickable osascript banner if the binary still isn't there.
//
// Called from `flow skill install` (which `flow init` and `make install`
// both run), so it covers the source-build and release-binary install
// paths alike, and re-checks on every `flow skill update` after an
// upgrade. Only ever acts when the binary is genuinely absent, so it's a
// one-time cost rather than per-upgrade work.
func ensureNotifierInstalled() {
	if runtime.GOOS != "darwin" {
		return
	}
	if notify.Available() {
		return
	}
	if _, err := lookPathRunner("brew"); err != nil {
		fmt.Fprintln(os.Stderr,
			"note: terminal-notifier is not installed, so notification banners will not be clickable.")
		fmt.Fprintln(os.Stderr,
			"      Install it to enable click-to-focus: brew install terminal-notifier")
		return
	}

	fmt.Fprintln(os.Stderr, "installing terminal-notifier (enables click-to-focus notification banners)...")
	if err := brewInstallRunner("terminal-notifier"); err != nil {
		fmt.Fprintf(os.Stderr,
			"warning: could not install terminal-notifier: %v\n", err)
		fmt.Fprintln(os.Stderr,
			"         Banners will still appear, but will not be clickable.")
		fmt.Fprintln(os.Stderr,
			"         To retry: brew install terminal-notifier")
		return
	}
	fmt.Fprintln(os.Stderr, "installed terminal-notifier")
}
