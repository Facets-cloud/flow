package app

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"sync"

	"github.com/fogleman/gg"
	"github.com/golang/freetype/truetype"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"

	"flow/internal/stats"
)

// cardFonts holds the parsed TrueType fonts, loaded once.
type cardFonts struct {
	bold    *truetype.Font
	regular *truetype.Font
}

var (
	cardFontsOnce sync.Once
	cardFontsVal  cardFonts
	cardFontsErr  error
)

func loadCardFonts() (cardFonts, error) {
	cardFontsOnce.Do(func() {
		b, err := truetype.Parse(gobold.TTF)
		if err != nil {
			cardFontsErr = fmt.Errorf("parse gobold: %w", err)
			return
		}
		r, err := truetype.Parse(goregular.TTF)
		if err != nil {
			cardFontsErr = fmt.Errorf("parse goregular: %w", err)
			return
		}
		cardFontsVal = cardFonts{bold: b, regular: r}
	})
	return cardFontsVal, cardFontsErr
}

func newFace(f *truetype.Font, size float64) font.Face {
	return truetype.NewFace(f, &truetype.Options{
		Size:    size,
		DPI:     96,
		Hinting: font.HintingFull,
	})
}

// cardPalette holds the colour values used throughout the PNG card.
const (
	// Background: #1b1714
	bgR, bgG, bgB = 0x1b / 255.0, 0x17 / 255.0, 0x14 / 255.0
	// Card gradient start: #2a2420
	cardStartR, cardStartG, cardStartB = 0x2a / 255.0, 0x24 / 255.0, 0x20 / 255.0
	// Card gradient end: #3a322b
	cardEndR, cardEndG, cardEndB = 0x3a / 255.0, 0x32 / 255.0, 0x2b / 255.0
	// Accent: #e8a87c
	accentR, accentG, accentB = 0xe8 / 255.0, 0xa8 / 255.0, 0x7c / 255.0
	// Primary text: #f3ede4
	primaryR, primaryG, primaryB = 0xf3 / 255.0, 0xed / 255.0, 0xe4 / 255.0
)

