package portfolios

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"norviq-api/internal/httpx"
	"norviq-api/internal/store/portfoliocoredb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Now is the clock Fluent timestamps use; tests may override it.
var Now = func() time.Time { return time.Now().UTC() }

// IsFollowed is PilotFollowGuard.isFollowed: any follow row, paused or not,
// manages the list.
func IsFollowed(ctx context.Context, db portfoliocoredb.DBTX, portfolioListID uuid.UUID) (bool, error) {
	n, err := portfoliocoredb.New(db).CountPilotFollowsForPortfolioList(ctx, &portfolioListID)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ErrPilotFollowed is PilotFollowGuard.ensureNotFollowed's 409.
func ErrPilotFollowed() *httpx.Error {
	return httpx.Conflict("This portfolio is managed by a pilot follow. Stop following to edit it.")
}

// EnsureNotFollowed is PilotFollowGuard.ensureNotFollowed.
func EnsureNotFollowed(ctx context.Context, db portfoliocoredb.DBTX, portfolioListID uuid.UUID) error {
	followed, err := IsFollowed(ctx, db, portfolioListID)
	if err != nil {
		return err
	}
	if followed {
		return ErrPilotFollowed()
	}
	return nil
}

// EnsureDefaultPortfolioListID is ensureDefaultPortfolioListId: the user's
// default portfolio, promoting or creating one when there is none. A list a
// pilot follow manages is never the default (it is demoted if flagged).
func EnsureDefaultPortfolioListID(ctx context.Context, db portfoliocoredb.DBTX, userID uuid.UUID) (uuid.UUID, error) {
	q := portfoliocoredb.New(db)
	existing, err := q.DefaultPortfolioList(ctx, userID)
	if err != nil {
		return uuid.Nil, err
	}
	if len(existing) > 0 {
		id := existing[0].ID
		followed, err := IsFollowed(ctx, db, id)
		if err != nil {
			return uuid.Nil, err
		}
		if !followed {
			return id, nil
		}
		now := Now()
		if err := q.SetPortfolioListDefault(ctx, portfoliocoredb.SetPortfolioListDefaultParams{ID: id, IsDefault: false, UpdatedAt: &now}); err != nil {
			return uuid.Nil, err
		}
	}

	followedRaw, err := q.FollowedPortfolioListIDs(ctx, userID)
	if err != nil {
		return uuid.Nil, err
	}
	followed := make([]uuid.UUID, 0, len(followedRaw))
	for _, f := range followedRaw {
		if f != nil {
			followed = append(followed, *f)
		}
	}
	fallback, err := q.OldestPortfolioListExcluding(ctx, portfoliocoredb.OldestPortfolioListExcludingParams{UserID: userID, Excluded: followed})
	if err != nil {
		return uuid.Nil, err
	}
	if len(fallback) > 0 {
		// Fluent's save is a no-op (no updated_at bump) when nothing changed.
		if !fallback[0].IsDefault {
			now := Now()
			if err := q.SetPortfolioListDefault(ctx, portfoliocoredb.SetPortfolioListDefaultParams{ID: fallback[0].ID, IsDefault: true, UpdatedAt: &now}); err != nil {
				return uuid.Nil, err
			}
		}
		return fallback[0].ID, nil
	}

	name, err := AvailablePortfolioListName(ctx, db, "Main Portfolio", userID)
	if err != nil {
		return uuid.Nil, err
	}
	created, err := q.InsertPortfolioList(ctx, NewPortfolioListParams(userID, name, true))
	if err != nil {
		return uuid.Nil, err
	}
	return created.ID, nil
}

// NewPortfolioListParams is PortfolioList(userId:name:isDefault:) with the
// model's defaults (personal, individual, actual, USD) and Fluent's create
// timestamps (created_at = updated_at = now).
func NewPortfolioListParams(userID uuid.UUID, name string, isDefault bool) portfoliocoredb.InsertPortfolioListParams {
	return portfoliocoredb.InsertPortfolioListParams{
		ID:           uuid.New(),
		UserID:       userID,
		Name:         name,
		IsDefault:    isDefault,
		Purpose:      PurposePersonal,
		Ownership:    OwnershipIndividual,
		Mode:         ModeActual,
		BaseCurrency: "USD",
		CreatedAt:    Now(),
	}
}

// AvailablePortfolioListName is availablePortfolioListName: base, else
// "base 2", "base 3", …
func AvailablePortfolioListName(ctx context.Context, db portfoliocoredb.DBTX, base string, userID uuid.UUID) (string, error) {
	names, err := portfoliocoredb.New(db).PortfolioListNames(ctx, userID)
	if err != nil {
		return "", err
	}
	taken := make(map[string]bool, len(names))
	for _, n := range names {
		taken[n] = true
	}
	if !taken[base] {
		return base, nil
	}
	n := 2
	for taken[fmt.Sprintf("%s %d", base, n)] {
		n++
	}
	return fmt.Sprintf("%s %d", base, n), nil
}

// ResolvePortfolioListID is resolvePortfolioListId: a blank id resolves to
// the default list (or nil when defaultWhenMissing is false); otherwise the
// id must parse ("Invalid portfolioListId.") and be owned by the user
// ("Portfolio list not found.").
func ResolvePortfolioListID(ctx context.Context, db portfoliocoredb.DBTX, requestedID *string, userID uuid.UUID, defaultWhenMissing bool) (*uuid.UUID, error) {
	raw := ""
	if requestedID != nil {
		raw = TrimSwift(*requestedID)
	}
	if raw == "" {
		if !defaultWhenMissing {
			return nil, nil
		}
		id, err := EnsureDefaultPortfolioListID(ctx, db, userID)
		if err != nil {
			return nil, err
		}
		return &id, nil
	}
	listID, ok := ParseSwiftUUID(raw)
	if !ok {
		return nil, httpx.BadRequest("Invalid portfolioListId.")
	}
	_, err := portfoliocoredb.New(db).GetOwnedPortfolioList(ctx, portfoliocoredb.GetOwnedPortfolioListParams{ID: listID, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("Portfolio list not found.")
	}
	if err != nil {
		return nil, err
	}
	return &listID, nil
}

// NormalizeListName is normalizeListName(_:field:): trimmed, non-empty, at
// most 80 characters (Swift counts grapheme clusters; runes are the closest
// cheap approximation and agree for everything but combining sequences).
func NormalizeListName(raw, field string) (string, error) {
	n := TrimSwift(raw)
	if n == "" {
		return "", httpx.BadRequest(field + " is required.")
	}
	if utf8.RuneCountInString(n) > 80 {
		return "", httpx.BadRequest(field + " must be at most 80 characters.")
	}
	return n, nil
}

// TrimSwift is trimmingCharacters(in: .whitespacesAndNewlines).
func TrimSwift(s string) string {
	return strings.TrimFunc(s, isSwiftWhitespace)
}

func isSwiftWhitespace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\v', '\f', '\r', 0x85, 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

// ParseSwiftUUID is UUID(uuidString:): the canonical 36-character hyphenated
// form only, either case (Go's uuid.Parse also accepts braces, urn: and
// unhyphenated forms, which Swift rejects).
func ParseSwiftUUID(s string) (uuid.UUID, bool) {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

// FormatISODateTime is ISO8601DateFormatter with .withFractionalSeconds
// (formatISODateTime in ListSupport.swift): UTC, milliseconds, "Z". The
// formatter rounds to the nearest millisecond (a stored .669583 renders as
// .670 in Vapor), it does not truncate.
func FormatISODateTime(t time.Time) string {
	return t.UTC().Round(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
}

// FormatISODateTimePtr is formatISODateTime for an optional date.
func FormatISODateTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := FormatISODateTime(*t)
	return &s
}

// FormatDateOnly is the "yyyy-MM-dd" UTC DateFormatter.
func FormatDateOnly(t time.Time) string { return t.UTC().Format("2006-01-02") }
