package stats

import (
	"os"
	"path/filepath"
	"testing"
)

func TestComputeSavings(t *testing.T) {
	c := DefaultConstants() // 20 min/run, 5 min/switch, 1500 tok/kb, $100/hr
	n := Counts{AutoRuns: 3, OwnerTicks: 3, ResumeLookups: 6, RefLookups: 6, KBLookups: 2, CrossLookups: 4}
	s := ComputeSavings(c, n)

	// automation: (3+3)*20/60 = 2.0 hrs ; switch: (6+6)*5/60 = 1.0 hr
	if s.AutomationHours != 2.0 || s.ContextSwitchHours != 1.0 {
		t.Errorf("hours = %v/%v, want 2.0/1.0", s.AutomationHours, s.ContextSwitchHours)
	}
	if s.KBTokens != 3000 { // 2*1500
		t.Errorf("KBTokens = %d, want 3000", s.KBTokens)
	}
	if s.AddressableCount != 10 { // cross4 + ref6
		t.Errorf("AddressableCount = %d, want 10", s.AddressableCount)
	}
	if s.TotalHours != 3.0 || s.TotalDollars != 300.0 { // 3hrs * $100
		t.Errorf("total = %v hrs / $%v, want 3.0/300", s.TotalHours, s.TotalDollars)
	}
}

func TestLoadConstantsDefaultsAndOverride(t *testing.T) {
	dir := t.TempDir()
	// missing file → defaults
	if got := LoadConstants(filepath.Join(dir, "nope.json")); got != DefaultConstants() {
		t.Errorf("missing file should give defaults, got %+v", got)
	}
	// valid override
	p := filepath.Join(dir, "stats.json")
	if err := os.WriteFile(p, []byte(`{"minutes_per_unattended_run":30,"dollar_per_hour":150}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LoadConstants(p)
	if got.MinutesPerUnattendedRun != 30 || got.DollarPerHour != 150 {
		t.Errorf("override not applied: %+v", got)
	}
	// zero/missing fields fall back to defaults
	if got.MinutesPerContextSwitch != 5 || got.TokensPerKBLookup != 1500 {
		t.Errorf("unset fields should default: %+v", got)
	}
}

func TestLoadConstantsCorrupt(t *testing.T) {
	dir := t.TempDir()
	// corrupt file → defaults (a stderr notice is also emitted, but asserting
	// on stderr is brittle; the defaults return is the contract that matters)
	p := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(p, []byte("not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := LoadConstants(p); got != DefaultConstants() {
		t.Errorf("corrupt file should give defaults, got %+v", got)
	}
}
