package portfoliomgmt

import (
	"slices"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/store/portfoliocoredb"
	"norviq-api/internal/store/portfoliomgmtdb"
)

// Raw values of the StockPlanShared enums these routes read and write
// (Portfolio/PortfolioManagementDTOs.swift).
var (
	purposes        = []string{portfolios.PurposePersonal, portfolios.PurposeRetirement}
	ownerships      = []string{portfolios.OwnershipIndividual, portfolios.OwnershipJoint}
	modes           = []string{portfolios.ModeActual, portfolios.ModeHypothetical}
	roles           = []string{portfolios.RoleOwner, portfolios.RoleEditor}
	membershipState = []string{"invited", "active", "revoked", "left"}
	invitationState = []string{"pending", "accepted", "revoked", "expired"}
)

// orDefault is `E(rawValue: raw) ?? fallback`.
func orDefault(raw string, allowed []string, fallback string) string {
	if slices.Contains(allowed, raw) {
		return raw
	}
	return fallback
}

// Portfolio is StockPlanShared.Portfolio.
type Portfolio struct {
	ID                string                  `json:"id"`
	OwnerUserID       string                  `json:"ownerUserId"`
	Name              string                  `json:"name"`
	Purpose           string                  `json:"purpose"`
	Ownership         string                  `json:"ownership"`
	Mode              string                  `json:"mode"`
	BaseCurrency      string                  `json:"baseCurrency"`
	IsDefault         bool                    `json:"isDefault"`
	SourcePortfolioID *string                 `json:"sourcePortfolioId,omitempty"`
	ClonedAt          *string                 `json:"clonedAt,omitempty"`
	ArchivedAt        *string                 `json:"archivedAt,omitempty"`
	CurrentUserRole   string                  `json:"currentUserRole"`
	Capabilities      portfolios.Capabilities `json:"capabilities"`
	CreatedAt         string                  `json:"createdAt"`
	UpdatedAt         *string                 `json:"updatedAt,omitempty"`
}

// PortfolioPage is PortfolioPageResponse (nextCursor is always nil).
type PortfolioPage struct {
	Items []Portfolio `json:"items"`
}

// makeResponse is PortfolioManagementController.makeResponse.
func makeResponse(ctx portfolios.AccessContext) Portfolio {
	p := ctx.Portfolio
	var source *string
	if p.SourcePortfolioID != nil {
		s := apijson.FormatUUID(*p.SourcePortfolioID)
		source = &s
	}
	return Portfolio{
		ID:                apijson.FormatUUID(p.ID),
		OwnerUserID:       apijson.FormatUUID(p.UserID),
		Name:              p.Name,
		Purpose:           orDefault(p.Purpose, purposes, portfolios.PurposePersonal),
		Ownership:         orDefault(p.Ownership, ownerships, portfolios.OwnershipIndividual),
		Mode:              orDefault(p.Mode, modes, portfolios.ModeActual),
		BaseCurrency:      p.BaseCurrency,
		IsDefault:         p.IsDefault,
		SourcePortfolioID: source,
		ClonedAt:          portfolios.FormatISODateTimePtr(p.ClonedAt),
		ArchivedAt:        portfolios.FormatISODateTimePtr(p.ArchivedAt),
		CurrentUserRole:   ctx.Role,
		Capabilities:      ctx.Capabilities(),
		CreatedAt:         portfolios.FormatISODateTime(p.CreatedAt),
		UpdatedAt:         portfolios.FormatISODateTimePtr(p.UpdatedAt),
	}
}

// coreList converts this package's row type to the shared one (same
// columns, same Go types).
func coreList(p portfoliomgmtdb.PortfolioList) portfoliocoredb.PortfolioList {
	return portfoliocoredb.PortfolioList(p)
}

// Membership is StockPlanShared.PortfolioMembership.
type Membership struct {
	ID          string  `json:"id"`
	PortfolioID string  `json:"portfolioId"`
	UserID      string  `json:"userId"`
	DisplayName string  `json:"displayName"`
	Email       string  `json:"email"`
	Role        string  `json:"role"`
	Status      string  `json:"status"`
	JoinedAt    *string `json:"joinedAt,omitempty"`
	CreatedAt   string  `json:"createdAt"`
}

// formatOptional is `formatISODateTime(date) ?? ""`.
func formatOptional(t *time.Time) string {
	if t == nil {
		return ""
	}
	return portfolios.FormatISODateTime(*t)
}

// Invitation is StockPlanShared.PortfolioInvitation.
type Invitation struct {
	ID          string  `json:"id"`
	PortfolioID string  `json:"portfolioId"`
	Email       string  `json:"email"`
	Role        string  `json:"role"`
	Status      string  `json:"status"`
	ExpiresAt   string  `json:"expiresAt"`
	AcceptedAt  *string `json:"acceptedAt,omitempty"`
	CreatedAt   string  `json:"createdAt"`
}

