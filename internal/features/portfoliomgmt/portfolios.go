package portfoliomgmt

import (
	"context"
	"sort"
	"strings"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/store/portfoliocoredb"
	"norviq-api/internal/store/portfoliomgmtdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// cloneSourceProvider is the source_provider copied holdings get.
const cloneSourceProvider = "portfolio_clone"

// parameter is the controller's parameter(_:_:): a strict UUID path
// parameter, else 400 "Invalid <name>.".
func parameter(c *echo.Context, name string) (uuid.UUID, error) {
	id, ok := portfolios.ParseSwiftUUID(c.Param(name))
	if !ok {
		return uuid.Nil, httpx.BadRequest("Invalid " + name + ".")
	}
	return id, nil
}

// accessFor is the controller's access(_:userId:editing:ownerOnly:).
func (h *handlers) accessFor(c *echo.Context, userID uuid.UUID, opt portfolios.RequireOptions) (portfolios.AccessContext, error) {
	id, err := parameter(c, "portfolioId")
	if err != nil {
		return portfolios.AccessContext{}, err
	}
	return h.access.Require(c.Request().Context(), h.db, id, userID, opt)
}

// normalizeCurrency is String(raw.trimmed.uppercased().prefix(3)).
func normalizeCurrency(raw string) string {
	r := []rune(strings.ToUpper(portfolios.TrimSwift(raw)))
	if len(r) > 3 {
		r = r[:3]
	}
	return string(r)
}

// GET /v1/portfolios
func (h *handlers) list(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if _, err := portfolios.EnsureDefaultPortfolioListID(ctx, h.db, s.UserID); err != nil {
		return err
	}
	q := portfoliomgmtdb.New(h.db)
	owned, err := q.OwnedPortfolioLists(ctx, s.UserID)
	if err != nil {
		return err
	}
	sharedIDs, err := q.ActiveMembershipPortfolioIDs(ctx, s.UserID)
	if err != nil {
		return err
	}
	var shared []portfoliomgmtdb.PortfolioList
	if len(sharedIDs) > 0 {
		if shared, err = q.PortfolioListsByIDs(ctx, sharedIDs); err != nil {
			return err
		}
	}
	items := []Portfolio{}
	owned = append(owned, shared...)
	all := owned
	for i := range all {
		p := &all[i]
		if p.ArchivedAt != nil {
			continue
		}
		ac, err := h.access.Require(ctx, h.db, p.ID, s.UserID, portfolios.RequireOptions{})
		if err != nil {
			return err
		}
		items = append(items, makeResponse(ac))
	}
	return httpx.OK(c, PortfolioPage{Items: items})
}

// createRequest is StockPlanShared.PortfolioCreateRequest.
type createRequest struct {
	Name               string
	Purpose            string
	Ownership          string
	Mode               string
	BaseCurrency       string
	SourcePortfolioID  *string
	CopyRetirementPlan bool
}

func decodeCreate(c *echo.Context) (createRequest, error) {
	var r createRequest
	o, err := readObject(c)
	if err != nil {
		return r, err
	}
	if r.Name, err = o.String("name"); err != nil {
		return r, err
	}
	if r.Purpose, err = o.Enum("purpose", "PortfolioPurpose", purposes); err != nil {
		return r, err
	}
	if r.Ownership, err = o.Enum("ownership", "PortfolioOwnership", ownerships); err != nil {
		return r, err
	}
	if r.Mode, err = o.Enum("mode", "PortfolioMode", modes); err != nil {
		return r, err
	}
	if r.BaseCurrency, err = o.String("baseCurrency"); err != nil {
		return r, err
	}
	if r.SourcePortfolioID, err = o.OptionalString("sourcePortfolioId"); err != nil {
		return r, err
	}
	if r.CopyRetirementPlan, err = o.Bool("copyRetirementPlan"); err != nil {
		return r, err
	}
	return r, nil
}

// POST /v1/portfolios
func (h *handlers) create(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	payload, err := decodeCreate(c)
	if err != nil {
		return err
	}
	if payload.SourcePortfolioID != nil && payload.Mode != portfolios.ModeHypothetical {
		return httpx.BadRequest("Clones must use hypothetical mode.")
	}
	if payload.Mode == portfolios.ModeHypothetical && payload.Ownership == portfolios.OwnershipJoint {
		return httpx.BadRequest("Hypothetical portfolios cannot be joint.")
	}

	snap, err := h.ent.Resolve(ctx, s.UserID)
	if err != nil {
		return err
	}
	count, err := portfoliomgmtdb.New(h.db).CountActiveOwnedPortfolioLists(ctx, s.UserID)
	if err != nil {
		return err
	}
	isBasic := payload.Purpose == portfolios.PurposePersonal && payload.Ownership == portfolios.OwnershipIndividual && payload.Mode == portfolios.ModeActual
	if !snap.IsPro() && !isBasic {
		return entitlements.UpgradeRequired(entitlements.FeatureAdvancedPortfolios, snap.Level)
	}
	limit := 1
	if snap.IsPro() {
		limit = 25
	}
	if int(count) >= limit {
		return entitlements.UpgradeLimit(entitlements.FeaturePortfolioLists, snap.Level, limit, int(count))
	}
	return h.insertAndCopy(c, s.UserID, payload, count == 0, payload.SourcePortfolioID != nil)
}

// insertAndCopy is the shared tail of create and createFromPayload: save the
// list, copy the sandbox when it names a source, answer 201 with the fresh
// access context.
func (h *handlers) insertAndCopy(c *echo.Context, userID uuid.UUID, payload createRequest, isDefault, cloned bool) error {
	ctx := c.Request().Context()
	name, err := portfolios.NormalizeListName(payload.Name, "name")
	if err != nil {
		return err
	}
	params := portfolios.NewPortfolioListParams(userID, name, isDefault)
	params.Purpose = payload.Purpose
	params.Ownership = payload.Ownership
	params.Mode = payload.Mode
	params.BaseCurrency = normalizeCurrency(payload.BaseCurrency)
	// Vapor dropped a malformed sourcePortfolioId silently (while still
	// setting clonedAt), and checked the source only after saving the new
	// list, so an unseen source left an orphan list behind its 404. v2
	// rejects a malformed id, checks the source first, and saves the list
	// and its copied sandbox in one transaction.
	if payload.SourcePortfolioID != nil {
		id, ok := portfolios.ParseSwiftUUID(*payload.SourcePortfolioID)
		if !ok {
			return httpx.BadRequest("Invalid sourcePortfolioId.")
		}
		if _, err := h.access.Require(ctx, h.db, id, userID, portfolios.RequireOptions{}); err != nil {
			return err
		}
		if params.Mode != portfolios.ModeHypothetical {
			return httpx.BadRequest("Only hypothetical portfolios can clone holdings.")
		}
		params.SourcePortfolioID = &id
	}
	if cloned {
		now := params.CreatedAt
		params.ClonedAt = &now
	}
	var dest portfoliocoredb.PortfolioList
	err = pgx.BeginFunc(ctx, h.db, func(tx pgx.Tx) error {
		var err error
		if dest, err = portfoliocoredb.New(tx).InsertPortfolioList(ctx, params); err != nil {
			return listNameConflict(err)
		}
		if dest.SourcePortfolioID != nil {
			return copySandbox(ctx, tx, *dest.SourcePortfolioID, dest, userID, payload.CopyRetirementPlan)
		}
		return nil
	})
	if err != nil {
		return err
	}
	ac, err := h.access.Require(ctx, h.db, dest.ID, userID, portfolios.RequireOptions{})
	if err != nil {
		return err
	}
	return httpx.Created(c, makeResponse(ac))
}

// copySandbox copies the source's holdings, cash positions and (optionally)
// retirement plan into a new hypothetical portfolio, inside the transaction
// that saved it. The caller has checked access to the source and the mode.
func copySandbox(ctx context.Context, tx pgx.Tx, sourceID uuid.UUID, dest portfoliocoredb.PortfolioList, userID uuid.UUID, copyRetirementPlan bool) error {
	q := portfoliomgmtdb.New(tx)
	stocks, err := q.StocksForPortfolio(ctx, sourceID)
	if err != nil {
		return err
	}
	provider := cloneSourceProvider
	for i := range stocks {
		st := &stocks[i]
		if err := q.InsertStock(ctx, portfoliomgmtdb.InsertStockParams{
			ID:              uuid.New(),
			UserID:          userID,
			PortfolioListID: dest.ID,
			Symbol:          st.Symbol,
			Shares:          st.Shares,
			BuyPrice:        st.BuyPrice,
			BuyDate:         st.BuyDate,
			Notes:           st.Notes,
			Category:        st.Category,
			SourceProvider:  &provider,
			CreatedAt:       portfolios.Now(),
		}); err != nil {
			return err
		}
	}
	cash, err := q.CashPositionsToCopy(ctx, sourceID)
	if err != nil {
		return err
	}
	for _, cp := range cash {
		if _, err := q.InsertCashPosition(ctx, portfoliomgmtdb.InsertCashPositionParams{
			ID:          uuid.New(),
			PortfolioID: dest.ID,
			Label:       cp.Label,
			Currency:    cp.Currency,
			Balance:     cp.Balance,
			AsOf:        cp.AsOf,
			CreatedAt:   ptr(portfolios.Now()),
		}); err != nil {
			return err
		}
	}
	if copyRetirementPlan {
		plans, err := q.RetirementPlanForPortfolio(ctx, sourceID)
		if err != nil {
			return err
		}
		if len(plans) > 0 {
			if err := q.InsertRetirementPlan(ctx, portfoliomgmtdb.InsertRetirementPlanParams{
				ID:          uuid.New(),
				PortfolioID: dest.ID,
				RuleVersion: plans[0].RuleVersion,
				InputJson:   plans[0].InputJson,
				CreatedAt:   ptr(portfolios.Now()),
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func ptr[T any](v T) *T { return &v }

// GET /v1/portfolios/:portfolioId
func (h *handlers) get(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ac, err := h.accessFor(c, s.UserID, portfolios.RequireOptions{})
	if err != nil {
		return err
	}
	return httpx.OK(c, makeResponse(ac))
}

// PATCH /v1/portfolios/:portfolioId
func (h *handlers) update(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ac, err := h.accessFor(c, s.UserID, portfolios.RequireOptions{Editing: true, OwnerOnly: true})
	if err != nil {
		return err
	}
	o, err := readObject(c)
	if err != nil {
		return err
	}
	name, err := o.OptionalString("name")
	if err != nil {
		return err
	}
	purpose, err := o.OptionalEnum("purpose", "PortfolioPurpose", purposes)
	if err != nil {
		return err
	}
	ownership, err := o.OptionalEnum("ownership", "PortfolioOwnership", ownerships)
	if err != nil {
		return err
	}
	currency, err := o.OptionalString("baseCurrency")
	if err != nil {
		return err
	}
	p := ac.Portfolio
	changed := false
	if name != nil {
		n, err := portfolios.NormalizeListName(*name, "name")
		if err != nil {
			return err
		}
		p.Name, changed = n, true
	}
	if purpose != nil {
		p.Purpose, changed = *purpose, true
	}
	if ownership != nil {
		p.Ownership, changed = *ownership, true
	}
	if currency != nil {
		p.BaseCurrency, changed = normalizeCurrency(*currency), true
	}
	// Fluent's save() is a no-op without an assignment; any assignment,
	// even to the same value, writes and bumps updated_at.
	if changed {
		row, err := portfoliomgmtdb.New(h.db).UpdatePortfolioListDetails(c.Request().Context(), portfoliomgmtdb.UpdatePortfolioListDetailsParams{
			ID: p.ID, Name: p.Name, Purpose: p.Purpose, Ownership: p.Ownership, BaseCurrency: p.BaseCurrency, UpdatedAt: ptr(portfolios.Now()),
		})
		if err != nil {
			return listNameConflict(err)
		}
		p = coreList(row)
	}
	ac.Portfolio = p
	return httpx.OK(c, makeResponse(ac))
}

// POST /v1/portfolios/:portfolioId/archive
func (h *handlers) archive(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ac, err := h.accessFor(c, s.UserID, portfolios.RequireOptions{OwnerOnly: true})
	if err != nil {
		return err
	}
	if ac.Portfolio.IsDefault {
		return httpx.BadRequest("The default portfolio cannot be archived.")
	}
	row, err := portfoliomgmtdb.New(h.db).ArchivePortfolioList(c.Request().Context(), portfoliomgmtdb.ArchivePortfolioListParams{
		ID: ac.Portfolio.ID, ArchivedAt: ptr(portfolios.Now()),
	})
	if err != nil {
		return err
	}
	ac.Portfolio = coreList(row)
	return httpx.OK(c, makeResponse(ac))
}

// DELETE /v1/portfolios/:portfolioId
func (h *handlers) delete(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ac, err := h.accessFor(c, s.UserID, portfolios.RequireOptions{OwnerOnly: true})
	if err != nil {
		return err
	}
	if ac.Portfolio.IsDefault {
		return httpx.BadRequest("The default portfolio cannot be deleted.")
	}
	id := ac.Portfolio.ID
	ctx := c.Request().Context()
	err = pgx.BeginFunc(ctx, h.db, func(tx pgx.Tx) error {
		// A stopped follow's pilot account is not a connected account: it
		// goes with the portfolio. While a follow exists it still blocks.
		followed, err := portfolios.IsFollowed(ctx, tx, id)
		if err != nil {
			return err
		}
		q := portfoliomgmtdb.New(tx)
		var connected int64
		if followed {
			connected, err = q.CountAccountsInPortfolio(ctx, &id)
		} else {
			connected, err = q.CountAccountsInPortfolioExceptBroker(ctx, portfoliomgmtdb.CountAccountsInPortfolioExceptBrokerParams{PortfolioID: &id, Broker: portfolios.PilotBroker})
		}
		if err != nil {
			return err
		}
		if connected != 0 {
			return httpx.Conflict("Move connected accounts to another portfolio before deleting this portfolio.")
		}
		if !followed {
			if err := portfolios.RemovePilotLeftovers(ctx, tx, id); err != nil {
				return err
			}
		}
		// Holdings that are not pilot leftovers hit stocks' ON DELETE
		// RESTRICT here: a raw 500 in Vapor, a 409 in v2.
		return holdingsConflict(q.DeletePortfolioList(ctx, id))
	})
	if err != nil {
		return err
	}
	return httpx.NoContent(c)
}

// POST /v1/portfolios/:portfolioId/clone
func (h *handlers) clone(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	source, err := h.accessFor(c, s.UserID, portfolios.RequireOptions{})
	if err != nil {
		return err
	}
	o, err := readObject(c)
	if err != nil {
		return err
	}
	name, err := o.String("name")
	if err != nil {
		return err
	}
	copyPlan, err := o.Bool("copyRetirementPlan")
	if err != nil {
		return err
	}
	purpose := portfolios.PurposePersonal
	if source.Portfolio.Purpose == portfolios.PurposeRetirement {
		purpose = portfolios.PurposeRetirement
	}
	sourceID := apijson.FormatUUID(source.Portfolio.ID)
	payload := createRequest{
		Name:               name,
		Purpose:            purpose,
		Ownership:          portfolios.OwnershipIndividual,
		Mode:               portfolios.ModeHypothetical,
		BaseCurrency:       source.Portfolio.BaseCurrency,
		SourcePortfolioID:  &sourceID,
		CopyRetirementPlan: copyPlan,
	}

	// createFromPayload
	ctx := c.Request().Context()
	snap, err := h.ent.Resolve(ctx, s.UserID)
	if err != nil {
		return err
	}
	if !snap.IsPro() {
		return entitlements.UpgradeRequired(entitlements.FeatureAdvancedPortfolios, snap.Level)
	}
	count, err := portfoliomgmtdb.New(h.db).CountActiveOwnedPortfolioLists(ctx, s.UserID)
	if err != nil {
		return err
	}
	if count >= 25 {
		return entitlements.UpgradeLimit(entitlements.FeaturePortfolioLists, snap.Level, 25, int(count))
	}
	return h.insertAndCopy(c, s.UserID, payload, false, true)
}

// GET /v1/portfolios/:portfolioId/accounts
func (h *handlers) accounts(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ac, err := h.accessFor(c, s.UserID, portfolios.RequireOptions{})
	if err != nil {
		return err
	}
	id := ac.Portfolio.ID
	rows, err := portfoliomgmtdb.New(h.db).AccountsInPortfolio(c.Request().Context(), &id)
	if err != nil {
		return err
	}
	out := make([]Account, 0, len(rows))
	for i := range rows {
		out = append(out, makeAccount(&rows[i]))
	}
	return httpx.OK(c, out)
}

// PUT /v1/portfolios/:portfolioId/accounts/:accountId
func (h *handlers) assignAccount(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ac, err := h.accessFor(c, s.UserID, portfolios.RequireOptions{Editing: true, OwnerOnly: true})
	if err != nil {
		return err
	}
	if ac.Portfolio.Mode != portfolios.ModeActual {
		return httpx.BadRequest("Connected accounts can only belong to actual portfolios.")
	}
	accountID, err := parameter(c, "accountId")
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	found, err := portfoliomgmtdb.New(h.db).OwnedAccount(ctx, portfoliomgmtdb.OwnedAccountParams{ID: accountID, UserID: s.UserID})
	if err != nil {
		return err
	}
	if len(found) == 0 {
		return httpx.NotFound("Account not found.")
	}
	// The pilot account holds a follow's simulated cash; moving it would
	// carry that cash into a real portfolio.
	if found[0].Broker == portfolios.PilotBroker {
		return httpx.Unprocessable("Simulated accounts can't be reassigned.")
	}
	listID := ac.Portfolio.ID
	if _, err := portfoliocoredb.New(h.db).SetAccountPortfolio(ctx, portfoliocoredb.SetAccountPortfolioParams{
		ID: accountID, PortfolioID: &listID, UpdatedAt: ptr(portfolios.Now()),
	}); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

// GET /v1/portfolios/compare?left=&right=
func (h *handlers) compare(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	// req.query.decode(ComparisonQuery.self): both keys required, in
	// declaration order.
	left := httpx.QueryString(c, "left")
	if left == nil {
		return queryValueNotFound("left")
	}
	right := httpx.QueryString(c, "right")
	if right == nil {
		return queryValueNotFound("right")
	}
	leftID, okL := portfolios.ParseSwiftUUID(*left)
	rightID, okR := portfolios.ParseSwiftUUID(*right)
	if !okL || !okR {
		return httpx.BadRequest("left and right must be portfolio IDs.")
	}
	ctx := c.Request().Context()
	l, err := h.access.Require(ctx, h.db, leftID, s.UserID, portfolios.RequireOptions{})
	if err != nil {
		return err
	}
	r, err := h.access.Require(ctx, h.db, rightID, s.UserID, portfolios.RequireOptions{})
	if err != nil {
		return err
	}
	q := portfoliomgmtdb.New(h.db)
	leftStocks, err := q.StocksForPortfolio(ctx, leftID)
	if err != nil {
		return err
	}
	rightStocks, err := q.StocksForPortfolio(ctx, rightID)
	if err != nil {
		return err
	}
	return httpx.OK(c, makeComparison(l, leftStocks, r, rightStocks))
}

// queryValueNotFound is URLEncodedFormDecoder's valueNotFound for a missing
// String key.
func queryValueNotFound(key string) error {
	return httpx.BadRequest("No value found (expected type 'String') at path '" + key + "'. No String was found at '" + key + "'.")
}

// symbolValues is Dictionary(grouping: by: symbol.uppercased()) summed as
// shares * buyPrice, in row order within each symbol.
func symbolValues(stocks []portfoliomgmtdb.Stock) map[string]float64 {
	out := map[string]float64{}
	for i := range stocks {
		st := &stocks[i]
		k := strings.ToUpper(st.Symbol)
		out[k] += st.Shares * st.BuyPrice
	}
	return out
}

func sortedKeys(maps ...map[string]float64) []string {
	set := map[string]bool{}
	for _, m := range maps {
		for k := range m {
			set[k] = true
		}
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// total sums the values in symbol order. Swift summed Dictionary.values in
// hash order, so its last floating-point bits could vary between runs.
func total(m map[string]float64) float64 {
	t := 0.0
	for _, k := range sortedKeys(m) {
		t += m[k]
	}
	return t
}

// makeComparison is PortfolioManagementController.makeComparison.
func makeComparison(left portfolios.AccessContext, leftStocks []portfoliomgmtdb.Stock, right portfolios.AccessContext, rightStocks []portfoliomgmtdb.Stock) Comparison {
	lv, rv := symbolValues(leftStocks), symbolValues(rightStocks)
	lt, rt := total(lv), total(rv)
	holdings := []ComparisonHolding{}
	for _, sym := range sortedKeys(lv, rv) {
		l, r := lv[sym], rv[sym]
		lw, rw := 0.0, 0.0
		if lt > 0 {
			lw = l / lt * 100
		}
		if rt > 0 {
			rw = r / rt * 100
		}
		holdings = append(holdings, ComparisonHolding{
			Symbol: sym, LeftValue: l, RightValue: r,
			LeftWeightPercent: lw, RightWeightPercent: rw,
			ValueDifference: r - l, WeightDifferencePercent: rw - lw,
		})
	}
	column := func(ac portfolios.AccessContext, t float64, n int) ComparisonColumn {
		return ComparisonColumn{
			PortfolioID:  apijson.FormatUUID(ac.Portfolio.ID),
			Name:         ac.Portfolio.Name,
			BaseCurrency: ac.Portfolio.BaseCurrency,
			TotalValue:   t,
			CashBalance:  0,
			HoldingCount: n,
		}
	}
	return Comparison{
		Left:        column(left, lt, len(leftStocks)),
		Right:       column(right, rt, len(rightStocks)),
		Holdings:    holdings,
		GeneratedAt: portfolios.FormatISODateTime(portfolios.Now()),
	}
}
