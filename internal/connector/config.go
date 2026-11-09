package connector

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// ConfigFile is the user's connector routing config inside the connectors
// root. It belongs to flow and the user, not to any connector, so the
// user decides the order and connector authors do not.
//
//	$FLOW_ROOT/connectors/config.json
//	  {"priority":{"flowhub":1,"macos":2},"escalate_after":"10m"}
const ConfigFile = "config.json"

// MaxEscalate bounds the escalation delay: the detached dispatcher must
// not linger longer than a day per ask.
const MaxEscalate = 24 * time.Hour

// Config is the user's routing preferences across installed connectors.
type Config struct {
	// Priority maps connector name → tier number. Lower runs earlier;
	// equal numbers share a tier and run in parallel.
	Priority map[string]int `json:"priority,omitempty"`
	// EscalateAfter is a Go duration ("10m"); "" means off.
	EscalateAfter string `json:"escalate_after,omitempty"`
}

// LoadConfig reads root/config.json. A missing file is the zero Config
// (no priorities, no escalation — i.e. today's all-in-parallel behavior).
func LoadConfig(root string) (Config, error) {
	var cfg Config
	data, err := os.ReadFile(filepath.Join(root, ConfigFile))
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", filepath.Join(root, ConfigFile), err)
	}
	return cfg, nil
}

// SaveConfig writes root/config.json atomically (temp file + rename).
func SaveConfig(root string, cfg Config) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	if len(cfg.Priority) == 0 {
		cfg.Priority = nil
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(root, ".config-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(root, ConfigFile))
}

// Escalate is the parsed escalation delay; 0 means off. An invalid stored
// value is reported as an error (callers treat it as off).
func (c Config) Escalate() (time.Duration, error) {
	if c.EscalateAfter == "" {
		return 0, nil
	}
	return ParseEscalate(c.EscalateAfter)
}

// ParseEscalate validates a user-supplied escalation delay: "off" → 0,
// otherwise a positive Go duration no longer than MaxEscalate.
func ParseEscalate(s string) (time.Duration, error) {
	if s == "off" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("escalate %q: want a duration like 10m or 1h, or off", s)
	}
	if d <= 0 {
		return 0, fmt.Errorf("escalate %q must be positive (use off to disable)", s)
	}
	if d > MaxEscalate {
		return 0, fmt.Errorf("escalate %q exceeds the 24h maximum", s)
	}
	return d, nil
}

// Tiers groups connectors into ordered tiers by priority: ascending tier
// number, equal numbers together, connectors without a priority last as
// one tier. With no priorities at all it returns a single tier holding
// every connector — exactly the pre-priority all-in-parallel dispatch.
// Order within a tier follows the input order.
func Tiers(ms []*Manifest, priority map[string]int) [][]*Manifest {
	if len(ms) == 0 {
		return nil
	}
	byN := map[int][]*Manifest{}
	var rest []*Manifest
	for _, m := range ms {
		if n, ok := priority[m.Name]; ok {
			byN[n] = append(byN[n], m)
			continue
		}
		rest = append(rest, m)
	}
	ns := make([]int, 0, len(byN))
	for n := range byN {
		ns = append(ns, n)
	}
	sort.Ints(ns)
	var out [][]*Manifest
	for _, n := range ns {
		out = append(out, byN[n])
	}
	if len(rest) > 0 {
		out = append(out, rest)
	}
	return out
}
