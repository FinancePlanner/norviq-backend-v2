package news

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/swiftjson"
	"norviq-api/internal/store/newsdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// Thesis Watch raw values (StockPlanShared ThesisWatch* enums).
const (
	scopeForYou    = "for_you"
	scopeHoldings  = "holdings"
	scopeWatchlist = "watchlist"
	scopeSectors   = "sectors"
	scopeMarket    = "market"

	relHolding   = "holding"
	relWatchlist = "watchlist"
	relMarket    = "market"

	impactSupports             = "supports"
	impactChallenges           = "challenges"
	impactNeutral              = "neutral"
	impactInsufficientEvidence = "insufficient_evidence"
	impactNotAssessed          = "not_assessed"

	signalRelevant    = "relevant"
	signalNotRelevant = "not_relevant"
	signalClear       = "clear"

	severityLow    = "low"
	severityMedium = "medium"
	severityHigh   = "high"
)

var (
	validScopes     = []string{scopeForYou, scopeHoldings, scopeWatchlist, scopeSectors, scopeMarket}
	validSeverities = []string{severityLow, severityMedium, severityHigh}
	validEventTypes = []string{
		"earnings", "guidance", "regulation", "merger_acquisition", "management", "product",
		"capital_return", "analyst_action", "macro", "legal", "other",
	}
	validImpacts = []string{impactSupports, impactChallenges, impactNeutral, impactInsufficientEvidence, impactNotAssessed}
	validSignals = []string{signalRelevant, signalNotRelevant, impactSupports, impactChallenges, impactNeutral, signalClear}
)

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// Exposure is ThesisWatchExposure.
type Exposure struct {
	Currency      string  `json:"currency"`
	Value         float64 `json:"value"`
	WeightPercent float64 `json:"weightPercent"`
}

// Capabilities is ThesisWatchCapabilities.
type Capabilities struct {
	IsPro               bool `json:"isPro"`
	PersonalizedRanking bool `json:"personalizedRanking"`
	ThesisAnalysis      bool `json:"thesisAnalysis"`
	PushAlerts          bool `json:"pushAlerts"`
	MaxItems            int  `json:"maxItems"`
}

// Story is ThesisWatchStory.
type Story struct {
	ID              string    `json:"id"`
	Headline        string    `json:"headline"`
	Source          *string   `json:"source,omitempty"`
	URL             string    `json:"url"`
	ImageURL        *string   `json:"imageUrl,omitempty"`
	PublishedAt     string    `json:"publishedAt"`
	ProviderSummary *string   `json:"providerSummary,omitempty"`
	Summary         *string   `json:"summary,omitempty"`
	WhyItMatters    *string   `json:"whyItMatters,omitempty"`
	Symbols         []string  `json:"symbols"`
	Sectors         []string  `json:"sectors"`
	Relationship    string    `json:"relationship"`
	EventType       string    `json:"eventType"`
	Severity        string    `json:"severity"`
	Exposure        *Exposure `json:"exposure,omitempty"`
	ThesisImpact    string    `json:"thesisImpact"`
	Confidence      *float64  `json:"confidence,omitempty"`
	Feedback        *string   `json:"feedback,omitempty"`
	IsRead          bool      `json:"isRead"`
	IsDismissed     bool      `json:"isDismissed"`
}

// FeedResponse is ThesisWatchFeedResponse.
type FeedResponse struct {
	Items        []Story      `json:"items"`
	NextCursor   *string      `json:"nextCursor,omitempty"`
	GeneratedAt  string       `json:"generatedAt"`
	Capabilities Capabilities `json:"capabilities"`
}

// NotificationPreferences is ThesisWatchNotificationPreferences.
type NotificationPreferences struct {
	Enabled  bool   `json:"enabled"`
	Timezone string `json:"timezone"`
	Policy   string `json:"policy"`
	DailyCap int    `json:"dailyCap"`
}

func notificationPreferences(enabled bool, timezone string) NotificationPreferences {
	return NotificationPreferences{Enabled: enabled, Timezone: timezone, Policy: "thesis_challenges_and_major_events", DailyCap: 3}
}

