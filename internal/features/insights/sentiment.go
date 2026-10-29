package insights

import (
	"math"
	"strings"
	"time"
	"unicode"
)

// SentimentSource raw values, persisted in ticker_sentiment_posts.source.
const (
	SourceX            = "x"
	SourceReddit       = "reddit"
	SourceStocktwits   = "stocktwits"
	SourceNews         = "news"
	SourceInvesting    = "investing"
	SourceSeekingAlpha = "seeking_alpha"
)

var (
	// allSources is SentimentSource.allCases.
	allSources = []string{SourceX, SourceReddit, SourceStocktwits, SourceNews, SourceInvesting, SourceSeekingAlpha}
	// inexpensiveSources is SentimentSource.inexpensive.
	inexpensiveSources = []string{SourceNews, SourceStocktwits, SourceReddit}
)

// resolvedSource is TickerSentimentPost.resolvedSource: unknown or nil is x.
func resolvedSource(raw *string) string {
	if raw != nil {
		for _, s := range allSources {
			if s == *raw {
				return s
			}
		}
	}
	return SourceX
}

// SentimentTier raw values (sentiment_universe_symbols.tier).
const (
	TierUser     = "user"
	TierSP500    = "sp500"
	TierTrending = "trending"
)

// tiersByPriority is SentimentTier.allCases sorted by priority.
var tiersByPriority = []string{TierUser, TierTrending, TierSP500}

func tierPriority(tier string) int {
	switch tier {
	case TierUser:
		return 0
	case TierTrending:
		return 1
	case TierSP500:
		return 2
	}
	return math.MaxInt
}

func tierSources(tier string) []string {
	if tier == TierUser {
		return allSources
	}
	return inexpensiveSources
}

// resolvedTier is SentimentUniverseSymbol.resolvedTier: unknown is trending.
func resolvedTier(raw string) string {
	switch raw {
	case TierUser, TierSP500, TierTrending:
		return raw
	}
	return TierTrending
}

// Classifier labels (SentimentClassifier).
const (
	positiveLabel  = "positive"
	neutralLabel   = "neutral"
	negativeLabel  = "negative"
	labelThreshold = 0.15
	saturation     = 2.0
	negationWindow = 3
)

// classifierResult is SentimentClassifier.Result.
type classifierResult struct {
	Score      *float64
	Label      string
	Confidence float64
	MatchCount int
}

// classify is SentimentClassifier.classify: a deterministic lexicon scorer
// with word-boundary matching, negation and evidence-based confidence.
func classify(text string) classifierResult {
	lowered := strings.ToLower(text)
	tokens := tokenize(lowered)

	sum := 0.0
	matches := 0
	for _, p := range phraseLexicon {
		if strings.Contains(lowered, p.phrase) {
			sum += p.weight
			matches++
		}
	}
	for i, token := range tokens {
		weight, ok := wordLexicon[token]
		if !ok {
			continue
		}
		matches++
		if isNegated(tokens, i) {
			sum += -weight * 0.8
		} else {
			sum += weight
		}
	}
	if matches == 0 {
		return classifierResult{Label: neutralLabel}
	}
	score := sum / (math.Abs(sum) + saturation)
	evidence := math.Min(1.0, float64(matches)/4.0)
	return classifierResult{
		Score:      &score,
		Label:      labelForScore(score),
		Confidence: math.Min(1.0, evidence*(0.5+0.5*math.Abs(score))),
		MatchCount: matches,
	}
}

// labelForScore is SentimentClassifier.label(forScore:).
func labelForScore(score float64) string {
	if score > labelThreshold {
		return positiveLabel
	}
	if score < -labelThreshold {
		return negativeLabel
	}
	return neutralLabel
}

func isNegated(tokens []string, termIndex int) bool {
	lower := max(0, termIndex-negationWindow)
	for _, t := range tokens[lower:termIndex] {
		if _, ok := negators[t]; ok {
			return true
		}
	}
	return false
}

