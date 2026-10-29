package reporting

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// renderHTML is ReportHTMLRenderer.render, byte for byte.
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
@page { size: A4; margin: 18mm 16mm 20mm; @bottom-right { content: "Norviq | " counter(page) " / " counter(pages); color: #727b72; font-size: 9px; } }
* { box-sizing: border-box; } body { margin: 0; color: #172019; font: 12px -apple-system, BlinkMacSystemFont, "Inter", sans-serif; background: #fff; }
header { padding: 32px; border-radius: 18px; color: #f5f7f2; background: linear-gradient(135deg,#14291d,#32543c); margin-bottom: 22px; }
.brand { letter-spacing: .16em; text-transform: uppercase; font-size: 10px; opacity: .75; } h1 { font: 34px Georgia, serif; margin: 28px 0 8px; } header p { max-width: 70%; color: #d7e2d8; }
.meta { margin-top: 28px; color: #b9c9bd; font-size: 10px; } .portfolio { page-break-inside: avoid; margin: 0 0 24px; border: 1px solid #dfe5df; border-radius: 14px; padding: 20px; }
.section-head { display:flex; justify-content:space-between; align-items:flex-start; } .eyebrow { color:#68736b; margin:0 0 4px; text-transform:uppercase; letter-spacing:.13em; font-size:9px; } h2 { font: 24px Georgia,serif; margin:0; }
.metric { text-align:right; } .metric span,.metrics span { display:block; color:#68736b; font-size:10px; } .metric strong { font-size:20px; }
.metrics { display:grid; grid-template-columns:repeat(3,1fr); gap:10px; margin:18px 0; } .metrics div { background:#f3f5f1; border-radius:10px; padding:11px; } .metrics b { font-size:14px; }
table { width:100%; border-collapse:collapse; } th { color:#68736b; font-size:9px; letter-spacing:.08em; text-transform:uppercase; text-align:left; border-bottom:1px solid #cad3ca; padding:8px 6px; } td { border-bottom:1px solid #edf0ec; padding:8px 6px; } .num { text-align:right; }
footer { color:#727b72; font-size:9px; margin-top:18px; } </style></head><body>
<header><div class="brand">Norviq Advanced Reporting</div><h1>` + escapeHTML(d.Title) + `</h1><p>` + escapeHTML(description) + `</p><div class="meta">Generated ` + escapeHTML(generated) + `</div></header>
` + cards.String() + `
<footer>Educational information only. Values reflect available portfolio records and user-entered assumptions at generation time.</footer>
</body></html>`
}

func escapeHTML(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

func money(v float64, currency string) string { return fmt.Sprintf("%s %.2f", currency, v) }
