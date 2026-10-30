package recaps

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/store/recapsdb"

	"github.com/google/uuid"
)

// Facts is RecapFacts: everything the deck needs about one user and one
// period.
type Facts struct {
	ActiveDays           int
	ActiveDaysFirstHalf  int
	ActiveDaysSecondHalf int
	LongestStreakDays    int

	TradeCount     int
	BuyCount       int
	SellCount      int
	TradesBySymbol map[string]int

	ResearchTouchesBySymbol map[string]int
	NotesWritten            int
	WatchlistAdded          int
	TargetsSet              int
	TargetsHit              int

	Growth []Growth

	DividendsReceived []Money
	DividendPayments  int
	DividendPayers    int

	GoalContributions     []Money
	GoalContributionCount int

	BadgesEarned []Badge
	XPEarned     int

	PilotFollows     int
	BoardPosts       int
	BoardComments    int
	AIRequests       int
	ExpensesRecorded int

	BestStreakEverDays int
	TotalBadgesEver    int
}

// HasAnyActivity is hasAnyActivity.
func (f *Facts) HasAnyActivity() bool {
	for _, n := range []int{
		f.ActiveDays, f.TradeCount, f.NotesWritten, f.WatchlistAdded, f.TargetsSet, f.TargetsHit,
		f.DividendPayments, f.GoalContributionCount, f.XPEarned, f.PilotFollows,
		f.BoardPosts, f.BoardComments, f.AIRequests, f.ExpensesRecorded,
	} {
		if n > 0 {
			return true
		}
	}
	return len(f.BadgesEarned) > 0 || len(f.ResearchTouchesBySymbol) > 0
}

// DayNumber is RecapStreaks.dayNumber: days since 1970-01-01 for a
// yyyy-MM-dd string, false if malformed.
func DayNumber(localDate string) (int, bool) {
	parts := strings.Split(localDate, "-")
	if len(parts) != 3 || len(parts[0]) != 4 || len(parts[1]) != 2 || len(parts[2]) != 2 {
		return 0, false
	}
	year, e1 := strconv.Atoi(parts[0])
	month, e2 := strconv.Atoi(parts[1])
	day, e3 := strconv.Atoi(parts[2])
	if e1 != nil || e2 != nil || e3 != nil || month < 1 || month > 12 || day < 1 || day > 31 {
		return 0, false
	}
	y := year
	if month <= 2 {
		y--
	}
	era := y / 400
	if y < 0 {
		era = (y - 399) / 400
	}
	yearOfEra := y - era*400
	dayOfYear := (153*((month+9)%12)+2)/5 + day - 1
	dayOfEra := yearOfEra*365 + yearOfEra/4 - yearOfEra/100 + dayOfYear
	return era*146_097 + dayOfEra - 719_468, true
}

// LongestStreak is RecapStreaks.longest(localDates:).
func LongestStreak(localDates []string) int {
	set := map[int]bool{}
	for _, d := range localDates {
		if n, ok := DayNumber(d); ok {
			set[n] = true
		}
	}
	days := make([]int, 0, len(set))
	for d := range set {
		days = append(days, d)
	}
	sort.Ints(days)
	best, run := 0, 0
	for i, d := range days {
		if i > 0 && d == days[i-1]+1 {
			run++
		} else {
			run = 1
		}
		best = max(best, run)
	}
	return best
}

// money rounds to cents (Swift's .rounded(): half away from zero).
func money(v float64) float64 { return math.Round(v*100) / 100 }

type amount struct {
	currency string
	value    float64
}

// byCurrency sums per currency, sorted by currency code.
func byCurrency(amounts []amount) []Money {
	totals := map[string]float64{}
	order := []string{}
	for _, a := range amounts {
		if _, ok := totals[a.currency]; !ok {
			order = append(order, a.currency)
		}
		totals[a.currency] += a.value
	}
	sort.Strings(order)
	out := make([]Money, 0, len(order))
	for _, c := range order {
		out = append(out, Money{Amount: money(totals[c]), Currency: c})
	}
	return out
}

// collector is RecapFactsCollector.
type collector struct {
	q recapsdb.Querier
}

type window struct {
	start, end       time.Time // instants in the user's zone
	startDay, endDay time.Time // calendar days (UTC midnight)
	firstDate        string
	lastDate         string
	firstHalfLast    string
	pilotPortfolios  []uuid.UUID
	pilotWatchlists  []uuid.UUID
}

