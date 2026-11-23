package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"flow/internal/flowdb"
	"flow/internal/harness"
)

// Legacy strip: older flow versions wrote their Claude hooks into
// ~/.claude/settings.json. The flow plugin now carries them, so those
// entries must go. But a Claude Code that never loads the plugin (an older
// release, an org policy that blocks plugins, the plugin disabled in any
// settings layer) would be left with no flow hooks at all, and a session
// that started before the plugin existed would lose its hooks until it
// restarts.
//
// So the automatic strip runs from exactly one place: the PLUGIN copy of
// the SessionStart hook (FLOW_HOOK_SRC=plugin). That invocation is the proof
// that the plugin loads on this machine. It also strips only when no OTHER
// Claude session flow can see is live (ps for `claude --session-id|--resume
// <id>`, plus `claude agents`). The session that fires it needs no restart:
// it loaded the plugin hooks at start, and Claude live-reloads
// settings.json, so its legacy copies just disappear.
//
// If the plugin never fires, the strip never happens and the legacy hooks
// keep working (the claim guard in hookguard.go keeps them from doubling
// with any plugin copies). Ordinary flow commands and `flow skill
// install|update` never strip on their own; `flow skill update
// --strip-legacy` forces it.
//
// Known caveat (accepted while flow is alpha): a plain `claude` started
// with no session id in its argv is invisible to the live check. If the
// strip runs under one that did not load the plugin, it loses its flow
// hooks until it restarts. The FYI the strip leaves says so.

// liveClaudeSessions returns the live Claude session ids (lower-case): the
// ps scan plus every running background agent. A var so tests can stub it.
var liveClaudeSessions = func() (map[string]int, error) {
	h, err := harnessByName(string(harness.NameClaude))
	if err != nil {
		return nil, err
	}
	live, err := h.LiveSessionIDs()
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for id, n := range live {
		out[strings.ToLower(id)] += n
	}
	// A fresh `claude --bg --name <n>` carries no id in argv, so ps can't
	// map it. Ask the agent registry whatever the terminal backend is.
	if bg, ok := h.(harness.BackgroundLauncher); ok {
		agents, err := backgroundAgentsWithin(bg, bgAgentsTimeout)
		if err != nil {
			return nil, err
		}
		for _, a := range agents {
			if a.SessionID != "" && a.PID > 0 {
				out[strings.ToLower(a.SessionID)]++
			}
		}
	}
	return out, nil
}

// bgAgentsTimeout bounds the `claude agents` query the live check makes
// (it can take seconds right after a launch). A timeout means "unknown",
// so the strip waits for a later session start.
var bgAgentsTimeout = 3 * time.Second

func backgroundAgentsWithin(bg harness.BackgroundLauncher, d time.Duration) ([]harness.BackgroundAgent, error) {
	type res struct {
		agents []harness.BackgroundAgent
		err    error
	}
	ch := make(chan res, 1)
	go func() {
		a, err := bg.BackgroundAgents()
		ch <- res{a, err}
	}()
	select {
	case r := <-ch:
		return r.agents, r.err
	case <-time.After(d):
		return nil, fmt.Errorf("claude agents: no answer within %s", d)
	}
}

func legacyStripDonePath() string {
	d := hooksStateDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "legacy-stripped")
}

// legacyStripGrace is how long after a strip the claim guard keeps
// claiming although settings.json is clean: a legacy copy that started
// before the strip may still be on its way to its guard.
var legacyStripGrace = time.Minute

