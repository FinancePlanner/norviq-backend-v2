package reporting

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"norviq-api/internal/store/reportingdb"

	"github.com/google/uuid"
)

// Document is ReportDocument.
type Document struct {
	Title       string
	Description *string
	GeneratedAt time.Time
	Template    TemplateInput
	Portfolios  []PortfolioSection
}

// PortfolioSection is ReportDocument.PortfolioSection.
type PortfolioSection struct {
	ID       uuid.UUID
	Name     string
	Currency string
	Holdings []Holding
	Cash     float64
}

// InvestedValue is the sum of holding values.
func (p PortfolioSection) InvestedValue() float64 {
	var total float64
	for _, h := range p.Holdings {
		total += h.Value
	}
	return total
}

// TotalValue is invested plus cash.
func (p PortfolioSection) TotalValue() float64 { return p.InvestedValue() + p.Cash }

// Holding is ReportDocument.Holding.
type Holding struct {
	Symbol   string
	Shares   float64
	Price    float64
	Value    float64
	Category string
}

// collectDocument is ReportDocumentCollector.collect: every portfolio a
// block names (comparison portfolios are not collected), its stocks by
// symbol at cost (shares * buy price), and its summed cash balances.
func collectDocument(ctx context.Context, q *reportingdb.Queries, t TemplateInput, now time.Time) (Document, error) {
	seen := map[uuid.UUID]bool{}
	var ids []uuid.UUID
	for i := range t.Blocks {
		b := &t.Blocks[i]
		for _, raw := range b.PortfolioIDs {
			if id, ok := parseSwiftUUID(raw); ok && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	sections := []PortfolioSection{}
	if len(ids) > 0 {
		portfolios, err := q.PortfoliosByIDs(ctx, ids)
		if err != nil {
			return Document{}, err
		}
		for _, p := range portfolios {
			stocks, err := q.StocksByPortfolio(ctx, p.ID)
			if err != nil {
				return Document{}, err
			}
			balances, err := q.CashBalancesByPortfolio(ctx, p.ID)
			if err != nil {
				return Document{}, err
			}
			var cash float64
			for _, b := range balances {
				cash += b
			}
			holdings := make([]Holding, 0, len(stocks))
			for _, s := range stocks {
				holdings = append(holdings, Holding{
					Symbol: s.Symbol, Shares: s.Shares, Price: s.BuyPrice,
					Value: s.Shares * s.BuyPrice, Category: s.Category,
				})
			}
			sections = append(sections, PortfolioSection{
				ID: p.ID, Name: p.Name, Currency: p.BaseCurrency, Holdings: holdings, Cash: cash,
			})
		}
	}
	sort.SliceStable(sections, func(i, j int) bool { return sections[i].Name < sections[j].Name })
	return Document{
		Title:       t.Name,
		Description: t.Description,
		GeneratedAt: now,
		Template:    t,
		Portfolios:  sections,
	}, nil
}

// parseSwiftUUID is UUID(uuidString:): only the 36-character hyphenated
// form, either case. uuid.Parse also takes braces, urn: and bare hex.
func parseSwiftUUID(s string) (uuid.UUID, bool) {
	if len(s) != 36 {
		return uuid.UUID{}, false
	}
	id, err := uuid.Parse(s)
	return id, err == nil
}

// swiftCapitalized is String.capitalized: words split on whitespace, first
// letter upper-cased and the rest lower-cased.
func swiftCapitalized(s string) string {
	var b strings.Builder
	start := true
	for _, r := range s {
		if unicode.IsSpace(r) {
			start = true
			b.WriteRune(r)
			continue
		}
		if start {
			b.WriteRune(unicode.ToUpper(r))
			start = false
		} else {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// swiftDouble is Double.description: shortest round-trip digits, ".0" on
// integral values, exponent form below 1e-4 and from 2^53 up.
func swiftDouble(v float64) string {
	switch {
	case math.IsNaN(v):
		return "nan"
	case math.IsInf(v, 1):
		return "inf"
	case math.IsInf(v, -1):
		return "-inf"
	}
	a := math.Abs(v)
	if a != 0 && (a < 1e-4 || a >= 1<<53) {
		return strconv.FormatFloat(v, 'e', -1, 64)
	}
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// reportFilename is AdvancedReportWorker.reportFilename.
func reportFilename(title, format string, now time.Time) string {
	lower := strings.ToLower(title)
	var b strings.Builder
	dash := false
	for _, r := range lower {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "norviq-report"
	} else if utf8.RuneCountInString(slug) > 60 {
		slug = slug[:60] // ASCII only by construction
	}
	return fmt.Sprintf("%s-%d.%s", slug, now.Unix(), format)
}
