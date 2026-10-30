package brokers

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/crypto"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/store/brokersdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// sodWebServiceMarker is IBKRConnectMarkers.sodWebService: the externalId of
// an IBKR Web Service (SOD) connection.
const sodWebServiceMarker = "sod-web-service"

// Connection modes (IBKRConnectionMode raw values).
const (
	modeGateway = "Gateway"
	modeSOD     = "Web Service"
)

func connectionMode(externalID *string) (string, bool) {
	if externalID == nil {
		return "", false
	}
	if *externalID == sodWebServiceMarker {
		return modeSOD, true
	}
	return modeGateway, true
}

// service is DefaultBrokersService plus the sync services it builds.
type service struct {
	db           *pgxpool.Pool
	logger       *slog.Logger
	vault        *crypto.Vault
	gateway      gatewayClient // the configured gateway client
	oauth        *oauthClient  // nil when IBKR OAuth2 is not configured
	oauthOn      bool          // ibkrConnectMode == .oauth2
	http         *http.Client
	search       symbolSearch
	env          func(string) string
	now          func() time.Time
	sleep        func(ctx context.Context, d time.Duration) error
	entitlements *entitlements.Service
	extractor    Extractor
}

func (s *service) q() *brokersdb.Queries { return brokersdb.New(s.db) }

// conn is a BrokerConnection model: persisted false until first saved.
type conn struct {
	brokersdb.BrokerConnection
	persisted bool
}

// save is Fluent's save: an insert the first time, an update after.
func (s *service) save(ctx context.Context, db brokersdb.DBTX, c *conn) error {
	q := brokersdb.New(db)
	now := s.now()
	if !c.persisted {
		row, err := q.InsertConnection(ctx, brokersdb.InsertConnectionParams{
			ID: c.ID, UserID: c.UserID, Provider: c.Provider, ExternalID: c.ExternalID,
			AccessToken: c.AccessToken, RefreshToken: c.RefreshToken, ExpiresAt: c.ExpiresAt,
			Status: c.Status, DisplayName: c.DisplayName, StatusDetail: c.StatusDetail,
			ConnectedAt: c.ConnectedAt, LastSyncedAt: c.LastSyncedAt, PortfolioListID: c.PortfolioListID, Now: now,
		})
		if err != nil {
			return err
		}
		c.BrokerConnection, c.persisted = row, true
		return nil
	}
	row, err := q.UpdateConnection(ctx, brokersdb.UpdateConnectionParams{
		ID: c.ID, ExternalID: c.ExternalID, AccessToken: c.AccessToken, RefreshToken: c.RefreshToken,
		ExpiresAt: c.ExpiresAt, Status: c.Status, DisplayName: c.DisplayName, StatusDetail: c.StatusDetail,
		ConnectedAt: c.ConnectedAt, LastSyncedAt: c.LastSyncedAt, PortfolioListID: c.PortfolioListID, Now: &now,
	})
	if err != nil {
		return err
	}
	c.BrokerConnection = row
	return nil
}

// find is DatabaseBrokersRepository.find.
func (s *service) find(ctx context.Context, db brokersdb.DBTX, provider string, userID uuid.UUID) (*conn, error) {
	rows, err := brokersdb.New(db).FindConnection(ctx, brokersdb.FindConnectionParams{UserID: userID, Provider: provider})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &conn{BrokerConnection: rows[0], persisted: true}, nil
}

func (s *service) list(ctx context.Context, userID uuid.UUID) ([]ConnectionResponse, error) {
	rows, err := s.q().ListConnections(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]ConnectionResponse, len(rows))
	for i := range rows {
		r := &rows[i]
		out[i] = connectionResponse(*r)
	}
	return out, nil
}

func (s *service) get(ctx context.Context, provider string, userID uuid.UUID) (ConnectionResponse, error) {
	normalized, err := NormalizeProvider(provider)
	if err != nil {
		return ConnectionResponse{}, err
	}
	c, err := s.find(ctx, s.db, normalized, userID)
	if err != nil {
		return ConnectionResponse{}, err
	}
	if c == nil {
		return ConnectionResponse{}, errBrokerNotFound()
	}
	return connectionResponse(c.BrokerConnection), nil
}

