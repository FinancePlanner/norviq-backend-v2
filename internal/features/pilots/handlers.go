package pilots

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/achievementhooks"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/store/pilotsdb"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// PilotSummary is the shared DTO.
type PilotSummary struct {
	Slug          string  `json:"slug"`
	DisplayName   string  `json:"displayName"`
	Kind          string  `json:"kind"`
	Chamber       *string `json:"chamber,omitempty"`
	UpdatedAt     *string `json:"updatedAt,omitempty"`
	HoldingsCount int     `json:"holdingsCount"`
}

// PilotWeight is the shared DTO.
type PilotWeight struct {
	Symbol string  `json:"symbol"`
	Weight float64 `json:"weight"`
}

// PilotDisclosureItem is the shared DTO.
type PilotDisclosureItem struct {
	Symbol          string   `json:"symbol"`
	Side            string   `json:"side"`
	Instrument      string   `json:"instrument"`
	TransactionDate *string  `json:"transactionDate,omitempty"`
	DisclosureDate  *string  `json:"disclosureDate,omitempty"`
	AmountMin       *float64 `json:"amountMin,omitempty"`
	AmountMax       *float64 `json:"amountMax,omitempty"`
	Period          *string  `json:"period,omitempty"`
}

// PilotDetail is the shared DTO.
type PilotDetail struct {
	Pilot             PilotSummary          `json:"pilot"`
	Weights           []PilotWeight         `json:"weights"`
	SkippedPuts       int                   `json:"skippedPuts"`
	RecentDisclosures []PilotDisclosureItem `json:"recentDisclosures"`
	LagNote           string                `json:"lagNote"`
}

// PilotFollowResponse is the shared DTO.
type PilotFollowResponse struct {
	ID              string       `json:"id"`
	Pilot           PilotSummary `json:"pilot"`
	TargetKind      string       `json:"targetKind"`
	PortfolioListID *string      `json:"portfolioListId,omitempty"`
	WatchlistListID *string      `json:"watchlistListId,omitempty"`
	StartingCapital *float64     `json:"startingCapital,omitempty"`
	Currency        string       `json:"currency"`
	Status          string       `json:"status"`
	AppliedVersion  int          `json:"appliedVersion"`
	CreatedAt       string       `json:"createdAt"`
}

// PilotFollowEventResponse is the shared DTO.
type PilotFollowEventResponse struct {
	ID          string   `json:"id"`
	BookVersion int      `json:"bookVersion"`
	Kind        string   `json:"kind"`
	Symbol      string   `json:"symbol"`
	Quantity    *float64 `json:"quantity,omitempty"`
	Price       *float64 `json:"price,omitempty"`
	PricedAt    string   `json:"pricedAt"`
	Note        *string  `json:"note,omitempty"`
}

// PilotFollowSnapshotResponse is the shared DTO.
type PilotFollowSnapshotResponse struct {
	Date  string  `json:"date"`
	Value float64 `json:"value"`
	Cash  float64 `json:"cash"`
}

// createRequest is PilotFollowCreateRequest on the wire.
type createRequest struct {
	PilotSlug       *string          `json:"pilotSlug"`
	TargetKind      *string          `json:"targetKind"`
	PortfolioListID *string          `json:"portfolioListId"`
	WatchlistListID *string          `json:"watchlistListId"`
	StartingCapital *json.RawMessage `json:"startingCapital"`
}

// updateRequest is PilotFollowUpdateRequest on the wire.
type updateRequest struct {
	Status *string `json:"status"`
}

// LagNote is PilotController.lagNote(for:).
func LagNote(kind string) string {
	if pilotKind(kind) == KindFund {
		return "13F filings arrive up to 135 days after the positions they report. Simulated trades are priced when Norviq sees the filing. No real money is invested."
	}
	return "Congressional trades are disclosed up to 45 days after they happen. Simulated trades are priced when Norviq sees the disclosure, not on the original trade date. No real money is invested."
}

type handlers struct {
	q       *pilotsdb.Queries
	ent     *entitlements.Service
	follows *FollowService
	now     func() time.Time
}

// requireEnabled is the controller's per-request PILOTS_ENABLED check: a
// bare 404 when off.
func requireEnabled() error {
	if !Enabled() {
		return httpx.NotFound("")
	}
	return nil
}

func iso(t time.Time) string { return apijson.FormatTime(t) }

func (h *handlers) summary(ctx context.Context, p pilotsdb.Pilot) (PilotSummary, *pilotsdb.PilotBookVersion, error) {
	s := PilotSummary{Slug: p.Slug, DisplayName: p.DisplayName, Kind: pilotKind(p.Kind), Chamber: p.Chamber}
	latest, err := h.q.LatestBookVersion(ctx, p.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, nil, nil
	}
	if err != nil {
		return s, nil, err
	}
	s.UpdatedAt = ptr(iso(latest.ComputedAt))
	s.HoldingsCount = len(decodeWeights(latest.Weights))
	return s, &latest, nil
}

