package social

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"norviq-api/internal/httpx"
	"norviq-api/internal/store/socialdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// XFollowingImport: "Find friends from X", a one-off OAuth flow with
// follows.read. The X token is used for this one request and never stored.
const (
	xProvider    = "x"
	xFlowPurpose = "social_x_import"
	xScopes      = "tweet.read users.read follows.read"
	// X caps a page at 1,000; five pages covers all but very large accounts.
	xMaxPages = 5
)

// XImportMatch is SocialXImportMatch.
type XImportMatch struct {
	XHandle string      `json:"xHandle"`
	User    UserSummary `json:"user"`
}

// XImportMatches is SocialXImportMatchesResponse.
type XImportMatches struct {
	Matches               []XImportMatch `json:"matches"`
	TotalFollowingScanned int            `json:"totalFollowingScanned"`
}

func (h *handlers) xConfig() (*xConfig, error) {
	if !ConfigFromEnv().XImport {
		return nil, httpx.NotFound("X import is not available.")
	}
	cfg := xConfigFromEnv()
	if cfg == nil {
		return nil, httpx.NotFound("X import is not available.")
	}
	return cfg, nil
}

// POST /v1/social/discovery/x/start
func (h *handlers) xStart(c *echo.Context) error {
	userID := sessionUser(c)
	cfg, err := h.xConfig()
	if err != nil {
		return err
	}
	raw, err := decodeStart(c)
	if err != nil {
		return err
	}
	redirect, err := validatedRedirectURI(raw, h.production)
	if err != nil {
		return err
	}
	state, verifier := randomURLSafeString(32), randomURLSafeString(64)
	id, err := h.startFlow(c.Request().Context(), xProvider, xFlowPurpose, redirect, userID, state, randomURLSafeString(32), verifier)
	if err != nil {
		return err
	}
	authURL := withQuery(h.ep.XAuthorize, [][2]string{
		{"response_type", "code"},
		{"client_id", cfg.ClientID},
		{"redirect_uri", redirect},
		{"scope", xScopes},
		{"state", state},
		{"code_challenge", codeChallenge(verifier)},
		{"code_challenge_method", "S256"},
	})
	return httpx.OK(c, oauthStartResponse{FlowID: apiUUID(id), AuthorizationURL: authURL, ExpiresIn: 600})
}

type xUser struct {
	ID       string  `json:"id"`
	Username *string `json:"username"`
}