// makeInvitation is PortfolioManagementController.makeInvitation.
func makeInvitation(r *portfoliomgmtdb.PortfolioInvitation) Invitation {
	return Invitation{
		ID:          apijson.FormatUUID(r.ID),
		PortfolioID: apijson.FormatUUID(r.PortfolioID),
		Email:       r.Email,
		Role:        orDefault(r.Role, roles, portfolios.RoleEditor),
		Status:      orDefault(r.Status, invitationState, "pending"),
		ExpiresAt:   portfolios.FormatISODateTime(r.ExpiresAt),
		AcceptedAt:  portfolios.FormatISODateTimePtr(r.AcceptedAt),
		CreatedAt:   formatOptional(r.CreatedAt),
	}
}

// CashPosition is StockPlanShared.PortfolioCashPosition.
type CashPosition struct {
	ID          string  `json:"id"`
	PortfolioID string  `json:"portfolioId"`
	Label       string  `json:"label"`
	Currency    string  `json:"currency"`
	Balance     float64 `json:"balance"`
	AsOf        string  `json:"asOf"`
	CreatedAt   string  `json:"createdAt"`
	UpdatedAt   *string `json:"updatedAt,omitempty"`
}

// makeCashPosition is PortfolioManagementController.makeCashPosition.
func makeCashPosition(r *portfoliomgmtdb.PortfolioCashPosition) CashPosition {
	return CashPosition{
		ID:          apijson.FormatUUID(r.ID),
		PortfolioID: apijson.FormatUUID(r.PortfolioID),
		Label:       r.Label,
		Currency:    r.Currency,
		Balance:     r.Balance,
		AsOf:        portfolios.FormatISODateTime(r.AsOf),
		CreatedAt:   formatOptional(r.CreatedAt),
		UpdatedAt:   portfolios.FormatISODateTimePtr(r.UpdatedAt),
	}
}

// Account is the Fluent `Account` model encoded as Content: every property
// under its Swift name, nil optionals as explicit nulls (Fluent's
// OptionalField/Timestamp encode through a single-value container).
type Account struct {
	ID                 apijson.UUID  `json:"id"`
	UserID             apijson.UUID  `json:"userId"`
	PortfolioID        *apijson.UUID `json:"portfolioId"`
	ExternalID         string        `json:"externalId"`
	Broker             string        `json:"broker"`
	DisplayName        *string       `json:"displayName"`
	BaseCurrency       string        `json:"baseCurrency"`
	TaxWrapper         *string       `json:"taxWrapper"`
	TaxJurisdiction    *string       `json:"taxJurisdiction"`
	TaxOwnerMemberID   *string       `json:"taxOwnerMemberId"`
	LotSelectionMethod *string       `json:"lotSelectionMethod"`
	CreatedAt          *apijson.Time `json:"createdAt"`
	UpdatedAt          *apijson.Time `json:"updatedAt"`
}

func makeAccount(a *portfoliomgmtdb.Account) Account {
	created := apijson.NewTime(a.CreatedAt)
	return Account{
		ID:                 apijson.NewUUID(a.ID),
		UserID:             apijson.NewUUID(a.UserID),
		PortfolioID:        apijson.UUIDPtr(a.PortfolioID),
		ExternalID:         a.ExternalID,
		Broker:             a.Broker,
		DisplayName:        a.DisplayName,
		BaseCurrency:       a.BaseCurrency,
		TaxWrapper:         a.TaxWrapper,
		TaxJurisdiction:    a.TaxJurisdiction,
		TaxOwnerMemberID:   a.TaxOwnerMemberID,
		LotSelectionMethod: a.LotSelectionMethod,
		CreatedAt:          &created,
		UpdatedAt:          apijson.TimePtr(a.UpdatedAt),
	}
}

// ComparisonHolding is StockPlanShared.PortfolioComparisonHolding (its `id`
// is a computed property, never encoded).
type ComparisonHolding struct {
	Symbol                  string  `json:"symbol"`
	LeftValue               float64 `json:"leftValue"`
	RightValue              float64 `json:"rightValue"`
	LeftWeightPercent       float64 `json:"leftWeightPercent"`
	RightWeightPercent      float64 `json:"rightWeightPercent"`
	ValueDifference         float64 `json:"valueDifference"`
	WeightDifferencePercent float64 `json:"weightDifferencePercent"`
}

// ComparisonColumn is StockPlanShared.PortfolioComparisonColumn.
type ComparisonColumn struct {
	PortfolioID  string  `json:"portfolioId"`
	Name         string  `json:"name"`
	BaseCurrency string  `json:"baseCurrency"`
	TotalValue   float64 `json:"totalValue"`
	CashBalance  float64 `json:"cashBalance"`
	HoldingCount int     `json:"holdingCount"`
}

// Comparison is StockPlanShared.PortfolioComparison.
type Comparison struct {
	Left        ComparisonColumn    `json:"left"`
	Right       ComparisonColumn    `json:"right"`
	Holdings    []ComparisonHolding `json:"holdings"`
	GeneratedAt string              `json:"generatedAt"`
}