func (h *handlers) listPilots(c *echo.Context) error {
	if err := requireEnabled(); err != nil {
		return err
	}
	ctx := c.Request().Context()
	pilots, err := h.q.ListActivePilots(ctx)
	if err != nil {
		return err
	}
	out := make([]PilotSummary, 0, len(pilots))
	for i := range pilots {
		s, _, err := h.summary(ctx, pilots[i])
		if err != nil {
			return err
		}
		out = append(out, s)
	}
	return httpx.OK(c, out)
}

func (h *handlers) pilotDetail(c *echo.Context) error {
	if err := requireEnabled(); err != nil {
		return err
	}
	ctx := c.Request().Context()
	p, err := h.q.ActivePilotBySlug(ctx, c.Param("slug"))
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NotFound("Pilot not found.")
	}
	if err != nil {
		return err
	}
	s, latest, err := h.summary(ctx, p)
	if err != nil {
		return err
	}
	recent, err := h.q.RecentDisclosures(ctx, p.ID)
	if err != nil {
		return err
	}
	detail := PilotDetail{Pilot: s, Weights: []PilotWeight{}, RecentDisclosures: []PilotDisclosureItem{}, LagNote: LagNote(p.Kind)}
	if latest != nil {
		w := decodeWeights(latest.Weights)
		for _, sym := range sortedWeights(w) {
			detail.Weights = append(detail.Weights, PilotWeight{Symbol: sym, Weight: w[sym]})
		}
		detail.SkippedPuts = int(latest.SkippedPuts)
	}
	for i := range recent {
		r := &recent[i]
		detail.RecentDisclosures = append(detail.RecentDisclosures, PilotDisclosureItem{
			Symbol: r.Symbol, Side: r.Side, Instrument: r.Instrument, TransactionDate: r.TransactionDate,
			DisclosureDate: r.DisclosureDate, AmountMin: r.AmountMin, AmountMax: r.AmountMax, Period: r.Period,
		})
	}
	return httpx.OK(c, detail)
}

func (h *handlers) response(ctx context.Context, f pilotsdb.PilotFollow) (PilotFollowResponse, error) {
	p, err := h.q.PilotByID(ctx, f.PilotID)
	if errors.Is(err, pgx.ErrNoRows) {
		return PilotFollowResponse{}, httpx.NotFound("Pilot not found.")
	}
	if err != nil {
		return PilotFollowResponse{}, err
	}
	s, _, err := h.summary(ctx, p)
	if err != nil {
		return PilotFollowResponse{}, err
	}
	status := f.Status
	if status != StatusActive && status != StatusPaused {
		status = StatusActive
	}
	created := h.now()
	if f.CreatedAt != nil {
		created = *f.CreatedAt
	}
	r := PilotFollowResponse{
		ID: apijson.FormatUUID(f.ID), Pilot: s, TargetKind: targetOf(f),
		StartingCapital: f.StartingCapital, Currency: f.Currency, Status: status,
		AppliedVersion: int(f.AppliedVersion), CreatedAt: iso(created),
	}
	if f.PortfolioListID != nil {
		r.PortfolioListID = ptr(apijson.FormatUUID(*f.PortfolioListID))
	}
	if f.WatchlistListID != nil {
		r.WatchlistListID = ptr(apijson.FormatUUID(*f.WatchlistListID))
	}
	return r, nil
}

func (h *handlers) listFollows(c *echo.Context) error {
	if err := requireEnabled(); err != nil {
		return err
	}
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	follows, err := h.q.ListFollowsByUser(ctx, s.UserID)
	if err != nil {
		return err
	}
	out := make([]PilotFollowResponse, 0, len(follows))
	for i := range follows {
		r, err := h.response(ctx, follows[i])
		if err != nil {
			return err
		}
		out = append(out, r)
	}
	return httpx.OK(c, out)
}