func (c collector) collect(ctx context.Context, userID uuid.UUID, p Period, loc *time.Location) (*Facts, error) {
	w := window{}
	w.start, w.end = p.Bounds(loc)
	w.startDay, w.endDay = p.Bounds(time.UTC)
	w.firstDate, w.lastDate = p.LocalDateRange()
	if p.Month != 0 {
		w.firstHalfLast = fmt.Sprintf("%04d-%02d-15", p.Year, p.Month)
	} else {
		w.firstHalfLast = fmt.Sprintf("%04d-06-30", p.Year)
	}
	lists, err := c.q.RecapPilotFollowLists(ctx, userID)
	if err != nil {
		return nil, err
	}
	w.pilotPortfolios, w.pilotWatchlists = []uuid.UUID{}, []uuid.UUID{}
	for _, l := range lists {
		if l.PortfolioListID != nil {
			w.pilotPortfolios = append(w.pilotPortfolios, *l.PortfolioListID)
		}
		if l.WatchlistListID != nil {
			w.pilotWatchlists = append(w.pilotWatchlists, *l.WatchlistListID)
		}
	}
	f := &Facts{TradesBySymbol: map[string]int{}, ResearchTouchesBySymbol: map[string]int{}, Growth: []Growth{}}
	for _, step := range []func(context.Context, uuid.UUID, *window, *Facts) error{
		c.checkIns, c.trades, c.research, c.growth, c.dividends, c.goals, c.gamification, c.social,
	} {
		if err := step(ctx, userID, &w, f); err != nil {
			return nil, err
		}
	}
	return f, nil
}

func (c collector) checkIns(ctx context.Context, userID uuid.UUID, w *window, f *Facts) error {
	all, err := c.q.RecapCheckInDates(ctx, userID)
	if err != nil {
		return err
	}
	in := map[string]bool{}
	for _, d := range all {
		if d >= w.firstDate && d <= w.lastDate {
			in[d] = true
		}
	}
	dates := make([]string, 0, len(in))
	for d := range in {
		dates = append(dates, d)
		if d <= w.firstHalfLast {
			f.ActiveDaysFirstHalf++
		}
	}
	f.ActiveDays = len(in)
	f.ActiveDaysSecondHalf = f.ActiveDays - f.ActiveDaysFirstHalf
	f.LongestStreakDays = LongestStreak(dates)
	f.BestStreakEverDays = LongestStreak(all)
	return nil
}

func (c collector) trades(ctx context.Context, userID uuid.UUID, w *window, f *Facts) error {
	rows, err := c.q.RecapTrades(ctx, recapsdb.RecapTradesParams{UserID: userID, StartDay: w.startDay, EndDay: w.endDay})
	if err != nil {
		return err
	}
	for _, r := range rows {
		f.TradeCount++
		if strings.EqualFold(r.Type, "BUY") { // the query admits only BUY, SELL, buy, sell
			f.BuyCount++
		} else {
			f.SellCount++
		}
		f.TradesBySymbol[strings.ToUpper(r.Symbol)]++
	}
	return nil
}

func (c collector) research(ctx context.Context, userID uuid.UUID, w *window, f *Facts) error {
	touch := func(symbol string) {
		key := strings.ToUpper(strings.TrimSpace(symbol))
		if key != "" {
			f.ResearchTouchesBySymbol[key]++
		}
	}
	watchlist, err := c.q.RecapWatchlistSymbols(ctx, recapsdb.RecapWatchlistSymbolsParams{
		UserID: userID, StartAt: w.start, EndAt: w.end, Excluded: w.pilotWatchlists,
	})
	if err != nil {
		return err
	}
	f.WatchlistAdded = len(watchlist)
	for _, s := range watchlist {
		touch(s)
	}
	notes, err := c.q.RecapNoteSymbols(ctx, recapsdb.RecapNoteSymbolsParams{UserID: userID, StartAt: w.start, EndAt: w.end})
	if err != nil {
		return err
	}
	f.NotesWritten = len(notes)
	for _, s := range notes {
		touch(s)
	}
	targets, err := c.q.RecapTargets(ctx, recapsdb.RecapTargetsParams{UserID: userID, StartAt: w.start, EndAt: w.end})
	if err != nil {
		return err
	}
	inPeriod := func(t *time.Time) bool { return t != nil && !t.Before(w.start) && t.Before(w.end) }
	for i := range targets {
		t := &targets[i]
		if inPeriod(&t.CreatedAt) {
			f.TargetsSet++
			touch(t.Symbol)
		}
		if inPeriod(t.AlertTriggeredAt) {
			f.TargetsHit++
		}
	}
	memos, err := c.q.RecapMemoSymbols(ctx, recapsdb.RecapMemoSymbolsParams{UserID: userID, StartAt: &w.start, EndAt: &w.end})
	if err != nil {
		return err
	}
	for _, s := range memos {
		touch(s)
	}
	stocks, err := c.q.RecapStockSymbols(ctx, recapsdb.RecapStockSymbolsParams{UserID: userID, StartAt: w.start, EndAt: w.end})
	if err != nil {
		return err
	}
	for _, s := range stocks {
		touch(s)
	}
	return nil
}

