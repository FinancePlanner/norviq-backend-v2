package boards

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"norviq-api/internal/config"
	"norviq-api/internal/features/aiplatform"
	"norviq-api/internal/features/earnings"
	"norviq-api/internal/features/macro"
	"norviq-api/internal/features/market"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/platform/joblock"
	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/platform/newsprovider"
	"norviq-api/internal/server"
	"norviq-api/internal/store/boardsdb"
	"norviq-api/internal/store/macrodb"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DailyBoardPostJob, DailyBoardPublisher.run/compose/write/candidate,
// LiveDailyBoardSources and OpenAIDailyBoardWriter, plus the parts of
// MarketBriefSchedule the schedule reads.

// MARK: - When (DailyBoardSchedule, MarketBriefSchedule)

var lisbon = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Lisbon")
	if err != nil {
		return time.UTC
	}
	return loc
}()

const (
	// briefEveningStart is MarketBriefSchedule.eveningStart (22:30 Lisbon).
	briefEveningStart = 22*60 + 30
	briefEveningEnd   = 24 * 60
	// dailyWeekendStart is DailyBoardSchedule.weekendStart (10:00 Lisbon).
	dailyWeekendStart = 10 * 60
)

func lisbonMinutes(t time.Time) int {
	l := t.In(lisbon)
	return l.Hour()*60 + l.Minute()
}

func lisbonDate(t time.Time) string { return t.In(lisbon).Format(dailyDayLayout) }

// DailyLatestDueDate is DailyBoardSchedule.latestDueDate: the latest day
// whose window has opened, weekdays at 22:30 Lisbon and weekends at 10:00.
// A day missed until the next one opens is not written late.
func DailyLatestDueDate(now time.Time) (string, bool) {
	local := now.In(lisbon)
	for offset := range 2 {
		day := local.AddDate(0, 0, -offset)
		if offset == 0 {
			weekday := int(day.Weekday()) + 1
			opens := dailyWeekendStart
			if weekday >= 2 && weekday <= 6 {
				opens = briefEveningStart
			}
			if lisbonMinutes(now) < opens {
				continue
			}
		}
		return lisbonDate(day), true
	}
	return "", false
}

// dailyEveningBriefDue is MarketBriefSchedule.dueSlot(now:) answering the
// evening slot: a weekday between 22:30 and midnight Lisbon.
func dailyEveningBriefDue(now time.Time) (string, bool) {
	local := now.In(lisbon)
	weekday := int(local.Weekday()) + 1
	if weekday < 2 || weekday > 6 {
		return "", false
	}
	if m := lisbonMinutes(now); m >= briefEveningStart && m < briefEveningEnd {
		return lisbonDate(now), true
	}
	return "", false
}

// DailySessionDate is DailyBoardSchedule.sessionDate: the Lisbon date of an
// exchange timestamp in seconds or milliseconds.
func DailySessionDate(timestamp float64) (string, bool) {
	if math.IsNaN(timestamp) || math.IsInf(timestamp, 0) || timestamp <= 0 {
		return "", false
	}
	seconds := timestamp
	if timestamp > 10_000_000_000 {
		seconds = timestamp / 1000
	}
	sec, frac := math.Modf(seconds)
	return lisbonDate(time.Unix(int64(sec), int64(frac*1e9))), true
}

// MARK: - Sources

// DailySources is DailyBoardSources: everything the post reads from outside
// the database. Every call degrades to nil or empty.
type DailySources interface {
	Headlines(ctx context.Context) []DailyHeadline
	// LargeCapMovers is today's biggest movers among the largest US
	// companies, biggest move first.
	LargeCapMovers(ctx context.Context) []string
	Quote(ctx context.Context, symbol string) *DailyStockQuote
	Metrics(ctx context.Context, symbol string) map[string]float64
	Earnings(ctx context.Context, from, to string, symbol *string) []DailyEarnings
	Bars(ctx context.Context, symbol string) *DailyBarSeries
	Macro(ctx context.Context, country, key string) *DailyMacroReading
}