// ownedFollow is the follow named by :followId, owned by the session user.
func (h *handlers) ownedFollow(c *echo.Context) (pilotsdb.PilotFollow, error) {
	s, err := auth.Require(c)
	if err != nil {
		return pilotsdb.PilotFollow{}, err
	}
	id, ok := parseSwiftUUID(c.Param("followId"))
	if !ok {
		return pilotsdb.PilotFollow{}, httpx.NotFound("Follow not found.")
	}
	f, err := h.q.FollowByIDForUser(c.Request().Context(), pilotsdb.FollowByIDForUserParams{ID: id, UserID: s.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return pilotsdb.PilotFollow{}, httpx.NotFound("Follow not found.")
	}
	return f, err
}

func (h *handlers) getFollow(c *echo.Context) error {
	if err := requireEnabled(); err != nil {
		return err
	}
	f, err := h.ownedFollow(c)
	if err != nil {
		return err
	}
	r, err := h.response(c.Request().Context(), f)
	if err != nil {
		return err
	}
	return httpx.OK(c, r)
}

func required(field string, present bool) error {
	if !present {
		return httpx.BadRequest("No such key '" + field + "' at path ''.")
	}
	return nil
}

func oneOf(typeName, v string, allowed ...string) error {
	for _, a := range allowed {
		if v == a {
			return nil
		}
	}
	return httpx.BadRequest("Cannot initialize " + typeName + " from invalid String value " + v)
}

func (h *handlers) createFollow(c *echo.Context) error {
	if err := requireEnabled(); err != nil {
		return err
	}
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var body createRequest
	if err := httpx.Decode(c, &body); err != nil {
		return err
	}
	if err := required("pilotSlug", body.PilotSlug != nil); err != nil {
		return err
	}
	if err := required("targetKind", body.TargetKind != nil); err != nil {
		return err
	}
	if err := oneOf("PilotFollowTargetKind", *body.TargetKind, TargetPortfolio, TargetWatchlist); err != nil {
		return err
	}
	req := CreateRequest{PilotSlug: *body.PilotSlug, TargetKind: *body.TargetKind, PortfolioListID: body.PortfolioListID, WatchlistListID: body.WatchlistListID}
	if body.StartingCapital != nil && string(*body.StartingCapital) != "null" {
		var v float64
		if err := json.Unmarshal(*body.StartingCapital, &v); err != nil {
			return httpx.BadRequest("Expected to decode Double but found a string instead.")
		}
		req.StartingCapital = &v
	}
	// portfolio:write (the route) covers creating the follow; a token also
	// needs write access to what the follow fills.
	if req.TargetKind == TargetPortfolio {
		err = auth.CheckScope(c, auth.ScopeHoldingsWrite)
	} else {
		err = auth.CheckScope(c, auth.ScopeWatchlistWrite)
	}
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	snap, err := h.ent.Resolve(ctx, s.UserID)
	if err != nil {
		return err
	}
	f, err := h.follows.Create(ctx, req, s.UserID, snap, h.now())
	if err != nil {
		return err
	}
	achievementhooks.Schedule(s.UserID)
	r, err := h.response(ctx, f)
	if err != nil {
		return err
	}
	return httpx.Created(c, r)
}

func (h *handlers) updateFollow(c *echo.Context) error {
	if err := requireEnabled(); err != nil {
		return err
	}
	f, err := h.ownedFollow(c)
	if err != nil {
		return err
	}
	var body updateRequest
	if err := httpx.Decode(c, &body); err != nil {
		return err
	}
	if err := required("status", body.Status != nil); err != nil {
		return err
	}
	if err := oneOf("PilotFollowStatus", *body.Status, StatusActive, StatusPaused); err != nil {
		return err
	}
	ctx := c.Request().Context()
	updated, err := h.q.UpdateFollowStatus(ctx, pilotsdb.UpdateFollowStatusParams{ID: f.ID, Status: *body.Status})
	switch {
	case err == nil:
		f = updated
	case errors.Is(err, pgx.ErrNoRows): // unchanged: Fluent writes nothing
	default:
		return err
	}
	r, err := h.response(ctx, f)
	if err != nil {
		return err
	}
	return httpx.OK(c, r)
}

// deleteFollow stops the follow. The portfolio or watchlist it wrote to is
// kept: those are the user's now.
func (h *handlers) deleteFollow(c *echo.Context) error {
	if err := requireEnabled(); err != nil {
		return err
	}
	f, err := h.ownedFollow(c)
	if err != nil {
		return err
	}
	if err := h.q.DeleteFollow(c.Request().Context(), f.ID); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

func (h *handlers) events(c *echo.Context) error {
	if err := requireEnabled(); err != nil {
		return err
	}
	f, err := h.ownedFollow(c)
	if err != nil {
		return err
	}
	rows, err := h.q.ListFollowEvents(c.Request().Context(), f.ID)
	if err != nil {
		return err
	}
	out := make([]PilotFollowEventResponse, 0, len(rows))
	for i := range rows {
		e := &rows[i]
		out = append(out, PilotFollowEventResponse{
			ID: apijson.FormatUUID(e.ID), BookVersion: int(e.BookVersion), Kind: e.Kind, Symbol: e.Symbol,
			Quantity: e.Quantity, Price: e.Price, PricedAt: iso(e.PricedAt), Note: e.Note,
		})
	}
	return httpx.OK(c, out)
}

func (h *handlers) snapshots(c *echo.Context) error {
	if err := requireEnabled(); err != nil {
		return err
	}
	f, err := h.ownedFollow(c)
	if err != nil {
		return err
	}
	rows, err := h.q.ListFollowSnapshots(c.Request().Context(), f.ID)
	if err != nil {
		return err
	}
	out := make([]PilotFollowSnapshotResponse, 0, len(rows))
	for _, s := range rows {
		out = append(out, PilotFollowSnapshotResponse{Date: s.CapturedOn.UTC().Format(time.DateOnly), Value: s.Value, Cash: s.Cash})
	}
	return httpx.OK(c, out)
}
