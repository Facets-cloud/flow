package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

// flowBinPath is the absolute path of the running flow binary, handed to
// connectors as FLOW_BIN so they call back the same build. Overridable in
// tests.
var flowBinPath = func() string {
	p, err := os.Executable()
	if err != nil {
		return "flow"
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// connectorTimeout bounds one connector invocation. Overridable in tests.
var connectorTimeout = connector.Timeout

func secretStore() (connector.SecretStore, error) {
	root, err := flowRoot()
	if err != nil {
		return connector.SecretStore{}, err
	}
	return connector.SecretStore{FileDir: filepath.Join(root, "secrets")}, nil
}

// invokeConnector runs one verb of an installed connector with its secret,
// FLOW_BIN and FLOW_ROOT in the environment.
func invokeConnector(m *connector.Manifest, verb string, p connector.Payload) error {
	store, err := secretStore()
	if err != nil {
		return err
	}
	secret, err := store.Get(m.Name)
	if err != nil {
		return fmt.Errorf("%s: %w (re-add the connector)", m.Name, err)
	}
	root, err := flowRoot()
	if err != nil {
		return err
	}
	if p.Options == nil {
		p.Options = []string{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // keep <, >, & literal for simple shell parsers
	if err := enc.Encode(p); err != nil {
		return err
	}
	payload := bytes.TrimRight(buf.Bytes(), "\n")
	env := append(connectorEnv(),
		"FLOW_CONNECTOR_TOKEN="+secret, "FLOW_BIN="+flowBinPath(), "FLOW_ROOT="+root)
	return connector.Run(m, verb, payload, env, connectorTimeout)
}

// testPayload is what `run test` receives: the send shape with id "test".
func testPayload() connector.Payload {
	return connector.Payload{
		ID: "test", FromTask: "flow", Body: "flow connector test", Kind: "test",
		CreatedAt: flowdb.NowISO(), ReplyHint: "test notifications take no reply",
	}
}

const connectorUsage = `usage: flow connector <add|list|remove|test> ...
  flow connector add <dir>        validate, install, store a secret, run its test
  flow connector list             installed connectors
  flow connector remove <name>    delete the connector and its secret
  flow connector test [<name>]    send a test notification (all if no name)`

// cmdConnector implements `flow connector ...` (opt-in human pings).
func cmdConnector(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, connectorUsage)
		return 2
	}
	switch args[0] {
	case "add":
		return cmdConnectorAdd(args[1:])
	case "list", "ls":
		return cmdConnectorList(args[1:])
	case "remove", "rm":
		return cmdConnectorRemove(args[1:])
	case "test":
		return cmdConnectorTest(args[1:])
	case "dispatch":
		return cmdConnectorDispatch(args[1:])
	case "-h", "--help", "help":
		fmt.Println(connectorUsage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "error: unknown connector subcommand %q\n%s\n", args[0], connectorUsage)
		return 2
	}
}

func cmdConnectorAdd(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: flow connector add <dir>")
		return 2
	}
	src, err := filepath.Abs(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	m, err := connector.Load(src)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: not a valid connector: %v\n", err)
		return 1
	}
	root, err := connectorsRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	dest := filepath.Join(root, m.Name)
	if _, err := os.Stat(dest); err == nil {
		fmt.Fprintf(os.Stderr, "error: connector %q is already installed — remove it first (flow connector remove %s)\n", m.Name, m.Name)
		return 1
	}
	store, err := secretStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	rollback := func() {
		_ = os.RemoveAll(dest)
		_ = store.Delete(m.Name)
	}
	if err := copyTree(src, dest); err != nil {
		rollback()
		fmt.Fprintf(os.Stderr, "error: copy connector: %v\n", err)
		return 1
	}
	where, err := store.Put(m.Name, connector.NewSecret())
	if err != nil {
		rollback()
		fmt.Fprintf(os.Stderr, "error: store secret: %v\n", err)
		return 1
	}
	installed, err := connector.Load(dest)
	if err == nil {
		err = invokeConnector(installed, connector.VerbTest, testPayload())
	}
	if err != nil {
		rollback()
		fmt.Fprintf(os.Stderr, "error: connector test failed, not installed: %v\n", err)
		return 1
	}
	fmt.Printf("installed connector %s → %s (secret in %s); test notification sent\n", m.Name, dest, where)
	return 0
}

func cmdConnectorList(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: flow connector list")
		return 2
	}
	root, err := connectorsRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	ms, errs := connector.List(root)
	if len(ms) == 0 && len(errs) == 0 {
		fmt.Println("no connectors installed — human mail just waits in the inbox (add one: flow connector add <dir>)")
		return 0
	}
	store, err := secretStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	for _, m := range ms {
		where := store.Where(m.Name)
		if where == "" {
			where = "MISSING secret"
		}
		fmt.Printf("%-16s events=%s  secret=%s  %s\n", m.Name, strings.Join(m.Events, ","), where, m.Dir)
	}
	for _, e := range errs {
		fmt.Printf("invalid: %v\n", e)
	}
	return 0
}