// DailyMarket is the part of the market data service the post reads
// (*marketdata.Service).
type DailyMarket interface {
	Quote(ctx context.Context, symbol string) (marketdata.QuoteResponse, error)
	BasicFinancials(ctx context.Context, symbol string) (marketdata.BasicFinancialsResponse, error)
	Overview(ctx context.Context) (marketdata.MarketOverviewResponse, error)
}

// DailyCalendar is app.earningsService (*earnings.Calendar).
type DailyCalendar interface {
	Fetch(ctx context.Context, q earnings.Query) ([]earnings.ItemResponse, error)
}

// LiveDailySources is LiveDailyBoardSources.
type LiveDailySources struct {
	// News is app.dailyBoardNewsProvider: nil when no provider is set up.
	News      newsprovider.Provider
	Market    DailyMarket
	Calendar  DailyCalendar
	MacroRepo macro.Repo
	Yahoo     YahooDailyBars
	Logger    *slog.Logger
	Now       func() time.Time
}

const (
	// briefMaxHeadlines is MarketBriefFacts.maxHeadlines.
	briefMaxHeadlines = 25
	// briefHeadlineMaxAge is MarketBriefFacts.headlineMaxAge.
	briefHeadlineMaxAge = 24 * time.Hour
)

func (s *LiveDailySources) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Headlines implements DailySources.
func (s *LiveDailySources) Headlines(ctx context.Context) []DailyHeadline {
	if s.News == nil {
		return []DailyHeadline{}
	}
	items, err := s.News.FetchGeneral(ctx)
	if err != nil {
		s.Logger.Warn("daily_board_post news_failed", "error", err.Error())
		return []DailyHeadline{}
	}
	now := s.now()
	fresh := []newsprovider.Item{}
	for _, it := range items {
		if now.Sub(it.PublishedAt) <= briefHeadlineMaxAge {
			fresh = append(fresh, it)
		}
	}
	sort.SliceStable(fresh, func(i, j int) bool { return fresh[i].PublishedAt.After(fresh[j].PublishedAt) })
	if len(fresh) > briefMaxHeadlines {
		fresh = fresh[:briefMaxHeadlines]
	}
	out := make([]DailyHeadline, 0, len(fresh))
	for _, it := range fresh {
		tickers := []string{}
		for _, t := range it.RelatedSymbols {
			if DailyIsStockTicker(t) {
				tickers = append(tickers, t)
			}
		}
		// Vapor passed the whole headline as one "ticker" to
		// MarketBriefValidator.tickers, so this names a ticker only when the
		// headline is itself one (ported as is).
		for _, t := range briefTickers([]string{it.Headline}) {
			if DailyIsStockTicker(t) {
				tickers = append(tickers, t)
			}
		}
		out = append(out, DailyHeadline{Title: it.Headline, URL: it.URL, Tickers: tickers})
	}
	return out
}

// LargeCapMovers implements DailySources: the market overview's heatmap,
// or, when that is down (FMP's daily cap is often spent by evening), the
// fixed large-cap list ranked by its quotes.
func (s *LiveDailySources) LargeCapMovers(ctx context.Context) []string {
	overview, err := s.Market.Overview(ctx)
	if err != nil {
		s.Logger.Warn("daily_board_post movers_failed", "error", err.Error())
	} else if len(overview.Heatmap) > 0 {
		tiles := append([]marketdata.MarketHeatmapTile{}, overview.Heatmap...)
		sort.SliceStable(tiles, func(i, j int) bool { return math.Abs(tiles[i].ChangePct) > math.Abs(tiles[j].ChangePct) })
		out := make([]string, len(tiles))
		for i, t := range tiles {
			out[i] = t.Symbol
		}
		return out
	}
	quotes := []DailyStockQuote{}
	for _, symbol := range DailyLargeCaps {
		if q := s.Quote(ctx, symbol); q != nil {
			quotes = append(quotes, *q)
		}
	}
	sort.SliceStable(quotes, func(i, j int) bool { return math.Abs(quotes[i].ChangePercent) > math.Abs(quotes[j].ChangePercent) })
	out := make([]string, len(quotes))
	for i, q := range quotes {
		out[i] = q.Symbol
	}
	return out
}

