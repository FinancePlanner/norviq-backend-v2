package marketdata

import (
	"context"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/store/marketdatadb"

	"github.com/google/uuid"
)

const fmpProviderName = "fmp"

// NormalizeFMPResultLimit is normalizeFMPResultLimit: limits must be
// positive, and free/starter plans are clamped to five rows.
func (s *Service) NormalizeFMPResultLimit(raw, def *int) (*int, error) {
	resolved := raw
	if resolved == nil {
		resolved = def
	}
	if resolved == nil {
		return nil, nil
	}
	if *resolved <= 0 {
		return nil, badRequest("`limit` must be greater than 0.")
	}
	if s.Tier == TierPremium {
		return ptr(*resolved), nil
	}
	return ptr(min(*resolved, 5)), nil
}

// payloadRow is the shape every FMP payload cache table reads back as.
type payloadRow struct {
	Payload   string
	CreatedAt *time.Time
	UpdatedAt *time.Time
}

// payloadCached is the shared flow of ratiosTTM, financialGrowth,
// analystEstimates and ratios: Redis, then the cache row when fresh, then
// gate (tier checks, which Vapor ran only after a cache miss), then FMP and
// both caches; on failure a decodable stale row wins.
func payloadCached[T any](
	ctx context.Context, s *Service, key, operation string,
	load func() (*payloadRow, error),
	gate func() error,
	fetch func(*FMP) (T, error),
	store func(payload string) error,
) (T, error) {
	var zero T
	ttl := s.Config.FMPTTL
	var hot T
	if s.Cache.Get(ctx, key, &hot) {
		return hot, nil
	}
	existing, err := load()
	if err != nil {
		return zero, err
	}
	if existing != nil && isFresh(firstTime(existing.UpdatedAt, existing.CreatedAt), ttl, s.now()) {
		if cached, ok := decodePayload[T](existing.Payload); ok {
			s.Cache.Set(ctx, key, cached, ttl)
			return cached, nil
		}
	}
	if err := gate(); err != nil {
		return zero, err
	}
	fmp, err := s.RequireFMP()
	if err != nil {
		return zero, err
	}
	fresh, err := fetch(fmp)
	if err == nil {
		var payload []byte
		if payload, err = apijson.Marshal(fresh); err == nil {
			if err = store(string(payload)); err == nil {
				s.Cache.Set(ctx, key, fresh, ttl)
				return fresh, nil
			}
		}
	}
	if existing != nil {
		if cached, ok := decodePayload[T](existing.Payload); ok {
			s.logger().Warn("market."+operation+" stale fallback", "key", key)
			s.Cache.Set(ctx, key, cached, ttl)
			return cached, nil
		}
	}
	return zero, mapFMPProviderError(err, operation)
}

func loadRow[R any](row R, err error, conv func(R) payloadRow) (*payloadRow, error) {
	if err != nil {
		if noRows(err) {
			return nil, nil
		}
		return nil, err
	}
	out := conv(row)
	return &out, nil
}

func (s *Service) statementPrelude(rawSymbol string, limit *int, rawPeriod *string, operation string) (symbol string, outLimit *int, period *string, fmp *FMP, err error) {
	if symbol, err = normalizeSymbol(rawSymbol); err != nil {
		return
	}
	period = normalizeOptionalText(rawPeriod)
	if outLimit, err = s.NormalizeFMPResultLimit(limit, nil); err != nil {
		return
	}
	if err = s.ValidateFMPSymbolAccess(symbol, operation); err != nil {
		return
	}
	fmp, err = s.RequireFMP()
	return
}

// BalanceSheetStatement is balanceSheetStatement (no cache).
func (s *Service) BalanceSheetStatement(ctx context.Context, rawSymbol string, limit *int, period *string) ([]BalanceSheetStatementResponse, error) {
	symbol, l, p, fmp, err := s.statementPrelude(rawSymbol, limit, period, "balance-sheet-statement")
	if err != nil {
		return nil, err
	}
	out, err := fmp.BalanceSheetStatement(ctx, symbol, l, p)
	if err != nil {
		return nil, mapFMPProviderError(err, "balance-sheet-statement")
	}
	return out, nil
}

// CashFlowStatement is cashFlowStatement (no cache).
func (s *Service) CashFlowStatement(ctx context.Context, rawSymbol string, limit *int, period *string) ([]CashFlowStatementResponse, error) {
	symbol, l, p, fmp, err := s.statementPrelude(rawSymbol, limit, period, "cash-flow-statement")
	if err != nil {
		return nil, err
	}
	out, err := fmp.CashFlowStatement(ctx, symbol, l, p)
	if err != nil {
		return nil, mapFMPProviderError(err, "cash-flow-statement")
	}
	return out, nil
}