// growth: per portfolio list and currency, the market value on the list's
// first and last snapshot day (lists with one day are skipped), summed per
// currency.
func (c collector) growth(ctx context.Context, userID uuid.UUID, w *window, f *Facts) error {
	rows, err := c.q.RecapSnapshots(ctx, recapsdb.RecapSnapshotsParams{
		UserID: userID, StartDay: w.startDay, EndDay: w.endDay, Excluded: w.pilotPortfolios,
	})
	if err != nil {
		return err
	}
	type listKey struct {
		list     uuid.UUID
		currency string
	}
	type span struct{ first, last *recapsdb.RecapSnapshotsRow }
	groups := map[listKey]*span{}
	var keys []listKey
	for i := range rows {
		r := &rows[i]
		k := listKey{r.PortfolioListID, r.Currency}
		g, ok := groups[k]
		if !ok {
			g = &span{first: r, last: r}
			groups[k] = g
			keys = append(keys, k)
			continue
		}
		if r.CapturedOn.Before(g.first.CapturedOn) {
			g.first = r
		}
		if r.CapturedOn.After(g.last.CapturedOn) {
			g.last = r
		}
	}
	type total struct{ start, end float64 }
	totals := map[string]*total{}
	for _, k := range keys {
		g := groups[k]
		if g.first.CapturedOn.Equal(g.last.CapturedOn) {
			continue
		}
		t, ok := totals[k.currency]
		if !ok {
			t = &total{}
			totals[k.currency] = t
		}
		t.start += g.first.MarketValue
		t.end += g.last.MarketValue
	}
	currencies := make([]string, 0, len(totals))
	for c := range totals {
		currencies = append(currencies, c)
	}
	sort.Strings(currencies)
	for _, cur := range currencies {
		f.Growth = append(f.Growth, Growth{Currency: cur, StartValue: money(totals[cur].start), EndValue: money(totals[cur].end)})
	}
	return nil
}

func (c collector) dividends(ctx context.Context, userID uuid.UUID, w *window, f *Facts) error {
	rows, err := c.q.RecapDividends(ctx, recapsdb.RecapDividendsParams{UserID: userID, StartDay: w.startDay, EndDay: w.endDay})
	if err != nil {
		return err
	}
	amounts := make([]amount, 0, len(rows))
	payers := map[uuid.UUID]bool{}
	for _, r := range rows {
		amounts = append(amounts, amount{r.Currency, r.Amount})
		payers[r.InstrumentID] = true
	}
	f.DividendsReceived = byCurrency(amounts)
	f.DividendPayments = len(rows)
	f.DividendPayers = len(payers)
	return nil
}

func (c collector) goals(ctx context.Context, userID uuid.UUID, w *window, f *Facts) error {
	rows, err := c.q.RecapGoalContributions(ctx, recapsdb.RecapGoalContributionsParams{UserID: userID, StartAt: w.start, EndAt: w.end})
	if err != nil {
		return err
	}
	amounts := make([]amount, 0, len(rows))
	for _, r := range rows {
		amounts = append(amounts, amount{r.BaseCurrency, r.Amount})
	}
	f.GoalContributions = byCurrency(amounts)
	f.GoalContributionCount = len(rows)
	return nil
}

func (c collector) gamification(ctx context.Context, userID uuid.UUID, w *window, f *Facts) error {
	badges, err := c.q.RecapBadges(ctx, userID)
	if err != nil {
		return err
	}
	f.TotalBadgesEver = len(badges)
	f.BadgesEarned = []Badge{}
	for _, b := range badges {
		if !b.EarnedAt.Before(w.start) && b.EarnedAt.Before(w.end) {
			f.BadgesEarned = append(f.BadgesEarned, Badge{Type: b.BadgeType, Tier: b.Tier})
		}
	}
	points, err := c.q.RecapXPPoints(ctx, recapsdb.RecapXPPointsParams{UserID: userID, StartAt: w.start, EndAt: w.end})
	if err != nil {
		return err
	}
	for _, p := range points {
		f.XPEarned += int(p)
	}
	return nil
}

func (c collector) social(ctx context.Context, userID uuid.UUID, w *window, f *Facts) error {
	follows, err := c.q.RecapPilotFollowCount(ctx, recapsdb.RecapPilotFollowCountParams{UserID: userID, StartAt: &w.start, EndAt: &w.end})
	if err != nil {
		return err
	}
	posts, err := c.q.RecapBoardPostCount(ctx, recapsdb.RecapBoardPostCountParams{UserID: userID, StartAt: w.start, EndAt: w.end})
	if err != nil {
		return err
	}
	comments, err := c.q.RecapBoardCommentCount(ctx, recapsdb.RecapBoardCommentCountParams{UserID: userID, StartAt: w.start, EndAt: w.end})
	if err != nil {
		return err
	}
	ai, err := c.q.RecapAIRequestCounts(ctx, recapsdb.RecapAIRequestCountsParams{UserID: userID, StartDay: w.startDay, EndDay: w.endDay})
	if err != nil {
		return err
	}
	expenses, err := c.q.RecapExpenseCount(ctx, recapsdb.RecapExpenseCountParams{UserID: userID, StartDay: w.startDay, EndDay: w.endDay})
	if err != nil {
		return err
	}
	f.PilotFollows, f.BoardPosts, f.BoardComments, f.ExpensesRecorded = int(follows), int(posts), int(comments), int(expenses)
	for _, n := range ai {
		f.AIRequests += int(n)
	}
	return nil
}
