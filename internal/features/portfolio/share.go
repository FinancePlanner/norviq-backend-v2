package portfolio

import (
	"crypto/rand"
	"encoding/base64"
	"os"
	"strings"
	"unicode"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/store/portfoliodb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// ShareAllScope is PortfolioShareController.allScope: every actual
// portfolio the caller owns.
const ShareAllScope = "all"

// ShareLinkResponse is StockPlanShared.PortfolioShareLinkResponse.
type ShareLinkResponse struct {
	Slug      string `json:"slug"`
	URL       string `json:"url"`
	Scope     string `json:"scope"`
	CreatedAt string `json:"createdAt"`
}

type shareLinkStatusResponse struct {
	Link *ShareLinkResponse `json:"link,omitempty"`
}

// ShareBaseURL is PortfolioShareController.baseURL(): the web host that
// renders /p/{slug}, read from SHARE_PORTFOLIO_BASE_URL on every call.
func ShareBaseURL() string {
	// trimmingCharacters(in: .whitespaces): spaces and tabs, not newlines.
	raw := strings.TrimFunc(os.Getenv("SHARE_PORTFOLIO_BASE_URL"), func(r rune) bool {
		return r == '\t' || unicode.Is(unicode.Zs, r)
	})
	if raw == "" {
		raw = "https://norviq.org"
	}
	return strings.TrimSuffix(raw, "/")
}

// NewShareSlug is OpaqueToken.generate(prefix: "p", byteCount: 16).
func NewShareSlug() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "p" + base64.RawURLEncoding.EncodeToString(b)
}

// ShareLink is makeResponse(_:scope:).
func ShareLink(l portfoliodb.PortfolioShareLink, scope string) ShareLinkResponse {
	return ShareLinkResponse{
		Slug:      l.Slug,
		URL:       ShareBaseURL() + "/p/" + l.Slug,
		Scope:     scope,
		CreatedAt: apijson.FormatTime(l.CreatedAt),
	}
}

type ownerScope struct {
	userID uuid.UUID
	listID *uuid.UUID
	scope  string
}

// resolveOwnerScope: only the owner may share a specific portfolio; "all"
// is always the caller's own.
func (h *handlers) resolveOwnerScope(c *echo.Context) (ownerScope, error) {
	s, err := auth.Require(c)
	if err != nil {
		return ownerScope{}, err
	}
	scope := c.Param("scope")
	if scope == "" {
		return ownerScope{}, httpx.BadRequest("")
	}
	if scope == ShareAllScope {
		return ownerScope{userID: s.UserID, scope: scope}, nil
	}
	f, err := h.filters.Resolve(c.Request().Context(), h.d.DB, &scope, s.UserID, true)
	if err != nil {
		return ownerScope{}, err
	}
	return ownerScope{userID: s.UserID, listID: f.PortfolioID, scope: scope}, nil
}

func (h *handlers) activeLink(c *echo.Context, o ownerScope) (*portfoliodb.PortfolioShareLink, error) {
	rows, err := portfoliodb.New(h.d.DB).ActiveShareLinkForScope(c.Request().Context(), portfoliodb.ActiveShareLinkForScopeParams{UserID: o.userID, PortfolioListID: o.listID})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

func (h *handlers) listShareLinks(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	rows, err := portfoliodb.New(h.d.DB).ActiveShareLinksForUser(c.Request().Context(), s.UserID)
	if err != nil {
		return err
	}
	out := make([]ShareLinkResponse, 0, len(rows))
	for _, l := range rows {
		scope := ShareAllScope
		if l.PortfolioListID != nil {
			scope = apijson.FormatUUID(*l.PortfolioListID)
		}
		out = append(out, ShareLink(l, scope))
	}
	return httpx.OK(c, out)
}

func (h *handlers) shareLinkStatus(c *echo.Context) error {
	o, err := h.resolveOwnerScope(c)
	if err != nil {
		return err
	}
	link, err := h.activeLink(c, o)
	if err != nil {
		return err
	}
	var resp shareLinkStatusResponse
	if link != nil {
		r := ShareLink(*link, o.scope)
		resp.Link = &r
	}
	return httpx.OK(c, resp)
}

func (h *handlers) createShareLink(c *echo.Context) error {
	o, err := h.resolveOwnerScope(c)
	if err != nil {
		return err
	}
	existing, err := h.activeLink(c, o)
	if err != nil {
		return err
	}
	if existing != nil {
		return httpx.OK(c, ShareLink(*existing, o.scope))
	}
	link, err := portfoliodb.New(h.d.DB).InsertShareLink(c.Request().Context(), portfoliodb.InsertShareLinkParams{
		ID: uuid.New(), UserID: o.userID, PortfolioListID: o.listID, Slug: NewShareSlug(), CreatedAt: portfolios.Now(),
	})
	if err != nil {
		return err
	}
	return httpx.OK(c, ShareLink(link, o.scope))
}

func (h *handlers) revokeShareLinks(c *echo.Context) error {
	o, err := h.resolveOwnerScope(c)
	if err != nil {
		return err
	}
	// Every active row: racing creates can leave duplicates.
	now := portfolios.Now()
	if err := portfoliodb.New(h.d.DB).RevokeShareLinksForScope(c.Request().Context(), portfoliodb.RevokeShareLinksForScopeParams{
		UserID: o.userID, RevokedAt: &now, PortfolioListID: o.listID,
	}); err != nil {
		return err
	}
	return httpx.NoContent(c)
}
