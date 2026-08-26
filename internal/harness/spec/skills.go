package spec

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"flow/internal/harness"
	"flow/internal/harness/managedblock"
)

// blockName is the marker identity flow claims inside a file it does
// not own. Stable — changing it orphans every block already installed.
const blockName = "flow:managed"

// hookDirective is what the instruction-directive strategy asks the
// agent to do. It is prose, not configuration, because the agent
// reading it is the only thing that will act on it.
//
// It names the same commands the config-patch strategy registers, so a
// harness using either path ends up invoking identical flow code.
const hookDirective = `At the start of a work session, run ` + "`flow hook session-start`" + ` and follow
any instructions it prints. If the conversation drifts away from the bound
task, or before you finish, run ` + "`flow hook user-prompt-submit`" + ` and follow that.`

// skillDir renders the manifest's skill directory.
func (a *Adapter) skillDir() (string, error) {
	return ExpandPath(a.spec.Skills.Dir, a.vars())
}

// SkillInstallPath returns the SKILL.md inside the skill directory.
func (a *Adapter) SkillInstallPath() (string, error) {
	dir, err := a.skillDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "SKILL.md"), nil
}

// SkillVersionPath returns the sidecar recording which flow version
// wrote the current skill content.
func (a *Adapter) SkillVersionPath() (string, error) {
	dir, err := a.skillDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "VERSION"), nil
}

// InstallSkill writes the skill tree and, for pointer-discovery
// harnesses, the managed block that makes the harness aware of it.
//
// Frontmatter is checked BEFORE anything is written. A harness that
// rejects a SKILL.md missing `description:` would otherwise accept the
// install silently and then ignore the skill forever — a failure that
// looks like flow not working rather than a malformed file.
func (a *Adapter) InstallSkill(files fs.FS) error {
	dir, err := a.skillDir()
	if err != nil {
		return err
	}
	if err := a.checkFrontmatter(files); err != nil {
		return err
	}
	// Shared with the hand-written adapter so the two installed trees
	// cannot diverge, and so a renamed reference is pruned rather than
	// left behind as an orphan.
	if err := harness.SyncTree(files, dir, harness.SkillVersionFile); err != nil {
		return err
	}
	if a.spec.Skills.Discovery != "pointer" {
		return nil
	}
	_, err = a.applyPointerBlock(dir)
	return err
}

// blockVars is the template environment for a pointer block: the
// standard variables, promoted through embedding, plus the two only a
// block can meaningfully use.
type blockVars struct {
	Vars
	SkillPath     string
	HookDirective string
}

// applyPointerBlock writes the managed region that tells a harness with
// no skill mechanism where flow's skill lives.
func (a *Adapter) applyPointerBlock(skillDir string) (bool, error) {
	p := a.spec.Skills.Pointer
	v := a.vars()
	file, err := ExpandPath(p.File, v)
	if err != nil {
		return false, fmt.Errorf("skills.pointer.file: %w", err)
	}

	body, err := ExpandText(p.Block, blockVars{
		Vars:          v,
		SkillPath:     filepath.Join(skillDir, "SKILL.md"),
		HookDirective: a.hookDirectiveText(),
	})
	if err != nil {
		return false, fmt.Errorf("skills.pointer.block: %w", err)
	}
	return managedblock.Block{
		Path:    file,
		Name:    blockName,
		Comment: managedblock.Comment(p.Comment),
	}.Apply(body)
}

// hookDirectiveText returns the self-invocation instructions, or empty
// when the instruction-directive strategy is not enabled. A manifest
// referencing {{.HookDirective}} without the strategy gets nothing,
// which is correct: flow should not ask an agent to call hooks that
// were wired a different way.
func (a *Adapter) hookDirectiveText() string {
	if a.spec.Hooks.Has(StrategyInstructionDirective) {
		return hookDirective
	}
	return ""
}

// OwnsSkillDir reports whether this manifest claims the skill
// directory, and so whether uninstall will delete it.
func (a *Adapter) OwnsSkillDir() bool { return a.spec.Skills.Owns() }

// UninstallSkill removes the skill tree and the managed block.
//
// The directory is removed ONLY when the manifest claims to own it.
// A harness pointing at another's directory (praxis reads claude's
// ~/.claude/skills) must leave it alone; its pointer block is still
// removed, so the harness stops being told about a skill it no longer
// has any business reading.
func (a *Adapter) UninstallSkill() error {
	if a.spec.Skills.Discovery == "pointer" && a.spec.Skills.Pointer != nil {
		file, err := ExpandPath(a.spec.Skills.Pointer.File, a.vars())
		if err != nil {
			return err
		}
		if _, err := (managedblock.Block{
			Path:    file,
			Name:    blockName,
			Comment: managedblock.Comment(a.spec.Skills.Pointer.Comment),
		}).Remove(); err != nil {
			return err
		}
	}
	if !a.spec.Skills.Owns() {
		return nil
	}
	dir, err := a.skillDir()
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// checkFrontmatter enforces the keys a harness demands in SKILL.md.
func (a *Adapter) checkFrontmatter(files fs.FS) error {
	want := a.spec.Skills.RequireFrontmatter
	if len(want) == 0 {
		return nil
	}
	f, err := files.Open("SKILL.md")
	if err != nil {
		return fmt.Errorf("harness %s requires frontmatter but the skill has no SKILL.md: %w", a.spec.Name, err)
	}
	defer f.Close()

	keys := map[string]bool{}
	sc := bufio.NewScanner(f)
	inFront := false
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "---" {
			if !inFront {
				inFront = true
				continue
			}
			break
		}
		if !inFront {
			// No opening delimiter on the first non-empty line means
			// there is no frontmatter at all.
			if strings.TrimSpace(line) != "" {
				break
			}
			continue
		}
		if k, _, ok := strings.Cut(line, ":"); ok {
			keys[strings.TrimSpace(k)] = true
		}
	}
	var missing []string
	for _, k := range want {
		if !keys[k] {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf(
			"harness %s rejects a SKILL.md without frontmatter key(s) %s; the skill would be installed and then silently ignored",
			a.spec.Name, strings.Join(missing, ", "))
	}
	return nil
}

// Skills reports the skill-install capability, or nil when the manifest
// declares no [skills] table.
func (a *Adapter) Skills() harness.SkillInstaller {
	if a.spec.Skills == nil {
		return nil
	}
	return a
}