// recordCsvImport is DatabaseBrokersRepository.upsertCsvImport: a token-less
// connection (or a new one) gets status "csv"; every one is touched.
func (s *service) recordCsvImport(ctx context.Context, provider string, userID uuid.UUID) (ConnectionResponse, error) {
	normalized, err := NormalizeProvider(provider)
	if err != nil {
		return ConnectionResponse{}, err
	}
	c, err := s.find(ctx, s.db, normalized, userID)
	if err != nil {
		return ConnectionResponse{}, err
	}
	if c == nil {
		c = &conn{BrokerConnection: brokersdb.BrokerConnection{ID: uuid.New(), UserID: userID, Provider: normalized, Status: "csv"}}
	} else if c.AccessToken == nil && c.RefreshToken == nil && c.ExternalID == nil {
		c.Status = "csv"
	}
	if err := s.save(ctx, s.db, c); err != nil {
		return ConnectionResponse{}, err
	}
	return connectionResponse(c.BrokerConnection), nil
}

// connectionUpdate is upsertBrokerConnection's arguments; nil keeps the
// stored value (except Status and StatusDetail, which always overwrite).
type connectionUpdate struct {
	externalID, displayName, statusDetail *string
	status                                string
	connectedAt, lastSyncedAt, expiresAt  *time.Time
	portfolioListID                       *uuid.UUID
	accessToken, refreshToken             *string // plaintext; encrypted here
}

func (s *service) upsertConnection(ctx context.Context, provider string, userID uuid.UUID, u connectionUpdate) (*conn, error) {
	c, err := s.find(ctx, s.db, provider, userID)
	if err != nil {
		return nil, err
	}
	if c == nil {
		c = &conn{BrokerConnection: brokersdb.BrokerConnection{ID: uuid.New(), UserID: userID, Provider: provider, Status: u.status}}
	}
	if u.externalID != nil {
		c.ExternalID = u.externalID
	}
	if u.accessToken != nil {
		enc, err := s.vault.Encrypt(*u.accessToken, crypto.ContextBroker)
		if err != nil {
			return nil, err
		}
		c.AccessToken = &enc
	}
	if u.refreshToken != nil {
		enc, err := s.vault.Encrypt(*u.refreshToken, crypto.ContextBroker)
		if err != nil {
			return nil, err
		}
		c.RefreshToken = &enc
	}
	if u.expiresAt != nil {
		c.ExpiresAt = u.expiresAt
	}
	if u.displayName != nil {
		c.DisplayName = u.displayName
	}
	c.Status = u.status
	c.StatusDetail = u.statusDetail
	if u.connectedAt != nil {
		c.ConnectedAt = u.connectedAt
	}
	if u.lastSyncedAt != nil {
		c.LastSyncedAt = u.lastSyncedAt
	}
	if u.portfolioListID != nil {
		c.PortfolioListID = u.portfolioListID
	}
	if err := s.save(ctx, s.db, c); err != nil {
		return nil, err
	}
	return c, nil
}

// modeConflict is ibkrConnectionModeConflictReason.
func (s *service) modeConflict(ctx context.Context, desired string, userID uuid.UUID) (string, error) {
	existing, err := s.find(ctx, s.db, "ibkr", userID)
	if err != nil || existing == nil || existing.Status == "disconnected" {
		return "", err
	}
	mode, ok := connectionMode(existing.ExternalID)
	if !ok || mode == desired {
		return "", nil
	}
	return "Disconnect the existing IBKR " + mode + " connection before connecting IBKR " + desired + ".", nil
}

// resolveListForConnect is resolvePortfolioListId(defaultWhenMissing: true)
// followed by PilotFollowGuard.ensureNotFollowed.
func (s *service) resolveListForConnect(ctx context.Context, requested *string, userID uuid.UUID) (*uuid.UUID, error) {
	id, err := portfolios.ResolvePortfolioListID(ctx, s.db, requested, userID, true)
	if err != nil {
		return nil, err
	}
	if id != nil {
		if err := portfolios.EnsureNotFollowed(ctx, s.db, *id); err != nil {
			return nil, err
		}
	}
	return id, nil
}