// feedbackSignal is ThesisWatchFeedbackSignal in a request body.
type feedbackSignal string

// SwiftEnum implements swiftjson.Enum.
func (feedbackSignal) SwiftEnum() (string, []string, bool) {
	return "ThesisWatchFeedbackSignal", validSignals, false
}

type feedbackRequest struct {
	Signal feedbackSignal `json:"signal"`
}

type updatePreferencesRequest struct {
	Enabled  bool   `json:"enabled"`
	Timezone string `json:"timezone"`
}

type classRule struct {
	event string
	terms []string
}

var classRules = []classRule{
	{"guidance", []string{"guidance", "outlook", "forecast", "profit warning"}},
	{"earnings", []string{"earnings", "revenue", "quarter", "eps", "results"}},
	{"regulation", []string{"regulator", "regulation", "antitrust", "sec ", "fda", "eu commission"}},
	{"merger_acquisition", []string{"acquire", "acquisition", "merger", "takeover", "buyout"}},
	{"management", []string{"ceo", "cfo", "chairman", "executive", "resigns", "appointed"}},
	{"capital_return", []string{"dividend", "buyback", "repurchase", "capital return"}},
	{"legal", []string{"lawsuit", "court", "settlement", "probe", "investigation"}},
	{"analyst_action", []string{"upgrade", "downgrade", "price target", "analyst"}},
	{"product", []string{"launch", "product", "approval", "recall", "patent"}},
	{"macro", []string{"inflation", "interest rate", "fed ", "ecb ", "tariff", "recession"}},
}

var (
	highTerms   = []string{"bankruptcy", "fraud", "recall", "profit warning", "cuts guidance", "investigation", "takeover"}
	mediumTerms = []string{"earnings", "guidance", "dividend", "ceo", "merger", "lawsuit", "approval", "downgrade"}
)

func anyIn(text string, terms []string) bool {
	for _, t := range terms {
		if strings.Contains(text, t) {
			return true
		}
	}
	return false
}

// classify is ThesisWatchClassifier.classify.
func classify(headline string, summary *string) (event, severity string) {
	s := ""
	if summary != nil {
		s = *summary
	}
	text := strings.ToLower(headline + " " + s)
	event = "other"
	for _, r := range classRules {
		if anyIn(text, r.terms) {
			event = r.event
			break
		}
	}
	switch {
	case anyIn(text, highTerms):
		severity = severityHigh
	case anyIn(text, mediumTerms):
		severity = severityMedium
	default:
		severity = severityLow
	}
	return event, severity
}

// rank is ThesisWatchRanker.score.
func rank(relationship string, weightPercent float64, severity, impact string, publishedAt time.Time, feedback *string, now time.Time) float64 {
	rel := 0.0
	switch relationship {
	case relHolding:
		rel = 40
	case relWatchlist:
		rel = 15
	}
	ageHours := math.Max(0, now.Sub(publishedAt).Seconds()/3600)
	freshness := math.Max(0, 20*(1-ageHours/72))
	sev := 0.0
	switch severity {
	case severityHigh:
		sev = 15
	case severityMedium:
		sev = 7
	}
	imp := 0.0
	switch impact {
	case impactChallenges:
		imp = 10
	case impactSupports:
		imp = 4
	}
	fb := 0.0
	if feedback != nil && *feedback == signalRelevant {
		fb = 5
	}
	return rel + math.Min(math.Max(weightPercent, 0), 20) + freshness + sev + imp + fb
}

var (
	nonAlnumSpace = regexp.MustCompile(`[^a-z0-9 ]`)
	spaceRun      = regexp.MustCompile(`\s+`)
)

// clusterKey is makeClusterKey.
func clusterKey(publishedAt time.Time, headline string) string {
	day := int64(swiftUnixSeconds(publishedAt) / 86400)
	normalized := nonAlnumSpace.ReplaceAllString(strings.ToLower(headline), " ")
	normalized = strings.TrimSpace(spaceRun.ReplaceAllString(normalized, " "))
	if utf8.RuneCountInString(normalized) > 240 {
		normalized = string([]rune(normalized)[:240])
	}
	return strconv.FormatInt(day, 10) + ":" + normalized
}

