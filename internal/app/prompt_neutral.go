package app

import "flow/internal/harness/codex"

// Session prompts are read by Claude Code or Codex, so they never assume
// one harness. Claude Code has a Skill tool; Codex lists installed skills
// and reads SKILL.md by path, so the Codex install path is spelled out.
// Kept shell-safe (no quotes, backticks or dollar signs): bootstrap
// prompts are shell-quoted as one positional argument.

// flowSkillStep is the "load the flow skill" instruction (no step number,
// no trailing period).
func flowSkillStep() string {
	return "Load the flow skill: " + flowSkillHow()
}

// flowSkillHow says how to load the flow skill in either harness (no
// leading verb, no trailing period); hook text prefixes its own wording.
func flowSkillHow() string {
	s := "invoke it via the Skill tool if your harness has one; otherwise read its SKILL.md"
	if p, err := codex.New().SkillInstallPath(); err == nil {
		s += " (Codex: " + p + ")"
	}
	return s + " and the references/ files it points to on demand"
}

// repoInstructionFiles names the per-repo instruction files both
// harnesses honor.
const repoInstructionFiles = "CLAUDE.md / AGENTS.md"
