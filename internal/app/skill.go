package app

import (
	"embed"
	"flow/internal/harness"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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

// readSkillVersion returns the version recorded for the ambient/default
// harness. Kept for callers that only care about "this session's"
// installation; anything that mutates installations must go per-harness
// via readSkillVersionFor.
func readSkillVersion() string {
	return readSkillVersionFor(defaultHarness())
}

// readSkillVersionFor returns the version string recorded in h's
// skill-version sidecar, or "" if missing/unreadable.
func readSkillVersionFor(h harness.Harness) string {
	p, err := h.SkillVersionPath()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// writeSkillVersionFor records `v` for h. Every install path writes a
// sidecar adjacent to the skill it actually installed rather than
// resolving the ambient/default harness again. Errors are non-fatal —
// failing to write the sidecar should never block a successful install.
func writeSkillVersionFor(h harness.Harness, v string) error {
	p, err := h.SkillVersionPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(v+"\n"), 0o644)
}

// maybeAutoUpgradeSkill checks each installed skill's recorded version
// against the running binary's version and, where they differ, refreshes
// that harness's skill bytes + hooks. Designed to run on every flow
// invocation so the user gets a self-healing upgrade after replacing the
// binary.
//
// It sweeps EVERY registered harness, not just the ambient one: `flow
// init` installs for all of them, and an installation that only upgrades
// when you happen to run flow from inside that particular agent would
// silently rot forever.
//
// The check is intentionally conservative — per harness it does nothing
// when:
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
	for _, h := range allHarnesses() {
		skillPath, err := h.SkillInstallPath()
		if err != nil {
			continue
		}
		if _, err := os.Stat(skillPath); err != nil {
			// Not installed → user opted out; don't reinstall behind
			// their back.
			continue
		}
		if readSkillVersionFor(h) == Version {
			continue
		}
		// Version mismatch — refresh skill bytes and both hooks.
		if err := h.InstallSkill(skillFiles()); err != nil {
			continue
		}
		_ = writeSkillVersionFor(h, Version)
		_, _ = h.InstallSessionStartHook(hookCommand)
		_, _ = h.InstallUserPromptSubmitHook(userPromptSubmitHookCommand)
		fmt.Fprintf(os.Stderr, "flow: upgraded %s skill to %s\n", h.Name(), Version)
	}
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

	// Install for every registered harness, matching `flow init`. Doing
	// only the ambient one would leave the other installations to rot at
	// whatever version first wrote them, since `flow skill update` is
	// normally run from an ordinary terminal.
	harnesses := allHarnesses()
	dests := make([]string, len(harnesses))
	var existing []string
	for i, h := range harnesses {
		dest, err := h.SkillInstallPath()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %s: %v\n", h.Name(), err)
			return 1
		}
		dests[i] = dest
		if _, err := os.Stat(dest); err == nil {
			existing = append(existing, dest)
		} else if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "error: stat %s: %v\n", dest, err)
			return 1
		}
	}
	// Refuse before touching anything, so a partial install can't happen.
	if len(existing) > 0 && !*force {
		fmt.Fprintf(os.Stderr, "error: %s already exists; use --force to overwrite\n",
			strings.Join(existing, ", "))
		return 1
	}

	for i, h := range harnesses {
		if err := h.InstallSkill(skillFiles()); err != nil {
			fmt.Fprintf(os.Stderr, "error: install %s skill: %v\n", h.Name(), err)
			return 1
		}
		if err := writeSkillVersionFor(h, Version); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not record %s skill version: %v\n", h.Name(), err)
		}
		fmt.Printf("installed flow skill for %s to %s\n", h.Name(), dests[i])
	}

	if *skipHook {
		fmt.Println("--skip-hook: leaving harness settings alone")
		return 0
	}
	// Hook failures are non-fatal: the skill is still usable without
	// them and the user can wire them manually. Report and keep going so
	// one harness's settings quirk can't block the others.
	for _, h := range harnesses {
		if added, err := h.InstallSessionStartHook(hookCommand); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not install %s SessionStart hook: %v\n", h.Name(), err)
		} else if added {
			fmt.Printf("installed %s SessionStart hook (fires on startup + resume)\n", h.Name())
		} else {
			fmt.Printf("%s SessionStart hook already installed — leaving as is\n", h.Name())
		}
		if added, err := h.InstallUserPromptSubmitHook(userPromptSubmitHookCommand); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not install %s UserPromptSubmit hook: %v\n", h.Name(), err)
		} else if added {
			fmt.Printf("installed %s UserPromptSubmit hook (nudges drift/close-out on bound-session prompts)\n", h.Name())
		} else {
			fmt.Printf("%s UserPromptSubmit hook already installed — leaving as is\n", h.Name())
		}
	}
	return 0
}

func skillUninstall(args []string) int {
	fs := flagSet("skill uninstall")
	keepHook := fs.Bool("keep-hook", false, "don't remove the SessionStart hook")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	// Uninstall must be the exact inverse of install/init, which cover
	// every registered harness — otherwise `flow skill uninstall` leaves
	// another agent's skill dir and, worse, a live `flow hook` entry in
	// its settings.json.
	for _, h := range allHarnesses() {
		dest, err := h.SkillInstallPath()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %s: %v\n", h.Name(), err)
			return 1
		}
		skillDir := filepath.Dir(dest)
		if _, err := os.Stat(skillDir); os.IsNotExist(err) {
			fmt.Printf("flow skill not installed for %s at %s — nothing to do\n", h.Name(), skillDir)
		} else {
			if err := h.UninstallSkill(); err != nil {
				fmt.Fprintf(os.Stderr, "error: uninstall %s skill: %v\n", h.Name(), err)
				return 1
			}
			fmt.Printf("uninstalled flow skill for %s from %s\n", h.Name(), skillDir)
		}

		if *keepHook {
			continue
		}
		if removed, err := h.UninstallSessionStartHook(hookCommand); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not remove %s SessionStart hook: %v\n", h.Name(), err)
		} else if removed {
			fmt.Printf("removed %s SessionStart hook\n", h.Name())
		}
		if removed, err := h.UninstallUserPromptSubmitHook(userPromptSubmitHookCommand); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not remove %s UserPromptSubmit hook: %v\n", h.Name(), err)
		} else if removed {
			fmt.Printf("removed %s UserPromptSubmit hook\n", h.Name())
		}
	}
	if *keepHook {
		fmt.Println("--keep-hook: leaving hooks in place")
	}
	return 0
}
