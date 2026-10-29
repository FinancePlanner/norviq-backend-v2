package portfoliomgmt

import (
	"regexp"
	"strconv"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/store/portfoliomgmtdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// Foundation's ISO8601DateFormatter parses through ICU in lenient mode, so
// Vapor accepted far more than the ISO forms. These patterns reproduce what
// the parity Vapor (macOS Foundation) was observed to accept: any of
// "-./ " between date fields, 1-3 digit months, overflowing days (Feb 30 is
// Mar 2), single-digit hours, "z"/"GMT" zones, offsets as +hh, +hhmm or
// +hh:mm, and anything after a parsed prefix ignored.
var (
	isoDatePrefix = regexp.MustCompile(`^\s*(\d+)[-./ ](\d{1,3})[-./ ](\d+)`)
	isoTimeSuffix = regexp.MustCompile(`^T(\d{1,2}):(\d{2}):(\d{2})\s*(Z|z|GMT|([+-])(\d{2})(?::?(\d{2}))?)`)
)

// lenientDate parses the date prefix; ok is false when a field is out of
// ICU's range (month 1-12, day 1-31).
func lenientDate(raw string) (y, m, d int, rest string, ok bool) {
	g := isoDatePrefix.FindStringSubmatch(raw)
	if g == nil {
		return 0, 0, 0, "", false
	}
	y, _ = strconv.Atoi(g[1])
	m, _ = strconv.Atoi(g[2])
	d, _ = strconv.Atoi(g[3])
	if m < 1 || m > 12 || d < 1 || d > 31 {
		return 0, 0, 0, "", false
	}
	return y, m, d, raw[len(g[0]):], true
}

// parseDate is the controller's parseDate: ISO8601DateFormatter's default
// (.withInternetDateTime: date, time to the second, zone), then
// .withFullDate (UTC midnight), else 400 "Invalid date.".
func parseDate(raw string) (time.Time, error) {
	y, m, d, rest, ok := lenientDate(raw)
	if !ok {
		return time.Time{}, httpx.BadRequest("Invalid date.")
	}
	if g := isoTimeSuffix.FindStringSubmatch(rest); g != nil {
		hh, _ := strconv.Atoi(g[1])
		mm, _ := strconv.Atoi(g[2])
		ss, _ := strconv.Atoi(g[3])
		if hh <= 24 && mm < 60 && ss < 60 {
			offset := 0
			if g[5] != "" {
				oh, _ := strconv.Atoi(g[6])
				om, _ := strconv.Atoi(g[7])
				offset = oh*3600 + om*60
				if g[5] == "-" {
					offset = -offset
				}
			}
			return time.Date(y, time.Month(m), d, hh, mm, ss, 0, time.UTC).Add(-time.Duration(offset) * time.Second), nil
		}
	}
	return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC), nil
}

// cashRequest is StockPlanShared.PortfolioCashPositionRequest.
type cashRequest struct {
	Label    string
	Currency string
	Balance  float64
	AsOf     string
}

func decodeCash(c *echo.Context) (cashRequest, error) {
	var r cashRequest
	o, err := readObject(c)
	if err != nil {
		return r, err
	}
	if r.Label, err = o.String("label"); err != nil {
		return r, err
	}
	if r.Currency, err = o.String("currency"); err != nil {
		return r, err
	}
	if r.Balance, err = o.Double("balance"); err != nil {
		return r, err
	}
	if r.AsOf, err = o.String("asOf"); err != nil {
		return r, err
	}
	return r, nil
}

// editableCash is the common prologue of the cash writes: editing access,
// then the pilot-follow guard.
func (h *handlers) editableCash(c *echo.Context) (portfolios.AccessContext, error) {
	s, err := auth.Require(c)
	if err != nil {
		return portfolios.AccessContext{}, err
	}
	ac, err := h.accessFor(c, s.UserID, portfolios.RequireOptions{Editing: true})
	if err != nil {
		return portfolios.AccessContext{}, err
	}
	if err := portfolios.EnsureNotFollowed(c.Request().Context(), h.db, ac.Portfolio.ID); err != nil {
		return portfolios.AccessContext{}, err
	}
	return ac, nil
}

