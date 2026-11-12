// Package connector implements the connector contract: an opt-in folder
// (connector.json + an executable) that flow invokes to ping the human
// about mail addressed to them (role "human") or to carry a message to
// another flow hub (role "peer"). See the skill references
// references/connectors.md and references/peers.md for the public
// contract.
//
//	$FLOW_ROOT/connectors/<name>/
//	  connector.json  {"name","contract":2,"roles":["human"|"peer",...],
//	                   "exec":"./run","capabilities":["send"],
//	                   "events":["ask","urgent"]}   (events: human role)
//	  run             any executable: `run send` / `run test`, JSON on stdin
//
// Contract 1 manifests (no roles) keep working and mean roles ["human"].
package connector

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ManifestFile is the manifest's file name inside a connector folder.
const ManifestFile = "connector.json"

// Contract is the newest contract version this flow speaks; contract 1
// manifests are still accepted (as role "human").
const Contract = 2

// Roles a contract-2 connector may take.
const (
	RoleHuman = "human" // pings the local human about their mail
	RolePeer  = "peer"  // carries messages to other flow hubs
)

// Events a connector may subscribe to.
const (
	EventAsk    = "ask"    // a normal human ask
	EventUrgent = "urgent" // an --urgent human ask
)

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Manifest is a parsed, validated connector.json.
type Manifest struct {
	Name         string   `json:"name"`
	Contract     int      `json:"contract"`
	Roles        []string `json:"roles,omitempty"`
	Exec         string   `json:"exec"`
	Capabilities []string `json:"capabilities"`
	Events       []string `json:"events"`
	// Dir is the connector folder the manifest was loaded from.
	Dir string `json:"-"`
}

// ExecPath is the absolute path of the connector's executable.
func (m *Manifest) ExecPath() string {
	return filepath.Join(m.Dir, filepath.FromSlash(m.Exec))
}

// Wants reports whether the connector subscribed to an event.
func (m *Manifest) Wants(event string) bool {
	for _, e := range m.Events {
		if e == event {
			return true
		}
	}
	return false
}

// EffectiveRoles is the connector's roles; a contract-1 manifest (no
// roles) is ["human"].
func (m *Manifest) EffectiveRoles() []string {
	if len(m.Roles) == 0 {
		return []string{RoleHuman}
	}
	return m.Roles
}

// HasRole reports whether the connector takes role r.
func (m *Manifest) HasRole(r string) bool {
	for _, x := range m.EffectiveRoles() {
		if x == r {
			return true
		}
	}
	return false
}

// Load reads and validates the manifest in dir.
func Load(dir string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", ManifestFile, err)
	}
	m := &Manifest{}
	if err := json.Unmarshal(data, m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", ManifestFile, err)
	}
	m.Dir = dir
	if err := m.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, ManifestFile), err)
	}
	return m, nil
}

func (m *Manifest) validate() error {
	if !nameRe.MatchString(m.Name) {
		return fmt.Errorf("name %q must be lowercase letters, digits, '-' or '_'", m.Name)
	}
	switch m.Contract {
	case 1:
		if len(m.Roles) != 0 {
			return fmt.Errorf(`roles need "contract": 2 (contract 1 is role human only)`)
		}
	case 2:
		if len(m.Roles) == 0 {
			return fmt.Errorf(`contract 2 needs "roles": ["human"], ["peer"] or ["human","peer"]`)
		}
		for _, r := range m.Roles {
			if r != RoleHuman && r != RolePeer {
				return fmt.Errorf("unknown role %q (contract 2 knows %q and %q)", r, RoleHuman, RolePeer)
			}
		}
	default:
		return fmt.Errorf("contract %d unsupported (this flow speaks contracts 1 and %d)", m.Contract, Contract)
	}
	if m.Exec == "" {
		return fmt.Errorf(`exec is required (e.g. "./run")`)
	}
	if filepath.IsAbs(m.Exec) {
		return fmt.Errorf("exec %q must be relative to the connector folder", m.Exec)
	}
	if rel := filepath.Clean(filepath.FromSlash(m.Exec)); rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("exec %q escapes the connector folder", m.Exec)
	}
	fi, err := os.Stat(m.ExecPath())
	if err != nil {
		return fmt.Errorf("exec %q: %w", m.Exec, err)
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("exec %q is not an executable file (chmod +x it)", m.Exec)
	}
	hasSend := false
	for _, c := range m.Capabilities {
		if c == "send" {
			hasSend = true
		}
	}
	if !hasSend {
		return fmt.Errorf(`capabilities must include "send"`)
	}
	if m.HasRole(RoleHuman) && len(m.Events) == 0 {
		return fmt.Errorf(`events must list at least one of "ask", "urgent"`)
	}
	for _, e := range m.Events {
		if e != EventAsk && e != EventUrgent {
			return fmt.Errorf("unknown event %q (the contract knows %q and %q)", e, EventAsk, EventUrgent)
		}
	}
	return nil
}

// List returns the valid connectors installed under root (one folder per
// connector, named after it), sorted by name, plus an error for each
// folder that is not a valid connector. A missing root means none.
func List(root string) ([]*Manifest, []error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []error{err}
	}
	var out []*Manifest
	var errs []error
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(dir, ManifestFile)); os.IsNotExist(err) {
			continue // not a connector folder
		}
		m, err := Load(dir)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if m.Name != e.Name() {
			errs = append(errs, fmt.Errorf("%s: folder name %q does not match manifest name %q", dir, e.Name(), m.Name))
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, errs
}