// IncomeStatement is incomeStatement (no cache).
func (s *Service) IncomeStatement(ctx context.Context, rawSymbol string, limit *int, period *string) ([]IncomeStatementResponse, error) {
	symbol, l, p, fmp, err := s.statementPrelude(rawSymbol, limit, period, "income-statement")
	if err != nil {
		return nil, err
	}
	out, err := fmp.IncomeStatement(ctx, symbol, l, p)
	if err != nil {
		return nil, mapFMPProviderError(err, "income-statement")
	}
	return out, nil
}

// RatiosTTM is ratiosTTM.
func (s *Service) RatiosTTM(ctx context.Context, rawSymbol string) ([]RatiosTTMResponse, error) {
	symbol, err := normalizeSymbol(rawSymbol)
	if err != nil {
		return nil, err
	}
	return payloadCached(ctx, s, "market:ratios-ttm:fmp:"+symbol, "ratios-ttm",
		func() (*payloadRow, error) {
			r, err := s.Q.GetRatiosTTMCache(ctx, marketdatadb.GetRatiosTTMCacheParams{Provider: fmpProviderName, Symbol: symbol})
			return loadRow(r, err, func(r marketdatadb.GetRatiosTTMCacheRow) payloadRow { return payloadRow(r) })
		},
		func() error { return s.ValidateFMPSymbolAccess(symbol, "ratios-ttm") },
		func(f *FMP) ([]RatiosTTMResponse, error) { return f.RatiosTTM(ctx, symbol) },
		func(payload string) error {
			return s.Q.UpsertRatiosTTMCache(ctx, marketdatadb.UpsertRatiosTTMCacheParams{ID: uuid.New(), Provider: fmpProviderName, Symbol: symbol, Payload: payload})
		})
}

// GradesConsensus is gradesConsensus (no cache).
func (s *Service) GradesConsensus(ctx context.Context, rawSymbol string) ([]GradesConsensusResponse, error) {
	symbol, err := normalizeSymbol(rawSymbol)
	if err != nil {
		return nil, err
	}
	if err := s.ValidateFMPSymbolAccess(symbol, "grades-consensus"); err != nil {
		return nil, err
	}
	fmp, err := s.RequireFMP()
	if err != nil {
		return nil, err
	}
	out, err := fmp.GradesConsensus(ctx, symbol)
	if err != nil {
		return nil, mapFMPProviderError(err, "grades-consensus")
	}
	return out, nil
}

// FinancialGrowth is financialGrowth (period default "FY", limit 5).
func (s *Service) FinancialGrowth(ctx context.Context, rawSymbol string, rawLimit *int, rawPeriod *string) ([]FinancialGrowthResponse, error) {
	symbol, err := normalizeSymbol(rawSymbol)
	if err != nil {
		return nil, err
	}
	period := "FY"
	if p := normalizeOptionalText(rawPeriod); p != nil {
		period = *p
	}
	l, err := s.NormalizeFMPResultLimit(rawLimit, ptr(5))
	if err != nil {
		return nil, err
	}
	limit := *l
	key := "market:financial-growth:fmp:" + symbol + ":" + period + ":" + strconv.Itoa(limit)
	return payloadCached(ctx, s, key, "financial-growth",
		func() (*payloadRow, error) {
			r, err := s.Q.GetFinancialGrowthCache(ctx, marketdatadb.GetFinancialGrowthCacheParams{Provider: fmpProviderName, Symbol: symbol, Period: period, Limit: int64(limit)})
			return loadRow(r, err, func(r marketdatadb.GetFinancialGrowthCacheRow) payloadRow { return payloadRow(r) })
		},
		func() error { return s.ValidateFMPSymbolAccess(symbol, "financial-growth") },
		func(f *FMP) ([]FinancialGrowthResponse, error) {
			return f.FinancialGrowth(ctx, symbol, ptr(limit), ptr(period))
		},
		func(payload string) error {
			return s.Q.UpsertFinancialGrowthCache(ctx, marketdatadb.UpsertFinancialGrowthCacheParams{ID: uuid.New(), Provider: fmpProviderName, Symbol: symbol, Period: period, Limit: int64(limit), Payload: payload})
		})
}