// POST /v1/social/discovery/x/exchange
func (h *handlers) xExchange(c *echo.Context) error {
	userID := sessionUser(c)
	cfg, err := h.xConfig()
	if err != nil {
		return err
	}
	in, err := decodeExchange(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	flow, redirect, err := h.consumeFlow(ctx, in, xProvider, xFlowPurpose, userID, "X import flow is invalid or expired")
	if err != nil {
		return err
	}
	token, err := h.xExchangeCode(ctx, strings.TrimSpace(in.code), redirect, flow.CodeVerifier, cfg)
	if err != nil {
		return err
	}
	var me struct {
		Data *xUser `json:"data"`
	}
	if err := h.xFetch(ctx, h.ep.XAPI+"/2/users/me", token, &me); err != nil {
		return err
	}
	if me.Data == nil {
		return xDecodeError()
	}
	following, err := h.xFollowing(ctx, me.Data.ID, token)
	if err != nil {
		return err
	}

	handles := map[string]string{}
	var ids []string
	for _, u := range following {
		if _, ok := handles[u.ID]; ok {
			continue
		}
		handle := u.ID
		if u.Username != nil {
			handle = *u.Username
		}
		handles[u.ID] = handle
		ids = append(ids, u.ID)
	}
	handleByUser := map[uuid.UUID]string{}
	if len(ids) > 0 {
		identities, err := socialdb.New(h.svc.DB).IdentitiesByProviderUserIDs(ctx, socialdb.IdentitiesByProviderUserIDsParams{
			Provider: xProvider, ProviderUserIds: ids,
		})
		if err != nil {
			return err
		}
		for _, i := range identities {
			if i.UserID != userID {
				handleByUser[i.UserID] = handles[i.ProviderUserID]
			}
		}
	}
	hidden, err := h.svc.BlockedEitherWay(ctx, userID)
	if err != nil {
		return err
	}
	var candidates []uuid.UUID
	for id := range handleByUser {
		if _, ok := hidden[id]; !ok {
			candidates = append(candidates, id)
		}
	}
	settings, err := h.svc.Settings(ctx, candidates)
	if err != nil {
		return err
	}
	var allowed []uuid.UUID
	for _, id := range candidates {
		if st, ok := settings[id]; !ok || st.DiscoverableByX {
			allowed = append(allowed, id)
		}
	}
	summaries, err := h.svc.Summaries(ctx, allowed, userID)
	if err != nil {
		return err
	}
	out := XImportMatches{Matches: []XImportMatch{}, TotalFollowingScanned: len(following)}
	for _, id := range allowed {
		if u, ok := summaries[id]; ok {
			out.Matches = append(out.Matches, XImportMatch{XHandle: handleByUser[id], User: u})
		}
	}
	sort.SliceStable(out.Matches, func(i, j int) bool {
		return strings.ToLower(out.Matches[i].XHandle) < strings.ToLower(out.Matches[j].XHandle)
	})
	return httpx.OK(c, out)
}

func (h *handlers) xExchangeCode(ctx context.Context, code, redirect, verifier string, cfg *xConfig) (string, error) {
	if code == "" {
		return "", httpx.BadRequest("OAuth authorization code is required")
	}
	form := buildQuery([][2]string{
		{"grant_type", "authorization_code"},
		{"code", code},
		{"redirect_uri", redirect},
		{"code_verifier", verifier},
		{"client_id", cfg.ClientID},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.ep.XToken, strings.NewReader(form))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if cfg.ClientSecret != nil {
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(cfg.ClientID+":"+*cfg.ClientSecret)))
	}
	r, err := h.do(req)
	if err != nil {
		return "", err
	}
	var tok struct {
		AccessToken *string `json:"access_token"`
	}
	if r.status != http.StatusOK || json.Unmarshal(r.body, &tok) != nil || tok.AccessToken == nil || *tok.AccessToken == "" {
		h.log.Warn("social.x_import token exchange failed", "status", r.status)
		return "", httpx.Abort(http.StatusBadGateway, "X didn't accept the sign-in. Please try again.")
	}
	return *tok.AccessToken, nil
}

func (h *handlers) xFollowing(ctx context.Context, xUserID, token string) ([]xUser, error) {
	users := []xUser{}
	next := ""
	for range xMaxPages {
		items := [][2]string{{"max_results", "1000"}, {"user.fields", "username"}}
		if next != "" {
			items = append(items, [2]string{"pagination_token", next})
		}
		var page struct {
			Data []xUser `json:"data"`
			Meta *struct {
				NextToken *string `json:"next_token"`
			} `json:"meta"`
		}
		if err := h.xFetch(ctx, withQuery(h.ep.XAPI+"/2/users/"+xUserID+"/following", items), token, &page); err != nil {
			return nil, err
		}
		users = append(users, page.Data...)
		if page.Meta == nil || page.Meta.NextToken == nil || *page.Meta.NextToken == "" {
			break
		}
		next = *page.Meta.NextToken
	}
	return users, nil
}

// xDecodeError stands in for the DecodingError Vapor turned into a 400 when
// X answered 200 with an unexpected body.
func xDecodeError() error {
	return httpx.BadRequest("Data corrupted at path ''. The given data was not valid JSON.")
}

func (h *handlers) xFetch(ctx context.Context, target, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	r, err := h.do(req)
	if err != nil {
		return err
	}
	if r.status != http.StatusOK {
		h.log.Warn("social.x_import request failed", "status", r.status)
		if r.status == http.StatusTooManyRequests {
			return httpx.Abort(http.StatusBadGateway, "X is rate limiting this right now. Try again in 15 minutes.")
		}
		return httpx.Abort(http.StatusBadGateway, "Couldn't read who you follow on X.")
	}
	if err := json.Unmarshal(r.body, out); err != nil {
		return xDecodeError()
	}
	return nil
}