// tokenize splits on anything that is not a letter, digit or apostrophe.
func tokenize(lowercased string) []string {
	var tokens []string
	var cur strings.Builder
	for _, r := range lowercased {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '\'' {
			cur.WriteRune(r)
		} else if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}
	return tokens
}

var negators = func() map[string]struct{} {
	m := map[string]struct{}{}
	for _, w := range []string{
		"not", "no", "never", "isn't", "isnt", "aren't", "arent", "wasn't", "wasnt",
		"won't", "wont", "don't", "dont", "doesn't", "doesnt", "didn't", "didnt",
		"can't", "cant", "cannot", "nothing", "hardly", "barely", "without", "avoid",
	} {
		m[w] = struct{}{}
	}
	return m
}()

var wordLexicon = func() map[string]float64 {
	lex := map[string]float64{}
	set := func(weight float64, terms ...string) {
		for _, t := range terms {
			lex[t] = weight
		}
	}
	set(1.0,
		"bullish", "moon", "mooning", "moass", "squeeze", "rally", "rallying",
		"soar", "soared", "soaring", "surge", "surged", "surging", "breakout",
		"outperform", "outperformed", "undervalued", "oversold", "accumulate",
		"accumulating", "longing", "bagholding")
	set(0.5,
		"up", "buy", "buying", "bought", "long", "calls", "green", "gain", "gains",
		"gained", "rise", "rising", "rose", "jump", "jumped", "climb", "climbed",
		"strong", "beat", "beats", "exceeded", "optimistic", "positive", "growth",
		"profit", "profits", "upside", "support", "hold", "holding", "conviction",
		"rip", "ripping", "printing", "tendies", "yolo")
	set(-1.0,
		"bearish", "crash", "crashed", "crashing", "plunge", "plunged", "plunging",
		"tumble", "tumbled", "collapse", "collapsed", "bagholder", "overvalued",
		"overbought", "breakdown", "underperform", "underperformed", "bubble",
		"fraud", "scam", "halted", "delisted", "bankruptcy", "dilution")
	set(-0.5,
		"down", "sell", "selling", "sold", "short", "shorting", "puts", "red", "fall",
		"falling", "fell", "drop", "dropped", "dropping", "decline", "declining",
		"slump", "weak", "weakness", "miss", "missed", "pessimistic", "negative",
		"loss", "losses", "recession", "correction", "downside", "resistance",
		"dump", "dumping", "dumped", "rekt", "cooked")
	return lex
}()

type weightedPhrase struct {
	phrase string
	weight float64
}

// phraseLexicon is matched as substrings (phrases and emoji).
var phraseLexicon = []weightedPhrase{
	{"diamond hands", 1.0},
	{"to the moon", 1.0},
	{"buy the dip", 0.8},
	{"record high", 0.8},
	{"all time high", 0.8},
	{"all-time high", 0.8},
	{"loading up", 0.6},
	{"paper hands", -0.8},
	{"dead cat bounce", -0.8},
	{"bag holder", -0.8},
	{"catching a falling knife", -1.0},
	{"record low", -0.8},
	{"profit taking", -0.4},
	{"🚀", 1.0},
	{"📈", 0.8},
	{"💎", 0.8},
	{"🙌", 0.4},
	{"🌙", 0.6},
	{"🐂", 0.8},
	{"📉", -0.8},
	{"🐻", -1.0},
	{"💀", -0.8},
	{"🚮", -0.8},
	{"🧻", -0.6},
}

// scored is SentimentScoring.Scored.
type scored struct {
	Score      *float64
	Label      string
	Confidence *float64
}

// ingestedPost is IngestedPost: one post on its way into
// ticker_sentiment_posts, before scoring.
type ingestedPost struct {
	DedupeKey          string
	Symbol             string
	Author             *string
	AuthorHandle       *string
	Text               string
	URL                *string
	PostedAt           time.Time
	Source             string
	ProvidedScore      *float64
	ProvidedLabel      *string
	ProvidedConfidence *float64
}

