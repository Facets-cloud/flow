package app

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	connectorsamples "flow/examples/connectors"
	"flow/internal/connector"
	"flow/internal/flowdb"
)

// sampleConnectors is the embedded copy of examples/connectors (one dir
// per sample). Overridable in tests.
var sampleConnectors fs.FS = connectorsamples.Samples

// runConnector executes one connector verb. Overridable in tests.
var runConnector = connector.Run

// runPeerConnector executes a peer connector's send, returning stdout.
// Overridable in tests.
var runPeerConnector = connector.RunOutput

// chmodSampleExec marks the manifest's exec target executable. It reads
// the raw manifest because connector.Load already demands the exec bit.
func chmodSampleExec(dir string) error {
	raw, err := os.ReadFile(filepath.Join(dir, connector.ManifestFile))
	if err != nil {
		return err
	}
	var mf struct {
		Exec string `json:"exec"`
	}
	if err := json.Unmarshal(raw, &mf); err != nil {
		return err
	}
	rel := filepath.Clean(filepath.FromSlash(mf.Exec))
	if mf.Exec == "" || !filepath.IsLocal(rel) {
		return fmt.Errorf("sample exec %q must be a path inside the folder", mf.Exec)
	}
	return os.Chmod(filepath.Join(dir, rel), 0o755)
}