// Quote implements DailySources.
func (s *LiveDailySources) Quote(ctx context.Context, symbol string) *DailyStockQuote {
	q, err := s.Market.Quote(ctx, symbol)
	if err != nil || q.PercentChange == nil || math.IsNaN(*q.PercentChange) || math.IsInf(*q.PercentChange, 0) {
		return nil
	}
	session, ok := DailySessionDate(q.Timestamp)
	if !ok {
		return nil
	}
	return &DailyStockQuote{Symbol: q.Symbol, Price: q.CurrentPrice, ChangePercent: *q.PercentChange, SessionDate: session}
}

// Metrics implements DailySources: the numeric, finite basic-financials
// metrics by Finnhub's key.
func (s *LiveDailySources) Metrics(ctx context.Context, symbol string) map[string]float64 {
	out := map[string]float64{}
	resp, err := s.Market.BasicFinancials(ctx, symbol)
	if err != nil {
		return out
	}
	for k, v := range resp.Metric {
		if n, ok := v.Number(); ok && !math.IsNaN(n) && !math.IsInf(n, 0) {
			out[k] = n
		}
	}
	return out
}

// Earnings implements DailySources.
func (s *LiveDailySources) Earnings(ctx context.Context, from, to string, symbol *string) []DailyEarnings {
	rows, err := s.Calendar.Fetch(ctx, earnings.Query{From: &from, To: &to, Symbol: symbol})
	if err != nil {
		s.Logger.Warn("daily_board_post earnings_failed", "error", err.Error())
		return []DailyEarnings{}
	}
	out := []DailyEarnings{}
	for _, r := range rows {
		if r.Symbol == nil || r.Date == nil {
			continue
		}
		out = append(out, DailyEarnings{
			Symbol: strings.ToUpper(*r.Symbol), Date: *r.Date, Hour: r.Hour,
			EpsEstimate: r.EpsEstimate, RevenueEstimate: r.RevenueEstimate,
		})
	}
	return out
}

// Bars implements DailySources.
func (s *LiveDailySources) Bars(ctx context.Context, symbol string) *DailyBarSeries {
	y := s.Yahoo
	if y.Logger == nil {
		y.Logger = s.Logger
	}
	return y.Series(ctx, symbol)
}

// Macro implements DailySources: the latest period and the one before it,
// within the last half year.
func (s *LiveDailySources) Macro(ctx context.Context, country, key string) *DailyMacroReading {
	since, ok := DailyAddDays(-190, lisbonDate(s.now()))
	var from *string
	if ok {
		from = &since
	}
	points, err := s.MacroRepo.Series(ctx, country, key, from, nil, 2)
	if err != nil || len(points) == 0 {
		return nil
	}
	latest := points[len(points)-1]
	r := &DailyMacroReading{Period: latest.PeriodDate, Value: latest.Value}
	if len(points) > 1 {
		r.Prior = f64(points[len(points)-2].Value)
	}
	return r
}

// MARK: - Model (OpenAIDailyBoardWriter)

// DailyWriter is DailyBoardWriting.
type DailyWriter interface {
	Draft(ctx context.Context, prompt string) (title, body string, err error)
}

// AIDailyWriter is OpenAIDailyBoardWriter: one JSON-mode chat on the app's
// default chain (Anthropic first in the Haiku trial). It sends no sampling
// parameters; the platform's adapters own that.
type AIDailyWriter struct{ Client ai.ChatClient }

// Draft implements DailyWriter.
func (w AIDailyWriter) Draft(ctx context.Context, prompt string) (string, string, error) {
	reply, err := w.Client.Chat(ctx, []ai.Message{
		ai.Text("system", DailySystemPrompt),
		ai.Text("user", prompt),
	}, []ai.Tool{}, ai.ResponseFormatJSON)
	if err != nil {
		return "", "", err
	}
	if reply.Content == nil {
		return "", "", httpx.Abort(http.StatusBadGateway, "The daily post draft was empty.")
	}
	return DailyParseDraft(*reply.Content)
}

// MARK: - Publish (DailyBoardPublisher)

// dailyDraftsPerAttempt is DailyBoardPublisher.draftsPerAttempt: one draft,
// and one more with the first one's fault named.
const dailyDraftsPerAttempt = 2

