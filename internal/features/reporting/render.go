package reporting

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// renderHTML is ReportHTMLRenderer.render's layout and markup. The palette is
// the Precision identity (norviq/docs/precision-identity.md, 2026-10-30), not
// the Vapor original's forest green, and the Georgia headings are gone.
func renderHTML(d Document) string {
	var cards strings.Builder
	for _, p := range d.Portfolios {
		var rows strings.Builder
		for _, h := range p.Holdings {
			rows.WriteString(`<tr><td>` + escapeHTML(h.Symbol) + `</td><td>` + escapeHTML(swiftCapitalized(h.Category)) +
				`</td><td class="num">` + fmt.Sprintf("%.4f", h.Shares) + `</td><td class="num">` + money(h.Value, p.Currency) + `</td></tr>`)
		}
		cards.WriteString(`<section class="portfolio">
  <div class="section-head"><div><p class="eyebrow">Portfolio</p><h2>` + escapeHTML(p.Name) + `</h2></div><div class="metric"><span>Total value</span><strong>` + money(p.TotalValue(), p.Currency) + `</strong></div></div>
  <div class="metrics"><div><span>Invested</span><b>` + money(p.InvestedValue(), p.Currency) + `</b></div><div><span>Cash</span><b>` + money(p.Cash, p.Currency) + `</b></div><div><span>Holdings</span><b>` + strconv.Itoa(len(p.Holdings)) + `</b></div></div>
  <table><thead><tr><th>Symbol</th><th>Asset class</th><th class="num">Shares</th><th class="num">Value</th></tr></thead><tbody>` + rows.String() + `</tbody></table>
</section>`)
	}
	generated := d.GeneratedAt.UTC().Format(time.RFC3339)
	description := "A composed view of your selected portfolios."
	if d.Description != nil {
		description = *d.Description
	}
	return `<!doctype html><html><head><meta charset="utf-8"><style>
@page { size: A4; margin: 18mm 16mm 20mm; @bottom-right { content: "Norviq | " counter(page) " / " counter(pages); color: #4a5568; font-size: 9px; } }
* { box-sizing: border-box; } body { margin: 0; color: #111b2b; font: 12px -apple-system, BlinkMacSystemFont, "Geist", "Inter", sans-serif; background: #fff; font-variant-numeric: tabular-nums; }
header { padding: 32px; border-radius: 18px; color: #e9e9e5; background: #0a101a; margin-bottom: 22px; }
.brand { letter-spacing: .16em; text-transform: uppercase; font-size: 10px; color: #d8b477; } h1 { font-size: 30px; font-weight: 700; letter-spacing: -.01em; margin: 28px 0 8px; } header p { max-width: 70%; color: #9da9b8; }
.meta { margin-top: 28px; color: #9da9b8; font-size: 10px; } .portfolio { page-break-inside: avoid; margin: 0 0 24px; border: 1px solid #e2e4e8; border-radius: 14px; padding: 20px; }
.section-head { display:flex; justify-content:space-between; align-items:flex-start; } .eyebrow { color:#80612a; margin:0 0 4px; text-transform:uppercase; letter-spacing:.13em; font-size:9px; } h2 { font-size:22px; font-weight:700; margin:0; }
.metric { text-align:right; } .metric span,.metrics span { display:block; color:#4a5568; font-size:10px; } .metric strong { font-size:20px; }
.metrics { display:grid; grid-template-columns:repeat(3,1fr); gap:10px; margin:18px 0; } .metrics div { background:#f7f4ee; border-radius:10px; padding:11px; } .metrics b { font-size:14px; }
table { width:100%; border-collapse:collapse; } th { color:#4a5568; font-size:9px; letter-spacing:.08em; text-transform:uppercase; text-align:left; border-bottom:1px solid #d5d9e0; padding:8px 6px; } td { border-bottom:1px solid #eceef2; padding:8px 6px; } .num { text-align:right; }
footer { color:#4a5568; font-size:9px; margin-top:18px; } </style></head><body>
<header><div class="brand">Norviq Advanced Reporting</div><h1>` + escapeHTML(d.Title) + `</h1><p>` + escapeHTML(description) + `</p><div class="meta">Generated ` + escapeHTML(generated) + `</div></header>
` + cards.String() + `
<footer>Educational information only. Values reflect available portfolio records and user-entered assumptions at generation time.</footer>
</body></html>`
}

func escapeHTML(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

func money(v float64, currency string) string { return fmt.Sprintf("%s %.2f", currency, v) }
