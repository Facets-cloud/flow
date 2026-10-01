// Package desktop opens flow task sessions in Claude Desktop's Code tab
// through the app's claude:// deep links. Claude Desktop cannot run an
// arbitrary shell command the way a terminal tab does, so this is not a
// spawner.SpawnTab backend: callers build one of two links instead.
//
//	claude://code/new?folder=<dir>&q=<prompt>  new Code session in <dir>, prompt pre-filled
//	claude://resume?session=<uuid>             import/open an existing Claude Code session
//
// The new-session link only pre-fills the prompt; the user sends it. The
// session id is chosen by Desktop, so the prompt itself binds the session
// to its task (`flow do --here`). The resume link takes a session that
// already has a transcript under ~/.claude/projects; Desktop keeps writing
// to that same transcript, so the task's stored session id stays valid.
package desktop

import (
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
)

// MaxPromptLen is Claude Desktop's cap on the q parameter of a
// claude://code/new link (the app truncates anything longer).
const MaxPromptLen = 14336

// OpenURL hands a URL to macOS LaunchServices. Package var so tests can
// capture the link instead of opening the app.
var OpenURL = func(link string) error {
	return exec.Command("open", link).Run()
}

// NewSessionURL builds the claude://code/new link for a new Code session
// in folder with prompt pre-filled.
func NewSessionURL(folder, prompt string) string {
	q := url.Values{}
	q.Set("folder", folder)
	q.Set("q", prompt)
	return "claude://code/new?" + q.Encode()
}

// ResumeURL builds the claude://resume link for an existing session.
func ResumeURL(sessionID string) string {
	q := url.Values{}
	q.Set("session", sessionID)
	return "claude://resume?" + q.Encode()
}

// OpenNewSession opens a new Claude Desktop Code session in folder with
// prompt pre-filled. It refuses prompts Desktop would truncate or reject
// rather than letting a half-delivered bootstrap prompt through.
func OpenNewSession(folder, prompt string) error {
	if folder == "" {
		return errors.New("claude desktop: new session needs a folder")
	}
	if len(prompt) > MaxPromptLen {
		return fmt.Errorf("claude desktop: prompt is %d characters, over Desktop's %d limit", len(prompt), MaxPromptLen)
	}
	if strings.HasPrefix(prompt, "/") {
		return errors.New("claude desktop: prompt cannot start with '/' (Desktop rejects slash commands in links)")
	}
	if err := OpenURL(NewSessionURL(folder, prompt)); err != nil {
		return fmt.Errorf("claude desktop: open new session: %w", err)
	}
	return nil
}

// OpenSession opens an existing Claude Code session in Claude Desktop.
func OpenSession(sessionID string) error {
	if sessionID == "" {
		return errors.New("claude desktop: resume needs a session id")
	}
	if err := OpenURL(ResumeURL(sessionID)); err != nil {
		return fmt.Errorf("claude desktop: resume session: %w", err)
	}
	return nil
}