// startIBKRConnect is DefaultBrokersService.startIBKRConnect.
func (s *service) startIBKRConnect(ctx context.Context, r *http.Request, redirectURI string, portfolioListID *string, userID uuid.UUID) (ConnectStartResponse, error) {
	normalized, err := normalizeRedirectURI(redirectURI)
	if err != nil {
		return ConnectStartResponse{}, err
	}
	if err := s.validateRedirectURI(normalized); err != nil {
		return ConnectStartResponse{}, err
	}
	state := randomURLSafeString(32)
	const expiresIn = 600
	listID, err := s.resolveListForConnect(ctx, portfolioListID, userID)
	if err != nil {
		return ConnectStartResponse{}, err
	}
	flow, err := s.q().InsertFlow(ctx, brokersdb.InsertFlowParams{
		ID: uuid.New(), UserID: userID, Provider: "ibkr", State: state, RedirectUri: normalized,
		PortfolioListID: listID, ExpiresAt: s.now().Add(expiresIn * time.Second), Now: s.now(),
	})
	if err != nil {
		return ConnectStartResponse{}, err
	}
	callback, err := brokerCallbackURL(r)
	if err != nil {
		return ConnectStartResponse{}, err
	}
	var authorization string
	if s.oauthOn {
		if s.oauth == nil {
			return ConnectStartResponse{}, httpx.Abort(http.StatusServiceUnavailable, "IBKR OAuth2 is not configured.")
		}
		authorization = s.oauth.authorizationURL(state, callback)
	} else {
		authorization = callback + "?" + encodeQueryItems([][2]string{{"flowId", apijson.FormatUUID(flow.ID)}, {"state", state}})
	}
	return ConnectStartResponse{FlowID: apijson.FormatUUID(flow.ID), AuthorizationURL: authorization, ExpiresIn: expiresIn}, nil
}

// connectIBKRCredentials stores IBKR Web Service (SOD) credentials.
func (s *service) connectIBKRCredentials(ctx context.Context, token, queryID string, portfolioListID *string, userID uuid.UUID) (ConnectionResponse, error) {
	token, queryID = portfolios.TrimSwift(token), portfolios.TrimSwift(queryID)
	if token == "" {
		return ConnectionResponse{}, httpx.BadRequest("IBKR Web Service token is required.")
	}
	if queryID == "" {
		return ConnectionResponse{}, httpx.BadRequest("IBKR Web Service query ID is required.")
	}
	reason, err := s.modeConflict(ctx, modeSOD, userID)
	if err != nil {
		return ConnectionResponse{}, err
	}
	if reason != "" {
		return ConnectionResponse{}, httpx.Conflict(reason)
	}
	listID, err := s.resolveListForConnect(ctx, portfolioListID, userID)
	if err != nil {
		return ConnectionResponse{}, err
	}
	now := s.now()
	marker, display := sodWebServiceMarker, "IBKR Web Service"
	detail := "Credentials saved. Statement sync runs after IBKR publishes the daily SOD files."
	c, err := s.upsertConnection(ctx, "ibkr", userID, connectionUpdate{
		externalID: &marker, displayName: &display, status: "connected", statusDetail: &detail,
		connectedAt: &now, portfolioListID: listID, accessToken: &token, refreshToken: &queryID,
	})
	if err != nil {
		return ConnectionResponse{}, err
	}
	c.ExternalID, c.ExpiresAt = &marker, nil
	if err := s.save(ctx, s.db, c); err != nil {
		return ConnectionResponse{}, err
	}
	return connectionResponse(c.BrokerConnection), nil
}

// handleIBKRCallback is DefaultBrokersService.handleIBKRCallback: it
// always answers 303 to the app once the flow is valid.
func (s *service) handleIBKRCallback(ctx context.Context, r *http.Request, flowID *uuid.UUID, code, providerError *string, state string, onConnected func(uuid.UUID)) (string, error) {
	now := s.now()
	normalizedState := portfolios.TrimSwift(state)
	var flows []brokersdb.BrokerOauthFlow
	var err error
	if flowID != nil {
		flows, err = s.q().OpenFlowByID(ctx, brokersdb.OpenFlowByIDParams{Provider: "ibkr", ID: *flowID})
	} else {
		flows, err = s.q().OpenFlowByState(ctx, brokersdb.OpenFlowByStateParams{Provider: "ibkr", State: normalizedState})
	}
	if err != nil {
		return "", err
	}
	if len(flows) == 0 {
		return "", httpx.Abort(http.StatusUnauthorized, "Broker connect flow is invalid or expired.")
	}
	flow := flows[0]
	if !flow.ExpiresAt.After(now) {
		return "", httpx.Abort(http.StatusUnauthorized, "Broker connect flow expired.")
	}
	if flow.State != normalizedState {
		return "", httpx.Abort(http.StatusUnauthorized, "Broker connect state mismatch.")
	}
	if err := s.q().MarkFlowUsed(ctx, brokersdb.MarkFlowUsedParams{ID: flow.ID, Now: &now}); err != nil {
		return "", err
	}
	reason, err := s.modeConflict(ctx, modeGateway, flow.UserID)
	if err != nil {
		return "", err
	}
	if reason != "" {
		return brokerAppRedirectURL(flow.RedirectUri, "error", reason), nil
	}
	if err := s.connectFromCallback(ctx, r, flow, code, providerError, now); err != nil {
		message := errorReason(err)
		_, _ = s.upsertConnection(ctx, "ibkr", flow.UserID, connectionUpdate{
			status: "error", statusDetail: &message, portfolioListID: flow.PortfolioListID,
		})
		return brokerAppRedirectURL(flow.RedirectUri, "error", message), nil
	}
	if onConnected != nil {
		onConnected(flow.UserID)
	}
	return brokerAppRedirectURL(flow.RedirectUri, "success", ""), nil
}