func cmdConnectorRemove(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: flow connector remove <name>")
		return 2
	}
	name := args[0]
	root, err := connectorsRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	dir := filepath.Join(root, name)
	if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		fmt.Fprintf(os.Stderr, "error: bad connector name %q\n", name)
		return 2
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		fmt.Fprintf(os.Stderr, "error: no connector %q installed\n", name)
		return 1
	}
	store, err := secretStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if err := store.Delete(name); err != nil {
		fmt.Fprintf(os.Stderr, "error: remove secret: %v\n", err)
		return 1
	}
	fmt.Printf("removed connector %s (folder and secret)\n", name)
	return 0
}

func cmdConnectorTest(args []string) int {
	if len(args) > 1 {
		fmt.Fprintln(os.Stderr, "usage: flow connector test [<name>]")
		return 2
	}
	ms := installedConnectors()
	if len(args) == 1 {
		var pick []*connector.Manifest
		for _, m := range ms {
			if m.Name == args[0] {
				pick = append(pick, m)
			}
		}
		if len(pick) == 0 {
			fmt.Fprintf(os.Stderr, "error: no connector %q installed\n", args[0])
			return 1
		}
		ms = pick
	}
	if len(ms) == 0 {
		fmt.Fprintln(os.Stderr, "error: no connectors installed (add one: flow connector add <dir>)")
		return 1
	}
	rc := 0
	for _, m := range ms {
		if err := invokeConnector(m, connector.VerbTest, testPayload()); err != nil {
			fmt.Printf("%s: FAIL %v\n", m.Name, err)
			rc = 1
			continue
		}
		fmt.Printf("%s: ok — test notification sent\n", m.Name)
	}
	return rc
}

// cmdConnectorDispatch is the hidden detached worker spawned after a
// pinging send: `flow connector dispatch <id>`. It runs `send` on every
// installed connector subscribed to the row's event (ask | urgent), all in
// parallel so one hanging connector never delays the others, logs one
// line per connector to connectors/dispatch.log, and stamps notified_at
// when at least one succeeded.
func cmdConnectorDispatch(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: flow connector dispatch <message-id>")
		return 2
	}
	db, err := openBusDB()
	if err != nil {
		logDispatch("%s error %v", args[0], err)
		return 1
	}
	defer db.Close()
	m, err := flowdb.GetBusMessageByID(db, args[0])
	if err != nil || m == nil {
		logDispatch("%s error no such message", args[0])
		fmt.Fprintf(os.Stderr, "error: no message %q\n", args[0])
		return 1
	}
	if m.Kind != "message" || m.ToTaskSlug != "" {
		logDispatch("%s error not a human ask", m.ID)
		fmt.Fprintf(os.Stderr, "error: %s is not a human ask\n", m.ID)
		return 1
	}
	if m.SeenAt != "" {
		logDispatch("%s skip already seen", m.ID)
		return 0
	}
	event := connector.EventAsk
	if m.Urgent {
		event = connector.EventUrgent
	}
	var targets []*connector.Manifest
	for _, c := range installedConnectors() {
		if c.Wants(event) {
			targets = append(targets, c)
		}
	}
	p := connector.Payload{
		ID: m.ID, FromTask: m.FromTaskSlug, Body: m.Body, Options: m.Options,
		Urgent: m.Urgent, Kind: "ask", CreatedAt: m.CreatedAt,
		ReplyHint: fmt.Sprintf(`"$FLOW_BIN" human reply %s "<answer>"  (or: "$FLOW_BIN" human seen %s)`, m.ID, m.ID),
	}
	errs := make([]error, len(targets))
	var wg sync.WaitGroup
	for i, c := range targets {
		wg.Add(1)
		go func(i int, c *connector.Manifest) {
			defer wg.Done()
			errs[i] = invokeConnector(c, connector.VerbSend, p)
		}(i, c)
	}
	wg.Wait()
	ok := false
	for i, c := range targets {
		if errs[i] != nil {
			logDispatch("%s %s fail %v", m.ID, c.Name, errs[i])
			continue
		}
		ok = true
		logDispatch("%s %s ok", m.ID, c.Name)
	}
	if ok {
		if err := flowdb.MarkNotified(db, m.ID); err != nil {
			logDispatch("%s error mark notified: %v", m.ID, err)
		}
	}
	return 0
}

// copyTree copies a connector folder (regular files and directories,
// permissions kept). Symlinks are refused so an install cannot point
// outside the copied folder.
func copyTree(src, dest string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		switch {
		case info.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("%s is a symlink — connectors must be plain files", rel)
		case !info.Mode().IsRegular():
			return fmt.Errorf("%s is not a regular file", rel)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}
