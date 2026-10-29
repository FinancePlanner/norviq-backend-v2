package assistant

import (
	"encoding/json"
	"errors"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/achievementhooks"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/platform/swiftjson"
	"norviq-api/internal/store/assistantdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// Wire types from StockPlanShared/AI/PositionMemoDTOs.swift.

// MemoCard is PositionMemoCard.
type MemoCard struct {
	ID         string `json:"id"`
	Symbol     string `json:"symbol"`
	Title      string `json:"title"`
	Verdict    string `json:"verdict"`
	Bookmarked bool   `json:"bookmarked"`
}

// MemoMark is PositionMemoMark. Every optional is omitted when nil, both on
// the wire and in the sealed column (plain JSONEncoder).
type MemoMark struct {
	Cost                        *float64 `json:"cost,omitempty"`
	CostCurrency                *string  `json:"costCurrency,omitempty"`
	CostSource                  string   `json:"costSource"`
	LiveSymbol                  *string  `json:"liveSymbol,omitempty"`
	LivePrice                   *float64 `json:"livePrice,omitempty"`
	LiveCurrency                *string  `json:"liveCurrency,omitempty"`
	PrimarySymbol               *string  `json:"primarySymbol,omitempty"`
	PrimaryPrice                *float64 `json:"primaryPrice,omitempty"`
	PrimaryCurrency             *string  `json:"primaryCurrency,omitempty"`
	FxPair                      *string  `json:"fxPair,omitempty"`
	FxRate                      *float64 `json:"fxRate,omitempty"`
	FxDate                      *string  `json:"fxDate,omitempty"`
	PriceInCostCurrency         *float64 `json:"priceInCostCurrency,omitempty"`
	DrawdownPercent             *float64 `json:"drawdownPercent,omitempty"`
	StatedPercent               *float64 `json:"statedPercent,omitempty"`
	PriceImpliedByStatedPercent *float64 `json:"priceImpliedByStatedPercent,omitempty"`
	BreakevenPercent            *float64 `json:"breakevenPercent,omitempty"`
	Shares                      *float64 `json:"shares,omitempty"`
	LotAveragePrice             *float64 `json:"lotAveragePrice,omitempty"`
}

// MemoSection is PositionMemoSection.
type MemoSection struct {
	Heading    string   `json:"heading"`
	Paragraphs []string `json:"paragraphs"`
}

// MemoSource is PositionMemoSource.
type MemoSource struct {
	Label  string  `json:"label"`
	Symbol *string `json:"symbol,omitempty"`
	AsOf   *string `json:"asOf,omitempty"`
	URL    *string `json:"url,omitempty"`
}

type memoListItem struct {
	ID            string `json:"id"`
	AskedSymbol   string `json:"askedSymbol"`
	PrimarySymbol string `json:"primarySymbol"`
	Title         string `json:"title"`
	Verdict       string `json:"verdict"`
	Bookmarked    bool   `json:"bookmarked"`
	CreatedAt     string `json:"createdAt"`
}

type memoDetail struct {
	ID            string        `json:"id"`
	AskedSymbol   string        `json:"askedSymbol"`
	PrimarySymbol string        `json:"primarySymbol"`
	Title         string        `json:"title"`
	Mark          MemoMark      `json:"mark"`
	Sections      []MemoSection `json:"sections"`
	Verdict       string        `json:"verdict"`
	Sources       []MemoSource  `json:"sources"`
	Footer        string        `json:"footer"`
	Bookmarked    bool          `json:"bookmarked"`
	CreatedAt     string        `json:"createdAt"`
}

// memoFooter is PositionMemoCopy.footer.
const memoFooter = "This is Q's view of your holding, not financial advice. Figures come from the sources listed and from your saved cost."

// requireMemoAccess: session, then the Pro gate (aiInsights).
func (h *handlers) requireMemoAccess(c *echo.Context) (uuid.UUID, error) {
	s, err := auth.Require(c)
	if err != nil {
		return uuid.Nil, err
	}
	if err := h.ent.RequirePremium(c.Request().Context(), entitlements.FeatureAIInsights, s.UserID); err != nil {
		return uuid.Nil, err
	}
	return s.UserID, nil
}

// seal is PositionMemoService.seal: plain JSONEncoder, then PII.
func (h *handlers) seal(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return h.pii.EncryptString(string(raw))
}

// open is PositionMemoService.open. A JSON-encoded String column decodes
// into a Go string the same way.
func (h *handlers) open(payload []byte, v any) error {
	text, err := h.pii.DecryptString(payload)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(text), v)
}

func (h *handlers) memoCard(row assistantdb.PositionMemo) (MemoCard, error) {
	var title, verdict string
	if err := h.open(row.TitleEncrypted, &title); err != nil {
		return MemoCard{}, err
	}
	if err := h.open(row.VerdictEncrypted, &verdict); err != nil {
		return MemoCard{}, err
	}
	return MemoCard{ID: apijson.FormatUUID(row.ID), Symbol: row.PrimarySymbol, Title: title, Verdict: verdict, Bookmarked: row.Bookmarked}, nil
}