func (s *service) connectFromCallback(ctx context.Context, r *http.Request, flow brokersdb.BrokerOauthFlow, code, providerError *string, now time.Time) error {
	if providerError != nil {
		if e := portfolios.TrimSwift(*providerError); e != "" {
			return httpx.BadRequest("IBKR authorization failed: " + e)
		}
	}
	callback, err := brokerCallbackURL(r)
	if err != nil {
		return err
	}
	client := s.gateway
	var access, refresh *string
	var expiresAt *time.Time
	if s.oauthOn {
		if s.oauth == nil {
			return httpx.Abort(http.StatusServiceUnavailable, "IBKR OAuth2 is not configured.")
		}
		if code == nil || portfolios.TrimSwift(*code) == "" {
			return httpx.BadRequest("Missing IBKR authorization code.")
		}
		tok, err := s.oauth.exchangeCode(ctx, portfolios.TrimSwift(*code), callback)
		if err != nil {
			return err
		}
		if tok.expiresIn != nil {
			t := s.now().Add(time.Duration(*tok.expiresIn) * time.Second)
			expiresAt = &t
		}
		client = s.gatewayFor(s.oauth.config.apiBaseURL, &tok.accessToken)
		access, refresh = &tok.accessToken, tok.refreshToken
	}
	account, err := client.requirePrimaryAccount(ctx)
	if err != nil {
		return err
	}
	c, err := s.upsertConnection(ctx, "ibkr", flow.UserID, connectionUpdate{
		externalID: &account.ExternalID, displayName: account.DisplayName, status: "connected",
		connectedAt: &now, portfolioListID: flow.PortfolioListID,
		accessToken: access, refreshToken: refresh, expiresAt: expiresAt,
	})
	if err != nil {
		return err
	}
	_, err = s.ibkrSync(&client).sync(ctx, c, flow.UserID)
	return err
}

// gatewayFor is IBKRBrokerGatewayClient(baseURL:defaultCurrency:accessToken:).
func (s *service) gatewayFor(baseURL string, accessToken *string) gatewayClient {
	g := s.gateway
	g.BaseURL, g.AccessToken = baseURL, accessToken
	return g
}

// syncIBKR is DefaultBrokersService.syncIBKR.
func (s *service) syncIBKR(ctx context.Context, userID uuid.UUID) (SyncResponse, error) {
	c, err := s.find(ctx, s.db, "ibkr", userID)
	if err != nil {
		return SyncResponse{}, err
	}
	if c == nil {
		return SyncResponse{}, errBrokerNotFound()
	}
	if c.ExternalID != nil && *c.ExternalID == sodWebServiceMarker {
		resp, err := s.sodSync(ctx, c)
		if err != nil {
			// The SOD service stamps statusDetail on its own errors; stamp
			// anything else here.
			var sodErr *sodError
			if c.StatusDetail == nil || !errors.As(err, &sodErr) {
				c.Status = "error"
				detail := errorReason(err)
				c.StatusDetail = &detail
				_ = s.save(ctx, s.db, c)
			}
			return SyncResponse{}, err
		}
		return resp, nil
	}
	resp, err := func() (SyncResponse, error) {
		client, err := s.syncDataClient(ctx, c)
		if err != nil {
			return SyncResponse{}, err
		}
		return s.ibkrSync(&client).sync(ctx, c, userID)
	}()
	if err != nil {
		c.Status = "error"
		detail := errorReason(err)
		c.StatusDetail = &detail
		_ = s.save(ctx, s.db, c)
		return SyncResponse{}, err
	}
	return resp, nil
}

