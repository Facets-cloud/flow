package server

import (
	"fmt"
)

// slackTaskOpener adapts the in-server browser-terminal bridge to the
// monitor.TaskOpener interface. The Slack listener uses this so newly
// spawned slack-reply tasks attach to a PTY managed by the server (and
// streamed to the UI's terminal panel) instead of opening an iTerm tab.
type slackTaskOpener struct {
	server *Server
}

// OpenInUI ensures the task is startable, sets the provider to claude,
// and attaches it to a managed PTY so the UI's terminal bridge can stream
// the session. The PTY starts immediately — the user can attach the UI
// pane whenever they get to it; Claude bootstraps in the background and
// processes the inbox.jsonl as soon as the agent is up.
//
// The attach uses a default 120x32 size which the UI will renegotiate
// when its WebSocket connects with the actual viewport size.
func (o *slackTaskOpener) OpenInUI(slug string) error {
	if o == nil || o.server == nil {
		return fmt.Errorf("slack opener: server not wired")
	}
	// openBrowserTerminalBridge validates the task, sets provider, and
	// returns the "bridge ready" actionResponse. We discard the response
	// because there's no UI request to answer — the user will pick it up
	// out-of-band when they open the UI.
	resp, _ := o.server.openBrowserTerminalBridge(slug, "claude")
	if !resp.OK {
		return fmt.Errorf("slack opener: bridge prep: %s", resp.Message)
	}
	// Attach starts the PTY in the terminal hub. cols/rows are sane
	// defaults; the UI sends a resize on first WS connect.
	if _, err := o.server.terminals.attach(slug, 120, 32); err != nil {
		return fmt.Errorf("slack opener: attach pty: %w", err)
	}
	return nil
}
