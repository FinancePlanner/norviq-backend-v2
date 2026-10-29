package tax

import (
	"context"
	"fmt"
	"strings"
	"time"

	"norviq-api/internal/httpx"
	dec "norviq-api/internal/platform/nsdecimal"
	"norviq-api/internal/store/taxdb"

	"github.com/google/uuid"
)

// advanceLumpSumError is GermanyAdvanceLumpSumError; String is Swift's
// "\(error)" of the case.
type advanceLumpSumError string

func (e advanceLumpSumError) Error() string { return string(e) }

type advanceLumpSum struct {
	basisRate, gross, taxable dec.Decimal
}

// basisRate is GermanyAdvanceLumpSumRateRegistry.
func basisRate(year int) (dec.Decimal, bool) {
	switch year {
	case 2025:
		return fd(0.0253), true
	case 2026:
		return fd(0.0320), true
	}
	return zero, false
}

// calculateAdvanceLumpSum is GermanyAdvanceLumpSumCalculator.calculate.
func calculateAdvanceLumpSum(year int, beginning, ending, distributions dec.Decimal, month *int, classification string) (advanceLumpSum, error) {
	rate, ok := basisRate(year)
	if !ok {
		return advanceLumpSum{}, advanceLumpSumError(fmt.Sprintf("unsupportedCalculationYear(%d)", year))
	}
	if beginning.Sign() < 0 || ending.Sign() < 0 {
		return advanceLumpSum{}, advanceLumpSumError("invalidMarketValue")
	}
	if distributions.Sign() < 0 {
		return advanceLumpSum{}, advanceLumpSumError("invalidDistributions")
	}
	if month != nil && (*month < 1 || *month > 12) {
		return advanceLumpSum{}, advanceLumpSumError("invalidAcquisitionMonth")
	}
	exemption, ok := fundExemptionRate(classification)
	if !ok {
		return advanceLumpSum{}, advanceLumpSumError("unknownFundClassification")
	}
	baseYield := dmax(zero, beginning.Mul(fd(0.70)).Mul(rate))
	cap := dmax(zero, ending.Sub(beginning).Add(distributions))
	adjusted := dmax(zero, dmin(baseYield, cap).Sub(distributions))
	factor := di(1)
	if month != nil {
		factor = di(13 - *month).Div(di(12))
	}
	gross := adjusted.Mul(factor)
	return advanceLumpSum{basisRate: rate, gross: gross, taxable: gross.Mul(di(1).Sub(exemption))}, nil
}

type calculatedHolding struct {
	input    FundAnnualHoldingInput
	result   advanceLumpSum
	lot      *taxdb.Lot
	quantity *dec.Decimal
}