// syncDataClient is the gateway client a stored connection syncs through:
// the plain gateway, or the OAuth Web API with a refreshed (or re-sealed
// legacy plaintext) token.
func (s *service) syncDataClient(ctx context.Context, c *conn) (gatewayClient, error) {
	if c.ExternalID != nil && *c.ExternalID == sodWebServiceMarker {
		return gatewayClient{}, httpx.Abort(http.StatusServiceUnavailable,
			"IBKR statement sync is not available yet. Your Web Service token and query ID are saved.")
	}
	if c.AccessToken == nil || portfolios.TrimSwift(*c.AccessToken) == "" || s.oauth == nil {
		return s.gateway, nil
	}
	storedAccess := portfolios.TrimSwift(*c.AccessToken)
	access, err := s.vault.Decrypt(storedAccess, crypto.ContextBroker)
	if err != nil {
		return gatewayClient{}, err
	}
	var storedRefresh, refresh *string
	if c.RefreshToken != nil {
		t := portfolios.TrimSwift(*c.RefreshToken)
		storedRefresh = &t
		if t != "" {
			plain, err := s.vault.Decrypt(t, crypto.ContextBroker)
			if err != nil {
				return gatewayClient{}, err
			}
			refresh = &plain
		}
	}
	threshold := s.now().Add(60 * time.Second)
	if c.ExpiresAt != nil && !c.ExpiresAt.After(threshold) && refresh != nil && *refresh != "" {
		tok, err := s.oauth.refresh(ctx, *refresh)
		if err != nil {
			return gatewayClient{}, err
		}
		enc, err := s.vault.Encrypt(tok.accessToken, crypto.ContextBroker)
		if err != nil {
			return gatewayClient{}, err
		}
		newRefresh := *refresh
		if tok.refreshToken != nil {
			newRefresh = *tok.refreshToken
		}
		encRefresh, err := s.vault.Encrypt(newRefresh, crypto.ContextBroker)
		if err != nil {
			return gatewayClient{}, err
		}
		c.AccessToken, c.RefreshToken = &enc, &encRefresh
		c.ExpiresAt = nil
		if tok.expiresIn != nil {
			t := s.now().Add(time.Duration(*tok.expiresIn) * time.Second)
			c.ExpiresAt = &t
		}
		if err := s.save(ctx, s.db, c); err != nil {
			return gatewayClient{}, err
		}
		return s.gatewayFor(s.oauth.config.apiBaseURL, &tok.accessToken), nil
	}
	// Legacy plaintext rows predate the encryption service; re-encrypt on first use.
	if !crypto.IsEncrypted(storedAccess) || (storedRefresh != nil && *storedRefresh != "" && !crypto.IsEncrypted(*storedRefresh)) {
		enc, err := s.vault.Encrypt(access, crypto.ContextBroker)
		if err != nil {
			return gatewayClient{}, err
		}
		c.AccessToken = &enc
		if refresh != nil && *refresh != "" {
			encRefresh, err := s.vault.Encrypt(*refresh, crypto.ContextBroker)
			if err != nil {
				return gatewayClient{}, err
			}
			c.RefreshToken = &encRefresh
		}
		if err := s.save(ctx, s.db, c); err != nil {
			return gatewayClient{}, err
		}
	}
	return s.gatewayFor(s.oauth.config.apiBaseURL, &access), nil
}

// disconnectIBKR removes the synced holdings and forgets the tokens.
func (s *service) disconnectIBKR(ctx context.Context, userID uuid.UUID) (ConnectionResponse, error) {
	c, err := s.find(ctx, s.db, "ibkr", userID)
	if err != nil {
		return ConnectionResponse{}, err
	}
	if c == nil {
		return ConnectionResponse{}, errBrokerNotFound()
	}
	provider := "ibkr"
	if err := s.q().DeleteProviderStocks(ctx, brokersdb.DeleteProviderStocksParams{UserID: userID, SourceProvider: &provider}); err != nil {
		return ConnectionResponse{}, err
	}
	c.AccessToken, c.RefreshToken, c.ExpiresAt = nil, nil, nil
	c.Status, c.StatusDetail = "disconnected", nil
	if err := s.save(ctx, s.db, c); err != nil {
		return ConnectionResponse{}, err
	}
	return connectionResponse(c.BrokerConnection), nil
}

