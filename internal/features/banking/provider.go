package banking

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"time"
	"unicode"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/store/bankingdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
)

// Call is what a provider read from Vapor's Request: the Host and
// X-Forwarded-Proto headers (hosted-link callback URLs) and the callback's
// `code` query item. Background jobs pass the zero value, as Vapor's jobs
// passed a bare Request(application:on:).
type Call struct {
	Host           string
	ForwardedProto *string
	Code           *string
}

// Provider is BankProvider: one read-only aggregator driver.
type Provider interface {
	Kind() Kind
	// CreateLinkSession begins an SDK link flow (Plaid link_token).
	CreateLinkSession(ctx context.Context, call Call, userID uuid.UUID) (LinkSessionResponse, error)
	// Exchange completes an SDK link and persists the connection.
	Exchange(ctx context.Context, call Call, req ExchangeRequest, userID uuid.UUID) (*bankingdb.BankConnection, error)
	// Sync pulls new and changed transactions. It mutates conn as Swift's
	// in-memory model was mutated before each save.
	Sync(ctx context.Context, call Call, conn *bankingdb.BankConnection) (SyncResult, error)
	// Disconnect revokes access at the aggregator.
	Disconnect(ctx context.Context, call Call, conn *bankingdb.BankConnection) error
	ListInstitutions(ctx context.Context, call Call, country string) ([]InstitutionResponse, error)
	CreateHostedLink(ctx context.Context, call Call, userID uuid.UUID, institutionID, redirectURI string) (LinkSessionResponse, error)
	CompleteHostedLink(ctx context.Context, call Call, reference string) (*bankingdb.BankConnection, error)
}

// errUnsupported is BankProviderError.unsupportedOperation.
func errUnsupported() error {
	return httpx.BadRequest("Operation not supported by this bank provider.")
}

// errNotConfigured is BankProviderError.notConfigured(kind).
func errNotConfigured(k Kind) error {
	return httpx.Abort(http.StatusServiceUnavailable, fmt.Sprintf("Bank provider %s is not configured.", k))
}

// Registry is BankProviderRegistry.
type Registry struct{ providers map[Kind]Provider }

// NewRegistry indexes providers by kind; a later one replaces an earlier one.
func NewRegistry(providers ...Provider) *Registry {
	r := &Registry{providers: map[Kind]Provider{}}
	for _, p := range providers {
		r.providers[p.Kind()] = p
	}
	return r
}

// Provider is provider(for:).
func (r *Registry) Provider(k Kind) (Provider, error) {
	if p, ok := r.providers[k]; ok {
		return p, nil
	}
	return nil, errNotConfigured(k)
}

// preferredHostedKind is Enable Banking when configured, else GoCardless.
func (r *Registry) preferredHostedKind() Kind {
	if _, ok := r.providers[KindEnableBanking]; ok {
		return KindEnableBanking
	}
	return KindGoCardless
}

// RevokeTimeout caps one BankConnectionRevoker call.
const RevokeTimeout = 10 * time.Second

// Revoke is BankConnectionRevoker.revoke: best-effort revocation at the
// aggregator, capped at RevokeTimeout. Failures are logged, never returned,
// so a local disconnect or account deletion always goes through. Account
// deletion (DELETE /v1/users) calls this too.
func (r *Registry) Revoke(ctx context.Context, logger *slog.Logger, conn *bankingdb.BankConnection) {
	err := func() error {
		p, err := r.Provider(kindOrPlaid(conn.Provider))
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(ctx, RevokeTimeout)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- p.Disconnect(ctx, Call{}, conn) }()
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return errRevokeTimedOut
		}
	}()
	if err != nil {
		logger.Warn(fmt.Sprintf("bank.revoke_failed provider=%s connection_id=%s error_type=%T",
			conn.Provider, apijson.FormatUUID(conn.ID), err))
	}
}

var errRevokeTimedOut = errors.New("BankConnectionRevoker.TimedOut")

// base is what every provider shares.
type base struct {
	q      *bankingdb.Queries
	http   *http.Client
	redis  redis.UniversalClient
	logger *slog.Logger
	now    func() time.Time
}

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// consumeDailyBudget is the PSD2 4-calls/day/account budget kept in Redis.
// Without Redis it allows the call (fails open in dev), as Vapor did.
func (b *base) consumeDailyBudget(ctx context.Context, key string, budget int64) (bool, error) {
	if b.redis == nil {
		return true, nil
	}
	count, err := b.redis.Incr(ctx, key).Result()
	if err != nil {
		return false, err
	}
	if count == 1 {
		_ = b.redis.Expire(ctx, key, 24*time.Hour).Err()
	}
	return count <= budget, nil
}

// insertConnection is `BankConnection(...).save(on:)` for a new row.
func (b *base) insertConnection(ctx context.Context, c *bankingdb.BankConnection) error {
	now := b.now()
	c.ID = uuid.New()
	c.CreatedAt = now
	c.UpdatedAt = &now
	return b.q.InsertConnection(ctx, bankingdb.InsertConnectionParams{
		ID: c.ID, UserID: c.UserID, Provider: c.Provider, InstitutionID: c.InstitutionID,
		InstitutionName: c.InstitutionName, ProviderItemID: c.ProviderItemID,
		AccessTokenEnc: c.AccessTokenEnc, Status: c.Status, ConsentExpiresAt: c.ConsentExpiresAt, Now: now,
	})
}

