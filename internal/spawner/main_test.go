package spawner

import (
	"os"
	"testing"
)

// TestMain clears CLAUDE_CODE_ENTRYPOINT so detection tests behave the
// same when the suite runs inside a Claude Desktop Code session (which
// exports CLAUDE_CODE_ENTRYPOINT=claude-desktop and would otherwise win
// over every other signal). Desktop detection is tested explicitly.
func TestMain(m *testing.M) {
	os.Unsetenv("CLAUDE_CODE_ENTRYPOINT")
	os.Exit(m.Run())
}