// DailyRejectedError is DailyBoardPostError.rejected: both drafts failed the
// validator. Returned, not skipped, so the job asks again on its next tick.
type DailyRejectedError struct{ Reason DailyRejection }

func (e *DailyRejectedError) Error() string { return "draft rejected: " + e.Reason.String() }

// DailyPublisher is DailyBoardPublisher's model-facing half.
type DailyPublisher struct {
	Service *Service
	Sources DailySources
	Writer  DailyWriter
	Logger  *slog.Logger
}

func (p *DailyPublisher) logger() *slog.Logger {
	if p.Logger != nil {
		return p.Logger
	}
	return slog.Default()
}

// Run is DailyBoardPublisher.run: builds the day's facts, asks the model,
// and writes one post. Skips the day when no topic has enough data. A
// second call on the same date does nothing. topic overrides the rotation.
func (p *DailyPublisher) Run(ctx context.Context, tradingDate string, topic *string) (DailyOutcome, error) {
	runs, err := p.Service.Q.DailyRunByDate(ctx, tradingDate)
	if err != nil {
		return DailyOutcome{}, err
	}
	if len(runs) > 0 {
		return DailyOutcome{Kind: "alreadyRun", Status: runs[0].Status}, nil
	}
	candidate, err := p.Compose(ctx, tradingDate, topic)
	if err != nil {
		return DailyOutcome{}, err
	}
	if candidate == nil {
		if err := p.Service.SkipDaily(ctx, tradingDate, nil); err != nil {
			return DailyOutcome{}, err
		}
		return DailyOutcome{Kind: "skipped", Reason: "no topic had enough data"}, nil
	}
	title, body, err := p.Write(ctx, *candidate, tradingDate)
	if err != nil {
		return DailyOutcome{}, err
	}
	return p.Service.InsertDaily(ctx, tradingDate, *candidate, title, body)
}

// Compose is DailyBoardPublisher.compose: the first topic in the day's
// rotation, or the one asked for, that has enough facts behind it.
func (p *DailyPublisher) Compose(ctx context.Context, day string, topic *string) (*DailyCandidate, error) {
	history, err := p.Service.DailyHistory(ctx, day)
	if err != nil {
		return nil, err
	}
	topics := DailyTopics(day)
	if topic != nil {
		topics = []string{*topic}
	}
	for _, next := range topics {
		if c := p.Candidate(ctx, next, day, history); c != nil {
			return c, nil
		}
		p.logger().Info("daily_board_post topic_unavailable", "topic", next, "date", day)
	}
	return nil, nil
}

// Write is DailyBoardPublisher.write: drafts until the validator accepts,
// telling the model what was wrong the first time.
func (p *DailyPublisher) Write(ctx context.Context, c DailyCandidate, day string) (title, body string, err error) {
	prompt := DailyPrompt(c, day)
	var rejection *DailyRejection
	for range dailyDraftsPerAttempt {
		text := prompt
		if rejection != nil {
			text = prompt + "\n\nYour previous draft was rejected because " + rejection.String() + ". Write it again and fix that."
		}
		rawTitle, rawBody, err := p.Writer.Draft(ctx, text)
		if err != nil {
			return "", "", err
		}
		t, b, reason := DailyCheck(rawTitle, rawBody, c.Grounded)
		if reason == nil {
			return t, b, nil
		}
		p.logger().Info("daily_board_post draft_rejected", "date", day, "reason", reason.String())
		rejection = reason
	}
	if rejection == nil {
		rejection = &DailyRejection{Kind: "ungrounded"}
	}
	return "", "", &DailyRejectedError{Reason: *rejection}
}

