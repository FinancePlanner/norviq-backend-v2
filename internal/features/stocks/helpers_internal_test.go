package stocks

import (
	"encoding/base64"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/features/badges"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/store/badgesdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStockListCursorRoundTrip(t *testing.T) {
	id := uuid.New()
	at := time.Date(2026, 3, 4, 5, 6, 7, 123456000, time.UTC)
	raw := EncodeStockListCursor(at, id)
	c := ParseStockListCursor(&raw)
	if c == nil || c.ID == nil || *c.ID != id || !c.CreatedAt.Equal(at) {
		t.Fatalf("%+v", c)
	}
	// The legacy "<iso>|<uuid>" form, and a bare fractional timestamp.
	legacy := base64.RawURLEncoding.EncodeToString([]byte("2026-03-04T05:06:07.123456Z|" + id.String()))
	if c := ParseStockListCursor(&legacy); c == nil || *c.ID != id || !c.CreatedAt.Equal(time.Date(2026, 3, 4, 5, 6, 7, 123000000, time.UTC)) {
		t.Fatalf("legacy %+v", c)
	}
	bare := " 2026-03-04T05:06:07.5+01:00 "
	if c := ParseStockListCursor(&bare); c == nil || c.ID != nil || !c.CreatedAt.Equal(time.Date(2026, 3, 4, 4, 6, 7, 500000000, time.UTC)) {
		t.Fatalf("bare %+v", c)
	}
	for _, s := range []string{"", "  ", "2026-03-04T05:06:07Z", "!!!", base64.RawURLEncoding.EncodeToString([]byte("v1|x|y"))} {
		if c := ParseStockListCursor(&s); c != nil {
			t.Fatalf("%q parsed to %+v", s, c)
		}
	}
	if ParseStockListCursor(nil) != nil {
		t.Fatal("nil cursor")
	}
}

func TestDateParsers(t *testing.T) {
	for _, in := range []string{"2024-01-05", "2024-1-5", "\t2024-01-05"} {
		d, ok := parseDateFormatter(in)
		if !ok || d.Format("2006-01-02") != "2024-01-05" {
			t.Fatalf("%q: %v %v", in, d, ok)
		}
	}
	for _, in := range []string{"2024-02-30", "0000-01-01", "2024-01-05 ", "2024/01/05", "x"} {
		if _, ok := parseDateFormatter(in); ok {
			t.Fatalf("%q accepted", in)
		}
	}
	if d, err := parseBuyDate(" 2024-02-29\n"); err != nil || d.Format("2006-01-02") != "2024-02-29" {
		t.Fatalf("leap day %v %v", d, err)
	}
	// Vapor rolled these over (2025-02-30 was 2025-03-02); v2 rejects them.
	for _, in := range []string{"2025-02-30", "2025-02-29", "2025-13-01", "2025-00-10", "2025-01-00", "0000-01-01", "2025-2-3", "2024/01/01", "not-a-date"} {
		if _, err := parseBuyDate(in); err == nil {
			t.Fatalf("%q accepted", in)
		}
	}
	if _, err := parseRequiredDate("  ", "tradeDate"); err == nil || err.Error() == "" {
		t.Fatal("blank accepted")
	}
}

func TestSymbolHelpers(t *testing.T) {
	for in, want := range map[string]string{"brk.b<script>": "BRK.B", "aapl": "AAPL", "<x>": "", "rds-a/b": "RDS-A"} {
		if got := sanitizeShareSymbol(in); got != want {
			t.Fatalf("%q: %q", in, got)
		}
	}
	for in, want := range map[string]bool{"aapl": true, "BRK.B": true, "": false, "TOOLONGSYMBOL1": false, "A B": false} {
		if got := IsImportableTickerSymbol(in); got != want {
			t.Fatalf("%q: %v", in, got)
		}
	}
	if !strings.HasPrefix(NewManualExternalID(), ManualExternalIDPrefix) {
		t.Fatal("manual id prefix")
	}
}

func TestRenderSharePageEscapes(t *testing.T) {
	html := renderSharePage(sharePage{
		ogType:      "website",
		title:       `Hello "World" & <evil>`,
		description: "Line with <script>alert(1)</script>",
		url:         "https://example.com/?a=1&b=2",
		imageURL:    "https://example.com/og.png",
		twitterCard: "summary",
		redirect:    "https://example.com/?x=<y>",
	})
	if strings.Contains(html, "<script>alert(1)</script>") ||
		!strings.Contains(html, "&lt;script&gt;alert(1)&lt;/script&gt;") ||
		!strings.Contains(html, "Hello &quot;World&quot; &amp; &lt;evil&gt;") ||
		!strings.Contains(html, "?a=1&amp;b=2") {
		t.Fatal(html)
	}
}

func TestLedgerHTTPError(t *testing.T) {
	cases := map[string]struct {
		status int
		reason string
	}{
		"holdingNotFound":    {404, "Stock not found."},
		"insufficientShares": {400, "Cannot sell more shares than owned."},
		"insufficientCash":   {422, "Not enough cash to buy AAPL."},
	}
	for kind, want := range cases {
		var he *httpx.Error
		if err := ledgerHTTPError(&LedgerTradeError{Kind: kind, Symbol: "AAPL"}); !errors.As(err, &he) || he.Status != want.status || he.Reason != want.reason {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	other := errors.New("db down")
	if err := ledgerHTTPError(other); !errors.Is(err, other) {
		t.Fatalf("passthrough %v", err)
	}
}

// NewActionsForTest builds Actions on a stub market, for the external test
// package.
func NewActionsForTest(db *pgxpool.Pool, market MarketData, logger *slog.Logger) *Actions {
	return &Actions{h: &handlers{
		db: db, market: market, logger: logger, ent: entitlements.NewFromEnv(db),
		badges: &badges.Service{Q: badgesdb.New(db), Logger: logger.Warn},
	}}
}