// symbolPostBatch is SymbolPostBatch.
type symbolPostBatch struct {
	Symbol string
	Posts  []ingestedPost
}

// scorePost is SentimentScoring.score: trust an upstream score, otherwise
// run the lexicon.
func scorePost(p ingestedPost) scored {
	if p.ProvidedScore != nil {
		label := labelForScore(*p.ProvidedScore)
		if p.ProvidedLabel != nil {
			label = *p.ProvidedLabel
		}
		return scored{Score: p.ProvidedScore, Label: label, Confidence: p.ProvidedConfidence}
	}
	r := classify(p.Text)
	c := r.Confidence
	return scored{Score: r.Score, Label: r.Label, Confidence: &c}
}

// aggregateInput is one (scored, source) pair.
type aggregateInput struct {
	Scored scored
	Source string
}

// dailyAggregate is SentimentAggregator.DailyAggregate.
type dailyAggregate struct {
	Score         *float64
	Label         string
	Confidence    *float64
	PostCount     int
	PositiveCount int
	NeutralCount  int
	NegativeCount int
	SourceCounts  SourceCounts
}

// aggregate is SentimentAggregator.aggregate: confidence-weighted mean of
// the per-post scores; nil score when nothing was scoreable.
func aggregate(posts []aggregateInput) dailyAggregate {
	var counts SourceCounts
	positive, neutral, negative := 0, 0, 0
	weightedSum, weightTotal, confidenceSum := 0.0, 0.0, 0.0
	scoredCount := 0
	for _, p := range posts {
		counts.add(p.Source)
		switch p.Scored.Label {
		case positiveLabel:
			positive++
		case negativeLabel:
			negative++
		default:
			neutral++
		}
		if p.Scored.Score == nil {
			continue
		}
		conf := 0.5
		if p.Scored.Confidence != nil {
			conf = *p.Scored.Confidence
		}
		weight := math.Max(0.1, conf)
		weightedSum += *p.Scored.Score * weight
		weightTotal += weight
		confidenceSum += conf
		scoredCount++
	}
	out := dailyAggregate{
		Label:         neutralLabel,
		PostCount:     len(posts),
		PositiveCount: positive,
		NeutralCount:  neutral,
		NegativeCount: negative,
		SourceCounts:  counts,
	}
	if scoredCount == 0 || weightTotal <= 0 {
		return out
	}
	score := weightedSum / weightTotal
	sampleFactor := math.Min(1.0, float64(scoredCount)/10.0)
	confidence := (confidenceSum / float64(scoredCount)) * sampleFactor
	out.Score = &score
	out.Label = labelForScore(score)
	out.Confidence = &confidence
	return out
}

const minimumBaselineDays = 5

// volumeZScore is SentimentAggregator.volumeZScore.
func volumeZScore(todayCount int, baseline []int) *float64 {
	if len(baseline) < minimumBaselineDays {
		return nil
	}
	total := 0
	for _, b := range baseline {
		total += b
	}
	mean := float64(total) / float64(len(baseline))
	variance := 0.0
	for _, b := range baseline {
		variance += math.Pow(float64(b)-mean, 2)
	}
	variance /= float64(len(baseline))
	stdDev := math.Sqrt(variance)
	if stdDev <= 0.5 {
		if mean <= 0 {
			if todayCount > 0 {
				one := 1.0
				return &one
			}
			return nil
		}
		z := (float64(todayCount) - mean) / math.Max(1.0, mean)
		return &z
	}
	z := (float64(todayCount) - mean) / stdDev
	return &z
}

// delta is SentimentAggregator.delta.
func delta(today, yesterday *float64) *float64 {
	if today == nil || yesterday == nil {
		return nil
	}
	d := *today - *yesterday
	return &d
}