// Candidate is DailyBoardPublisher.candidate: one topic's facts, or nil when
// there is too little to say.
func (p *DailyPublisher) Candidate(ctx context.Context, topic, day string, history []DailyHistoryEntry) *DailyCandidate {
	src := p.Sources
	switch topic {
	case TopicMag7:
		snaps := []DailyStockSnapshot{}
		for _, symbol := range DailyMag7 {
			q := src.Quote(ctx, symbol)
			if q == nil {
				continue
			}
			snaps = append(snaps, dailySnapshot(*q, src.Metrics(ctx, symbol)))
		}
		pick := DailyPickMag7(snaps, history)
		if pick == nil {
			return nil
		}
		pick.NextEarnings = p.nextEarnings(ctx, pick.Symbol, day)
		return DailyStock(*pick, TopicMag7)

	case TopicStock:
		headlines := src.Headlines(ctx)
		// General news rarely tags tickers, so the day's biggest large-cap
		// movers are candidates too.
		movers := src.LargeCapMovers(ctx)
		quotes := []DailyStockQuote{}
		for _, symbol := range DailyHotStockCandidates(headlines, movers, 12) {
			if q := src.Quote(ctx, symbol); q != nil {
				quotes = append(quotes, *q)
			}
		}
		best, story := DailyHotStock(quotes, headlines, day)
		if best == nil {
			return nil
		}
		pick := dailySnapshot(*best, src.Metrics(ctx, best.Symbol))
		pick.Headline = story
		pick.NextEarnings = p.nextEarnings(ctx, best.Symbol, day)
		return DailyStock(pick, TopicStock)

	case TopicETF:
		order := DailyETFOrder(history)
		for _, fund := range order[:min(3, len(order))] {
			if series := src.Bars(ctx, fund.Symbol); series != nil {
				if c := DailyETF(fund, *series); c != nil {
					return c
				}
			}
		}
		return nil

	case TopicMacro:
		inputs := p.MacroInputs(ctx, day)
		preferred := DailyMacroAngle(day, history)
		angles := []string{preferred}
		for _, a := range append([]string{"inflation"}, dailyRotatingAngles...) {
			if a != preferred {
				angles = append(angles, a)
			}
		}
		for _, a := range angles {
			if c := DailyMacro(a, inputs); c != nil {
				return c
			}
		}
		return nil

	case TopicWeekRecap:
		indexes := []DailyNamedSeries{}
		for _, idx := range DailyRecapIndexes {
			if series := src.Bars(ctx, idx.Symbol); series != nil {
				indexes = append(indexes, DailyNamedSeries{Name: idx.Name, Series: *series})
			}
		}
		mag7 := []DailyBarSeries{}
		for _, symbol := range DailyMag7 {
			if series := src.Bars(ctx, symbol); series != nil {
				mag7 = append(mag7, *series)
			}
		}
		return DailyWeekRecap(day, indexes, mag7)

	case TopicWeekAhead:
		from, ok1 := DailyAddDays(1, day)
		through, ok2 := DailyAddDays(7, day)
		if !ok1 || !ok2 {
			return nil
		}
		// Swift evaluated the arguments in order: events, earnings, macro.
		events := macro.WorldEvents(from, through)
		rows := src.Earnings(ctx, from, through, nil)
		return DailyWeekAhead(events, rows, p.MacroInputs(ctx, day))
	}
	// index and news (v1 topics) are not scheduled any more.
	return nil
}

func dailySnapshot(q DailyStockQuote, metrics map[string]float64) DailyStockSnapshot {
	change := q.ChangePercent
	return DailyStockSnapshot{
		Symbol: strings.ToUpper(q.Symbol), Price: q.Price, ChangePercent: &change,
		SessionDate: q.SessionDate, Metrics: metrics,
	}
}

// nextEarnings is the symbol's first report in the 30 days after day.
func (p *DailyPublisher) nextEarnings(ctx context.Context, symbol, day string) *string {
	from, ok1 := DailyAddDays(1, day)
	to, ok2 := DailyAddDays(30, day)
	if !ok1 || !ok2 {
		return nil
	}
	upper := strings.ToUpper(symbol)
	first := ""
	for _, e := range p.Sources.Earnings(ctx, from, to, &symbol) {
		if e.Symbol == upper && e.Date >= from && (first == "" || e.Date < first) {
			first = e.Date
		}
	}
	if first == "" {
		return nil
	}
	s := DailyWithWeekday(first)
	return &s
}

