package connector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigMissingIsZero(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Priority) != 0 || cfg.EscalateAfter != "" {
		t.Errorf("cfg = %+v, want zero", cfg)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	root := filepath.Join(t.TempDir(), "connectors")
	cfg := Config{Priority: map[string]int{"flowhub": 1, "macos": 2}, EscalateAfter: "10m"}
	if err := SaveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, ConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"escalate_after": "10m"`) || !strings.Contains(string(raw), `"flowhub": 1`) {
		t.Errorf("config.json = %s", raw)
	}
	got, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.Priority["flowhub"] != 1 || got.Priority["macos"] != 2 || got.EscalateAfter != "10m" {
		t.Errorf("got %+v", got)
	}
	if d, _ := got.Escalate(); d != 10*time.Minute {
		t.Errorf("Escalate = %v", d)
	}
}

func TestConfigCorruptIsError(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ConfigFile), []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(root); err == nil {
		t.Error("corrupt config.json accepted")
	}
}

func TestParseEscalate(t *testing.T) {
	for in, want := range map[string]time.Duration{"off": 0, "10m": 10 * time.Minute, "24h": 24 * time.Hour, "90s": 90 * time.Second} {
		got, err := ParseEscalate(in)
		if err != nil || got != want {
			t.Errorf("ParseEscalate(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "0s", "-5m", "25h", "soon", "10"} {
		if _, err := ParseEscalate(bad); err == nil {
			t.Errorf("ParseEscalate(%q) accepted", bad)
		}
	}
}

func mf(names ...string) []*Manifest {
	var out []*Manifest
	for _, n := range names {
		out = append(out, &Manifest{Name: n})
	}
	return out
}

func tierNames(tiers [][]*Manifest) string {
	var parts []string
	for _, tier := range tiers {
		var ns []string
		for _, m := range tier {
			ns = append(ns, m.Name)
		}
		parts = append(parts, strings.Join(ns, ","))
	}
	return strings.Join(parts, " | ")
}

func TestTiers(t *testing.T) {
	cases := []struct {
		prio map[string]int
		want string
	}{
		{nil, "a,b,c"}, // no priorities: one parallel tier (today)
		{map[string]int{"c": 1, "a": 2, "b": 3}, "c | a | b"}, // strict order
		{map[string]int{"a": 1, "c": 1, "b": 2}, "a,c | b"},   // same n = one tier
		{map[string]int{"b": 5}, "b | a,c"},                   // unprioritized go last, together
		{map[string]int{"zz": 1}, "a,b,c"},                    // entries for absent connectors ignored
	}
	for _, c := range cases {
		if got := tierNames(Tiers(mf("a", "b", "c"), c.prio)); got != c.want {
			t.Errorf("Tiers(%v) = %q, want %q", c.prio, got, c.want)
		}
	}
	if got := Tiers(nil, map[string]int{"a": 1}); len(got) != 0 {
		t.Errorf("Tiers(no connectors) = %v", got)
	}
}
