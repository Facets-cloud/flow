package app

import (
	"html/template"
	"io"
	"os"

	"flow/internal/stats"
)

// cardData is the view model handed to the HTML template.
type cardData struct {
	Window                string
	LookupsCompact        string
	ContextTokensCompact  string
	TokensProcessedCompact string
	TasksDone             int
	ContextSwitchHours    float64
	ShowAutomation        bool
	AutomationRuns        int
	AutoRuns              int
	OwnerTicks            int
	PlaybookRuns          int
	TotalDollars          float64
	DollarPerHour         float64
}

var cardTmpl = template.Must(template.New("card").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<title>flow stats</title>
<style>
  body{margin:0;background:#1b1714;font-family:-apple-system,Segoe UI,Roboto,sans-serif}
  .card{width:680px;margin:32px auto;padding:48px;border-radius:20px;
        background:linear-gradient(135deg,#2a2420,#3a322b);color:#f3ede4}
  .wordmark{font-weight:800;letter-spacing:.5px;color:#e8a87c;font-size:22px}
  .sub{font-size:16px;opacity:.7;margin-top:6px}
  .big{font-size:64px;font-weight:800;margin:24px 0 0}
  .hero-sub{font-size:18px;opacity:.85;margin-top:4px}
  .grid{display:flex;gap:32px;margin-top:32px;flex-wrap:wrap}
  .stat .n{font-size:30px;font-weight:700}
  .stat .l{font-size:13px;opacity:.7}
  .mem{margin-top:28px;font-size:15px;opacity:.9}
  .auto{margin-top:20px;font-size:14px;background:rgba(232,168,124,.12);
        border-left:3px solid #e8a87c;padding:10px 14px;border-radius:6px}
  .foot{margin-top:24px;font-size:12px;opacity:.55}
</style></head><body>
<div class="card">
  <div class="wordmark">✦ flow — your AI remembered, so you didn't</div>
  <div class="sub">{{.Window}}</div>
  <div class="big">{{.LookupsCompact}}×</div>
  <div class="hero-sub">context recalls — you never re-explained</div>
  <div class="grid">
    <div class="stat"><div class="n">{{.ContextTokensCompact}}</div><div class="l">tokens never re-typed</div></div>
    <div class="stat"><div class="n">{{.TasksDone}}</div><div class="l">tasks shipped</div></div>
    <div class="stat"><div class="n">{{.TokensProcessedCompact}}</div><div class="l">tokens processed</div></div>
  </div>
  <div class="mem">≈ {{printf "%.1f" .ContextSwitchHours}} hrs of "where was I?" skipped</div>
  {{if .ShowAutomation}}<div class="auto">+ {{.AutomationRuns}} runs flow did unattended ({{.AutoRuns}} auto · {{.OwnerTicks}} owner · {{.PlaybookRuns}} playbooks)</div>{{end}}
  <div class="foot">counts exact · est. time/tokens · ≈${{printf "%.0f" .TotalDollars}} at ${{printf "%.0f" .DollarPerHour}}/hr</div>
</div>
</body></html>
`))

// renderCardHTML writes a self-contained HTML card for the stats.
func renderCardHTML(w io.Writer, s stats.Stats) error {
	autoRuns := s.AutoRuns
	ownerTicks := s.OwnerTicks
	playbookRuns := s.PlaybookRuns
	showAutomation := autoRuns+ownerTicks+playbookRuns > 0
	return cardTmpl.Execute(w, cardData{
		Window:                 s.Window,
		LookupsCompact:         humanCompact(int64(s.LookupsTotal)),
		ContextTokensCompact:   humanCompact(s.Savings.ContextTokens),
		TokensProcessedCompact: humanCompact(s.Tokens.Total()),
		TasksDone:              s.TasksDone,
		ContextSwitchHours:     s.Savings.ContextSwitchHours,
		ShowAutomation:         showAutomation,
		AutomationRuns:         autoRuns + ownerTicks + playbookRuns,
		AutoRuns:               autoRuns,
		OwnerTicks:             ownerTicks,
		PlaybookRuns:           playbookRuns,
		TotalDollars:           s.Savings.TotalDollars,
		DollarPerHour:          s.DollarPerHour,
	})
}

// writeCard renders the HTML card to a file.
func writeCard(path string, s stats.Stats) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return renderCardHTML(f, s)
}