// MacroInputs is DailyBoardPublisher.macroInputs. The macro tables store
// MacroCountry raw values ("US", "EA").
func (p *DailyPublisher) MacroInputs(ctx context.Context, day string) DailyMacroInputs {
	next := func(dates []string) *string {
		best := ""
		for _, d := range dates {
			if d > day && (best == "" || d < best) {
				best = d
			}
		}
		if best == "" {
			return nil
		}
		s := DailyWithWeekday(best)
		return &s
	}
	us, ea := string(macro.US), string(macro.EA)
	src := p.Sources
	in := DailyMacroInputs{
		USCPI:   src.Macro(ctx, us, macro.KeyHeadlineCPI),
		USCore:  src.Macro(ctx, us, macro.KeyCoreCPI),
		EACPI:   src.Macro(ctx, ea, macro.KeyHeadlineCPI),
		EACore:  src.Macro(ctx, ea, macro.KeyCoreCPI),
		FedRate: src.Macro(ctx, us, macro.KeyPolicyRate),
		ECBRate: src.Macro(ctx, ea, macro.KeyPolicyRate),
	}
	fomc := make([]string, len(macro.FOMCMeetings))
	for i, m := range macro.FOMCMeetings {
		fomc[i] = m.EndDate
	}
	in.NextUSCPI = next(macro.BLSReleaseDates)
	in.NextHICPFlash = next(macro.HICPFlashDates())
	in.NextFOMC = next(fomc)
	in.NextECB = next(macro.ECBDecisionDates())
	return in
}

// MARK: - Job (DailyBoardPostJob)

// DailyPostJobName is the JobLock name and the V2_JOBS entry.
const DailyPostJobName = "daily_board_post"

// dailyMaxAttempts is DailyBoardPostJob.maxAttempts: after three failed
// ticks a date is skipped.
const dailyMaxAttempts = 3

// DailyPostSettings are the environment variables the job reads.
type DailyPostSettings struct {
	// Enabled is DAILY_BOARD_POST_ENABLED: configure.swift registered the
	// job only when it was on.
	Enabled string `env:"DAILY_BOARD_POST_ENABLED"`
	// YahooChartBaseURL is v2-only, for test stubs; Vapor hard-coded
	// DefaultYahooChartBaseURL.
	YahooChartBaseURL string `env:"YAHOO_CHART_BASE_URL"`
}

// DailyPostJob is DailyBoardPostJob: after 90 s, every 300 s, publish one
// Daily board post after the evening market brief, or after that window has
// closed. One leader replica, one model call per date.
type DailyPostJob struct {
	// Sources and Writer override the live ones (tests).
	Sources DailySources
	Writer  DailyWriter
	// Now overrides the clock (tests).
	Now func() time.Time
	// InitialDelay and Interval override Vapor's 90 s and 300 s (tests).
	InitialDelay, Interval time.Duration
}

// Name implements features.Job.
func (DailyPostJob) Name() string { return DailyPostJobName }

// Run implements features.Job. A no-op unless DAILY_BOARD_POST_ENABLED and
// an AI provider key is configured, as in Vapor.
func (j DailyPostJob) Run(ctx context.Context, d *server.Deps) {
	settings, err := config.Load[DailyPostSettings]()
	if err != nil {
		d.Logger.Error("daily_board_post setup", "error", err)
		return
	}
	if !config.Bool(settings.Enabled, false) {
		d.Logger.Info("daily_board_post disabled: DAILY_BOARD_POST_ENABLED is off")
		return
	}
	writer := j.Writer
	if writer == nil {
		p, err := aiplatform.Platform(d)
		if err != nil {
			d.Logger.Error("daily_board_post setup", "error", err)
			return
		}
		// AIProviderConfiguration.load().isConfigured: the primary key, not
		// the Anthropic lead.
		if !p.Config.IsConfigured() {
			d.Logger.Info("daily_board_post disabled reason=missing_ai_provider_key")
			return
		}
		writer = AIDailyWriter{Client: p.Default}
	}
	sources := j.Sources
	if sources == nil {
		live, err := NewLiveDailySources(d, settings)
		if err != nil {
			d.Logger.Error("daily_board_post setup", "error", err)
			return
		}
		sources = live
	}
	runner := &DailyPostRunner{
		Publisher: &DailyPublisher{
			Service: &Service{DB: d.DB, Q: boardsdb.New(d.DB)},
			Sources: sources, Writer: writer, Logger: d.Logger,
		},
		DB: d.DB, Logger: d.Logger, LockSession: d.LockSession,
	}
	now := j.Now
	if now == nil {
		now = time.Now
	}
	initial, interval := j.InitialDelay, j.Interval
	if initial == 0 {
		initial = 90 * time.Second
	}
	if interval == 0 {
		interval = 300 * time.Second
	}
	if !dailySleep(ctx, initial) {
		return
	}
	for {
		runner.Tick(ctx, now())
		if !dailySleep(ctx, interval) {
			return
		}
	}
}