// saveFundAnnualInput is GermanyFundAnnualInputService.save.
func (s *service) saveFundAnnualInput(ctx context.Context, userID uuid.UUID, req FundAnnualInputRequest) (FundAdvanceLumpSumResponse, error) {
	accountID, ok1 := parseUUID(req.AccountID)
	instrumentID, ok2 := parseUUID(req.InstrumentID)
	if !ok1 || !ok2 {
		return FundAdvanceLumpSumResponse{}, httpx.BadRequest("Invalid account or instrument id.")
	}
	if len([]rune(req.Currency)) != 3 {
		return FundAdvanceLumpSumResponse{}, httpx.Unprocessable("Currency must be a three-letter code.")
	}
	if len(req.Holdings) == 0 || len(req.Holdings) > 100 {
		return FundAdvanceLumpSumResponse{}, httpx.Unprocessable("Provide between 1 and 100 annual holding tranches.")
	}
	seen := map[string]bool{}
	for _, h := range req.Holdings {
		if seen[h.ID] || strings.TrimSpace(h.ID) == "" {
			return FundAdvanceLumpSumResponse{}, httpx.Unprocessable("Holding ids must be non-empty and unique.")
		}
		seen[h.ID] = true
	}
	notFound := httpx.NotFound("Classified fund holding not found.")
	account, err := s.q.AccountByID(ctx, accountID)
	if isNoRows(err) || (err == nil && account.UserID != userID) {
		return FundAdvanceLumpSumResponse{}, notFound
	}
	if err != nil {
		return FundAdvanceLumpSumResponse{}, err
	}
	instrument, err := s.q.InstrumentByID(ctx, instrumentID)
	if isNoRows(err) {
		return FundAdvanceLumpSumResponse{}, notFound
	}
	if err != nil {
		return FundAdvanceLumpSumResponse{}, err
	}
	if instrument.FundClassification == nil || !oneOf(*instrument.FundClassification, fundClasses) || *instrument.FundClassification == "unknown" {
		return FundAdvanceLumpSumResponse{}, notFound
	}
	classification := *instrument.FundClassification
	ownsTx, err := s.q.TransactionExistsForAccountInstrument(ctx, taxdb.TransactionExistsForAccountInstrumentParams{AccountID: accountID, InstrumentID: instrumentID})
	if err != nil {
		return FundAdvanceLumpSumResponse{}, err
	}
	lots, err := s.q.LotsForAccountInstrumentByOpenDate(ctx, taxdb.LotsForAccountInstrumentByOpenDateParams{AccountID: accountID, InstrumentID: instrumentID})
	if err != nil {
		return FundAdvanceLumpSumResponse{}, err
	}
	if !ownsTx && len(lots) == 0 {
		return FundAdvanceLumpSumResponse{}, notFound
	}
	lotsByID := map[uuid.UUID]*taxdb.Lot{}
	for i := range lots {
		lotsByID[lots[i].ID] = &lots[i]
	}
	calculated := make([]calculatedHolding, 0, len(req.Holdings))
	for _, h := range req.Holdings {
		c := calculatedHolding{input: h}
		switch {
		case h.LotID != nil && h.Quantity != nil:
			id, ok := parseUUID(*h.LotID)
			lot := lotsByID[id]
			if !ok || lot == nil || !(h.Quantity.Sign() > 0) || h.Quantity.Cmp(fd(lot.OpenQuantity)) > 0 {
				return FundAdvanceLumpSumResponse{}, httpx.Unprocessable("Invalid annual holding lot or quantity.")
			}
			c.lot, c.quantity = lot, h.Quantity
		case h.LotID != nil || h.Quantity != nil:
			return FundAdvanceLumpSumResponse{}, httpx.Unprocessable("Lot id and quantity must be provided together.")
		case len(req.Holdings) == 1 && len(lots) == 1:
			c.lot, c.quantity = &lots[0], decPtr(fd(lots[0].OpenQuantity))
		}
		r, err := calculateAdvanceLumpSum(req.CalculationYear, h.BeginningMarketValue, h.EndingMarketValue, h.Distributions, h.AcquisitionMonth, classification)
		if err != nil {
			return FundAdvanceLumpSumResponse{}, httpx.Unprocessable("Invalid annual fund input: " + err.Error() + ".")
		}
		c.result = r
		calculated = append(calculated, c)
	}
	ruleVersion := newRuleRegistry(map[Jurisdiction]bool{DE: true}).pack(DE).ruleVersion
	currency := strings.ToUpper(req.Currency)
	now := s.now()
	err = s.inTx(ctx, func(q *taxdb.Queries) error {
		if err := q.DeleteFundAnnualHoldings(ctx, taxdb.DeleteFundAnnualHoldingsParams{
			UserID: userID, AccountID: accountID, InstrumentID: instrumentID, CalculationYear: int64(req.CalculationYear),
		}); err != nil {
			return err
		}
		for idx := range calculated {
			c := &calculated[idx]
			var lotID *uuid.UUID
			var qty *float64
			if c.lot != nil && c.quantity != nil {
				lotID = &c.lot.ID
				v := dd(*c.quantity)
				qty = &v
			}
			var month *int64
			if c.input.AcquisitionMonth != nil {
				m := int64(*c.input.AcquisitionMonth)
				month = &m
			}
			gross := dd(c.result.gross)
			if err := q.InsertFundAnnualHolding(ctx, taxdb.InsertFundAnnualHoldingParams{
				ID: uuid.New(), UserID: userID, AccountID: accountID, InstrumentID: instrumentID,
				CalculationYear: int64(req.CalculationYear), ClientHoldingID: c.input.ID, LotID: lotID,
				Quantity: qty, RemainingQuantity: qty, Currency: currency,
				BeginningMarketValue: dd(c.input.BeginningMarketValue), EndingMarketValue: dd(c.input.EndingMarketValue),
				Distributions: dd(c.input.Distributions), AcquisitionMonth: month, FundClassification: classification,
				BasisRate: dd(c.result.basisRate), GrossAdvanceLumpSum: gross, RemainingGrossAdvance: gross,
				TaxableAdvanceLumpSum: dd(c.result.taxable), RuleVersion: ruleVersion, Now: &now,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return FundAdvanceLumpSumResponse{}, err
	}
	holdings := make([]FundAnnualHoldingInput, len(calculated))
	gross, taxable := zero, zero
	for i := range calculated {
		c := &calculated[i]
		gross = gross.Add(c.result.gross)
		taxable = taxable.Add(c.result.taxable)
		var lotID *string
		if c.lot != nil {
			lotID = strPtr(uuidString(c.lot.ID))
		}
		holdings[i] = FundAnnualHoldingInput{
			ID: c.input.ID, LotID: lotID, Quantity: c.quantity,
			BeginningMarketValue: c.input.BeginningMarketValue, EndingMarketValue: c.input.EndingMarketValue,
			Distributions: c.input.Distributions, AcquisitionMonth: c.input.AcquisitionMonth,
		}
	}
	rate := zero
	if len(calculated) > 0 {
		rate = calculated[0].result.basisRate
	}
	return FundAdvanceLumpSumResponse{
		AccountID: uuidString(accountID), InstrumentID: uuidString(instrumentID),
		CalculationYear: req.CalculationYear, DeemedReceiptTaxYear: req.CalculationYear + 1, Currency: currency,
		FundClassification: classification, BasisRate: rate, GrossAdvanceLumpSum: gross,
		TaxableAdvanceLumpSum: taxable, Holdings: holdings, UpdatedAt: isoDate(now),
	}, nil
}

// fundAnnualInput is GermanyFundAnnualInputService.get.
func (s *service) fundAnnualInput(ctx context.Context, userID, accountID, instrumentID uuid.UUID, year int) (*FundAdvanceLumpSumResponse, error) {
	rows, err := s.q.FundAnnualHoldings(ctx, taxdb.FundAnnualHoldingsParams{
		UserID: userID, AccountID: accountID, InstrumentID: instrumentID, CalculationYear: int64(year),
	})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 || !oneOf(rows[0].FundClassification, fundClasses) {
		return nil, nil
	}
	first := rows[0]
	holdings := make([]FundAnnualHoldingInput, len(rows))
	gross, taxable := zero, zero
	var latest *time.Time
	for i := range rows {
		r := &rows[i]
		var qty *dec.Decimal
		if r.Quantity != nil {
			qty = decPtr(fd(*r.Quantity))
		}
		var month *int
		if r.AcquisitionMonth != nil {
			month = intPtr(int(*r.AcquisitionMonth))
		}
		holdings[i] = FundAnnualHoldingInput{
			ID: r.ClientHoldingID, LotID: uuidPtrString(r.LotID), Quantity: qty,
			BeginningMarketValue: fd(r.BeginningMarketValue), EndingMarketValue: fd(r.EndingMarketValue),
			Distributions: fd(r.Distributions), AcquisitionMonth: month,
		}
		gross = gross.Add(fd(r.GrossAdvanceLumpSum))
		taxable = taxable.Add(fd(r.TaxableAdvanceLumpSum))
		if r.UpdatedAt != nil && (latest == nil || r.UpdatedAt.After(*latest)) {
			latest = r.UpdatedAt
		}
	}
	return &FundAdvanceLumpSumResponse{
		AccountID: uuidString(accountID), InstrumentID: uuidString(instrumentID), CalculationYear: year,
		DeemedReceiptTaxYear: year + 1, Currency: first.Currency, FundClassification: first.FundClassification,
		BasisRate: fd(first.BasisRate), GrossAdvanceLumpSum: gross, TaxableAdvanceLumpSum: taxable,
		Holdings: holdings, UpdatedAt: isoDate(orNow(latest, s.now())),
	}, nil
}