func (h *handlers) listMemos(c *echo.Context) error {
	userID, err := h.requireMemoAccess(c)
	if err != nil {
		return err
	}
	params := assistantdb.ListMemosParams{UserID: userID, Bookmarked: httpx.QueryOptionalBool(c, "bookmarked")}
	if raw := httpx.QueryString(c, "conversationId"); raw != nil {
		if id, ok := portfolios.ParseSwiftUUID(*raw); ok {
			params.ConversationID = &id
		}
	}
	rows, err := h.q.ListMemos(c.Request().Context(), params)
	if err != nil {
		return err
	}
	now := h.now()
	out := make([]memoListItem, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		card, err := h.memoCard(*row)
		if err != nil {
			return err
		}
		out = append(out, memoListItem{
			ID: card.ID, AskedSymbol: row.AskedSymbol, PrimarySymbol: row.PrimarySymbol, Title: card.Title,
			Verdict: card.Verdict, Bookmarked: row.Bookmarked, CreatedAt: isoTimestamp(row.CreatedAt, now),
		})
	}
	return httpx.OK(c, out)
}

// memoID reads the :id the memo routes take; a bad one is a bare 400.
func memoID(c *echo.Context) (uuid.UUID, error) {
	id, ok := portfolios.ParseSwiftUUID(c.Param("id"))
	if !ok {
		return uuid.Nil, httpx.BadRequest("")
	}
	return id, nil
}

func (h *handlers) ownedMemo(c *echo.Context, id, userID uuid.UUID) (assistantdb.PositionMemo, error) {
	row, err := h.q.GetOwnedMemo(c.Request().Context(), assistantdb.GetOwnedMemoParams{ID: id, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return row, httpx.NotFound("")
	}
	return row, err
}

func (h *handlers) getMemo(c *echo.Context) error {
	userID, err := h.requireMemoAccess(c)
	if err != nil {
		return err
	}
	id, err := memoID(c)
	if err != nil {
		return err
	}
	row, err := h.ownedMemo(c, id, userID)
	if err != nil {
		return err
	}
	out := memoDetail{
		ID: apijson.FormatUUID(row.ID), AskedSymbol: row.AskedSymbol, PrimarySymbol: row.PrimarySymbol,
		Footer: memoFooter, Bookmarked: row.Bookmarked, CreatedAt: isoTimestamp(row.CreatedAt, h.now()),
	}
	if err := h.open(row.TitleEncrypted, &out.Title); err != nil {
		return err
	}
	if err := h.open(row.MarkEncrypted, &out.Mark); err != nil {
		return err
	}
	if err := h.open(row.SectionsEncrypted, &out.Sections); err != nil {
		return err
	}
	if err := h.open(row.VerdictEncrypted, &out.Verdict); err != nil {
		return err
	}
	if err := h.open(row.SourcesEncrypted, &out.Sources); err != nil {
		return err
	}
	if out.Sections == nil {
		out.Sections = []MemoSection{}
	}
	if out.Sources == nil {
		out.Sources = []MemoSource{}
	}
	for i := range out.Sections {
		if out.Sections[i].Paragraphs == nil {
			out.Sections[i].Paragraphs = []string{}
		}
	}
	return httpx.OK(c, out)
}

func (h *handlers) bookmarkMemo(c *echo.Context) error {
	userID, err := h.requireMemoAccess(c)
	if err != nil {
		return err
	}
	id, err := memoID(c)
	if err != nil {
		return err
	}
	var body struct {
		Bookmarked bool `json:"bookmarked"`
	}
	if err := swiftjson.DecodeRequest(c, &body); err != nil {
		return err
	}
	row, err := h.ownedMemo(c, id, userID)
	if err != nil {
		return err
	}
	if err := h.q.SetMemoBookmarked(c.Request().Context(), assistantdb.SetMemoBookmarkedParams{ID: row.ID, Bookmarked: body.Bookmarked}); err != nil {
		return err
	}
	row.Bookmarked = body.Bookmarked
	card, err := h.memoCard(row)
	if err != nil {
		return err
	}
	achievementhooks.Schedule(userID)
	return httpx.OK(c, card)
}

func (h *handlers) deleteMemo(c *echo.Context) error {
	userID, err := h.requireMemoAccess(c)
	if err != nil {
		return err
	}
	id, err := memoID(c)
	if err != nil {
		return err
	}
	row, err := h.ownedMemo(c, id, userID)
	if err != nil {
		return err
	}
	if err := h.q.DeleteMemo(c.Request().Context(), row.ID); err != nil {
		return err
	}
	return httpx.NoContent(c)
}