// NewLiveDailySources wires LiveDailyBoardSources from the shared services:
// the news chain, market data, the earnings calendar and the macro tables.
func NewLiveDailySources(d *server.Deps, settings DailyPostSettings) (*LiveDailySources, error) {
	mkt, err := market.SharedService(d)
	if err != nil {
		return nil, err
	}
	chain, err := newsprovider.Shared(d)
	if err != nil {
		return nil, err
	}
	cal, err := earnings.CalendarFromEnv(nil)
	if err != nil {
		return nil, err
	}
	base := strings.TrimSpace(settings.YahooChartBaseURL)
	if base == "" {
		base = DefaultYahooChartBaseURL
	}
	return &LiveDailySources{
		News: chain.Provider, Market: mkt, Calendar: cal,
		MacroRepo: macro.Repo{Q: macrodb.New(d.DB)},
		Yahoo:     YahooDailyBars{BaseURL: base, Client: &http.Client{}, Logger: d.Logger},
		Logger:    d.Logger,
	}, nil
}

func dailySleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// DailyPostRunner is one DailyBoardPostJob: its ticks and its in-memory
// failure count per date.
type DailyPostRunner struct {
	Publisher   *DailyPublisher
	DB          *pgxpool.Pool
	Logger      *slog.Logger
	LockSession *joblock.Session

	mu       sync.Mutex
	failures map[string]int
}

// Tick is DailyBoardPostJob.tick.
func (r *DailyPostRunner) Tick(ctx context.Context, now time.Time) {
	tradingDate, ok := DailyLatestDueDate(now)
	if !ok {
		return
	}
	joblock.RunAsLeaderWithSession(ctx, r.DB, r.LockSession, r.Logger, DailyPostJobName, func(ctx context.Context) {
		if err := r.tick(ctx, tradingDate, now); err != nil {
			count := r.recordFailure(tradingDate)
			r.Logger.Error("daily_board_post failed", "date", tradingDate,
				"attempt", fmt.Sprintf("%d/%d", count, dailyMaxAttempts), "error", err.Error())
		}
	})
}

func (r *DailyPostRunner) tick(ctx context.Context, tradingDate string, now time.Time) error {
	svc := r.Publisher.Service
	runs, err := svc.Q.DailyRunByDate(ctx, tradingDate)
	if err != nil {
		return err
	}
	if len(runs) > 0 {
		return nil
	}
	if r.waitingForBrief(ctx, tradingDate, now) {
		return nil
	}
	if r.failureCount(tradingDate) >= dailyMaxAttempts {
		return svc.SkipDaily(ctx, tradingDate, nil)
	}
	_, err = r.Publisher.Run(ctx, tradingDate, nil)
	return err
}

// waitingForBrief: during the evening window, wait until that date's brief
// exists so the post is not written from the morning's numbers. After
// midnight the brief is no longer required.
func (r *DailyPostRunner) waitingForBrief(ctx context.Context, tradingDate string, now time.Time) bool {
	due, ok := dailyEveningBriefDue(now)
	if !ok || due != tradingDate {
		return false
	}
	payloads, err := r.Publisher.Service.Q.DailyEveningBrief(ctx, tradingDate)
	if err == nil && len(payloads) > 0 && !json.Valid([]byte(payloads[0])) {
		// The repository decoded the payload; a corrupt one threw.
		err = errors.New("market brief payload does not decode")
	}
	if err != nil {
		r.Logger.Warn("daily_board_post brief_lookup_failed", "error", err.Error())
		return true
	}
	return len(payloads) == 0
}

func (r *DailyPostRunner) failureCount(date string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failures[date]
}

func (r *DailyPostRunner) recordFailure(date string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failures == nil {
		r.failures = map[string]int{}
	}
	r.failures[date]++
	return r.failures[date]
}