// MARK: - URL helpers

// normalizeRedirectURI trims and requires URL(string:) to parse with a
// scheme and either a host or an absolute path.
func normalizeRedirectURI(raw string) (string, error) {
	n := portfolios.TrimSwift(raw)
	invalid := httpx.BadRequest("Invalid broker redirect URI.")
	if n == "" || strings.ContainsAny(n, " \t\r\n\"<>\\^`{|}") {
		return "", invalid
	}
	u, err := url.Parse(n)
	if err != nil || u.Scheme == "" {
		return "", invalid
	}
	if u.Host == "" && !strings.HasPrefix(u.Path, "/") {
		return "", invalid
	}
	return n, nil
}

func (s *service) validateRedirectURI(uri string) error {
	var allowed []string
	for _, a := range strings.Split(s.env("OAUTH_ALLOWED_REDIRECT_URIS"), ",") {
		if a = portfolios.TrimSwift(a); a != "" {
			allowed = append(allowed, a)
		}
	}
	if len(allowed) == 0 {
		return httpx.Abort(http.StatusServiceUnavailable, "OAuth redirect allowlist is not configured.")
	}
	for _, a := range allowed {
		if a == uri {
			return nil
		}
	}
	return httpx.BadRequest("Broker redirect URI is not allowed.")
}

// brokerCallbackURL is makeBrokerCallbackURL: X-Forwarded-Proto (default
// https) + Host + /v1/auth/brokers/ibkr/callback.
func brokerCallbackURL(r *http.Request) (string, error) {
	scheme := "https"
	if v := r.Header.Values("X-Forwarded-Proto"); len(v) > 0 {
		scheme = v[0]
	}
	host := r.Host
	if host == "" {
		return "", httpx.Abort(http.StatusInternalServerError, "Missing request host.")
	}
	u, err := url.Parse(scheme + "://" + host)
	if err != nil || u.Host == "" {
		return "", httpx.Abort(http.StatusInternalServerError, "Failed to build broker callback URL.")
	}
	return scheme + "://" + host + "/v1/auth/brokers/ibkr/callback", nil
}

// brokerAppRedirectURL appends broker, status (and a non-empty error) to the
// app's redirect URI, keeping its own query items.
func brokerAppRedirectURL(base, status, errMessage string) string {
	u, err := url.Parse(base)
	if err != nil {
		u = &url.URL{}
	}
	var items [][2]string
	if u.RawQuery != "" {
		for _, pair := range strings.Split(u.RawQuery, "&") {
			k, v, _ := strings.Cut(pair, "=")
			dk, err1 := url.PathUnescape(k)
			dv, err2 := url.PathUnescape(v)
			if err1 != nil || err2 != nil {
				dk, dv = k, v
			}
			items = append(items, [2]string{dk, dv})
		}
	}
	items = append(items, [2]string{"broker", "ibkr"}, [2]string{"status", status})
	if errMessage != "" {
		items = append(items, [2]string{"error", errMessage})
	}
	u.RawQuery = encodeQueryItems(items)
	u.ForceQuery = false
	return u.String()
}

// encodeQueryItems is URLComponents.queryItems' encoding: everything outside
// the query-allowed set (minus & and =) is percent-encoded; spaces are %20.
func encodeQueryItems(items [][2]string) string {
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = queryEscape(it[0]) + "=" + queryEscape(it[1])
	}
	return strings.Join(parts, "&")
}

func queryEscape(s string) string {
	const allowed = "-._~!$'()*+,;:@/?"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(allowed, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteString("%" + strings.ToUpper(string("0123456789abcdef"[c>>4])+string("0123456789abcdef"[c&15])))
	}
	return b.String()
}

// randomURLSafeString draws from the RFC 3986 unreserved alphabet.
func randomURLSafeString(length int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._~"
	out := make([]byte, length)
	for i := range out {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			panic(err)
		}
		out[i] = alphabet[n.Int64()]
	}
	return string(out)
}

func parseSwiftUUID(s string) (uuid.UUID, bool) { return portfolios.ParseSwiftUUID(s) }
