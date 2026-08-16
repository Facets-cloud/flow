package app

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"flow/internal/harness"
)

// embeddedSkillFS holds the entire skill directory — SKILL.md (the lean
// resident core loaded by the Skill tool) plus references/*.md (rarely-
// needed workflows loaded on demand). Embedding the whole tree lets the
// installer lay the reference files down alongside SKILL.md so the core
// can point at them.
//
//go:embed skill
var embeddedSkillFS embed.FS

// skillFiles returns the embedded skill tree rooted at the skill/
// directory, so walking it yields "SKILL.md" and "references/<x>.md"
// (not "skill/SKILL.md"). Passed to Harness.InstallSkill.
func skillFiles() fs.FS {
	sub, err := fs.Sub(embeddedSkillFS, "skill")
	if err != nil {
		// The embed directive guarantees skill/ exists; this is
		// unreachable in a correctly-built binary.
		return embeddedSkillFS
	}
	return sub
}

// hookCommand is the exact string the harness's settings.json
// (settings.json / hooks.json depending on harness) records as the
// SessionStart hook handler. Stable — changing this string would
// orphan existing installations.
const hookCommand = "flow hook session-start"

// userPromptSubmitHookCommand is the exact string settings.json records
// as the UserPromptSubmit hook handler. In bound sessions the command
// injects a tiny drift/close-out anchor; unbound sessions no-op. Stable
// — changing it would orphan existing installations.
const userPromptSubmitHookCommand = "flow hook user-prompt-submit"

// readSkillVersion returns the version string recorded in the
// harness's skill-version sidecar, or "" if missing/unreadable.
func readSkillVersion() string {
	sk := defaultHarness().Skills()
	if sk == nil {
		return ""
	}
	p, err := sk.SkillVersionPath()
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
	return writeSkillVersionFor(defaultHarness(), v)
}

// writeSkillVersionFor records the version for one specific harness.
// Each harness owns its own sidecar, so a stale install of one does not
// suppress the auto-upgrade of another.
func writeSkillVersionFor(h harness.Harness, v string) error {
	sk := h.Skills()
	if sk == nil {
		return fmt.Errorf("harness %s has no skill install location", h.Name())
	}
	p, err := sk.SkillVersionPath()
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
	sk := h.Skills()
	if sk == nil {
		// Nothing to upgrade — this harness has no skill tree.
		return
	}
	skillPath, err := sk.SkillInstallPath()
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
	// Version mismatch — refresh skill bytes and the hooks.
	if err := sk.InstallSkill(skillFiles()); err != nil {
		return
	}
	_ = writeSkillVersion(Version)
	if hk := h.Hooks(); hk != nil {
		_, _ = hk.InstallSessionStartHook(hookCommand)
		_, _ = hk.InstallUserPromptSubmitHook(userPromptSubmitHookCommand)
	}
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

// harnessTargets resolves which harnesses a skill command applies to.
//
// The default is EVERY registered harness, not the ambient one. A user
// who has both claude and a manifest-defined harness installed wants
// one `flow skill install` to wire both — discovering months later that
// only one was set up is the failure this avoids.
func harnessTargets(only string) ([]harness.Harness, error) {
	if only != "" {
		h, err := harnessByName(only)
		if err != nil {
			return nil, err
		}
		return []harness.Harness{h}, nil
	}
	return allHarnesses(), nil
}

func skillInstall(args []string, forceDefault bool) int {
	fs := flagSet("skill install")
	force := fs.Bool("force", forceDefault, "overwrite an existing installation")
	skipHook := fs.Bool("skip-hook", false, "don't auto-install the SessionStart hook")
	only := fs.String("harness", "", "install for `<name>` only (default: every registered harness)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	targets, err := harnessTargets(*only)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// Harnesses legitimately SHARE a skill directory: praxis natively
	// scans ~/.claude/skills, so its manifest points at claude's tree.
	// Tracking what this run already wrote lets the second harness
	// recognize flow's own output instead of tripping the --force guard
	// meant to protect a user's pre-existing file.
	written := map[string]string{}

	var failed int
	for _, h := range targets {
		if installOne(h, *force, *skipHook, len(targets) > 1, written) != 0 {
			failed++
		}
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// installOne wires the skill and hooks for a single harness.
//
// multi says whether output should be prefixed with the harness name;
// with one target the prefix is noise, with several it is the only way
// to tell whose message you are reading.
func installOne(h harness.Harness, force, skipHook, multi bool, written map[string]string) int {
	p := ""
	if multi {
		p = string(h.Name()) + ": "
	}
	sk := h.Skills()
	if sk == nil {
		// Sweeping every harness: a missing capability is a fact to
		// report, not a failure. Explicitly targeted: the user asked
		// for something impossible and deserves a non-zero exit.
		if multi {
			fmt.Printf("%sno skill install location — skipped\n", p)
			return 0
		}
		fmt.Fprintf(os.Stderr, "error: harness %s has no skill install location\n", h.Name())
		return 1
	}
	dest, err := sk.SkillInstallPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s%v\n", p, err)
		return 1
	}
	if owner, shared := written[dest]; shared {
		// Same tree, already written by an earlier harness in this run.
		// Hooks still get wired below — they live in per-harness config
		// files even when the skill directory is shared.
		fmt.Printf("%sshares its skill tree with %s at %s — already installed\n", p, owner, dest)
	} else {
		if _, err := os.Stat(dest); err == nil && !force {
			fmt.Fprintf(os.Stderr, "error: %s%s already exists; use --force to overwrite\n", p, dest)
			return 1
		} else if err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "error: %sstat %s: %v\n", p, dest, err)
			return 1
		}
		if err := sk.InstallSkill(skillFiles()); err != nil {
			fmt.Fprintf(os.Stderr, "error: %s%v\n", p, err)
			return 1
		}
		if err := writeSkillVersionFor(h, Version); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %scould not record skill version: %v\n", p, err)
		}
		written[dest] = string(h.Name())
		fmt.Printf("%sinstalled flow skill to %s\n", p, dest)
	}

	if skipHook {
		fmt.Printf("%s--skip-hook: leaving harness settings alone\n", p)
		return 0
	}
	hk := h.Hooks()
	if hk == nil {
		// The skill is installed and usable; this harness simply has
		// no way to receive flow's lifecycle context.
		fmt.Printf("%sno hook mechanism — skill installed without hooks\n", p)
		return 0
	}
	// Only config-patch writes hook entries. The other strategies are
	// already in place by now — instruction-directive rode in with the
	// skill block above, prompt-prelude applies at launch — so reporting
	// them as "already installed" would be true but useless, and
	// reporting them as newly added would be false.
	if !slices.Contains(hk.Strategies(), harness.StrategyConfigPatch) {
		fmt.Printf("%shooks delivered by: %s\n", p, strings.Join(hk.Strategies(), ", "))
		return 0
	}
	if added, err := hk.InstallSessionStartHook(hookCommand); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %scould not install SessionStart hook: %v\n", p, err)
		// Non-fatal: the skill is still usable without the hook; the
		// user can wire it manually. Return 0 so `flow init` doesn't
		// fail on a settings quirk.
		return 0
	} else if added {
		fmt.Printf("%sinstalled SessionStart hook (fires on startup + resume)\n", p)
	} else {
		fmt.Printf("%sSessionStart hook already installed — leaving as is\n", p)
	}
	if added, err := hk.InstallUserPromptSubmitHook(userPromptSubmitHookCommand); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %scould not install UserPromptSubmit hook: %v\n", p, err)
		return 0
	} else if added {
		fmt.Printf("%sinstalled UserPromptSubmit hook (nudges drift/close-out on bound-session prompts)\n", p)
	} else {
		fmt.Printf("%sUserPromptSubmit hook already installed — leaving as is\n", p)
	}
	return 0
}