// markReauth is markReauthRequired: status reauth_required plus
// last_sync_status.
func (b *base) markReauth(ctx context.Context, c *bankingdb.BankConnection, lastSyncStatus string) error {
	status := statusReauthRequired
	now := b.now()
	c.Status, c.LastSyncStatus, c.UpdatedAt = status, &lastSyncStatus, &now
	return b.q.UpdateConnectionStatus(ctx, bankingdb.UpdateConnectionStatusParams{
		ID: c.ID, Status: status, LastSyncStatus: &lastSyncStatus, Now: now,
	})
}

// markSynced saves the success bookkeeping GoCardless and Enable Banking
// write after a sync.
func (b *base) markSynced(ctx context.Context, c *bankingdb.BankConnection) error {
	now := b.now()
	ok := "ok"
	c.LastSyncedAt, c.LastSyncStatus, c.LastSyncError, c.UpdatedAt = &now, &ok, nil, &now
	return b.q.UpdateConnectionSynced(ctx, bankingdb.UpdateConnectionSyncedParams{ID: c.ID, Now: now})
}

func (b *base) insertAccount(ctx context.Context, a *bankingdb.BankAccount) error {
	now := b.now()
	a.ID = uuid.New()
	a.CreatedAt = now
	a.UpdatedAt = &now
	return b.q.InsertAccount(ctx, bankingdb.InsertAccountParams{
		ID: a.ID, ConnectionID: a.ConnectionID, ProviderAccountID: a.ProviderAccountID, Name: a.Name,
		Mask: a.Mask, Currency: a.Currency, Type: a.Type, Balance: a.Balance, BalanceAsOf: a.BalanceAsOf,
		IdentificationHash: a.IdentificationHash, Now: now,
	})
}

func (b *base) updateAccount(ctx context.Context, a *bankingdb.BankAccount) error {
	now := b.now()
	a.UpdatedAt = &now
	return b.q.UpdateAccount(ctx, bankingdb.UpdateAccountParams{
		ID: a.ID, ProviderAccountID: a.ProviderAccountID, Name: a.Name, Mask: a.Mask,
		Currency: a.Currency, Type: a.Type, Balance: a.Balance, BalanceAsOf: a.BalanceAsOf,
		IdentificationHash: a.IdentificationHash, Now: now,
	})
}

// saveAccount is Fluent's save: insert a new row (zero id), else update.
func (b *base) saveAccount(ctx context.Context, a *bankingdb.BankAccount) error {
	if a.ID == uuid.Nil {
		return b.insertAccount(ctx, a)
	}
	return b.updateAccount(ctx, a)
}

func (b *base) insertTransaction(ctx context.Context, t *bankingdb.BankTransaction) error {
	now := b.now()
	t.ID = uuid.New()
	return b.q.InsertTransaction(ctx, bankingdb.InsertTransactionParams{
		ID: t.ID, AccountID: t.AccountID, UserID: t.UserID, ProviderTxID: t.ProviderTxID,
		DedupeHash: t.DedupeHash, Amount: t.Amount, Currency: t.Currency, OccurredOn: t.OccurredOn,
		Merchant: t.Merchant, DescriptionText: t.DescriptionText, Pending: t.Pending,
		ProviderCategory: t.ProviderCategory, Now: now,
	})
}

func (b *base) updateTransaction(ctx context.Context, t *bankingdb.BankTransaction) error {
	return b.q.UpdateTransactionFields(ctx, bankingdb.UpdateTransactionFieldsParams{
		ID: t.ID, ProviderTxID: t.ProviderTxID, Amount: t.Amount, Currency: t.Currency,
		OccurredOn: t.OccurredOn, Merchant: t.Merchant, DescriptionText: t.DescriptionText,
		Pending: t.Pending, DedupeHash: t.DedupeHash, ProviderCategory: t.ProviderCategory, Now: b.now(),
	})
}

// dedupeHash is the providers' SHA-256 over
// "key|date|amount(%.2f)|lowercased trimmed description".
func dedupeHash(key, date string, amount float64, description string) string {
	normalized := fmt.Sprintf("%s|%s|%.2f|%s", key, date, amount,
		strings.TrimFunc(strings.ToLower(description), isSwiftWhitespace))
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

// isSwiftWhitespace is CharacterSet.whitespaces (no newlines).
func isSwiftWhitespace(r rune) bool {
	return r == ' ' || r == '\t' || unicode.Is(unicode.Zs, r)
}

// trimmedNonEmpty is bankingTrimmedNonEmpty (whitespacesAndNewlines).
func trimmedNonEmpty(v *string) *string {
	if v == nil {
		return nil
	}
	t := strings.TrimFunc(*v, unicode.IsSpace)
	if t == "" {
		return nil
	}
	return &t
}

const referenceAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// randomReference is "nvq-" + n random alphanumerics.
func randomReference(n int) string {
	var b strings.Builder
	b.WriteString("nvq-")
	limit := big.NewInt(int64(len(referenceAlphabet)))
	for range n {
		i, err := rand.Int(rand.Reader, limit)
		if err != nil {
			panic(err)
		}
		b.WriteByte(referenceAlphabet[i.Int64()])
	}
	return b.String()
}

// callbackURL is the providers' callbackURL(req:): the request's own host,
// with X-Forwarded-Proto (default https) as the scheme.
func callbackURL(call Call, path string) (string, error) {
	scheme := "https"
	if call.ForwardedProto != nil {
		scheme = *call.ForwardedProto
	}
	if call.Host == "" {
		return "", httpx.Abort(http.StatusInternalServerError, "Missing request host.")
	}
	return scheme + "://" + call.Host + path, nil
}

func ptr[T any](v T) *T { return &v }

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
