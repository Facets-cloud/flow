package stats

import (
	"encoding/json"
	"fmt"
	"os"
)

// Constants are the user-editable counterfactual multipliers. They live in
// ~/.flow/stats.json (JSON, not TOML — the repo has no TOML dependency).
type Constants struct {
	MinutesPerUnattendedRun float64 `json:"minutes_per_unattended_run"`
	MinutesPerContextSwitch float64 `json:"minutes_per_context_switch"`
	DollarPerHour           float64 `json:"dollar_per_hour"`
}

// DefaultConstants are the shipped defaults.
func DefaultConstants() Constants {
	return Constants{
		MinutesPerUnattendedRun: 20,
		MinutesPerContextSwitch: 5,
		DollarPerHour:           100,
	}
}

// LoadConstants reads ~/.flow/stats.json. A missing file yields defaults
// silently; a corrupt file yields defaults with a stderr notice. Any field
// ≤ 0 (including unset) falls back to its individual default.
func LoadConstants(path string) Constants {
	def := DefaultConstants()
	data, err := os.ReadFile(path)
	if err != nil {
		return def
	}
	var c Constants
	if err := json.Unmarshal(data, &c); err != nil {
		fmt.Fprintf(os.Stderr, "warning: stats.json is malformed (%v); using defaults\n", err)
		return def
	}
	if c.MinutesPerUnattendedRun <= 0 {
		c.MinutesPerUnattendedRun = def.MinutesPerUnattendedRun
	}
	if c.MinutesPerContextSwitch <= 0 {
		c.MinutesPerContextSwitch = def.MinutesPerContextSwitch
	}
	if c.DollarPerHour <= 0 {
		c.DollarPerHour = def.DollarPerHour
	}
	return c
}

// Counts are the raw inputs to the savings model.
type Counts struct {
	AutoRuns      int
	OwnerTicks    int
	ResumeLookups int
	RefLookups    int
	CrossLookups  int
}

// Savings are the counterfactual estimates. AddressableCount is a count,
// NOT time/$ — it must never be summed into TotalHours/TotalDollars.
// ContextTokens is set by BuildStats from real file sizes after ComputeSavings.
type Savings struct {
	AutomationHours    float64
	ContextSwitchHours float64
	ContextTokens      int64
	AddressableCount   int
	TotalHours         float64
	TotalDollars       float64
}

// ComputeSavings applies the counterfactual model. TotalHours sums only the
// two time-valued levers (automation + context-switch); ContextTokens (tokens)
// and AddressableCount (a count) are reported separately by design.
// ContextTokens is NOT computed here — it is derived from real file sizes by
// BuildStats and assigned after this call.
func ComputeSavings(c Constants, n Counts) Savings {
	auto := float64(n.AutoRuns+n.OwnerTicks) * c.MinutesPerUnattendedRun / 60.0
	sw := float64(n.ResumeLookups+n.RefLookups) * c.MinutesPerContextSwitch / 60.0
	total := auto + sw
	return Savings{
		AutomationHours:    auto,
		ContextSwitchHours: sw,
		AddressableCount:   n.CrossLookups + n.RefLookups,
		TotalHours:         total,
		TotalDollars:       total * c.DollarPerHour,
	}
}