func swiftUnixSeconds(t time.Time) float64 {
	return float64(t.Unix()) + float64(t.Nanosecond())/1e9
}

// RefreshClusters is ThesisWatchService.refreshClusters: assigns every
// unclustered archive article from the last 30 days to a story.
func (s *Service) RefreshClusters(ctx context.Context) error {
	since := s.now().Add(-30 * 86400 * time.Second)
	articles, err := s.q.UnclusteredArchiveSince(ctx, since)
	if err != nil {
		return err
	}
	for i := range articles {
		a := &articles[i]
		key := clusterKey(a.PublishedAt, a.Headline)
		var storyID uuid.UUID
		existing, err := s.q.GetThesisStoryByClusterKey(ctx, key)
		switch {
		case err == nil:
			storyID = existing.ID
			if a.PublishedAt.After(existing.LastSeenAt) {
				event, severity := classify(a.Headline, a.Summary)
				now := s.now()
				if err := s.q.UpdateThesisStorySeen(ctx, newsdb.UpdateThesisStorySeenParams{
					LastSeenAt: a.PublishedAt, RepresentativeNewsID: a.ID, EventType: event, Severity: severity,
					Now: &now, ID: existing.ID,
				}); err != nil {
					return err
				}
			}
		case errors.Is(err, pgx.ErrNoRows):
			event, severity := classify(a.Headline, a.Summary)
			now := s.now()
			candidate := uuid.New()
			insertErr := s.q.InsertThesisStory(ctx, newsdb.InsertThesisStoryParams{
				ID: candidate, ClusterKey: key, RepresentativeNewsID: a.ID, EventType: event, Severity: severity,
				FirstSeenAt: a.PublishedAt, LastSeenAt: a.PublishedAt, Now: &now,
			})
			if insertErr != nil {
				concurrent, err := s.q.GetThesisStoryByClusterKey(ctx, key)
				if err != nil {
					return insertErr
				}
				candidate = concurrent.ID
			}
			storyID = candidate
		default:
			return err
		}
		now := s.now()
		if err := s.q.SetArchiveStory(ctx, newsdb.SetArchiveStoryParams{StoryID: &storyID, Now: &now, ID: a.ID}); err != nil {
			return err
		}
	}
	return nil
}