// AnalystEstimates is analystEstimates (period required).
func (s *Service) AnalystEstimates(ctx context.Context, rawSymbol, rawPeriod string, page, rawLimit *int) ([]AnalystEstimatesResponse, error) {
	symbol, err := normalizeSymbol(rawSymbol)
	if err != nil {
		return nil, err
	}
	period, err := normalizeRequiredText(rawPeriod, "period")
	if err != nil {
		return nil, err
	}
	limit, err := s.NormalizeFMPResultLimit(rawLimit, nil)
	if err != nil {
		return nil, err
	}
	key := "market:analyst-estimates:fmp:" + symbol + ":" + period
	return payloadCached(ctx, s, key, "analyst-estimates",
		func() (*payloadRow, error) {
			r, err := s.Q.GetAnalystEstimatesCache(ctx, marketdatadb.GetAnalystEstimatesCacheParams{Provider: fmpProviderName, Symbol: symbol, Period: period})
			return loadRow(r, err, func(r marketdatadb.GetAnalystEstimatesCacheRow) payloadRow { return payloadRow(r) })
		},
		func() error {
			if err := s.ValidateFMPSymbolAccess(symbol, "analyst-estimates"); err != nil {
				return err
			}
			if s.Tier != TierPremium && strings.ToLower(period) != "annual" {
				return paymentRequired("FMP free-tier for analyst-estimates is limited to annual reports.")
			}
			return nil
		},
		func(f *FMP) ([]AnalystEstimatesResponse, error) {
			return f.AnalystEstimates(ctx, symbol, period, page, limit)
		},
		func(payload string) error {
			return s.Q.UpsertAnalystEstimatesCache(ctx, marketdatadb.UpsertAnalystEstimatesCacheParams{ID: uuid.New(), Provider: fmpProviderName, Symbol: symbol, Period: period, Payload: payload})
		})
}

// Ratios is ratios (period default "FY", limit 5).
func (s *Service) Ratios(ctx context.Context, rawSymbol string, rawLimit *int, rawPeriod *string) ([]RatiosResponse, error) {
	symbol, err := normalizeSymbol(rawSymbol)
	if err != nil {
		return nil, err
	}
	period := "FY"
	if p := normalizeOptionalText(rawPeriod); p != nil {
		period = *p
	}
	l, err := s.NormalizeFMPResultLimit(rawLimit, ptr(5))
	if err != nil {
		return nil, err
	}
	limit := *l
	key := "market:ratios:fmp:" + symbol + ":" + period + ":" + strconv.Itoa(limit)
	return payloadCached(ctx, s, key, "ratios",
		func() (*payloadRow, error) {
			r, err := s.Q.GetRatiosCache(ctx, marketdatadb.GetRatiosCacheParams{Provider: fmpProviderName, Symbol: symbol, Period: period, Limit: int64(limit)})
			return loadRow(r, err, func(r marketdatadb.GetRatiosCacheRow) payloadRow { return payloadRow(r) })
		},
		func() error { return s.ValidateFMPSymbolAccess(symbol, "ratios") },
		func(f *FMP) ([]RatiosResponse, error) { return f.Ratios(ctx, symbol, ptr(limit), ptr(period)) },
		func(payload string) error {
			return s.Q.UpsertRatiosCache(ctx, marketdatadb.UpsertRatiosCacheParams{ID: uuid.New(), Provider: fmpProviderName, Symbol: symbol, Period: period, Limit: int64(limit), Payload: payload})
		})
}

// Earnings is earnings(symbol:limit:) (Redis only; limit default 100 before
// the plan clamp).
func (s *Service) Earnings(ctx context.Context, rawSymbol string, rawLimit *int) ([]EarningsResponse, error) {
	symbol, err := normalizeSymbol(rawSymbol)
	if err != nil {
		return nil, err
	}
	l, err := s.NormalizeFMPResultLimit(rawLimit, ptr(100))
	if err != nil {
		return nil, err
	}
	key := "market:earnings:fmp:" + symbol + ":" + strconv.Itoa(*l)
	var hot []EarningsResponse
	if s.Cache.Get(ctx, key, &hot) {
		return hot, nil
	}
	if err := s.ValidateFMPSymbolAccess(symbol, "earnings"); err != nil {
		return nil, err
	}
	fmp, err := s.RequireFMP()
	if err != nil {
		return nil, err
	}
	fresh, err := fmp.Earnings(ctx, symbol, l)
	if err != nil {
		return nil, mapFMPProviderError(err, "earnings")
	}
	s.Cache.Set(ctx, key, fresh, s.Config.FMPTTL)
	return fresh, nil
}

