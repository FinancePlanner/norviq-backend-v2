package digest

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// Rendered is DigestEmailRenderer.Rendered.
type Rendered struct {
	Subject string
	Text    string
	HTML    string
	Headers map[string]string
}

// Renderer is DigestEmailRenderer: a plain-text part and an HTML part with
// the same sections, plus the RFC 8058 one-click unsubscribe headers.
type Renderer struct {
	// WebBaseURL and APIBaseURL have no trailing slash.
	WebBaseURL, APIBaseURL string
}

const emailFooter = "Numbers come from your holdings' closing prices and your budget. Not investment advice."

// emailLogoPath is the Precision lockup (175x80, shown at 88x40) that
// norviq-web-v2 serves from static/static/brand/precision.
const emailLogoPath = "/static/brand/precision/logo-lockup-email.png"

// Render is DigestEmailRenderer.render.
func (r Renderer) Render(digestID uuid.UUID, p *Payload, unsubscribeToken string) Rendered {
	sections := r.Sections(p)
	token := queryEscape(unsubscribeToken)
	oneClick := r.APIBaseURL + "/v1/digest/unsubscribe?token=" + token
	unsubscribePage := r.WebBaseURL + "/digest/unsubscribe?token=" + token
	headline := Headline(p)

	var text strings.Builder
	text.WriteString(headline + "\n\n")
	for _, s := range sections {
		text.WriteString(s.Title + "\n")
		for _, l := range s.Lines {
			text.WriteString("  " + l + "\n")
		}
		text.WriteString("  " + r.link(digestID, s.Anchor) + "\n\n")
	}
	text.WriteString(emailFooter + "\nUnsubscribe: " + unsubscribePage + "\n")

	// Precision identity (norviq/docs/precision-identity.md), light scope: warm
	// page, white cards, navy ink, deep-gold links. The header is the one navy
	// band, carrying the raster lockup that norviq-web-v2 serves; the alt text
	// stands in when a client blocks images.
	var html strings.Builder
	html.WriteString(`<!doctype html><html><body style="margin:0;padding:0;background:#f7f4ee;">` +
		`<div style="max-width:560px;margin:0 auto;padding:24px 16px;font-family:-apple-system,Segoe UI,Helvetica,Arial,sans-serif;color:#111b2b;">` +
		`<div style="background:#0a101a;border-radius:12px;padding:16px 20px;margin:0 0 20px;">` +
		`<img src="` + escapeHTML(r.WebBaseURL+emailLogoPath) + `" width="88" height="40" alt="Norviq" style="display:block;border:0;color:#e9e9e5;font-size:18px;font-weight:600;"></div>` +
		`<h1 style="font-size:20px;line-height:1.3;margin:0 0 20px;">` + escapeHTML(headline) + `</h1>`)
	for _, s := range sections {
		html.WriteString(`<div style="background:#ffffff;border-radius:12px;padding:16px;margin:0 0 12px;">` +
			`<h2 style="font-size:15px;margin:0 0 8px;">` + escapeHTML(s.Title) + `</h2>`)
		for _, l := range s.Lines {
			html.WriteString(`<p style="font-size:14px;line-height:1.5;margin:0 0 4px;">` + escapeHTML(l) + `</p>`)
		}
		html.WriteString(`<a href="` + escapeHTML(r.link(digestID, s.Anchor)) + `" style="font-size:13px;color:#80612a;">Open in Norviq</a></div>`)
	}
	html.WriteString(`<p style="font-size:12px;color:#4a5568;line-height:1.5;margin:20px 0 0;">` + escapeHTML(emailFooter) + ` ` +
		`<a href="` + escapeHTML(unsubscribePage) + `" style="color:#4a5568;">Unsubscribe</a></p></div></body></html>`)

	return Rendered{
		Subject: Subject(p),
		Text:    text.String(),
		HTML:    html.String(),
		Headers: map[string]string{
			"List-Unsubscribe":      "<" + oneClick + ">",
			"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
		},
	}
}

// Section is DigestEmailRenderer.Section.
type Section struct {
	Anchor string
	Title  string
	Lines  []string
}

// Sections is DigestEmailRenderer.sections.
func (r Renderer) Sections(p *Payload) []Section {
	var out []Section
	if len(p.Holdings) > 0 {
		lines := make([]string, 0, len(p.Holdings)+1)
		for _, m := range p.Holdings {
			line := signedMoney(m.Change, m.Currency)
			if m.ChangePercent != nil {
				line += " (" + signedPercent(*m.ChangePercent) + ")"
			}
			lines = append(lines, line+", now "+money(m.EndValue, m.Currency))
		}
		if p.PricedPositions < p.TotalPositions {
			lines = append(lines, fmt.Sprintf("Based on %d of %d holdings with prices this week.", p.PricedPositions, p.TotalPositions))
		}
		out = append(out, Section{Anchor: "holdings", Title: "Your holdings this week", Lines: lines})
	}
	if len(p.Movers) > 0 {
		lines := make([]string, 0, len(p.Movers))
		for i := range p.Movers {
			m := &p.Movers[i]
			line := m.Symbol + " " + signedPercent(m.ChangePercent) + ": " + signedMoney(m.Contribution, m.Currency)
			if m.Why != nil && *m.Why != "" {
				line += ". " + *m.Why
			}
			lines = append(lines, line)
		}
		out = append(out, Section{Anchor: "movers", Title: "Top movers", Lines: lines})
	}
	if b := p.Budget; b != nil {
		lines := make([]string, 0, len(b.Lines))
		for _, l := range b.Lines {
			direction := "under"
			if l.Drift > 0 {
				direction = "over"
			}
			lines = append(lines, l.Title+": "+money(math.Abs(l.Drift), b.Currency)+" "+direction+" ("+
				money(l.Actual, b.Currency)+" of "+money(l.Target, b.Currency)+")")
		}
		out = append(out, Section{Anchor: "budget", Title: "Spending vs budget", Lines: lines})
	}
	if in := p.Inflation; in != nil {
		out = append(out, Section{Anchor: "inflation", Title: "Your inflation", Lines: []string{
			"Yours: " + percent(in.PersonalRate) + " vs official " + percent(in.OfficialRate) + " (" + in.Country + ", " + in.AsOf + ").",
		}})
	}
	if len(p.Earnings) > 0 {
		lines := make([]string, 0, len(p.Earnings))
		for _, e := range p.Earnings {
			lines = append(lines, e.Symbol+" reports on "+e.Date+hourLabel(e.Hour))
		}
		out = append(out, Section{Anchor: "earnings", Title: "Next week", Lines: lines})
	}
	return out
}

func (r Renderer) link(digestID uuid.UUID, anchor string) string {
	return r.WebBaseURL + "/digest/" + strings.ToLower(digestID.String()) + "?s=" + anchor + "&src=email"
}

func hourLabel(hour *string) string {
	if hour == nil {
		return ""
	}
	switch *hour {
	case "bmo":
		return " (before open)"
	case "amc":
		return " (after close)"
	}
	return ""
}

var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")

func escapeHTML(s string) string { return htmlEscaper.Replace(s) }

// queryEscape is addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed).
func queryEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-._~!$&'()*+,;=:@/?", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteString("%" + strings.ToUpper(strconv.FormatUint(uint64(c)|0x100, 16)[1:]))
	}
	return b.String()
}