// Feed is ThesisWatchService.feed.
func (s *Service) ThesisFeed(ctx context.Context, userID uuid.UUID, scope string, requestedSector *string, limit int, cursor *string) (FeedResponse, error) {
	if err := s.RefreshClusters(ctx); err != nil {
		return FeedResponse{}, err
	}
	isPro, err := s.pro.IsPro(ctx, userID)
	if err != nil {
		return FeedResponse{}, err
	}
	maxItems := 10
	if isPro {
		maxItems = 50
	}
	effectiveLimit := min(max(limit, 1), maxItems)
	offset := 0
	if cursor != nil {
		if n, err := strconv.Atoi(*cursor); err == nil {
			offset = max(n, 0)
		}
	}

	stocks, err := s.q.UserStocksForThesis(ctx, userID)
	if err != nil {
		return FeedResponse{}, err
	}
	holdings := map[string]float64{}
	for _, st := range stocks {
		sym := strings.ToUpper(st.Symbol)
		holdings[sym] += math.Max(0, st.Shares*st.BuyPrice)
	}
	totalValue := 0.0
	for _, v := range holdings {
		totalValue += v
	}
	watchRows, err := s.q.ActiveWatchlistSymbols(ctx, userID)
	if err != nil {
		return FeedResponse{}, err
	}
	watchlist := map[string]bool{}
	for _, w := range watchRows {
		watchlist[strings.ToUpper(w)] = true
	}
	tracked := map[string]bool{}
	for k := range holdings {
		tracked[k] = true
	}
	for k := range watchlist {
		tracked[k] = true
	}
	trackedList := make([]string, 0, len(tracked))
	for k := range tracked {
		trackedList = append(trackedList, k)
	}
	profiles, err := s.q.ProfileIndustries(ctx, trackedList)
	if err != nil {
		return FeedResponse{}, err
	}
	// Dictionary(uniqueKeysWithValues:) trapped on a symbol cached by two
	// providers; v2 keeps the last row instead of crashing.
	sectorsBySymbol := map[string]string{}
	for _, p := range profiles {
		if p.FinnhubIndustry != nil {
			sectorsBySymbol[strings.ToUpper(p.Symbol)] = *p.FinnhubIndustry
		}
	}
	notes, err := s.q.UserResearchNotes(ctx, userID)
	if err != nil {
		return FeedResponse{}, err
	}
	notesBySymbol := map[string][]newsdb.ResearchNote{}
	for i := range notes {
		k := strings.ToUpper(notes[i].Symbol)
		notesBySymbol[k] = append(notesBySymbol[k], notes[i])
	}

	now := s.now()
	articles, err := s.q.ArchiveSince(ctx, now.Add(-30*86400*time.Second))
	if err != nil {
		return FeedResponse{}, err
	}
	relevant := []newsdb.MarketNewsArchive{}
	for i := range articles {
		if articles[i].Symbol == "GENERAL" || tracked[strings.ToUpper(articles[i].Symbol)] {
			relevant = append(relevant, articles[i])
		}
	}
	storyIDSet := map[uuid.UUID]bool{}
	for i := range relevant {
		if relevant[i].StoryID != nil {
			storyIDSet[*relevant[i].StoryID] = true
		}
	}
	storyIDs := make([]uuid.UUID, 0, len(storyIDSet))
	for id := range storyIDSet {
		storyIDs = append(storyIDs, id)
	}
	stories, err := s.q.ListThesisStories(ctx, storyIDs)
	if err != nil {
		return FeedResponse{}, err
	}
	storiesByID := map[uuid.UUID]newsdb.ThesisWatchStory{}
	for i := range stories {
		storiesByID[stories[i].ID] = stories[i]
	}
	states, err := s.q.ListThesisUserStates(ctx, newsdb.ListThesisUserStatesParams{UserID: userID, StoryIds: storyIDs})
	if err != nil {
		return FeedResponse{}, err
	}
	statesByStory := map[uuid.UUID]*newsdb.ThesisWatchUserState{}
	for i := range states {
		statesByStory[states[i].StoryID] = &states[i]
	}

	// Dictionary(grouping:by: \.storyId), keeping the archive order inside
	// each group (newest first).
	grouped := map[uuid.UUID][]newsdb.MarketNewsArchive{}
	order := []uuid.UUID{}
	for i := range relevant {
		a := &relevant[i]
		if a.StoryID == nil {
			continue
		}
		if _, ok := grouped[*a.StoryID]; !ok {
			order = append(order, *a.StoryID)
		}
		grouped[*a.StoryID] = append(grouped[*a.StoryID], *a)
	}

	type ranked struct {
		score float64
		story Story
	}
	out := []ranked{}
	for _, storyID := range order {
		cluster := grouped[storyID]
		model, ok := storiesByID[storyID]
		if !ok {
			continue
		}
		rep := &cluster[0]
		for i := range cluster {
			if rep.PublishedAt.Before(cluster[i].PublishedAt) {
				rep = &cluster[i]
			}
		}
		if rep.Url == nil || !contains(validEventTypes, model.EventType) || !contains(validSeverities, model.Severity) {
			continue
		}
		symSet := map[string]bool{}
		symbols := []string{}
		for i := range cluster {
			u := strings.ToUpper(cluster[i].Symbol)
			if u != "GENERAL" && !symSet[u] {
				symSet[u] = true
				symbols = append(symbols, u)
			}
		}
		sort.Strings(symbols)
		var holdingSymbol *string
		for i := range symbols {
			if holdingSymbol == nil || holdings[*holdingSymbol] < holdings[symbols[i]] {
				holdingSymbol = &symbols[i]
			}
		}
		relationship := relMarket
		hasHolding, hasWatch := false, false
		for _, sym := range symbols {
			if _, ok := holdings[sym]; ok {
				hasHolding = true
			}
			if watchlist[sym] {
				hasWatch = true
			}
		}
		if hasHolding {
			relationship = relHolding
		} else if hasWatch {
			relationship = relWatchlist
		}
		value := 0.0
		for _, sym := range symbols {
			value += holdings[sym]
		}
		weight := 0.0
		if totalValue > 0 {
			weight = value / totalValue * 100
		}
		sectorSet := map[string]bool{}
		sectors := []string{}
		for _, sym := range symbols {
			if sec, ok := sectorsBySymbol[sym]; ok && !sectorSet[sec] {
				sectorSet[sec] = true
				sectors = append(sectors, sec)
			}
		}
		sort.Strings(sectors)
		if !includes(scope, relationship, sectors, requestedSector) {
			continue
		}

		state := statesByStory[storyID]
		if isPro && state == nil && holdingSymbol != nil {
			candidate := makeAnalysisState(userID, storyID, *holdingSymbol, rep.Headline, rep.Summary, weight, notesBySymbol[*holdingSymbol])
			if candidate != nil {
				if err := s.insertState(ctx, candidate); err != nil {
					existing, qerr := s.q.GetThesisUserState(ctx, newsdb.GetThesisUserStateParams{UserID: userID, StoryID: storyID})
					if qerr != nil {
						return FeedResponse{}, err
					}
					candidate = &existing
				}
				state = candidate
				statesByStory[storyID] = state
			}
		}
		if state != nil && (state.DismissedAt != nil || (state.Feedback != nil && *state.Feedback == signalNotRelevant)) {
			continue
		}
		impact := impactNotAssessed
		if state != nil && contains(validImpacts, state.Impact) {
			impact = state.Impact
		}
		var feedback *string
		if state != nil && state.Feedback != nil && contains(validSignals, *state.Feedback) {
			feedback = state.Feedback
		}
		var exposure *Exposure
		if isPro && value > 0 {
			exposure = &Exposure{Currency: "USD", Value: value, WeightPercent: weight}
		}
		story := Story{
			ID:              strings.ToUpper(storyID.String()),
			Headline:        rep.Headline,
			Source:          rep.Source,
			URL:             *rep.Url,
			ImageURL:        rep.ImageUrl,
			PublishedAt:     formatISOMillis(rep.PublishedAt),
			ProviderSummary: rep.Summary,
			Symbols:         symbols,
			Sectors:         sectors,
			Relationship:    relationship,
			EventType:       model.EventType,
			Severity:        model.Severity,
			Exposure:        exposure,
			ThesisImpact:    impactNotAssessed,
			Feedback:        feedback,
			IsRead:          state != nil && state.ReadAt != nil,
		}
		if isPro {
			story.ThesisImpact = impact
			if state != nil {
				story.Summary, story.WhyItMatters, story.Confidence = state.Summary, state.WhyItMatters, state.Confidence
			}
		}
		score := swiftUnixSeconds(rep.PublishedAt)
		if isPro {
			score = rank(relationship, weight, model.Severity, impact, rep.PublishedAt, feedback, s.now())
		}
		out = append(out, ranked{score: score, story: story})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score == out[j].score {
			return out[i].story.PublishedAt > out[j].story.PublishedAt
		}
		return out[i].score > out[j].score
	})
	page := []Story{}
	for i := offset; i < len(out) && len(page) < effectiveLimit; i++ {
		page = append(page, out[i].story)
	}
	var next *string
	if nextOffset := offset + len(page); nextOffset < len(out) {
		v := strconv.Itoa(nextOffset)
		next = &v
	}
	return FeedResponse{
		Items:       page,
		NextCursor:  next,
		GeneratedAt: formatISOMillis(s.now()),
		Capabilities: Capabilities{
			IsPro: isPro, PersonalizedRanking: isPro, ThesisAnalysis: isPro, PushAlerts: isPro, MaxItems: maxItems,
		},
	}, nil
}