// EarningsCalendar is earningsCalendar.
func (s *Service) EarningsCalendar(ctx context.Context, rawFrom, rawTo *string) ([]EarningsResponse, error) {
	from, err := parseOptionalDateOnly(rawFrom, "from")
	if err != nil {
		return nil, err
	}
	to, err := parseOptionalDateOnly(rawTo, "to")
	if err != nil {
		return nil, err
	}
	part := func(t *time.Time) string {
		if t == nil {
			return "none"
		}
		return FormatDateOnly(*t)
	}
	key := "market:earnings-calendar:fmp:" + part(from) + ":" + part(to)
	var hot []EarningsResponse
	if s.Cache.Get(ctx, key, &hot) {
		return hot, nil
	}
	if from != nil {
		now := s.now().UTC()
		var limit time.Time
		var desc string
		switch s.Tier {
		case TierStarter:
			limit, desc = addYears(now, -1), "limited to 1 year"
		case TierPremium:
			limit, desc = addYears(now, -5), "limited to 5 years"
		default:
			limit, desc = addMonths(now, -1), "limited to 1 month"
		}
		if from.Before(StartOfDay(limit)) {
			return nil, paymentRequired("This historical earnings range is " + desc + " on the current market data coverage.")
		}
	}
	if from != nil && to != nil && from.After(*to) {
		return nil, badRequest("`from` must be on or before `to`.")
	}
	fmp, err := s.RequireFMP()
	if err != nil {
		return nil, err
	}
	fresh, err := fmp.EarningsCalendar(ctx, from, to)
	if err != nil {
		return nil, mapFMPProviderError(err, "earnings-calendar")
	}
	s.Cache.Set(ctx, key, fresh, s.Config.FMPTTL)
	return fresh, nil
}

// EarningsTranscript is earningsTranscript.
func (s *Service) EarningsTranscript(ctx context.Context, rawSymbol string, rawDate *string, year, quarter *int) (EarningsTranscriptResponse, error) {
	symbol, err := normalizeSymbol(rawSymbol)
	if err != nil {
		return EarningsTranscriptResponse{}, err
	}
	parsed, err := parseOptionalDateOnly(rawDate, "date")
	if err != nil {
		return EarningsTranscriptResponse{}, err
	}
	var date *string
	if parsed != nil {
		date = ptr(FormatDateOnly(*parsed))
	}
	if (year == nil) != (quarter == nil) {
		return EarningsTranscriptResponse{}, badRequest("`year` and `quarter` must be provided together.")
	}
	if quarter != nil && (*quarter < 1 || *quarter > 4) {
		return EarningsTranscriptResponse{}, badRequest("`quarter` must be between 1 and 4.")
	}
	if date == nil && year == nil {
		return EarningsTranscriptResponse{}, badRequest("Query parameter `date` is required unless `year` and `quarter` are provided.")
	}
	orNone := func(p *string) string {
		if p == nil {
			return "none"
		}
		return *p
	}
	intOrNone := func(p *int) string {
		if p == nil {
			return "none"
		}
		return strconv.Itoa(*p)
	}
	key := "market:earnings-transcript:fmp:" + symbol + ":" + orNone(date) + ":" + intOrNone(year) + ":" + intOrNone(quarter)
	var hot EarningsTranscriptResponse
	if s.Cache.Get(ctx, key, &hot) {
		return hot, nil
	}
	if err := s.ValidateFMPSymbolAccess(symbol, "earnings-transcript"); err != nil {
		return EarningsTranscriptResponse{}, err
	}
	fmp, err := s.RequireFMP()
	if err != nil {
		return EarningsTranscriptResponse{}, err
	}
	fresh, err := fmp.EarningsTranscript(ctx, symbol, date, year, quarter)
	if err != nil {
		return EarningsTranscriptResponse{}, mapFMPProviderError(err, "earnings-transcript")
	}
	s.Cache.Set(ctx, key, fresh, s.Config.FMPTTL)
	return fresh, nil
}

// HistoricalSectorPerformance is historicalSectorPerformance (no cache).
func (s *Service) HistoricalSectorPerformance(ctx context.Context, rawSector string, rawExchange, rawFrom, rawTo *string) ([]HistoricalSectorPerformanceResponse, error) {
	sector, err := normalizeRequiredText(rawSector, "sector")
	if err != nil {
		return nil, err
	}
	exchange := normalizeOptionalText(rawExchange)
	from, to, err := parseHistoryRange(rawFrom, rawTo)
	if err != nil {
		return nil, err
	}
	fmp, err := s.RequireFMP()
	if err != nil {
		return nil, err
	}
	out, err := fmp.HistoricalSectorPerformance(ctx, sector, exchange, from, to)
	if err != nil {
		return nil, mapFMPProviderError(err, "historical-sector-performance")
	}
	return out, nil
}
