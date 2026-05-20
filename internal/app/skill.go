package app

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed skill/SKILL.md
var embeddedSkill []byte

// hookCommand is the exact string the harness's settings.json
// (settings.json / hooks.json depending on harness) records as the
// SessionStart hook handler. Stable — changing this string would
// orphan existing installations.
const hookCommand = "flow hook session-start"

// userPromptSubmitHookCommand is the legacy UserPromptSubmit hook
// string. flow no longer installs this hook (removed in
// v0.1.0-alpha.7), but every install/upgrade actively uninstalls
// stale entries so existing-user setups converge to a clean state.
const userPromptSubmitHookCommand = "flow hook user-prompt-submit"

// readSkillVersion returns the version string recorded in the
// harness's skill-version sidecar, or "" if missing/unreadable.
func readSkillVersion() string {
	p, err := defaultHarness().SkillVersionPath()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// writeSkillVersion records `v` as the version of the binary that
// installed the current skill content. Errors are non-fatal —
// failing to write the sidecar should never block a successful
// skill install.
func writeSkillVersion(v string) error {
	p, err := defaultHarness().SkillVersionPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(v+"\n"), 0o644)
}

// maybeAutoUpgradeSkill checks the recorded skill version against the
// running binary's version and, if they differ, refreshes the skill +
// SessionStart hook. Designed to run on every flow invocation so the
// user gets a self-healing upgrade flow after replacing the binary.
//
// The check is intentionally conservative — it does nothing when:
//   - The binary is a "dev" build (Version == "dev"). Local devs use
//     `make install` and shouldn't fight an auto-installer.
//   - The skill isn't installed at all (sentinel: SKILL.md missing).
//     Treat this as an explicit user opt-out; never re-install.
//   - The recorded version already matches Version. The common path.
//
// All errors are silent — auto-upgrade is best-effort plumbing, not a
// command. A user-visible failure here would be far more annoying
// than the eventual symptom of a stale skill.
func maybeAutoUpgradeSkill() {
	if Version == "" || Version == "dev" {
		return
	}
	h := defaultHarness()
	skillPath, err := h.SkillInstallPath()
	if err != nil {
		return
	}
	if _, err := os.Stat(skillPath); err != nil {
		// Not installed → user opted out; don't reinstall behind their back.
		return
	}
	if readSkillVersion() == Version {
		return
	}
	// Version mismatch — refresh skill bytes and the SessionStart hook.
	if err := h.InstallSkill(embeddedSkill); err != nil {
		return
	}
	_ = writeSkillVersion(Version)
	_, _ = h.InstallSessionStartHook(hookCommand)
	// UserPromptSubmit hook was removed in v0.1.0-alpha.7 — the
	// per-prompt token cost wasn't worth the marginal value. Actively
	// uninstall any stale entry left behind by older binaries.
	_, _ = h.UninstallUserPromptSubmitHook(userPromptSubmitHookCommand)
	fmt.Fprintf(os.Stderr, "flow: upgraded skill to %s\n", Version)
}

// cmdSkill dispatches `flow skill install|uninstall|update`.
func cmdSkill(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: skill requires a subcommand (install|uninstall|update)")
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "install":
		return skillInstall(rest, false)
	case "update":
		return skillInstall(rest, true)
	case "uninstall":
		return skillUninstall(rest)
	default:
		fmt.Fprintf(os.Stderr, "error: unknown skill subcommand %q\n", sub)
		return 2
	}
}

func skillInstall(args []string, forceDefault bool) int {
	fs := flagSet("skill install")
	force := fs.Bool("force", forceDefault, "overwrite an existing installation")
	skipHook := fs.Bool("skip-hook", false, "don't auto-install the SessionStart hook")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	h := defaultHarness()
	dest, err := h.SkillInstallPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if _, err := os.Stat(dest); err == nil && !*force {
		fmt.Fprintf(os.Stderr, "error: %s already exists; use --force to overwrite\n", dest)
		return 1
	} else if err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "error: stat %s: %v\n", dest, err)
		return 1
	}
	if err := h.InstallSkill(embeddedSkill); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if err := writeSkillVersion(Version); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not record skill version: %v\n", err)
	}
	fmt.Printf("installed flow skill to %s\n", dest)

	if *skipHook {
		fmt.Println("--skip-hook: leaving harness settings alone")
		return 0
	}
	if added, err := h.InstallSessionStartHook(hookCommand); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not install SessionStart hook: %v\n", err)
		// Non-fatal: the skill is still usable without the hook; the
		// user can wire it manually. Return 0 so `flow init` doesn't
		// fail on a settings quirk.
		return 0
	} else if added {
		fmt.Printf("installed SessionStart hook (fires on startup + resume)\n")
	} else {
		fmt.Println("SessionStart hook already installed — leaving as is")
	}
	// UserPromptSubmit hook was removed in v0.1.0-alpha.7. Actively
	// uninstall any stale entry left behind by older binaries so a
	// fresh `flow skill install` (or `update`) leaves a clean config.
	if removed, err := h.UninstallUserPromptSubmitHook(userPromptSubmitHookCommand); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not remove stale UserPromptSubmit hook: %v\n", err)
		return 0
	} else if removed {
		fmt.Println("removed stale UserPromptSubmit hook (no longer used)")
	}
	return 0
}

func skillUninstall(args []string) int {
	fs := flagSet("skill uninstall")
	keepHook := fs.Bool("keep-hook", false, "don't remove the SessionStart hook")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	h := defaultHarness()
	dest, err := h.SkillInstallPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	skillDir := filepath.Dir(dest)
	if _, err := os.Stat(skillDir); os.IsNotExist(err) {
		fmt.Printf("flow skill not installed at %s — nothing to do\n", skillDir)
	} else {
		if err := h.UninstallSkill(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Printf("uninstalled flow skill from %s\n", skillDir)
	}

	if *keepHook {
		fmt.Println("--keep-hook: leaving SessionStart hook in place")
		return 0
	}
	if removed, err := h.UninstallSessionStartHook(hookCommand); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not remove SessionStart hook: %v\n", err)
		return 0
	} else if removed {
		fmt.Println("removed SessionStart hook")
	}
	if removed, err := h.UninstallUserPromptSubmitHook(userPromptSubmitHookCommand); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not remove UserPromptSubmit hook: %v\n", err)
		return 0
	} else if removed {
		fmt.Println("removed UserPromptSubmit hook")
	}
	return 0
}