func skillUninstall(args []string) int {
	fs := flagSet("skill uninstall")
	keepHook := fs.Bool("keep-hook", false, "don't remove the SessionStart hook")
	only := fs.String("harness", "", "uninstall for `<name>` only (default: every registered harness)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	targets, err := harnessTargets(*only)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	rc := 0
	for _, h := range targets {
		if n := uninstallOne(h, *keepHook, len(targets) > 1); n != 0 {
			rc = n
		}
	}
	return rc
}

func uninstallOne(h harness.Harness, keepHook, multi bool) int {
	p := ""
	if multi {
		p = string(h.Name()) + ": "
	}
	sk := h.Skills()
	if sk == nil {
		fmt.Printf("%sno skill install location — nothing to do\n", p)
		return 0
	}
	dest, err := sk.SkillInstallPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s%v\n", p, err)
		return 1
	}
	skillDir := filepath.Dir(dest)
	_, statErr := os.Stat(skillDir)
	wasAbsent := os.IsNotExist(statErr)
	// Always call the idempotent uninstaller: pointer-discovery harnesses
	// may still own a managed instructions block even when their skill
	// directory was removed independently.
	if err := sk.UninstallSkill(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s%v\n", p, err)
		return 1
	}
	switch {
	case wasAbsent:
		fmt.Printf("%sunregistered flow skill from %s (skill tree already absent)\n", p, skillDir)
	case sk.OwnsSkillDir():
		fmt.Printf("%suninstalled flow skill from %s\n", p, skillDir)
	default:
		// Saying "uninstalled" here would be a false claim: the
		// directory belongs to another harness and still exists.
		fmt.Printf("%sunregistered from %s (left in place — another harness owns it)\n", p, skillDir)
	}

	if keepHook {
		fmt.Printf("%s--keep-hook: leaving SessionStart hook in place\n", p)
		return 0
	}
	hk := h.Hooks()
	if hk == nil {
		return 0
	}
	if removed, err := hk.UninstallSessionStartHook(hookCommand); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %scould not remove SessionStart hook: %v\n", p, err)
		return 0
	} else if removed {
		fmt.Printf("%sremoved SessionStart hook\n", p)
	}
	if removed, err := hk.UninstallUserPromptSubmitHook(userPromptSubmitHookCommand); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %scould not remove UserPromptSubmit hook: %v\n", p, err)
		return 0
	} else if removed {
		fmt.Printf("%sremoved UserPromptSubmit hook\n", p)
	}
	return 0
}
