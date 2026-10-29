package tax

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/entitlements"
	dec "norviq-api/internal/platform/nsdecimal"
	"norviq-api/internal/store/taxdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const taxDisclaimer = "Educational estimate only. Review all calculations and transactions with a qualified tax professional before acting or filing."

// pilotBroker is PilotAccountResolver.broker: pilot follow accounts hold
// simulated cash and are never taxable.
const pilotBroker = "pilot"

// service is DefaultTaxService plus the collaborators Vapor built per call.
type service struct {
	db      *pgxpool.Pool
	q       *taxdb.Queries
	catalog *catalog
	rules   ruleRegistry
	ent     *entitlements.Service
	// v2 is TAX_OPTIMIZATION_V2.
	v2  bool
	now func() time.Time
}

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// inTx is database.transaction.
func (s *service) inTx(ctx context.Context, fn func(q *taxdb.Queries) error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(s.q.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func nowPtr(t time.Time) *time.Time { return &t }

func defaultCurrency(j Jurisdiction) string {
	if j == US {
		return "USD"
	}
	return "EUR"
}

func positionKey(account, instrument uuid.UUID) string {
	return uuidString(account) + ":" + uuidString(instrument)
}

func f64(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func lower(p *string) string {
	if p == nil {
		return ""
	}
	return strings.ToLower(*p)
}

func isFundType(t *string) bool { return oneOf(lower(t), []string{"etf", "fund", "mutual_fund"}) }

func (s *service) capabilities(taxYear int) CapabilitiesResponse {
	return CapabilitiesResponse{GeneratedAt: isoDate(s.now()), Capabilities: s.rules.capabilities(taxYear)}
}

func instrumentMarketOption(in taxdb.Instrument) InstrumentMarketOption {
	status := "unknown"
	if in.RegulatedMarketSource != nil && in.RegulatedMarketStatus != nil && oneOf(*in.RegulatedMarketStatus, marketAdmissions) {
		status = *in.RegulatedMarketStatus
	}
	var fund *string
	if in.FundClassification != nil && oneOf(*in.FundClassification, fundClasses) {
		fund = in.FundClassification
	}
	return InstrumentMarketOption{
		ID: uuidString(in.ID), Symbol: in.Symbol, ListingExchange: in.ListingExchange,
		MarketAdmissionStatus: status, FundClassification: fund,
	}
}

func (s *service) profileContext(ctx context.Context, userID uuid.UUID, j Jurisdiction, taxYear int) (ProfileContextResponse, error) {
	existing, err := s.profile(ctx, userID, j, taxYear)
	if err != nil {
		return ProfileContextResponse{}, err
	}
	owned, err := s.q.AccountsForUserExceptBroker(ctx, taxdb.AccountsForUserExceptBrokerParams{UserID: userID, Broker: pilotBroker})
	if err != nil {
		return ProfileContextResponse{}, err
	}
	accounts := make([]ProfileAccountOption, 0, len(owned))
	accountIDs := make([]uuid.UUID, 0, len(owned))
	for idx := range owned {
		a := &owned[idx]
		accountIDs = append(accountIDs, a.ID)
		broker := strings.TrimSpace(a.Broker)
		name := ""
		if a.DisplayName != nil {
			name = strings.TrimSpace(*a.DisplayName)
		}
		if name == "" {
			if broker == "" {
				name = "Investment account"
			} else {
				name = strings.ToUpper(broker)
			}
		}
		wrapper := WrapperUnknown
		if a.TaxWrapper != nil {
			if w, ok := parseWrapper(*a.TaxWrapper); ok {
				wrapper = w
			}
		}
		method := "jurisdiction_default"
		if a.LotSelectionMethod != nil && oneOf(*a.LotSelectionMethod, lotMethods) {
			method = *a.LotSelectionMethod
		}
		accounts = append(accounts, ProfileAccountOption{
			ID: uuidString(a.ID), DisplayName: name, Broker: broker,
			BaseCurrency: strings.ToUpper(a.BaseCurrency), Wrapper: wrapper,
			OwnerMemberID: a.TaxOwnerMemberID, LotSelectionMethod: method,
		})
	}
	// localizedCaseInsensitiveCompare, then id.
	sort.SliceStable(accounts, func(x, y int) bool {
		a, b := strings.ToLower(accounts[x].DisplayName), strings.ToLower(accounts[y].DisplayName)
		if a == b {
			return accounts[x].ID < accounts[y].ID
		}
		return a < b
	})
	var lots []taxdb.Lot
	instrumentSet := map[uuid.UUID]bool{}
	if len(accountIDs) > 0 {
		ids, err := s.q.TransactionInstrumentIDsForAccounts(ctx, accountIDs)
		if err != nil {
			return ProfileContextResponse{}, err
		}
		for _, id := range ids {
			instrumentSet[id] = true
		}
		if lots, err = s.q.LotsForAccounts(ctx, accountIDs); err != nil {
			return ProfileContextResponse{}, err
		}
		for idx := range lots {
			l := &lots[idx]
			instrumentSet[l.InstrumentID] = true
		}
	}
	instruments := []InstrumentMarketOption{}
	fundIDs := map[uuid.UUID]bool{}
	if len(instrumentSet) > 0 {
		ids := make([]uuid.UUID, 0, len(instrumentSet))
		for id := range instrumentSet {
			ids = append(ids, id)
		}
		rows, err := s.q.InstrumentsByIDs(ctx, ids)
		if err != nil {
			return ProfileContextResponse{}, err
		}
		for idx := range rows {
			in := &rows[idx]
			instruments = append(instruments, instrumentMarketOption(*in))
			if isFundType(in.InstrumentType) {
				fundIDs[in.ID] = true
			}
		}
		sort.SliceStable(instruments, func(x, y int) bool {
			if instruments[x].Symbol != instruments[y].Symbol {
				return instruments[x].Symbol < instruments[y].Symbol
			}
			return instruments[x].ID < instruments[y].ID
		})
	}
	fundLots := []FundLotOption{}
	for idx := range lots {
		l := &lots[idx]
		if !fundIDs[l.InstrumentID] {
			continue
		}
		fundLots = append(fundLots, FundLotOption{
			ID: uuidString(l.ID), AccountID: uuidString(l.AccountID), InstrumentID: uuidString(l.InstrumentID),
			OpenedAt: isoDate(l.OpenDate), OriginalQuantity: fd(l.OpenQuantity), RemainingQuantity: fd(l.RemainingQuantity),
		})
	}
	sort.SliceStable(fundLots, func(x, y int) bool {
		if fundLots[x].OpenedAt != fundLots[y].OpenedAt {
			return fundLots[x].OpenedAt < fundLots[y].OpenedAt
		}
		return fundLots[x].ID < fundLots[y].ID
	})
	return ProfileContextResponse{
		Jurisdiction: j, TaxYear: taxYear, DefaultReportingCurrency: defaultCurrency(j),
		Profile: existing, Accounts: accounts, Instruments: instruments, FundLots: fundLots,
	}, nil
}

// ownedInstrument is the ownership check shared by market-admission and
// fund-classification.
func (s *service) ownedInstrument(ctx context.Context, userID, instrumentID uuid.UUID) (taxdb.Instrument, error) {
	notFound := httpx.NotFound("Instrument not found.")
	accounts, err := s.q.AccountsForUser(ctx, userID)
	if err != nil {
		return taxdb.Instrument{}, err
	}
	if len(accounts) == 0 {
		return taxdb.Instrument{}, notFound
	}
	ids := make([]uuid.UUID, len(accounts))
	for i := range accounts {
		a := &accounts[i]
		ids[i] = a.ID
	}
	ownsTx, err := s.q.TransactionExistsForAccountsInstrument(ctx, taxdb.TransactionExistsForAccountsInstrumentParams{AccountIds: ids, InstrumentID: instrumentID})
	if err != nil {
		return taxdb.Instrument{}, err
	}
	ownsLot, err := s.q.LotExistsForAccountsInstrument(ctx, taxdb.LotExistsForAccountsInstrumentParams{AccountIds: ids, InstrumentID: instrumentID})
	if err != nil {
		return taxdb.Instrument{}, err
	}
	if !ownsTx && !ownsLot {
		return taxdb.Instrument{}, notFound
	}
	in, err := s.q.InstrumentByID(ctx, instrumentID)
	if isNoRows(err) {
		return taxdb.Instrument{}, notFound
	}
	return in, err
}

func (s *service) saveMarketAdmission(ctx context.Context, userID, instrumentID uuid.UUID, status string) (InstrumentMarketOption, error) {
	in, err := s.ownedInstrument(ctx, userID, instrumentID)
	if err != nil {
		return InstrumentMarketOption{}, err
	}
	now := s.now()
	in.RegulatedMarketStatus = &status
	in.RegulatedMarketSource, in.RegulatedMarketReviewedAt = nil, nil
	if status != "unknown" {
		in.RegulatedMarketSource = strPtr("user_verified_document")
		in.RegulatedMarketReviewedAt = &now
	}
	if err := s.q.UpdateInstrumentMarketAdmission(ctx, taxdb.UpdateInstrumentMarketAdmissionParams{
		Status: in.RegulatedMarketStatus, Source: in.RegulatedMarketSource, ReviewedAt: in.RegulatedMarketReviewedAt,
		Now: &now, ID: in.ID,
	}); err != nil {
		return InstrumentMarketOption{}, err
	}
	return instrumentMarketOption(in), nil
}

func (s *service) saveFundClassification(ctx context.Context, userID, instrumentID uuid.UUID, classification string) (InstrumentMarketOption, error) {
	in, err := s.ownedInstrument(ctx, userID, instrumentID)
	if err != nil {
		return InstrumentMarketOption{}, err
	}
	in.FundClassification = &classification
	if err := s.q.UpdateInstrumentFundClassification(ctx, taxdb.UpdateInstrumentFundClassificationParams{
		FundClassification: &classification, Now: nowPtr(s.now()), ID: in.ID,
	}); err != nil {
		return InstrumentMarketOption{}, err
	}
	return instrumentMarketOption(in), nil
}

func (s *service) profileRow(ctx context.Context, userID uuid.UUID, j Jurisdiction, taxYear int) (*taxdb.TaxProfile, error) {
	row, err := s.q.ProfileFor(ctx, taxdb.ProfileForParams{UserID: userID, Jurisdiction: string(j), TaxYear: int64(taxYear)})
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *service) profile(ctx context.Context, userID uuid.UUID, j Jurisdiction, taxYear int) (*ProfileResponse, error) {
	row, err := s.profileRow(ctx, userID, j, taxYear)
	if err != nil || row == nil {
		return nil, err
	}
	resp, err := s.profileResponse(*row)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

func (s *service) profileResponse(row taxdb.TaxProfile) (ProfileResponse, error) {
	var req ProfileRequest
	if err := decodeStored(row.ProfileJson, &req); err != nil {
		return ProfileResponse{}, err
	}
	updated := row.CreatedAt
	if row.UpdatedAt != nil {
		updated = *row.UpdatedAt
	}
	return ProfileResponse{
		ID: uuidString(row.ID), Profile: req, IsComplete: row.IsComplete,
		MissingFields: missingFields(req), UpdatedAt: isoDate(updated),
	}, nil
}

func missingFields(r ProfileRequest) []string {
	missing := []string{}
	if len(r.Members) == 0 {
		missing = append(missing, "members")
	}
	if len(r.Accounts) == 0 {
		missing = append(missing, "accounts")
	}
	for _, a := range r.Accounts {
		if a.Wrapper == WrapperUnknown {
			missing = append(missing, "account_wrappers")
			break
		}
	}
	// String.count counts grapheme clusters.
	if len([]rune(r.ReportingCurrency)) != 3 {
		missing = append(missing, "reporting_currency")
	}
	if r.Jurisdiction == US {
		if r.ShortTermCapitalGainsRate == nil {
			missing = append(missing, "short_term_capital_gains_rate")
		}
		if r.LongTermCapitalGainsRate == nil {
			missing = append(missing, "long_term_capital_gains_rate")
		}
	} else if r.MarginalIncomeTaxRate == nil {
		missing = append(missing, "marginal_income_tax_rate")
	}
	return missing
}

func (s *service) saveProfile(ctx context.Context, userID uuid.UUID, req ProfileRequest) (ProfileResponse, error) {
	members := map[string]bool{}
	for _, m := range req.Members {
		members[m.ID] = true
	}
	accountSet := map[string]bool{}
	valid := len(members) == len(req.Members)
	for _, a := range req.Accounts {
		if !members[a.OwnerMemberID] {
			valid = false
		}
		accountSet[a.AccountID] = true
	}
	if !valid || len(accountSet) != len(req.Accounts) {
		return ProfileResponse{}, httpx.Unprocessable("Tax household and account classifications are invalid.")
	}
	requested := make([]uuid.UUID, 0, len(req.Accounts))
	for _, a := range req.Accounts {
		id, ok := parseUUID(a.AccountID)
		if !ok {
			return ProfileResponse{}, httpx.Unprocessable("A tax account identifier is invalid.")
		}
		requested = append(requested, id)
	}
	var owned []taxdb.Account
	if len(requested) > 0 {
		var err error
		owned, err = s.q.AccountsByIDsForUser(ctx, taxdb.AccountsByIDsForUserParams{UserID: userID, Ids: requested})
		if err != nil {
			return ProfileResponse{}, err
		}
	}
	if len(owned) != len(requested) {
		return ProfileResponse{}, httpx.Unprocessable("One or more tax accounts do not belong to this user.")
	}
	complete := len(missingFields(req)) == 0
	profileJSON, err := encodeSorted(req)
	if err != nil {
		return ProfileResponse{}, err
	}
	now := s.now()
	existing, err := s.profileRow(ctx, userID, req.Jurisdiction, req.TaxYear)
	if err != nil {
		return ProfileResponse{}, err
	}
	row := taxdb.TaxProfile{
		UserID: userID, Jurisdiction: string(req.Jurisdiction), TaxYear: int64(req.TaxYear),
		FilingStatus: req.FilingStatus, ReportingCurrency: strings.ToUpper(req.ReportingCurrency),
		ProfileJson: profileJSON, IsComplete: complete, UpdatedAt: &now,
	}
	if existing == nil {
		row.ID, row.CreatedAt = uuid.New(), now
		err = s.q.InsertProfile(ctx, taxdb.InsertProfileParams{
			ID: row.ID, UserID: userID, Jurisdiction: row.Jurisdiction, TaxYear: row.TaxYear,
			FilingStatus: row.FilingStatus, ReportingCurrency: row.ReportingCurrency,
			ProfileJson: row.ProfileJson, IsComplete: row.IsComplete, Now: now,
		})
	} else {
		row.ID, row.CreatedAt = existing.ID, existing.CreatedAt
		err = s.q.UpdateProfile(ctx, taxdb.UpdateProfileParams{
			FilingStatus: row.FilingStatus, ReportingCurrency: row.ReportingCurrency,
			ProfileJson: row.ProfileJson, IsComplete: row.IsComplete, Now: &now, ID: row.ID,
		})
	}
	if err != nil {
		return ProfileResponse{}, err
	}
	ownedByID := map[uuid.UUID]bool{}
	for idx := range owned {
		a := &owned[idx]
		ownedByID[a.ID] = true
	}
	jur := string(req.Jurisdiction)
	for _, a := range req.Accounts {
		id, _ := parseUUID(a.AccountID)
		if !ownedByID[id] {
			continue
		}
		wrapper, member, method := string(a.Wrapper), a.OwnerMemberID, a.LotSelectionMethod
		if err := s.q.UpdateAccountTaxClassification(ctx, taxdb.UpdateAccountTaxClassificationParams{
			TaxWrapper: &wrapper, TaxJurisdiction: &jur, TaxOwnerMemberID: &member,
			LotSelectionMethod: &method, Now: &now, ID: id,
		}); err != nil {
			return ProfileResponse{}, err
		}
	}
	return s.profileResponse(row)
}

func (s *service) notificationPreferences(ctx context.Context, userID uuid.UUID) (NotificationPreferences, error) {
	row, err := s.q.NotificationPreferenceForUser(ctx, userID)
	if isNoRows(err) {
		return NotificationPreferences{Enabled: false, CooldownDays: 7}, nil
	}
	if err != nil {
		return NotificationPreferences{}, err
	}
	return preferencesResponse(row.Enabled, row.MinimumBenefit, int(row.CooldownDays)), nil
}

func preferencesResponse(enabled bool, minimum *float64, cooldown int) NotificationPreferences {
	var m *dec.Decimal
	if minimum != nil {
		m = decPtr(fd(*minimum))
	}
	return NotificationPreferences{Enabled: enabled, MinimumBenefit: m, CooldownDays: cooldown}
}

func (s *service) saveNotificationPreferences(ctx context.Context, userID uuid.UUID, req NotificationPreferences) (NotificationPreferences, error) {
	var minimum *float64
	if req.MinimumBenefit != nil {
		v := dd(*req.MinimumBenefit)
		minimum = &v
	}
	cooldown := max(1, req.CooldownDays)
	now := s.now()
	row, err := s.q.NotificationPreferenceForUser(ctx, userID)
	switch {
	case isNoRows(err):
		err = s.q.InsertNotificationPreference(ctx, taxdb.InsertNotificationPreferenceParams{
			ID: uuid.New(), UserID: userID, Enabled: req.Enabled, MinimumBenefit: minimum,
			CooldownDays: int64(cooldown), Now: &now,
		})
	case err == nil:
		err = s.q.UpdateNotificationPreference(ctx, taxdb.UpdateNotificationPreferenceParams{
			Enabled: req.Enabled, MinimumBenefit: minimum, CooldownDays: int64(cooldown), Now: &now, ID: row.ID,
		})
	}
	if err != nil {
		return NotificationPreferences{}, err
	}
	return preferencesResponse(req.Enabled, minimum, cooldown), nil
}

func (s *service) saveOpportunityDecision(ctx context.Context, userID uuid.UUID, taxYear int, opportunityID string, benefit dec.Decimal, currency, status string) error {
	now := s.now()
	row, err := s.q.DecisionFor(ctx, taxdb.DecisionForParams{UserID: userID, TaxYear: int64(taxYear), OpportunityID: opportunityID})
	if isNoRows(err) {
		return s.q.InsertDecision(ctx, taxdb.InsertDecisionParams{
			ID: uuid.New(), UserID: userID, TaxYear: int64(taxYear), OpportunityID: opportunityID,
			Status: status, EstimatedBenefit: dd(benefit), Currency: currency, Now: &now,
		})
	}
	if err != nil {
		return err
	}
	return s.q.UpdateDecision(ctx, taxdb.UpdateDecisionParams{
		Status: status, EstimatedBenefit: dd(benefit), Currency: currency, Now: &now, ID: row.ID,
	})
}

func (s *service) dismissOpportunity(ctx context.Context, userID uuid.UUID, opportunityID string, j Jurisdiction, taxYear int) error {
	current, err := s.dashboard(ctx, userID, j, taxYear)
	if err != nil {
		return err
	}
	for idx := range current.Opportunities {
		o := &current.Opportunities[idx]
		if o.ID != opportunityID {
			continue
		}
		benefit := o.EstimatedTaxBenefit.Amount
		if o.EstimatedAfterCostBenefit != nil {
			benefit = o.EstimatedAfterCostBenefit.Amount
		}
		return s.saveOpportunityDecision(ctx, userID, taxYear, opportunityID, benefit, o.EstimatedTaxBenefit.Currency, "dismissed")
	}
	return httpx.NotFound("Tax opportunity not found.")
}

func (s *service) restoreOpportunity(ctx context.Context, userID uuid.UUID, opportunityID string, taxYear int) error {
	row, err := s.q.DecisionFor(ctx, taxdb.DecisionForParams{UserID: userID, TaxYear: int64(taxYear), OpportunityID: opportunityID})
	if isNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if row.Status != "dismissed" {
		return httpx.Abort(http.StatusConflict, "Only a dismissed opportunity can be restored.")
	}
	return s.q.DeleteDecision(ctx, row.ID)
}