// legacyStripRecent reports whether a strip removed entries within
// legacyStripGrace.
func legacyStripRecent() bool {
	p := legacyStripDonePath()
	if p == "" {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && time.Since(fi.ModTime()) < legacyStripGrace
}

// recordLegacyStrip stamps the last strip that removed entries. It is
// informational plus the guard's grace window, never a "done, stop
// checking" switch: the guard and the strip re-read settings.json.
func recordLegacyStrip() {
	p := legacyStripDonePath()
	if p == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = os.WriteFile(p, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600)
}

// legacyClaudeEntriesPresent reports whether ~/.claude/settings.json still
// holds any hook command an older flow wrote (exact JSON string match).
// A missing file is "absent"; any other read failure is an error, so a
// caller can tell "clean" from "could not look".
func legacyClaudeEntriesPresent() (bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	raw, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	s := string(raw)
	for _, c := range legacyHookCommands() {
		if strings.Contains(s, `"`+c+`"`) {
			return true, nil
		}
	}
	return false, nil
}

// legacyHookCommands is every bare command an older flow wrote.
func legacyHookCommands() []string {
	cmds := []string{postToolUseHookCommand, rewakeHookCommand}
	for _, fh := range flowHooks() {
		cmds = append(cmds, fh.command)
	}
	return cmds
}

// maybeAutoStripLegacy is the automatic strip, called by the SessionStart
// handler with its hook source. It only acts for the plugin copy (proof the
// plugin loads here) and is silent apart from the human FYI a strip leaves.
func maybeAutoStripLegacy(src, sessionID string) {
	if src != hookSrcPlugin || sessionID == "" || os.Getenv(selfUpgradeEnv) != "" {
		return
	}
	if present, err := legacyClaudeEntriesPresent(); err != nil || !present {
		return // the common, cheap path: nothing to strip
	}
	stripLegacyClaudeHooks(false, sessionID)
}

// legacyStripNotice is the FYI the strip leaves for the human. auto names
// the session whose plugin load triggered it.
func legacyStripNotice(stripAt time.Time, triggerSession string) string {
	at := stripAt.Local().Format("15:04")
	if triggerSession != "" {
		return fmt.Sprintf("flow: hooks moved to the flow plugin (it loaded in Claude session %s and no other flow-visible Claude session was live, so flow removed its old ~/.claude/settings.json hook entries at %s). Nothing to restart; only a plain `claude` opened outside flow before %s may lack flow hooks until it restarts.",
			shortID(triggerSession), at, at)
	}
	return fmt.Sprintf("flow: hooks moved to the flow plugin (forced with --strip-legacy at %s); restart any Claude sessions opened before %s to get flow hooks back.", at, at)
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// announceLegacyStrip leaves the strip notice for the human as an FYI on
// the bus (best-effort: errors are ignored) and, for a forced strip from
// the CLI, prints it to stderr too. A hook's stderr is never written.
func announceLegacyStrip(stripAt time.Time, triggerSession string) {
	msg := legacyStripNotice(stripAt, triggerSession)
	if triggerSession == "" {
		fmt.Fprintln(os.Stderr, msg)
	}
	db, err := openBusDB()
	if err != nil {
		return
	}
	defer db.Close()
	s := currentBusSender()
	_ = flowdb.InsertBusMessage(db, &flowdb.BusMessage{
		ID: newBusID(), CreatedAt: flowdb.NowISO(), Kind: "message",
		FromAssignee: s.Assignee, FromTaskSlug: s.TaskSlug, SenderSessionID: s.SessionID,
		ToAssignee: busUser, Body: msg, FYI: true,
	})
}

// stripLegacyClaudeHooks removes flow's legacy entries from
// ~/.claude/settings.json. force (--strip-legacy) skips the live check;
// otherwise selfSession is the plugin session asking, and any other live
// Claude session keeps the entries. Serialized by a flock so concurrent
// session starts strip (and announce) once. Returns whether entries were
// removed, and why not otherwise.
func stripLegacyClaudeHooks(force bool, selfSession string) (bool, string) {
	h, err := harnessByName(string(harness.NameClaude))
	if err != nil {
		return false, err.Error()
	}
	if _, ok := h.(harness.PluginHookInstaller); !ok {
		return false, "claude harness has no plugin support"
	}
	unlock := lockLegacyStrip()
	defer unlock()
	present, err := legacyClaudeEntriesPresent()
	if err != nil {
		return false, "cannot read ~/.claude/settings.json: " + err.Error()
	}
	if !present {
		return false, "no legacy flow hooks in ~/.claude/settings.json"
	}
	if !force {
		live, err := liveClaudeSessions()
		if err != nil {
			return false, "cannot list live Claude sessions: " + err.Error()
		}
		self := strings.ToLower(selfSession)
		for id := range live {
			if id != self {
				return false, "Claude session " + id + " is live"
			}
		}
	}
	n := migrateLegacyHooks(h, true)
	if still, err := legacyClaudeEntriesPresent(); err != nil || still {
		// A write failed part-way: keep the guard on (it re-reads the
		// file) and let a later session start retry.
		return false, "could not remove every legacy entry from ~/.claude/settings.json"
	}
	if n > 0 {
		recordLegacyStrip()
		announceLegacyStrip(time.Now(), selfSession)
	}
	return n > 0, ""
}

// lockLegacyStrip takes an exclusive flock on $FLOW_ROOT/hooks/strip.lock
// (fails open: a filesystem error returns a no-op unlock).
func lockLegacyStrip() func() {
	d := hooksStateDir()
	if d == "" || os.MkdirAll(d, 0o700) != nil {
		return func() {}
	}
	f, err := os.OpenFile(filepath.Join(d, "strip.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return func() {}
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}
}
