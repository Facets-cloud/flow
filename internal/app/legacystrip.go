package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/spawner"
)

// Legacy strip: older flow versions wrote their Claude hooks into
// ~/.claude/settings.json. The flow plugin now carries them, so those
// entries must go — but plugin hooks load only at session start, and a
// session that started before the plugin existed still runs on its
// settings copies. Stripping under it would silence its hooks until it
// restarts.
//
// So the strip waits: once the plugin is installed and enabled, and every
// live Claude session is either on plugin (a plugin copy ran in it, see
// hookguard.go) or gone, migrateLegacyHooks runs once and the
// done record ($FLOW_ROOT/hooks/legacy-stripped) stops further checks.
// The check runs on ordinary flow invocations at most once per
// legacyStripEvery — never inside a hook. `flow skill update
// --strip-legacy` forces it.
//
// Live detection is flow's own (ps for `claude --session-id|--resume
// <id>`, plus `claude agents` in bg mode). A plain `claude` started
// without an id in argv is invisible to it; such an unbound session keeps
// no task context and loses its flow hooks only until it restarts.

// legacyStripEvery throttles the automatic check.
var legacyStripEvery = 10 * time.Minute

// liveClaudeSessions returns the live Claude session ids (lower-case).
// A var so tests can stub it.
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
	if spawner.IsBackground() {
		if bg, ok := h.(harness.BackgroundLauncher); ok {
			if agents, err := bg.BackgroundAgents(); err == nil {
				for _, a := range agents {
					if a.SessionID != "" && a.PID > 0 {
						out[strings.ToLower(a.SessionID)]++
					}
				}
			}
		}
	}
	return out, nil
}

func legacyStripDonePath() string {
	d := hooksStateDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "legacy-stripped")
}

// legacyStripDone reports whether the strip already ran (a stat).
func legacyStripDone() bool {
	p := legacyStripDonePath()
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

func recordLegacyStripDone() {
	p := legacyStripDonePath()
	if p == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = os.WriteFile(p, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600)
}

// legacyClaudeEntriesPresent reports whether ~/.claude/settings.json still
// holds any hook command an older flow wrote (exact JSON string match).
func legacyClaudeEntriesPresent() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	raw, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		return false
	}
	s := string(raw)
	for _, c := range legacyHookCommands() {
		if strings.Contains(s, `"`+c+`"`) {
			return true
		}
	}
	return false
}

// legacyHookCommands is every bare command an older flow wrote.
func legacyHookCommands() []string {
	cmds := []string{postToolUseHookCommand, rewakeHookCommand}
	for _, fh := range flowHooks() {
		cmds = append(cmds, fh.command)
	}
	return cmds
}

// maybeStripLegacyHooks is the throttled automatic check, run from Run for
// ordinary commands. Silent unless it strips.
func maybeStripLegacyHooks() {
	if os.Getenv(selfUpgradeEnv) != "" || legacyStripDone() {
		return
	}
	d := hooksStateDir()
	if d == "" {
		return
	}
	stamp := filepath.Join(d, "legacy-strip-checked")
	if fi, err := os.Stat(stamp); err == nil && time.Since(fi.ModTime()) < legacyStripEvery {
		return
	}
	_ = os.MkdirAll(d, 0o700)
	_ = os.WriteFile(stamp, nil, 0o600)
	stripLegacyClaudeHooks(false, true)
}

// inHookPath is set by Run for `flow hook` / `flow inbox` (the hook and
// rewake-waiter paths). The strip never runs there: no settings rewrite,
// no ps scan, and nothing printed into a hook's output.
var inHookPath bool

// legacyStripNotice is the one line shown when the strip runs. Claude
// sessions opened before stripAt still ran on the legacy entries only if
// flow could not see them (a plain `claude` outside flow) or a forced
// strip overrode a live one; they get flow hooks back on restart.
func legacyStripNotice(stripAt time.Time) string {
	return fmt.Sprintf("flow: hooks moved to the flow plugin; restart any Claude sessions opened before %s to get flow hooks back.",
		stripAt.Local().Format("15:04"))
}

// announceLegacyStrip prints the strip notice to stderr and leaves it for
// the human as an FYI on the bus (best-effort: errors are ignored).
func announceLegacyStrip(stripAt time.Time) {
	msg := legacyStripNotice(stripAt)
	fmt.Fprintln(os.Stderr, msg)
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
// ~/.claude/settings.json when it is safe (see the file comment), or
// unconditionally with force. It returns whether entries were removed,
// and why not otherwise.
func stripLegacyClaudeHooks(force, quiet bool) (bool, string) {
	if inHookPath {
		return false, "not inside a hook"
	}
	h, err := harnessByName(string(harness.NameClaude))
	if err != nil {
		return false, err.Error()
	}
	ph, ok := h.(harness.PluginHookInstaller)
	if !ok {
		return false, "claude harness has no plugin support"
	}
	if legacyClaudeEntriesPresent() {
		// Entries (re)appeared (e.g. an older flow re-installed them):
		// the guard must claim again until they are gone.
		if p := legacyStripDonePath(); p != "" {
			_ = os.Remove(p)
		}
	} else {
		if ph.PluginInstalled() {
			recordLegacyStripDone()
		}
		return false, "no legacy flow hooks in ~/.claude/settings.json"
	}
	if !force {
		if !ph.PluginInstalled() || ph.PluginDisabled() {
			return false, "the flow plugin is not installed or is disabled"
		}
		live, err := liveClaudeSessions()
		if err != nil {
			return false, "cannot list live Claude sessions: " + err.Error()
		}
		for id := range live {
			if !sessionOnPlugin(id) {
				return false, "live Claude session " + id + " still runs on the legacy hooks (restart it, or force with --strip-legacy)"
			}
		}
	}
	n := migrateLegacyHooks(h, true)
	recordLegacyStripDone()
	if n > 0 {
		announceLegacyStrip(time.Now())
	}
	return n > 0, ""
}
