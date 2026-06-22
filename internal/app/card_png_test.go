package app

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"flow/internal/stats"
)

// statsWithAutomation returns a Stats value with non-zero automation counters.
func statsWithAutomation() stats.Stats {
	return stats.Stats{
		Window:       "all-time",
		LookupsTotal: 42,
		LookupsByKind: map[stats.LookupKind]int{
			stats.LookupResume:    10,
			stats.LookupReference: 8,
			stats.LookupCrossTask: 6,
			stats.LookupKB:        18,
		},
		TasksDone:    7,
		AutoRuns:     3,
		OwnerTicks:   5,
		PlaybookRuns: 2,
		Savings: stats.Savings{
			ContextTokens:   125000,
			AutomationHours: 2.5,
		},
		DollarPerHour: 100,
		Tokens: stats.Usage{
			Input:  500000,
			Output: 100000,
		},
	}
}

// statsWithoutAutomation returns a Stats value where automation is zero.
func statsWithoutAutomation() stats.Stats {
	return stats.Stats{
		Window:       "since 2026-01-01",
		LookupsTotal: 15,
		LookupsByKind: map[stats.LookupKind]int{
			stats.LookupResume: 5,
		},
		TasksDone:    2,
		AutoRuns:     0,
		OwnerTicks:   0,
		PlaybookRuns: 0,
		Savings: stats.Savings{
			ContextTokens: 30000,
		},
		DollarPerHour: 100,
		Tokens: stats.Usage{
			Input:  80000,
			Output: 20000,
		},
	}
}

// TestRenderCardPNG verifies that renderCardPNG returns a non-nil image with
// positive dimensions for both automation-present and automation-absent Stats,
// and that the automation-present image is taller (proving the conditional band).
func TestRenderCardPNG(t *testing.T) {
	withAuto := statsWithAutomation()
	withoutAuto := statsWithoutAutomation()

	imgAuto, err := renderCardPNG(withAuto)
	if err != nil {
		t.Fatalf("renderCardPNG (with automation) error: %v", err)
	}
	if imgAuto == nil {
		t.Fatal("renderCardPNG returned nil image (with automation)")
	}
	b := imgAuto.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		t.Errorf("with automation: expected positive dimensions, got %dx%d", b.Dx(), b.Dy())
	}

	imgNoAuto, err := renderCardPNG(withoutAuto)
	if err != nil {
		t.Fatalf("renderCardPNG (without automation) error: %v", err)
	}
	if imgNoAuto == nil {
		t.Fatal("renderCardPNG returned nil image (without automation)")
	}
	b2 := imgNoAuto.Bounds()
	if b2.Dx() <= 0 || b2.Dy() <= 0 {
		t.Errorf("without automation: expected positive dimensions, got %dx%d", b2.Dx(), b2.Dy())
	}

	// The automation-present card must be taller than the automation-absent card.
	if imgAuto.Bounds().Dy() <= imgNoAuto.Bounds().Dy() {
		t.Errorf("expected automation card height %d > no-automation card height %d",
			imgAuto.Bounds().Dy(), imgNoAuto.Bounds().Dy())
	}
}

// TestWriteCardPNG verifies that writeCardPNG writes a valid PNG file with
// sane dimensions that can be re-decoded.
func TestWriteCardPNG(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "card.png")

	s := statsWithAutomation()
	if err := writeCardPNG(path, s); err != nil {
		t.Fatalf("writeCardPNG error: %v", err)
	}

	// Re-open and decode to verify it's a valid PNG.
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open written PNG: %v", err)
	}
	defer f.Close()

	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("png.Decode error: %v", err)
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		t.Errorf("decoded PNG has non-positive dimensions: %dx%d", b.Dx(), b.Dy())
	}

	// Also check DecodeConfig for sane width/height.
	f2, err := os.Open(path)
	if err != nil {
		t.Fatalf("re-open for DecodeConfig: %v", err)
	}
	defer f2.Close()

	cfg, err := png.DecodeConfig(f2)
	if err != nil {
		t.Fatalf("png.DecodeConfig error: %v", err)
	}
	if cfg.Width < 100 || cfg.Height < 100 {
		t.Errorf("PNG config dimensions too small: %dx%d", cfg.Width, cfg.Height)
	}
}
