package app

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"sync"

	"github.com/fogleman/gg"
	"github.com/golang/freetype/truetype"
	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"

	"flow/internal/stats"
)

// flowWordmarkPNG is the official flow wordmark (light "flo" + gradient
// wave "w"), pre-rendered with a transparent background, embedded so the
// PNG card is fully self-contained.
//
//go:embed assets/flow-wordmark.png
var flowWordmarkPNG []byte

// flow brand palette (from site/styles.css :root, dark theme).
var (
	cPageBg    = hexColor("#0B0D10") // --bg-deep
	cCardTop   = hexColor("#1C2128") // --bg-paper-2 (card gradient top)
	cCardBot   = hexColor("#14171D") // --bg-elev   (card gradient bottom)
	cBorder    = hexColor("#30363D") // --rule
	cFg        = hexColor("#E6EDF3") // --fg
	cFgSoft    = hexColor("#C9D1D9") // --fg-soft
	cFgMuted   = hexColor("#8B949E") // --fg-muted
	cFgDim     = hexColor("#6E7681") // --fg-dim
	cGrad1     = hexColor("#0AEAA8") // wave gradient stops
	cGrad2     = hexColor("#0ADCCE")
	cGrad3     = hexColor("#198FFF")
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

	wordmarkOnce sync.Once
	wordmarkImg  image.Image
	wordmarkErr  error
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

func loadWordmark() (image.Image, error) {
	wordmarkOnce.Do(func() {
		wordmarkImg, wordmarkErr = png.Decode(bytes.NewReader(flowWordmarkPNG))
	})
	return wordmarkImg, wordmarkErr
}

func newFace(f *truetype.Font, size float64) font.Face {
	return truetype.NewFace(f, &truetype.Options{Size: size, DPI: 96, Hinting: font.HintingFull})
}

// renderCardPNG draws the flow-branded share card and returns the image.
func renderCardPNG(s stats.Stats) (image.Image, error) {
	fonts, err := loadCardFonts()
	if err != nil {
		return nil, err
	}
	logo, err := loadWordmark()
	if err != nil {
		return nil, err
	}

	const (
		canvasW = 940
		cardX   = 40.0
		cardPad = 56.0
		cardW   = canvasW - 2*cardX
		cornerR = 16.0
	)
	logoH := 34.0

	// Build the gradient hero up front so its real height drives layout.
	heroText := humanCompact(int64(s.LookupsTotal)) + "×"
	heroImg := gradientText(heroText, newFace(fonts.bold, 60))
	heroH := float64(heroImg.Bounds().Dy())

	showAuto := s.AutoRuns+s.OwnerTicks+s.PlaybookRuns > 0
	autoBlock := 0.0
	if showAuto {
		autoBlock = 10 + 26 + 30
	}
	// Sum of vertical advances (mirrors the draw sequence below).
	cardH := cardPad + (logoH + 20) + 28 + (heroH + 10) + 42 + 80 + 30 + autoBlock + 18 + 14 + 30
	canvasH := int(cardH) + 2*int(cardX)

	dc := gg.NewContext(canvasW, canvasH)

	// ── Page background ───────────────────────────────────────────────
	dc.SetColor(cPageBg)
	dc.Clear()

	// ── Card surface (subtle vertical gradient) + border ──────────────
	grad := gg.NewLinearGradient(0, cardX, 0, cardX+cardH)
	grad.AddColorStop(0, cCardTop)
	grad.AddColorStop(1, cCardBot)
	dc.NewSubPath()
	dc.DrawRoundedRectangle(cardX, cardX, cardW, cardH, cornerR)
	dc.SetFillStyle(grad)
	dc.FillPreserve()
	dc.SetColor(cBorder)
	dc.SetLineWidth(1.5)
	dc.Stroke()

	x := cardX + cardPad
	y := cardX + cardPad

	// ── Wordmark logo (top-left) + window label (top-right) ───────────
	lb := logo.Bounds()
	logoW := logoH * float64(lb.Dx()) / float64(lb.Dy())
	scaled := scaleImage(logo, int(logoW+0.5), int(logoH+0.5))
	dc.DrawImage(scaled, int(x), int(y))

	face13 := newFace(fonts.regular, 13)
	dc.SetFontFace(face13)
	dc.SetColor(cFgDim)
	dc.DrawStringAnchored(s.Window, cardX+cardW-cardPad, y+logoH/2, 1, 0.4)

	y += logoH + 20

	// ── Tagline ───────────────────────────────────────────────────────
	face15 := newFace(fonts.regular, 15)
	dc.SetFontFace(face15)
	dc.SetColor(cFgMuted)
	dc.DrawString("your AI remembered, so you didn't", x, y)
	y += 28

	// ── Hero number (brand gradient) ──────────────────────────────────
	dc.DrawImage(heroImg, int(x), int(y))
	y += heroH + 10

	// ── Hero sub ──────────────────────────────────────────────────────
	face18 := newFace(fonts.regular, 18)
	dc.SetFontFace(face18)
	dc.SetColor(cFgSoft)
	dc.DrawString("context recalls you never re-explained", x, y)
	y += 42

	// ── Stat row ──────────────────────────────────────────────────────
	face26 := newFace(fonts.bold, 26)
	statItems := []struct{ val, label string }{
		{humanCompact(s.Savings.ContextTokens), "tokens never retyped"},
		{fmt.Sprintf("%d", s.TasksDone), "tasks shipped"},
		{humanCompact(s.Tokens.Total()), "tokens processed"},
	}
	colW := (cardW - 2*cardPad) / float64(len(statItems))
	for i, item := range statItems {
		sx := x + float64(i)*colW
		dc.SetFontFace(face26)
		dc.SetColor(cFg)
		dc.DrawString(item.val, sx, y+26)
		dc.SetFontFace(face13)
		dc.SetColor(cFgMuted)
		dc.DrawString(item.label, sx, y+46)
	}
	y += 80

	// ── Resume line ───────────────────────────────────────────────────
	resumeCount := s.LookupsByKind[stats.LookupResume]
	dc.SetFontFace(face15)
	dc.SetColor(cFgSoft)
	dc.DrawString(fmt.Sprintf("%d instant resumes, straight back into work in context, not from scratch", resumeCount), x, y)
	y += 30

	// ── Automation band (conditional) ─────────────────────────────────
	if showAuto {
		y += 10
		dc.SetColor(cBorder)
		dc.SetLineWidth(1)
		dc.DrawLine(x, y, cardX+cardW-cardPad, y)
		dc.Stroke()
		y += 26

		totalRuns := s.AutoRuns + s.OwnerTicks + s.PlaybookRuns
		dollars := humanInt(int64(s.Savings.AutomationHours * s.DollarPerHour))
		face14 := newFace(fonts.regular, 14)
		dc.SetFontFace(face14)
		dc.SetColor(cFgMuted)
		dc.DrawString(fmt.Sprintf("+ %d runs flow did unattended (%d auto · %d owner · %d playbooks)   ~$%s",
			totalRuns, s.AutoRuns, s.OwnerTicks, s.PlaybookRuns, dollars), x, y)
		y += 30
	}

	// ── Footer ────────────────────────────────────────────────────────
	y += 18
	face12 := newFace(fonts.regular, 12)
	dc.SetFontFace(face12)
	dc.SetColor(cFgDim)
	dc.DrawString("counts exact · est. time/tokens", x, y)

	return dc.Image(), nil
}

// gradientText renders text filled with the flow wave gradient onto a
// tightly-sized transparent image.
func gradientText(text string, face font.Face) image.Image {
	measure := gg.NewContext(8, 8)
	measure.SetFontFace(face)
	w, h := measure.MeasureString(text)
	W := int(w + 8)
	H := int(h*1.18 + 4)
	baseline := h * 0.95 // approximate baseline within the box

	// Text as a white alpha mask.
	mc := gg.NewContext(W, H)
	mc.SetFontFace(face)
	mc.SetRGB(1, 1, 1)
	mc.DrawString(text, 4, baseline)

	out := gg.NewContext(W, H)
	out.SetMask(mc.AsMask())
	g := gg.NewLinearGradient(0, 0, float64(W), 0)
	g.AddColorStop(0, cGrad1)
	g.AddColorStop(0.55, cGrad2)
	g.AddColorStop(1, cGrad3)
	out.SetFillStyle(g)
	out.DrawRectangle(0, 0, float64(W), float64(H))
	out.Fill()
	return out.Image()
}

// scaleImage resizes src to w×h using high-quality resampling.
func scaleImage(src image.Image, w, h int) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	return dst
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

// hexColor parses "#RRGGBB" into an opaque color.
func hexColor(s string) color.Color {
	var r, g, b uint8
	fmt.Sscanf(s, "#%02x%02x%02x", &r, &g, &b)
	return color.NRGBA{R: r, G: g, B: b, A: 255}
}
