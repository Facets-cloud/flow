package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"flow/internal/connector"
	"flow/internal/flowdb"
)

// Human connectors: opt-in executables that ping the human about mail
// addressed to them (contract v1, see internal/connector and the skill
// reference references/connectors.md). Nothing pings unless the user
// installed one with `flow connector add`.
//
//	flow message user "q" ──insert (ping decided)──► bus_messages
//	        │ ping && ≥1 connector
//	        └─ spawn detached `flow connector dispatch <id>` ──► run send
//	                                                     (one per connector)
//	connector ──`flow human reply <id> "..."` (FLOW_CONNECTOR_TOKEN)──►
//	        reply row to user/<from_task>, reply_to=<id>

// connectorsRoot is $FLOW_ROOT/connectors.
func connectorsRoot() (string, error) {
	root, err := flowRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "connectors"), nil
}

// installedConnectors lists the valid installed connectors (invalid
// folders are skipped; `flow connector list` reports them).
func installedConnectors() []*connector.Manifest {
	root, err := connectorsRoot()
	if err != nil {
		return nil
	}
	ms, _ := connector.List(root)
	return ms
}

// logDispatch appends one line to $FLOW_ROOT/connectors/dispatch.log.
// Best-effort: logging must never fail the caller.
func logDispatch(format string, a ...any) {
	root, err := connectorsRoot()
	if err != nil {
		return
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(root, "dispatch.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, a...))
}

// maybeDispatch spawns the detached dispatcher for a freshly committed
// row when it was marked to ping and a connector is installed. The
// sender never waits on it and never sees its errors.
func maybeDispatch(m *flowdb.BusMessage) {
	if !m.Ping || len(installedConnectors()) == 0 {
		return
	}
	if err := dispatchLauncher(m.ID); err != nil {
		logDispatch("%s spawn-error %v", m.ID, err)
	}
}

// connectorEnv is the environment for flow's connector subprocesses: the
// caller's, minus the harness session variables, so nothing a connector
// runs is attributed to the asking agent's session.
func connectorEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID", "FLOW_CONNECTOR_TOKEN":
			continue
		}
		out = append(out, kv)
	}
	return out
}

// dispatchLauncher starts a detached `flow connector dispatch <id>` (own
// session, stdio to /dev/null) and does not wait. Overridable in tests.
var dispatchLauncher = func(id string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate flow binary: %w", err)
	}
	cmd := exec.Command(self, "connector", "dispatch", id)
	cmd.Env = connectorEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