// GET /v1/portfolios/:portfolioId/cash
func (h *handlers) cashPositions(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ac, err := h.accessFor(c, s.UserID, portfolios.RequireOptions{})
	if err != nil {
		return err
	}
	rows, err := portfoliomgmtdb.New(h.db).CashPositionsForPortfolio(c.Request().Context(), ac.Portfolio.ID)
	if err != nil {
		return err
	}
	out := make([]CashPosition, 0, len(rows))
	for i := range rows {
		out = append(out, makeCashPosition(&rows[i]))
	}
	return httpx.OK(c, out)
}

// POST /v1/portfolios/:portfolioId/cash
func (h *handlers) createCashPosition(c *echo.Context) error {
	ac, err := h.editableCash(c)
	if err != nil {
		return err
	}
	payload, err := decodeCash(c)
	if err != nil {
		return err
	}
	label, err := portfolios.NormalizeListName(payload.Label, "label")
	if err != nil {
		return err
	}
	currency := normalizeCurrency(payload.Currency)
	asOf, err := parseDate(payload.AsOf)
	if err != nil {
		return err
	}
	row, err := portfoliomgmtdb.New(h.db).InsertCashPosition(c.Request().Context(), portfoliomgmtdb.InsertCashPositionParams{
		ID:          uuid.New(),
		PortfolioID: ac.Portfolio.ID,
		Label:       label,
		Currency:    currency,
		Balance:     payload.Balance,
		AsOf:        asOf,
		CreatedAt:   ptr(portfolios.Now()),
	})
	if err != nil {
		return err
	}
	return httpx.Created(c, makeCashPosition(&row))
}

// findCash is the lookup shared by update and delete.
func (h *handlers) findCash(c *echo.Context, q *portfoliomgmtdb.Queries, cashID, listID uuid.UUID) (portfoliomgmtdb.PortfolioCashPosition, error) {
	found, err := q.CashPositionByID(c.Request().Context(), portfoliomgmtdb.CashPositionByIDParams{ID: cashID, PortfolioID: listID})
	if err != nil {
		return portfoliomgmtdb.PortfolioCashPosition{}, err
	}
	if len(found) == 0 {
		return portfoliomgmtdb.PortfolioCashPosition{}, httpx.NotFound("Cash position not found.")
	}
	return found[0], nil
}

// PUT /v1/portfolios/:portfolioId/cash/:cashId
func (h *handlers) updateCashPosition(c *echo.Context) error {
	ac, err := h.editableCash(c)
	if err != nil {
		return err
	}
	cashID, err := parameter(c, "cashId")
	if err != nil {
		return err
	}
	payload, err := decodeCash(c)
	if err != nil {
		return err
	}
	q := portfoliomgmtdb.New(h.db)
	record, err := h.findCash(c, q, cashID, ac.Portfolio.ID)
	if err != nil {
		return err
	}
	label, err := portfolios.NormalizeListName(payload.Label, "label")
	if err != nil {
		return err
	}
	currency := normalizeCurrency(payload.Currency)
	asOf, err := parseDate(payload.AsOf)
	if err != nil {
		return err
	}
	row, err := q.UpdateCashPosition(c.Request().Context(), portfoliomgmtdb.UpdateCashPositionParams{
		ID: record.ID, Label: label, Currency: currency, Balance: payload.Balance, AsOf: asOf, UpdatedAt: ptr(portfolios.Now()),
	})
	if err != nil {
		return err
	}
	return httpx.OK(c, makeCashPosition(&row))
}

// DELETE /v1/portfolios/:portfolioId/cash/:cashId
func (h *handlers) deleteCashPosition(c *echo.Context) error {
	ac, err := h.editableCash(c)
	if err != nil {
		return err
	}
	cashID, err := parameter(c, "cashId")
	if err != nil {
		return err
	}
	q := portfoliomgmtdb.New(h.db)
	record, err := h.findCash(c, q, cashID, ac.Portfolio.ID)
	if err != nil {
		return err
	}
	if err := q.DeleteCashPosition(c.Request().Context(), record.ID); err != nil {
		return err
	}
	return httpx.NoContent(c)
}