// renderCardPNG draws the share card and returns the resulting image.
// Layout mirrors renderCardHTML in card.go.
func renderCardPNG(s stats.Stats) (image.Image, error) {
	fonts, err := loadCardFonts()
	if err != nil {
		return nil, err
	}

	const (
		canvasW     = 1100
		cardPad     = 64.0
		cardX       = 40.0
		cardW       = canvasW - 2*cardX
		cornerR     = 24.0
		mutedAlpha  = 0.65
	)

	// Determine whether the automation band is needed.
	autoRuns := s.AutoRuns
	ownerTicks := s.OwnerTicks
	playbookRuns := s.PlaybookRuns
	showAuto := autoRuns+ownerTicks+playbookRuns > 0

	// Compute card height based on content sections.
	// Each section height is the approximate pixels it occupies.
	baseCardH := 420.0
	autoExtraH := 0.0
	if showAuto {
		autoExtraH = 80.0
	}
	cardH := baseCardH + autoExtraH
	canvasH := int(cardH) + 2*int(cardX)

	dc := gg.NewContext(canvasW, canvasH)

	// ── Background ──────────────────────────────────────────────────────────
	dc.SetRGB(bgR, bgG, bgB)
	dc.Clear()

	// ── Card gradient ───────────────────────────────────────────────────────
	grad := gg.NewLinearGradient(cardX, cardX, cardX+cardW, cardX+cardH)
	grad.AddColorStop(0, rgbColor(cardStartR, cardStartG, cardStartB))
	grad.AddColorStop(1, rgbColor(cardEndR, cardEndG, cardEndB))
	dc.DrawRoundedRectangle(cardX, float64(cardX), cardW, cardH, cornerR)
	dc.SetFillStyle(grad)
	dc.Fill()

	// ── Layout cursor ───────────────────────────────────────────────────────
	x := cardX + cardPad
	y := float64(cardX) + cardPad

	// ── Wordmark line ────────────────────────────────────────────────────────
	// Draw a small filled accent circle as the mark (bullet).
	dc.SetRGB(accentR, accentG, accentB)
	circR := 6.0
	// Vertically centre the circle with the text baseline at y+circR.
	// Bold 22pt face: ascent ~20px at 96dpi.
	dc.DrawCircle(x+circR, y+14.0, circR)
	dc.Fill()

	face22bold := newFace(fonts.bold, 22)
	dc.SetFontFace(face22bold)
	dc.SetRGB(accentR, accentG, accentB)
	dc.DrawString("flow - your AI remembered, so you didn't", x+circR*2+8, y+20)
	y += 36

	// ── Window label ─────────────────────────────────────────────────────────
	face14 := newFace(fonts.regular, 14)
	dc.SetFontFace(face14)
	dc.SetRGBA(primaryR, primaryG, primaryB, mutedAlpha)
	dc.DrawString(s.Window, x, y)
	y += 28

	// ── Hero number ──────────────────────────────────────────────────────────
	face64bold := newFace(fonts.bold, 64)
	dc.SetFontFace(face64bold)
	dc.SetRGB(primaryR, primaryG, primaryB)
	heroText := humanCompact(int64(s.LookupsTotal)) + "x"
	dc.DrawString(heroText, x, y+60)
	y += 80

	// ── Hero sub ─────────────────────────────────────────────────────────────
	face18 := newFace(fonts.regular, 18)
	dc.SetFontFace(face18)
	dc.SetRGBA(primaryR, primaryG, primaryB, 0.85)
	dc.DrawString("context recalls - you never re-explained", x, y)
	y += 40

	// ── Stat row ─────────────────────────────────────────────────────────────
	face13 := newFace(fonts.regular, 13)
	face28bold := newFace(fonts.bold, 28)
	statItems := []struct{ val, label string }{
		{humanCompact(s.Savings.ContextTokens), "tokens never re-typed"},
		{fmt.Sprintf("%d", s.TasksDone), "tasks shipped"},
		{humanCompact(s.Tokens.Total()), "tokens processed"},
	}
	colW := (cardW - 2*cardPad) / float64(len(statItems))
	for i, item := range statItems {
		sx := x + float64(i)*colW
		dc.SetFontFace(face28bold)
		dc.SetRGB(primaryR, primaryG, primaryB)
		dc.DrawString(item.val, sx, y+28)
		dc.SetFontFace(face13)
		dc.SetRGBA(primaryR, primaryG, primaryB, mutedAlpha)
		dc.DrawString(item.label, sx, y+46)
	}
	y += 68

	// ── Resume line ──────────────────────────────────────────────────────────
	resumeCount := s.LookupsByKind[stats.LookupResume]
	face15 := newFace(fonts.regular, 15)
	dc.SetFontFace(face15)
	dc.SetRGBA(primaryR, primaryG, primaryB, 0.9)
	resumeText := fmt.Sprintf("%d instant resumes - straight back into work, in context not from scratch", resumeCount)
	dc.DrawStringWrapped(resumeText, x, y, 0, 0, cardW-2*cardPad, 1.4, gg.AlignLeft)
	y += 40

	// ── Automation band (conditional) ────────────────────────────────────────
	if showAuto {
		// Thin separator rule.
		y += 8
		dc.SetRGBA(primaryR, primaryG, primaryB, 0.2)
		dc.SetLineWidth(1)
		dc.DrawLine(x, y, cardX+cardW-cardPad, y)
		dc.Stroke()
		y += 14

		totalRuns := autoRuns + ownerTicks + playbookRuns
		dollars := humanInt(int64(s.Savings.AutomationHours * s.DollarPerHour))
		autoText := fmt.Sprintf("+ %d runs flow did unattended (%d auto, %d owner, %d playbooks)  ~$%s",
			totalRuns, autoRuns, ownerTicks, playbookRuns, dollars)
		face14auto := newFace(fonts.regular, 14)
		dc.SetFontFace(face14auto)
		dc.SetRGB(accentR, accentG, accentB)
		dc.DrawStringWrapped(autoText, x, y, 0, 0, cardW-2*cardPad, 1.4, gg.AlignLeft)
		y += 36
	}

	// ── Footer ───────────────────────────────────────────────────────────────
	y = float64(cardX) + cardH - 32
	face12 := newFace(fonts.regular, 12)
	dc.SetFontFace(face12)
	dc.SetRGBA(primaryR, primaryG, primaryB, 0.55)
	dc.DrawString("counts exact - est. time/tokens", x, y)

	return dc.Image(), nil
}

// writeCardPNG renders the PNG card and writes it to path.
func writeCardPNG(path string, s stats.Stats) error {
	img, err := renderCardPNG(s)
	if err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

// rgbColor returns a color.NRGBA from normalised [0,1] float components (fully opaque).
func rgbColor(r, g, b float64) color.Color {
	return color.NRGBA{
		R: uint8(r * 255),
		G: uint8(g * 255),
		B: uint8(b * 255),
		A: 255,
	}
}
