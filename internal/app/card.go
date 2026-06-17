package app

import (
	"html/template"
	"io"
	"os"

	"flow/internal/stats"
)

// cardData is the view model handed to the HTML template.
type cardData struct {
	Window       string
	Lookups      int
	Tokens       int64
	TasksDone    int
	AutoRuns     int
	OwnerTicks   int
	TotalHours   float64
	TotalDollars float64
}

var cardTmpl = template.Must(template.New("card").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<title>flow stats</title>
<style>
  body{margin:0;background:#1b1714;font-family:-apple-system,Segoe UI,Roboto,sans-serif}
  .card{width:680px;margin:32px auto;padding:48px;border-radius:20px;
        background:linear-gradient(135deg,#2a2420,#3a322b);color:#f3ede4}
  .wordmark{font-weight:800;letter-spacing:.5px;color:#e8a87c;font-size:22px}
  .head{font-size:18px;opacity:.7;margin-top:24px}
  .big{font-size:64px;font-weight:800;margin:8px 0}
  .sub{font-size:16px;opacity:.85}
  .grid{display:flex;gap:32px;margin-top:32px;flex-wrap:wrap}
  .stat .n{font-size:30px;font-weight:700}
  .stat .l{font-size:13px;opacity:.7}
  .est{margin-top:28px;font-size:15px;opacity:.9}
  .foot{margin-top:24px;font-size:12px;opacity:.55}
</style></head><body>
<div class="card">
  <div class="wordmark">✦ flow</div>
  <div class="head">{{.Window}} · flow served you stored context</div>
  <div class="big">{{.Lookups}}×</div>
  <div class="sub">times it recalled context so you didn't have to.</div>
  <div class="grid">
    <div class="stat"><div class="n">{{.Tokens}}</div><div class="l">tokens processed</div></div>
    <div class="stat"><div class="n">{{.TasksDone}}</div><div class="l">tasks done</div></div>
    <div class="stat"><div class="n">{{.AutoRuns}}</div><div class="l">auto runs</div></div>
    <div class="stat"><div class="n">{{.OwnerTicks}}</div><div class="l">owner ticks</div></div>
  </div>
  <div class="est">≈ {{printf "%.1f" .TotalHours}} hrs · ${{printf "%.0f" .TotalDollars}} saved <em>(est.)</em></div>
  <div class="foot">Estimates use your ~/.flow/stats.json assumptions. Ground-truth counts are exact.</div>
</div>
</body></html>
`))

// renderCardHTML writes a self-contained HTML card for the stats.
func renderCardHTML(w io.Writer, s stats.Stats) error {
	return cardTmpl.Execute(w, cardData{
		Window:       s.Window,
		Lookups:      s.LookupsTotal,
		Tokens:       s.Tokens.Total(),
		TasksDone:    s.TasksDone,
		AutoRuns:     s.AutoRuns,
		OwnerTicks:   s.OwnerTicks,
		TotalHours:   s.Savings.TotalHours,
		TotalDollars: s.Savings.TotalDollars,
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
