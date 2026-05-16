package server

import (
	"flow/internal/flowdb"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

type uiToolCapability struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Path      string `json:"path,omitempty"`
}

type uiCapabilities struct {
	Providers []uiToolCapability `json:"providers"`
	Terminals []uiToolCapability `json:"terminals"`
}

func detectCapabilities() uiCapabilities {
	return uiCapabilities{
		Providers: []uiToolCapability{
			binaryCapability("claude", "Claude Code", "claude"),
			binaryCapability("codex", "Codex", "codex"),
		},
		Terminals: []uiToolCapability{
			macAppCapability("iterm", "iTerm", []string{
				"/Applications/iTerm.app",
				"/Applications/iTerm2.app",
				filepath.Join(homeDirOrEmpty(), "Applications", "iTerm.app"),
				filepath.Join(homeDirOrEmpty(), "Applications", "iTerm2.app"),
			}, "requires iTerm2.app and osascript"),
			macAppCapability("terminal", "Terminal.app", []string{
				"/System/Applications/Utilities/Terminal.app",
				"/Applications/Utilities/Terminal.app",
			}, "requires Terminal.app and osascript"),
			macAppCapability("warp", "Warp", []string{
				"/Applications/Warp.app",
				filepath.Join(homeDirOrEmpty(), "Applications", "Warp.app"),
			}, "requires Warp.app and osascript"),
			binaryCapability("kitty", "kitty", "kitty"),
			binaryCapability("alacritty", "Alacritty", "alacritty"),
			binaryCapability("ghostty", "Ghostty", "ghostty"),
			binaryCapability("wezterm", "WezTerm", "wezterm"),
			binaryCapability("tmux", "tmux", "tmux"),
			binaryCapability("vscode", "VS Code", "code"),
		},
	}
}

func binaryCapability(id, label, bin string) uiToolCapability {
	path, err := exec.LookPath(bin)
	if err != nil {
		return uiToolCapability{ID: id, Label: label, Available: false, Reason: bin + " not found on PATH"}
	}
	return uiToolCapability{ID: id, Label: label, Available: true, Path: path}
}

func macAppCapability(id, label string, appPaths []string, missingReason string) uiToolCapability {
	if runtime.GOOS != "darwin" {
		return uiToolCapability{ID: id, Label: label, Available: false, Reason: "macOS only"}
	}
	if _, err := exec.LookPath("osascript"); err != nil {
		return uiToolCapability{ID: id, Label: label, Available: false, Reason: "osascript not found on PATH"}
	}
	for _, path := range appPaths {
		if path == "" {
			continue
		}
		if st, err := os.Stat(path); err == nil && st.IsDir() {
			return uiToolCapability{ID: id, Label: label, Available: true, Path: path}
		}
	}
	return uiToolCapability{ID: id, Label: label, Available: false, Reason: missingReason}
}

func homeDirOrEmpty() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

func capabilityByID(items []uiToolCapability, id string) uiToolCapability {
	for _, item := range items {
		if item.ID == id {
			return item
		}
	}
	return uiToolCapability{ID: id, Label: id, Available: false, Reason: "unsupported"}
}

func (s *Server) availableProvider(raw string) (string, error) {
	provider, err := flowdb.NormalizeSessionProvider(raw)
	if err != nil {
		return "", err
	}
	caps := detectCapabilities()
	capability := capabilityByID(caps.Providers, provider)
	if capability.Available {
		return provider, nil
	}
	if raw == "" {
		for _, alt := range caps.Providers {
			if alt.Available {
				return alt.ID, nil
			}
		}
	}
	if capability.Reason == "" {
		capability.Reason = "not available"
	}
	return "", fmt.Errorf("%s is unavailable: %s", capability.Label, capability.Reason)
}

func (s *Server) ensureProviderAvailable(provider string) error {
	capability := capabilityByID(detectCapabilities().Providers, provider)
	if capability.Available {
		return nil
	}
	if capability.Reason == "" {
		capability.Reason = "not available"
	}
	return fmt.Errorf("%s is unavailable: %s", capability.Label, capability.Reason)
}

func (s *Server) ensureTerminalAvailable(kind string) error {
	capability := capabilityByID(detectCapabilities().Terminals, kind)
	if capability.Available {
		return nil
	}
	if capability.Reason == "" {
		capability.Reason = "not available"
	}
	return fmt.Errorf("%s is unavailable: %s", capability.Label, capability.Reason)
}