func includes(scope, relationship string, sectors []string, requested *string) bool {
	switch scope {
	case scopeForYou:
		return relationship != relMarket
	case scopeHoldings:
		return relationship == relHolding
	case scopeWatchlist:
		return relationship == relWatchlist
	case scopeSectors:
		if requested == nil || *requested == "" {
			return len(sectors) > 0
		}
		for _, sec := range sectors {
			if strings.EqualFold(sec, *requested) {
				return true
			}
		}
		return false
	default:
		return true
	}
}

var ignoredTokens = map[string]bool{
	"about": true, "after": true, "again": true, "could": true, "from": true, "have": true, "into": true,
	"more": true, "that": true, "their": true, "there": true, "these": true, "this": true, "with": true, "would": true,
}

// overlapScore counts distinct 4+ letter note tokens found in the article.
func overlapScore(articleText string, note *string) int {
	if note == nil {
		return 0
	}
	tokens := strings.FieldsFunc(strings.ToLower(*note), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	seen := map[string]bool{}
	n := 0
	for _, t := range tokens {
		if utf8.RuneCountInString(t) < 4 || ignoredTokens[t] || seen[t] {
			continue
		}
		seen[t] = true
		if strings.Contains(articleText, t) {
			n++
		}
	}
	return n
}

func conciseSummary(headline string, providerSummary *string) string {
	if providerSummary == nil {
		return headline
	}
	t := strings.TrimSpace(*providerSummary)
	if t == "" {
		return headline
	}
	if utf8.RuneCountInString(t) > 320 {
		return string([]rune(t)[:320])
	}
	return t
}

// makeAnalysisState is the deterministic thesis analysis of a story against
// the user's most recently updated research note on symbol.
func makeAnalysisState(userID, storyID uuid.UUID, symbol, headline string, providerSummary *string, exposureWeight float64, notes []newsdb.ResearchNote) *newsdb.ThesisWatchUserState {
	if len(notes) == 0 {
		return nil
	}
	note := notes[0]
	updated := func(n *newsdb.ResearchNote) time.Time {
		if n.UpdatedAt == nil {
			return time.Time{}
		}
		return *n.UpdatedAt
	}
	for i := range notes {
		if updated(&note).Before(updated(&notes[i])) {
			note = notes[i]
		}
	}
	summary := ""
	if providerSummary != nil {
		summary = *providerSummary
	}
	articleText := strings.ToLower(headline + " " + summary)
	risk := overlapScore(articleText, note.Risks)
	catalyst := overlapScore(articleText, note.Catalysts)
	var impact string
	var confidence float64
	switch {
	case risk > catalyst && risk > 0:
		impact, confidence = impactChallenges, math.Min(0.9, 0.55+float64(risk)*0.08)
	case catalyst > 0:
		impact, confidence = impactSupports, math.Min(0.9, 0.55+float64(catalyst)*0.08)
	default:
		impact, confidence = impactInsufficientEvidence, 0.35
	}
	exposureText := "your watchlist"
	if exposureWeight > 0 {
		exposureText = fmt.Sprintf("%.1f%% of your recorded cost basis", exposureWeight)
	}
	var why string
	switch impact {
	case impactChallenges:
		why = "This overlaps a risk in your " + symbol + " thesis and touches " + exposureText + ". Review whether the original risk limit still holds."
	case impactSupports:
		why = "This overlaps a catalyst in your " + symbol + " thesis and touches " + exposureText + ". Check whether the evidence changes your expected timing."
	default:
		why = "This affects " + symbol + ", which represents " + exposureText + ". The available article evidence is not strong enough to change your thesis."
	}
	sym, conc := symbol, conciseSummary(headline, providerSummary)
	return &newsdb.ThesisWatchUserState{
		ID: uuid.New(), UserID: userID, StoryID: storyID, Symbol: &sym, Impact: impact,
		Confidence: &confidence, Summary: &conc, WhyItMatters: &why,
	}
}

func (s *Service) insertState(ctx context.Context, st *newsdb.ThesisWatchUserState) error {
	now := s.now()
	st.CreatedAt, st.UpdatedAt = &now, &now
	return s.q.InsertThesisUserState(ctx, newsdb.InsertThesisUserStateParams{
		ID: st.ID, UserID: st.UserID, StoryID: st.StoryID, Symbol: st.Symbol, Impact: st.Impact,
		Confidence: st.Confidence, Summary: st.Summary, WhyItMatters: st.WhyItMatters,
		Feedback: st.Feedback, ReadAt: st.ReadAt, DismissedAt: st.DismissedAt, Now: &now,
	})
}

// ThesisStory is ThesisWatchService.story: the story as the for-you feed
// (first 50) presents it.
func (s *Service) ThesisStory(ctx context.Context, id, userID uuid.UUID) (Story, error) {
	feed, err := s.ThesisFeed(ctx, userID, scopeForYou, nil, 50, nil)
	if err != nil {
		return Story{}, err
	}
	for i := range feed.Items {
		if strings.EqualFold(feed.Items[i].ID, id.String()) {
			return feed.Items[i], nil
		}
	}
	return Story{}, httpx.NotFound("Thesis Watch story not found.")
}

// state loads the user's state for story id, or a new unsaved one.
func (s *Service) state(ctx context.Context, id, userID uuid.UUID) (*newsdb.ThesisWatchUserState, bool, error) {
	exists, err := s.q.ThesisStoryExists(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if !exists {
		return nil, false, httpx.NotFound("Thesis Watch story not found.")
	}
	st, err := s.q.GetThesisUserState(ctx, newsdb.GetThesisUserStateParams{UserID: userID, StoryID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return &newsdb.ThesisWatchUserState{ID: uuid.New(), UserID: userID, StoryID: id, Impact: impactNotAssessed}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &st, true, nil
}

func (s *Service) saveState(ctx context.Context, st *newsdb.ThesisWatchUserState, existing bool) error {
	if !existing {
		return s.insertState(ctx, st)
	}
	now := s.now()
	return s.q.UpdateThesisUserState(ctx, newsdb.UpdateThesisUserStateParams{
		Impact: st.Impact, Feedback: st.Feedback, ReadAt: st.ReadAt, DismissedAt: st.DismissedAt, Now: &now, ID: st.ID,
	})
}

// ThesisFeedback is ThesisWatchService.feedback.
func (s *Service) ThesisFeedback(ctx context.Context, id, userID uuid.UUID, signal string) error {
	st, existing, err := s.state(ctx, id, userID)
	if err != nil {
		return err
	}
	if signal == signalClear {
		st.Feedback, st.DismissedAt = nil, nil
	} else {
		sig := signal
		st.Feedback = &sig
		st.DismissedAt = nil
		if signal == signalNotRelevant {
			now := s.now()
			st.DismissedAt = &now
		}
		if signal == impactSupports || signal == impactChallenges || signal == impactNeutral {
			st.Impact = signal
		}
	}
	return s.saveState(ctx, st, existing)
}

// ThesisMarkRead is ThesisWatchService.markRead.
func (s *Service) ThesisMarkRead(ctx context.Context, id, userID uuid.UUID) error {
	st, existing, err := s.state(ctx, id, userID)
	if err != nil {
		return err
	}
	now := s.now()
	st.ReadAt = &now
	return s.saveState(ctx, st, existing)
}

// ThesisNotificationPreferences is notificationPreferences(userId:).
func (s *Service) ThesisNotificationPreferences(ctx context.Context, userID uuid.UUID) (NotificationPreferences, error) {
	p, err := s.q.GetThesisNotificationPreference(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return notificationPreferences(false, "UTC"), nil
	}
	if err != nil {
		return NotificationPreferences{}, err
	}
	return notificationPreferences(p.Enabled, p.Timezone), nil
}

// validTimeZone is TimeZone(identifier:) != nil.
func validTimeZone(id string) bool {
	if id == "" || id == "Local" {
		return false
	}
	_, err := time.LoadLocation(id)
	return err == nil
}

// UpdateThesisNotificationPreferences is updateNotificationPreferences.
func (s *Service) UpdateThesisNotificationPreferences(ctx context.Context, userID uuid.UUID, enabled bool, rawTZ string) (NotificationPreferences, error) {
	tz := strings.TrimSpace(rawTZ)
	if !validTimeZone(tz) {
		return NotificationPreferences{}, httpx.BadRequest("Invalid timezone identifier.")
	}
	isPro, err := s.pro.IsPro(ctx, userID)
	if err != nil {
		return NotificationPreferences{}, err
	}
	if enabled && !isPro {
		return NotificationPreferences{}, httpx.Abort(http.StatusPaymentRequired, "Thesis Watch push alerts require Pro.")
	}
	now := s.now()
	p, err := s.q.GetThesisNotificationPreference(ctx, userID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		err = s.q.InsertThesisNotificationPreference(ctx, newsdb.InsertThesisNotificationPreferenceParams{
			ID: uuid.New(), UserID: userID, Enabled: enabled, Timezone: tz, Now: &now,
		})
	case err == nil:
		err = s.q.UpdateThesisNotificationPreference(ctx, newsdb.UpdateThesisNotificationPreferenceParams{
			Enabled: enabled, Timezone: tz, Now: &now, ID: p.ID,
		})
	}
	if err != nil {
		return NotificationPreferences{}, err
	}
	return notificationPreferences(enabled, tz), nil
}

// GET /v1/news/thesis-watch?scope=&sector=&limit=&cursor=
func (h *handlers) thesisWatchFeed(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	scope := scopeForYou
	if raw := httpx.QueryString(c, "scope"); raw != nil {
		scope = *raw
	}
	if !contains(validScopes, scope) {
		return httpx.BadRequest("Invalid Thesis Watch scope.")
	}
	limit := clampedLimit(httpx.QueryOptionalInt(c, "limit"), 20, 50)
	resp, err := h.svc.ThesisFeed(c.Request().Context(), s.UserID, scope, httpx.QueryString(c, "sector"), limit, httpx.QueryString(c, "cursor"))
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

// GET /v1/news/thesis-watch/:storyId
func (h *handlers) thesisWatchStory(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := pathUUID(c, "storyId", "Invalid story ID")
	if err != nil {
		return err
	}
	story, err := h.svc.ThesisStory(c.Request().Context(), id, s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, story)
}

// POST /v1/news/thesis-watch/:storyId/feedback
func (h *handlers) thesisWatchFeedback(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := pathUUID(c, "storyId", "Invalid story ID")
	if err != nil {
		return err
	}
	var p feedbackRequest
	if err := swiftjson.DecodeRequest(c, &p); err != nil {
		return err
	}
	if err := h.svc.ThesisFeedback(c.Request().Context(), id, s.UserID, string(p.Signal)); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

// POST /v1/news/thesis-watch/:storyId/view
func (h *handlers) markThesisWatchStoryRead(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := pathUUID(c, "storyId", "Invalid story ID")
	if err != nil {
		return err
	}
	if err := h.svc.ThesisMarkRead(c.Request().Context(), id, s.UserID); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

// GET /v1/news/thesis-watch/notifications
func (h *handlers) thesisNotificationPreferences(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	resp, err := h.svc.ThesisNotificationPreferences(c.Request().Context(), s.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}

// PUT /v1/news/thesis-watch/notifications
func (h *handlers) updateThesisNotificationPreferences(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var p updatePreferencesRequest
	if err := swiftjson.DecodeRequest(c, &p); err != nil {
		return err
	}
	resp, err := h.svc.UpdateThesisNotificationPreferences(c.Request().Context(), s.UserID, p.Enabled, p.Timezone)
	if err != nil {
		return err
	}
	return httpx.OK(c, resp)
}
