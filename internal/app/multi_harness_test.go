package app

import (
	"os"
	"strings"
	"testing"
)

const harnessesLinePrefix = "harnesses available for children:"

// Both driver blocks list the harnesses a child can run on when more than
// one is installed, so a driver can honor the user's harness preference.
func TestDriverBlocksListHarnessesWhenBothPresent(t *testing.T) {
	withHarnessEnv(t, "claude", "codex")
	want := "harnesses available for children: claude, codex (flow auto … --harness <name>; follow the user's preference)"
	for name, block := range map[string]string{
		"driver block":     driverRoleBlock(),
		"delegation block": driverDelegationBlock(),
	} {
		if !strings.Contains(block, want) {
			t.Errorf("%s missing harness line %q:\n%s", name, want, block)
		}
	}
}

// With a single harness the blocks stay exactly as before: no extra line.
func TestDriverBlocksOmitHarnessesWhenOnlyOnePresent(t *testing.T) {
	for _, onPath := range []string{"claude", "codex"} {
		t.Run(onPath, func(t *testing.T) {
			withHarnessEnv(t, onPath)
			for name, block := range map[string]string{
				"driver block":     driverRoleBlock(),
				"delegation block": driverDelegationBlock(),
			} {
				if strings.Contains(block, harnessesLinePrefix) {
					t.Errorf("%s lists harnesses with only %s present:\n%s", name, onPath, block)
				}
			}
		})
	}
}

// The config home alone also counts as present (harnessPresent rule).
func TestDriverBlocksListHarnessesViaConfigHome(t *testing.T) {
	env := withHarnessEnv(t, "claude")
	if err := os.MkdirAll(env.codexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(driverRoleBlock(), harnessesLinePrefix+" claude, codex") {
		t.Errorf("codex config home should count as present:\n%s", driverRoleBlock())
	}
}

// The delegation reference carries the multi-harness guidance: pick by
// preference, ask once and save to kb/user.md, default to the caller's
// harness when headless, and offer the other harness on a limit hit.
func TestSkillDelegationCoversHarnessChoice(t *testing.T) {
	ref, err := os.ReadFile("skill/references/delegation.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := strings.Join(strings.Fields(string(ref)), " ")
	for _, want := range []string{
		"## Choosing the child's harness",
		"user's stated preference",
		"Run children on Claude or Codex?",
		"kb/user.md",
		"prefers Codex for auto children",
		"without asking again",
		"override per child",
		"caller's harness",
		// limit hit → offer the other harness
		"You've hit your",
		`quotaLimits.status:"rejected"`,
		`error:"rate_limit"`,
		"rate_limit_reached_type",
		"when it resets",
		"--harness <other>",
		"new slug",
		"No DB edits",
		"the driver itself",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("delegation.md missing %q", want)
		}
	}
}