// availableSamples lists the embedded sample connector names, sorted.
func availableSamples() []string {
	ents, err := fs.ReadDir(sampleConnectors, ".")
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range ents {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

// extractSample writes the embedded sample <name> into a fresh temp dir
// and returns that connector folder plus a cleanup func. embed keeps no
// file modes, so files land 0644 and the manifest's exec target 0755.
func extractSample(name string) (string, func(), error) {
	sub, err := fs.Sub(sampleConnectors, name)
	if err != nil {
		return "", nil, err
	}
	tmp, err := os.MkdirTemp("", "flow-connector-sample-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	dir := filepath.Join(tmp, name)
	err = fs.WalkDir(sub, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(path))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(sub, path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err == nil {
		err = chmodSampleExec(dir)
	}
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return dir, cleanup, nil
}

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

// humanConnectors lists the installed connectors with role "human" —
// the only ones human-ping dispatch (tiers, escalation) ever invokes.
func humanConnectors() []*connector.Manifest {
	var out []*connector.Manifest
	for _, m := range installedConnectors() {
		if m.HasRole(connector.RoleHuman) {
			out = append(out, m)
		}
	}
	return out
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
	if !m.Ping || len(humanConnectors()) == 0 {
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
	return connector.SecretStore{
		FileDir: filepath.Join(root, "secrets"),
		Account: connector.KeychainAccount(root),
	}, nil
}

// invokeConnector runs one verb of an installed connector with its secret,
// FLOW_BIN and FLOW_ROOT in the environment.
func invokeConnector(m *connector.Manifest, verb string, p connector.Payload) error {
	if p.Options == nil {
		p.Options = []string{}
	}
	payload, env, err := connectorCall(m, p)
	if err != nil {
		return err
	}
	return runConnector(m, verb, payload, env, connectorTimeout)
}

// connectorCall encodes a payload (HTML escaping off, so <, >, & stay
// literal for simple shell parsers) and builds the connector's
// environment: its secret, FLOW_BIN and FLOW_ROOT.
func connectorCall(m *connector.Manifest, p any) ([]byte, []string, error) {
	store, err := secretStore()
	if err != nil {
		return nil, nil, err
	}
	secret, err := store.Get(m.Name)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w (re-add the connector)", m.Name, err)
	}
	root, err := flowRoot()
	if err != nil {
		return nil, nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(p); err != nil {
		return nil, nil, err
	}
	env := append(connectorEnv(),
		"FLOW_CONNECTOR_TOKEN="+secret, "FLOW_BIN="+flowBinPath(), "FLOW_ROOT="+root)
	return bytes.TrimRight(buf.Bytes(), "\n"), env, nil
}

// testPayload is what `run test` receives: the send shape with id "test".
func testPayload() connector.Payload {
	return connector.Payload{
		ID: "test", FromTask: "flow", Body: "flow connector test", Kind: "test",
		CreatedAt: flowdb.NowISO(), ReplyHint: "test notifications take no reply",
	}
}

const connectorUsage = `usage: flow connector <add|list|remove|test|priority|escalate> ...
  flow connector add <dir|sample> validate, install, store a secret, run its test
                                  (<sample> = a built-in name, e.g. macos)
  flow connector list             installed connectors, priority order, escalation
  flow connector list --available built-in sample connectors (no checkout needed)
  flow connector remove <name>    delete the connector, its secret and priority
  flow connector test [<name>]    send a test notification (all if no name)
  flow connector priority <name> <n>       ping order: lower n first, same n together;
                                           a tier is tried only if the earlier one failed
  flow connector priority <name> --clear   drop its priority (unprioritized go last)
  flow connector escalate <dur>|off        if an ask stays unseen <dur> after a tier
                                           delivered it, ping the next tier (max 24h)`

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
	case "priority":
		return cmdConnectorPriority(args[1:])
	case "escalate":
		return cmdConnectorEscalate(args[1:])
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
		fmt.Fprintln(os.Stderr, "usage: flow connector add <dir|sample>")
		return 2
	}
	// An existing directory wins; otherwise the arg names an embedded
	// sample, so a binary-only install needs no repo checkout.
	src := args[0]
	if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
		known := false
		for _, n := range availableSamples() {
			if n == src {
				known = true
			}
		}
		if !known {
			fmt.Fprintf(os.Stderr, "error: %q is neither a connector directory nor a built-in sample (available: %s)\n",
				src, strings.Join(availableSamples(), ", "))
			return 1
		}
		dir, cleanup, err := extractSample(src)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: unpack sample %s: %v\n", src, err)
			return 1
		}
		defer cleanup()
		src = dir
	}
	src, err := filepath.Abs(src)
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
	if len(args) == 1 && args[0] == "--available" {
		fmt.Println("built-in sample connectors (install: flow connector add <name>):")
		for _, n := range availableSamples() {
			fmt.Printf("  %s\n", n)
		}
		return 0
	}
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: flow connector list [--available]")
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
	cfg, cfgErr := connector.LoadConfig(root)
	for _, m := range ms {
		where := store.Where(m.Name)
		if where == "" {
			where = "MISSING secret"
		}
		prio := "-"
		if n, ok := cfg.Priority[m.Name]; ok {
			prio = fmt.Sprint(n)
		}
		fmt.Printf("%-16s roles=%s  priority=%s  events=%s  secret=%s  %s\n", m.Name,
			strings.Join(m.EffectiveRoles(), ","), prio, strings.Join(m.Events, ","), where, m.Dir)
	}
	for _, e := range errs {
		fmt.Printf("invalid: %v\n", e)
	}
	if cfgErr != nil {
		fmt.Printf("invalid: %v (dispatch ignores it and pings all connectors at once)\n", cfgErr)
		return 0
	}
	var human []*connector.Manifest
	for _, m := range ms {
		if m.HasRole(connector.RoleHuman) {
			human = append(human, m)
		}
	}
	var order []string
	for _, tier := range connector.Tiers(human, cfg.Priority) {
		var ns []string
		for _, m := range tier {
			ns = append(ns, m.Name)
		}
		order = append(order, strings.Join(ns, "+"))
	}
	if len(order) == 0 {
		order = []string{"(no human connectors)"}
	}
	fmt.Printf("order: %s (urgent asks go to all at once)\n", strings.Join(order, " → "))
	esc := cfg.EscalateAfter
	if esc == "" {
		esc = "off"
	}
	fmt.Printf("escalate: %s\n", esc)
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
	if cfg, err := connector.LoadConfig(root); err == nil {
		if _, ok := cfg.Priority[name]; ok {
			delete(cfg.Priority, name)
			if err := connector.SaveConfig(root, cfg); err != nil {
				fmt.Fprintf(os.Stderr, "warning: drop priority: %v\n", err)
			}
		}
	}
	fmt.Printf("removed connector %s (folder and secret)\n", name)
	return 0
}

// cmdConnectorPriority sets or clears one connector's tier number in
// connectors/config.json (the user's routing, not the connector's).
func cmdConnectorPriority(args []string) int {
	const usage = "usage: flow connector priority <name> <n>|--clear   (n ≥ 1; lower n is pinged first)"
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	name, val := args[0], args[1]
	n := 0
	if val != "--clear" {
		v, err := strconv.Atoi(val)
		if err != nil || v < 1 {
			fmt.Fprintf(os.Stderr, "error: priority %q must be an integer ≥ 1\n%s\n", val, usage)
			return 2
		}
		n = v
	}
	root, err := connectorsRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	cfg, err := connector.LoadConfig(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v (fix or delete it)\n", err)
		return 1
	}
	if n == 0 {
		delete(cfg.Priority, name)
	} else {
		installed := false
		for _, m := range installedConnectors() {
			if m.Name == name {
				installed = true
			}
		}
		if !installed {
			fmt.Fprintf(os.Stderr, "error: no connector %q installed\n", name)
			return 1
		}
		if cfg.Priority == nil {
			cfg.Priority = map[string]int{}
		}
		cfg.Priority[name] = n
	}
	if err := connector.SaveConfig(root, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if n == 0 {
		fmt.Printf("cleared priority of %s\n", name)
	} else {
		fmt.Printf("priority %s = %d\n", name, n)
	}
	return 0
}

// cmdConnectorEscalate sets the global escalation delay (or off).
func cmdConnectorEscalate(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: flow connector escalate <dur>|off   (e.g. 10m; max 24h)")
		return 2
	}
	d, err := connector.ParseEscalate(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	root, err := connectorsRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	cfg, err := connector.LoadConfig(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v (fix or delete it)\n", err)
		return 1
	}
	cfg.EscalateAfter = ""
	if d > 0 {
		cfg.EscalateAfter = args[0]
	}
	if err := connector.SaveConfig(root, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if d == 0 {
		fmt.Println("escalation off")
	} else {
		fmt.Printf("escalation: an ask still unseen %v after a tier delivered it goes to the next tier\n", args[0])
	}
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

// dispatchSleep is the escalation wait inside the detached dispatcher.
// Overridable in tests so escalation never costs real minutes.
var dispatchSleep = time.Sleep

// cmdConnectorDispatch is the hidden detached worker spawned after a
// pinging send: `flow connector dispatch <id>`. It runs `send` on the
// installed connectors subscribed to the row's event (ask | urgent), logs
// one line per connector to connectors/dispatch.log, and stamps
// notified_at when at least one succeeded.
//
//	urgent          ─► every connector at once (parallel)
//	ask, no prio    ─► one tier = every connector at once (contract v1)
//	ask, priorities ─► tier 1 (parallel within) ─ any ok ─► done
//	                         └ all fail ─► tier 2 … none ok ─► stays unread
//	    escalate on & a tier succeeded: sleep; if still unseen ─► next
//	    tier (same fail-walk); repeat until seen or tiers run out. Each
//	    wait logs "<id> escalate armed <dur> → <next tier>" before it.
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
	if m.IsPeerOutbound() {
		return peerSend(db, m)
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
	for _, c := range humanConnectors() {
		if c.Wants(event) {
			targets = append(targets, c)
		}
	}
	p := connector.Payload{
		ID: m.ID, FromTask: m.FromTaskSlug, Body: m.Body, Options: m.Options,
		Urgent: m.Urgent, Kind: "ask", CreatedAt: m.CreatedAt,
		ReplyHint: fmt.Sprintf(`"$FLOW_BIN" human reply %s "<answer>"  (or: "$FLOW_BIN" human seen %s)`, m.ID, m.ID),
	}
	if m.IsPeerInbound() {
		p.FromPeer = m.Peer
	}

	var cfg connector.Config
	if root, err := connectorsRoot(); err == nil {
		if cfg, err = connector.LoadConfig(root); err != nil {
			// A broken config must never silence pings: fall back to
			// all-in-parallel, the pre-priority behavior.
			logDispatch("%s config error %v (ignoring priorities)", m.ID, err)
			cfg = connector.Config{}
		}
	}
	escalate, err := cfg.Escalate()
	if err != nil {
		logDispatch("%s config error %v (escalation off)", m.ID, err)
		escalate = 0
	}
	tiers := [][]*connector.Manifest{targets}
	if !m.Urgent {
		tiers = connector.Tiers(targets, cfg.Priority)
	}
	if len(targets) == 0 {
		tiers = nil
	}

	next, ok := walkTiers(db, m.ID, tiers, 0, p, "")
	for ok && escalate > 0 && next < len(tiers) {
		// Escalation state lives only in this process. Log the arm first
		// so a dispatcher that dies mid-wait (reboot, sleep, upgrade)
		// leaves an "armed" line with no "escalate → …"/"stop" after it.
		var names []string
		for _, c := range tiers[next] {
			names = append(names, c.Name)
		}
		logDispatch("%s escalate armed %v → %s", m.ID, escalate, strings.Join(names, ", "))
		dispatchSleep(escalate)
		cur, err := flowdb.GetBusMessageByID(db, m.ID)
		if err != nil || cur == nil {
			logDispatch("%s escalate stop: %v", m.ID, err)
			break
		}
		if cur.SeenAt != "" || cur.AnsweredAt != "" {
			logDispatch("%s escalate stop: seen", m.ID)
			break
		}
		next, ok = walkTiers(db, m.ID, tiers, next, p, "escalate → ")
	}
	return 0
}

// walkTiers runs tiers[from:] one at a time until one tier has a success
// (each tier's connectors run in parallel). It logs one line per
// connector, stamps notified_at on success, and — when more than one tier
// ran, or prefix is set — a path line like "<id> flowhub fail → macos ok".
// It returns the index after the succeeding tier and whether one did.
func walkTiers(db *sql.DB, id string, tiers [][]*connector.Manifest, from int, p connector.Payload, prefix string) (int, bool) {
	var path []string
	i := from
	ok := false
	for ; i < len(tiers) && !ok; i++ {
		tier := tiers[i]
		errs := make([]error, len(tier))
		var wg sync.WaitGroup
		for j, c := range tier {
			wg.Add(1)
			go func(j int, c *connector.Manifest) {
				defer wg.Done()
				errs[j] = invokeConnector(c, connector.VerbSend, p)
			}(j, c)
		}
		wg.Wait()
		var parts []string
		for j, c := range tier {
			if errs[j] != nil {
				logDispatch("%s %s fail %v", id, c.Name, errs[j])
				parts = append(parts, c.Name+" fail")
				continue
			}
			ok = true
			logDispatch("%s %s ok", id, c.Name)
			parts = append(parts, c.Name+" ok")
		}
		path = append(path, strings.Join(parts, ", "))
	}
	if ok {
		if err := flowdb.MarkNotified(db, id); err != nil {
			logDispatch("%s error mark notified: %v", id, err)
		}
	}
	if prefix != "" || len(path) > 1 {
		logDispatch("%s %s%s", id, prefix, strings.Join(path, " → "))
	}
	return i, ok
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
