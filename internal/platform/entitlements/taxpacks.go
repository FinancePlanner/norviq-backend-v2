package entitlements

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"norviq-api/internal/store/billingdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// TaxPackProductPrefix is TaxPackEntitlementService.productPrefix.
const TaxPackProductPrefix = "norviq_tax_pack_"

// TaxYearFromProductID is TaxPackEntitlementService.taxYear(fromProductId:):
// "norviq_tax_pack_2025" (any case, surrounding whitespace, any suffix after
// the four digits) is 2025. Anything else is not a pack.
func TaxYearFromProductID(productID *string) (int, bool) {
	if productID == nil {
		return 0, false
	}
	p := strings.ToLower(strings.TrimSpace(*productID))
	if !strings.HasPrefix(p, TaxPackProductPrefix) {
		return 0, false
	}
	rest := p[len(TaxPackProductPrefix):]
	end := strings.IndexFunc(rest, func(r rune) bool { return !unicode.IsNumber(r) })
	if end < 0 {
		end = len(rest)
	}
	digits := rest[:end]
	if utf8.RuneCountInString(digits) != 4 {
		return 0, false
	}
	year, err := strconv.Atoi(digits)
	if err != nil || year < 2000 || year > 2100 {
		return 0, false
	}
	return year, true
}

// TaxPackProductID is TaxPackEntitlementService.productId(taxYear:).
func TaxPackProductID(taxYear int) string {
	return TaxPackProductPrefix + strconv.Itoa(taxYear)
}

// GrantTaxPack is TaxPackEntitlementService.grant: idempotent per user and
// year. jurisdiction nil keeps the stored value.
func (s *Service) GrantTaxPack(ctx context.Context, userID uuid.UUID, taxYear int, source, productID string, providerEventID, jurisdiction *string) error {
	existing, err := s.q.TaxPackByUserYear(ctx, billingdb.TaxPackByUserYearParams{UserID: userID, TaxYear: int64(taxYear)})
	switch {
	case err == nil:
		j := existing.Jurisdiction
		if jurisdiction != nil {
			j = jurisdiction
		}
		return s.q.UpdateTaxPack(ctx, billingdb.UpdateTaxPackParams{
			ID: existing.ID, Source: source, ProductID: productID, ProviderEventID: providerEventID, Jurisdiction: j,
		})
	case errors.Is(err, pgx.ErrNoRows):
		now := s.now()
		return s.q.InsertTaxPack(ctx, billingdb.InsertTaxPackParams{
			ID: uuid.New(), UserID: userID, TaxYear: int64(taxYear), Jurisdiction: jurisdiction,
			Source: source, ProductID: productID, ProviderEventID: providerEventID, GrantedAt: &now,
		})
	default:
		return err
	}
}

// RevokeTaxPack is TaxPackEntitlementService.revoke.
func (s *Service) RevokeTaxPack(ctx context.Context, userID uuid.UUID, taxYear int) error {
	return s.q.DeleteTaxPack(ctx, billingdb.DeleteTaxPackParams{UserID: userID, TaxYear: int64(taxYear)})
}

// HasTaxPack is TaxPackEntitlementService.hasEntitlement.
func (s *Service) HasTaxPack(ctx context.Context, userID uuid.UUID, taxYear int) (bool, error) {
	n, err := s.q.CountTaxPacks(ctx, billingdb.CountTaxPacksParams{UserID: userID, TaxYear: int64(taxYear)})
	return n > 0, err
}
