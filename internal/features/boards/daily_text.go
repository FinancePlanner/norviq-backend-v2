package boards

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The text side of DailyBoardPost.swift (DailyBoardStyle, DailyBoardValidator,
// OpenAIDailyBoardWriter.parse) and the two MarketBrief helpers it calls
// (MarketBriefNumbers, MarketBriefValidator.isGrounded/tickers). The market
// brief itself is not ported here; these are private copies of its pure
// helpers.

// MARK: - MarketBriefNumbers

// briefToken is MarketBriefNumbers.Token.
type briefToken struct {
	// readings is every value the token could mean.
	readings []float64
	// tolerance is how far a reading may sit from a fact and still match.
	tolerance float64
}

func isBriefDigit(r rune) bool { return unicode.IsDigit(r) }

func isBriefGroupSeparator(r rune) bool { return r == ' ' || r == ' ' || r == ' ' }

// briefNumberTokens is MarketBriefNumbers.tokens(in:). Swift matched
//
//	(?<currency>[$€£])?(?<number>\d{1,3}(?:[   ]\d{3})+(?!\d)|\d+(?:[.,]\d+)+|\d+)(?<percent>\s?%)?
//
// which RE2 cannot express (the lookahead), so this scans the same grammar
// by hand, with the same leftmost, first-alternative-wins semantics.
func briefNumberTokens(text string) []briefToken {
	rs := []rune(text)
	out := []briefToken{}
	for i := 0; i < len(rs); {
		start, currency := i, false
		end := -1
		if rs[i] == '$' || rs[i] == '€' || rs[i] == '£' {
			if e := briefNumberAt(rs, i+1); e > 0 {
				start, currency, end = i+1, true, e
			}
		}
		if end < 0 {
			end = briefNumberAt(rs, i)
		}
		if end < 0 {
			i++
			continue
		}
		raw := string(rs[start:end])
		percent := false
		switch {
		case end+1 < len(rs) && unicode.IsSpace(rs[end]) && rs[end+1] == '%':
			percent, end = true, end+2
		case end < len(rs) && rs[end] == '%':
			percent, end = true, end+1
		}
		if tok, ok := briefToken0(raw, currency, percent); ok {
			out = append(out, tok)
		}
		i = end
	}
	return out
}

// briefNumberAt matches the number group at i and returns its end, or -1.
func briefNumberAt(rs []rune, i int) int {
	run := func(j int) int {
		k := j
		for k < len(rs) && isBriefDigit(rs[k]) {
			k++
		}
		return k - j
	}
	lead := run(i)
	if lead == 0 {
		return -1
	}
	// \d{1,3}(?:[   ]\d{3})+(?!\d): the leading digits must be the
	// whole run (a shorter prefix is followed by a digit, not a separator).
	if lead <= 3 {
		ends := []int{}
		p := i + lead
		for p+3 < len(rs) && isBriefGroupSeparator(rs[p]) &&
			isBriefDigit(rs[p+1]) && isBriefDigit(rs[p+2]) && isBriefDigit(rs[p+3]) {
			p += 4
			ends = append(ends, p)
		}
		for k := len(ends) - 1; k >= 0; k-- {
			if e := ends[k]; e >= len(rs) || !isBriefDigit(rs[e]) {
				return e
			}
		}
	}
	// \d+(?:[.,]\d+)+
	p := i + lead
	groups := 0
	for p+1 < len(rs) && (rs[p] == '.' || rs[p] == ',') && isBriefDigit(rs[p+1]) {
		p += 1 + run(p+1)
		groups++
	}
	if groups > 0 {
		return p
	}
	// \d+
	return i + lead
}

func briefToken0(raw string, currency, percent bool) (briefToken, bool) {
	if strings.ContainsAny(raw, ".,") {
		return briefToken{readings: briefReadings(raw), tolerance: 0.051}, true
	}
	var digits strings.Builder
	for _, r := range raw {
		if unicode.IsNumber(r) {
			digits.WriteRune(r)
		}
	}
	value, ok := swiftDouble(digits.String())
	if !ok {
		return briefToken{}, false
	}
	spaceGrouped := utf8.RuneCountInString(digits.String()) != utf8.RuneCountInString(raw)
	isLevel := value >= 1000 && (value < 1900 || value > 2100)
	if !spaceGrouped && !isLevel && !percent && !currency {
		return briefToken{}, false
	}
	return briefToken{readings: []float64{value}, tolerance: 0.5}, true
}

// briefReadings is MarketBriefNumbers.readings: "25.032" is 25.032 in en and
// 25032 in pt-PT.
func briefReadings(token string) []float64 {
	out := []float64{}
	if v, ok := swiftDouble(strings.ReplaceAll(token, ",", "")); ok {
		out = append(out, v)
	}
	if v, ok := swiftDouble(strings.ReplaceAll(strings.ReplaceAll(token, ".", ""), ",", ".")); ok {
		out = append(out, v)
	}
	return out
}

// swiftDouble is Double(String) for the digit-and-dot strings tokens hold.
func swiftDouble(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' {
			return 0, false
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0, false
	}
	return v, true
}

// briefIsGrounded is MarketBriefValidator.isGrounded: every checked number
// matches some fact under one of its readings, within the token's
// tolerance or 0.1% of the fact.
func briefIsGrounded(text string, grounded []float64) bool {
	for _, tok := range briefNumberTokens(text) {
		matched := false
		for _, reading := range tok.readings {
			for _, fact := range grounded {
				if math.Abs(fact-reading) <= math.Max(tok.tolerance, math.Abs(fact)*0.001) {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

var briefTickerPattern = regexp.MustCompile(`^[A-Z][A-Z.]{0,5}$`)

// briefTickers is MarketBriefValidator.tickers: trimmed, "$" dropped,
// uppercased, deduped, each value matched whole against [A-Z][A-Z.]{0,5}.
func briefTickers(raw []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range raw {
		t := strings.ToUpper(strings.ReplaceAll(strings.TrimFunc(v, isSwiftWhitespace), "$", ""))
		if !briefTickerPattern.MatchString(t) || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// isSwiftWhitespace is CharacterSet.whitespaces: tab and Zs, no newlines.
func isSwiftWhitespace(r rune) bool { return r == '\t' || unicode.Is(unicode.Zs, r) }

// MARK: - DailyBoardFacts

// DailyMinimumStockMove is DailyBoardFacts.minimumStockMove.
const DailyMinimumStockMove = 1.0

// DailyStockQuote is DailyBoardFacts.StockQuote.
type DailyStockQuote struct {
	Symbol        string
	Price         float64
	ChangePercent float64
	SessionDate   string
}

// DailyHeadline is DailyBoardFacts.Headline.
type DailyHeadline struct {
	Title   string
	URL     *string
	Tickers []string
}

// DailyRankedTickers is DailyBoardFacts.rankedTickers: most named first,
// then alphabetical.
func DailyRankedTickers(headlines []DailyHeadline, limit int) []string {
	counts := map[string]int{}
	for _, h := range headlines {
		for _, t := range h.Tickers {
			if DailyIsStockTicker(t) {
				counts[t]++
			}
		}
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	if len(keys) > limit {
		keys = keys[:max(limit, 0)]
	}
	return keys
}

// DailyHotStockCandidates is DailyBoardFacts.hotStockCandidates: tickers the
// news named, then the biggest large-cap movers, Mag 7 left to Monday.
func DailyHotStockCandidates(headlines []DailyHeadline, movers []string, limit int) []string {
	all := DailyRankedTickers(headlines, 8)
	for _, m := range movers {
		all = append(all, strings.ToUpper(m))
	}
	seen := map[string]bool{}
	out := []string{}
	for _, t := range all {
		if len(out) >= limit {
			break
		}
		if !DailyIsStockTicker(t) || isMag7(t) || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

var dailyStockTickerPattern = regexp.MustCompile(`^[A-Z]{1,5}(?:\.[A-Z]{1,2})?$`)

// DailyIsStockTicker is DailyBoardFacts.isStockTicker.
func DailyIsStockTicker(symbol string) bool {
	t := strings.ToUpper(symbol)
	if strings.HasPrefix(t, "^") || strings.Contains(t, "=") || t == "GENERAL" {
		return false
	}
	return dailyStockTickerPattern.MatchString(t)
}

// DailyHotStock is DailyBoardFacts.hotStock: the biggest same-day move of at
// least 1% among the quotes, Mag 7 left to Monday, with the headline that
// named it.
func DailyHotStock(stocks []DailyStockQuote, headlines []DailyHeadline, day string) (*DailyStockQuote, *DailyHeadline) {
	var best *DailyStockQuote
	for i := range stocks {
		s := stocks[i]
		ch := s.ChangePercent
		if s.SessionDate != day || isMag7(strings.ToUpper(s.Symbol)) ||
			math.IsNaN(ch) || math.IsInf(ch, 0) || math.Abs(ch) < DailyMinimumStockMove {
			continue
		}
		// max(by:) keeps the first of equal elements.
		if best == nil || math.Abs(best.ChangePercent) < math.Abs(ch) {
			best = &s
		}
	}
	if best == nil {
		return nil, nil
	}
	symbol := strings.ToUpper(best.Symbol)
	for i := range headlines {
		for _, t := range headlines[i].Tickers {
			if strings.EqualFold(t, symbol) {
				h := headlines[i]
				return best, &h
			}
		}
	}
	return best, nil
}

// MARK: - Style (DailyBoardStyle)

// DailyBannedPhrases is DailyBoardStyle.bannedPhrases.
var DailyBannedPhrases = []string{
	"delve", "landscape", "navigate", "navigating", "in today's", "dive in", "dive into", "deep dive",
	"buckle up", "rollercoaster", "roller coaster", "game-changer", "game changer", "only time will tell",
	"it's worth noting", "it is worth noting", "remains to be seen", "testament", "tapestry",
	"unprecedented", "in conclusion", "let's", "what do you think",
	"the facts i have", "the facts provided", "the data provided", "based on the data", "as an ai",
}

// icuWord is ICU's \w (NSRegularExpression): letters, marks, decimal
// digits, connector punctuation, ZWNJ and ZWJ. RE2's \b and \s are ASCII
// only, so the patterns below spell the boundaries out.
const (
	icuWord  = `\p{L}\p{M}\p{Nd}\p{Pc}\x{200C}\x{200D}`
	icuSpace = `[\s\p{Z}\x{85}]`
)

// dailyAdvicePatterns is DailyBoardStyle.advicePatterns, with \b as an
// explicit non-word neighbor around capture group 1.
var dailyAdvicePatterns = func() []*regexp.Regexp {
	src := []string{
		`(you|investors|traders|readers|members)` + icuSpace + `+(should|must|need to|ought to|may want to|might want to)`,
		`(consider|start|time to)` + icuSpace + `+(buying|selling|adding|trimming|accumulating|shorting)`,
		`(buy|sell)` + icuSpace + `+(now|the dip)`,
		`this is a (buy|sell)`,
	}
	out := make([]*regexp.Regexp, len(src))
	for i, s := range src {
		out[i] = regexp.MustCompile(`(?i)(?:^|[^` + icuWord + `])(` + s + `)(?:[^` + icuWord + `]|$)`)
	}
	return out
}()

var dailyBannedPatterns = func() []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(DailyBannedPhrases))
	for i, p := range DailyBannedPhrases {
		out[i] = regexp.MustCompile(`(?i)(?:^|[^` + icuWord + `])` + regexp.QuoteMeta(p))
	}
	return out
}()

// DailySystemPrompt is DailyBoardStyle.system.
var DailySystemPrompt = "You write the daily post for the Norviq community board, read by retail investors in Europe and the US. " +
	"One topic per post, explained well enough that members want to argue about it in the comments.\n" +
	"\n" +
	"Use only the facts in the user message. Every number you write must appear in the facts, with the same " +
	"precision. Never compute a new number, never round differently, and never add a price, percentage, date " +
	"or ticker that is not listed. Write dates as words if you like (Monday, October 26).\n" +
	"\n" +
	"Return JSON with keys title and body.\n" +
	"Title: under 110 characters. State the main fact, with its number when there is one. No question, " +
	"no clickbait.\n" +
	"Body: 120 to 220 words of plain text in 3 or 4 short paragraphs separated by a blank line.\n" +
	"1. What happened, with the key figures.\n" +
	"2. Context: two or three more figures from the facts that put it in perspective.\n" +
	"3. Why it could matter, as a possibility, not a prediction.\n" +
	"4. One specific question members can disagree about, tied to a figure in the post. For example: " +
	"\"Is 31.4x trailing earnings still cheap for a company growing this fast?\" Never a generic " +
	"\"What do you think?\".\n" +
	"\n" +
	"If the facts give no reason for a move, say plainly that no single cause is known yet. Never mention " +
	"the facts, the data you were given, or yourself.\n" +
	"\n" +
	"Write like a sharp analyst talking to peers: concrete, plain words, short sentences. No advice: never " +
	"tell the reader to buy, sell, hold or consider anything. No markdown, hashtags, emoji, exclamation marks " +
	"or em dashes. Do not use these words or phrases: " + strings.Join(DailyBannedPhrases, ", ") + "."

// DailyDescribe is DailyBoardStyle.describe.
func DailyDescribe(c DailyCandidate) string {
	switch c.Topic {
	case TopicMag7:
		return c.Name + ", one of the Magnificent Seven: what its recent move and valuation say"
	case TopicStock:
		for _, f := range c.Facts {
			if f.Label == DailyHeadlineLabel {
				return c.Name + ", a stock in the news today, and why it moved"
			}
		}
		return c.Name + ", one of the biggest movers among large US companies today"
	case TopicETF:
		return c.Name + ", a widely held ETF: what it tracks, what it costs and how it has done"
	case TopicWeekRecap:
		return "how the major stock indexes moved over the week that just ended"
	case TopicWeekAhead:
		return "the central-bank decisions, inflation releases and big earnings reports due in the next seven days"
	case TopicMacro:
		angle := ""
		if c.Angle != nil {
			angle = *c.Angle
		}
		switch angle {
		case "inflation":
			return "the latest inflation figures for the US and the euro area"
		case "rates":
			return "Fed and ECB policy rates measured against inflation"
		case "core_gap":
			return "the gap between core and headline inflation"
		}
		return "inflation in the US compared with the euro area"
	}
	return c.Name
}

// DailyPrompt is DailyBoardStyle.prompt(for:day:).
func DailyPrompt(c DailyCandidate, day string) string {
	lines := make([]string, len(c.Facts))
	for i, f := range c.Facts {
		lines[i] = "- " + f.Label + ": " + f.Value
	}
	return "Post date: " + DailyWithWeekday(day) + "\nTopic: " + DailyDescribe(c) + "\nFacts:\n" + strings.Join(lines, "\n")
}

// MARK: - Validator (DailyBoardValidator)

// DailyDisclosure is DailyBoardValidator.disclosure.
const DailyDisclosure = "Written by Norviq AI from the latest market data. Not financial advice."

const (
	dailyMaxTitleLength = 120
	dailyMaxBodyLength  = 1600
	dailyMinWords       = 80
	dailyMaxWords       = 260
	dailyMinBodyFigures = 3
)

// DailyRejection is DailyBoardValidator.Rejection.
type DailyRejection struct {
	// Kind is title, length, figures, ungrounded, phrase, advice or noQuestion.
	Kind  string
	Count int    // length (words), figures
	Text  string // phrase, advice
}

// String is the description read back to the model on its second try.
func (r DailyRejection) String() string {
	switch r.Kind {
	case "title":
		return "the title was empty, too long, or had an exclamation mark"
	case "length":
		return fmt.Sprintf("the body was %d words; it must be 120 to 220", r.Count)
	case "figures":
		return fmt.Sprintf("the body cited only %d figures from the facts; cite at least 3", r.Count)
	case "phrase":
		return "it used \"" + r.Text + "\", which is not allowed"
	case "advice":
		return "\"" + r.Text + "\" reads as investment advice"
	case "noQuestion":
		return "the last paragraph must end with one specific question for members"
	}
	return "it cited a number that is not in the facts, or rounded one differently"
}

// DailyCheck is DailyBoardValidator.check: the cleaned title and the body
// with the disclosure appended, or why the draft cannot run.
func DailyCheck(rawTitle, rawBody string, grounded []float64) (title, body string, rejection *DailyRejection) {
	title = strings.Join(strings.Fields(rawTitle), " ")
	body = DailyClean(rawBody)
	if n := utf8.RuneCountInString(title); n < 3 || n > dailyMaxTitleLength || strings.Contains(title, "!") {
		return "", "", &DailyRejection{Kind: "title"}
	}
	words := len(strings.Fields(body))
	if words < dailyMinWords || words > dailyMaxWords || utf8.RuneCountInString(body) > dailyMaxBodyLength {
		return "", "", &DailyRejection{Kind: "length", Count: words}
	}
	text := strings.ReplaceAll(title+"\n"+body, "’", "'")
	if phrase, ok := dailyBannedPhrase(text); ok {
		return "", "", &DailyRejection{Kind: "phrase", Text: phrase}
	}
	if advice, ok := dailyAdvice(text); ok {
		return "", "", &DailyRejection{Kind: "advice", Text: advice}
	}
	if figures := len(briefNumberTokens(body)); figures < dailyMinBodyFigures {
		return "", "", &DailyRejection{Kind: "figures", Count: figures}
	}
	if !briefIsGrounded(title+"\n"+body, grounded) {
		return "", "", &DailyRejection{Kind: "ungrounded"}
	}
	paragraphs := strings.Split(body, "\n\n")
	last := ""
	for i := len(paragraphs) - 1; i >= 0; i-- {
		if strings.TrimFunc(paragraphs[i], isSwiftWhitespace) != "" {
			last = paragraphs[i]
			break
		}
	}
	if !strings.Contains(last, "?") {
		return "", "", &DailyRejection{Kind: "noQuestion"}
	}
	return title, body + "\n\n" + DailyDisclosure, nil
}

// DailyClean is DailyBoardValidator.clean: dashes become commas, and blank
// lines collapse to one so paragraphs survive.
func DailyClean(raw string) string {
	text := strings.TrimSpace(raw)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, " — ", ", ")
	text = strings.ReplaceAll(text, "—", ", ")
	text = strings.ReplaceAll(text, " – ", ", ")
	for strings.Contains(text, "\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	}
	return text
}

func dailyBannedPhrase(text string) (string, bool) {
	if strings.Contains(text, "!") {
		return "!", true
	}
	for _, r := range text {
		if isEmojiPresentation(r) {
			return "emoji", true
		}
	}
	for i, re := range dailyBannedPatterns {
		if re.MatchString(text) {
			return DailyBannedPhrases[i], true
		}
	}
	return "", false
}

func dailyAdvice(text string) (string, bool) {
	for _, re := range dailyAdvicePatterns {
		if m := re.FindStringSubmatch(text); m != nil {
			return m[1], true
		}
	}
	return "", false
}

func isEmojiPresentation(r rune) bool {
	i := sort.Search(len(emojiPresentation), func(i int) bool { return emojiPresentation[i][1] >= r })
	return i < len(emojiPresentation) && emojiPresentation[i][0] <= r
}

// MARK: - Model reply (OpenAIDailyBoardWriter.parse)

// DailyParseDraft is OpenAIDailyBoardWriter.parse: strips a code fence and
// decodes {"title": "...", "body": "..."} the way JSONDecoder did (exact
// key names, both required strings).
func DailyParseDraft(content string) (title, body string, err error) {
	text := strings.TrimSpace(content)
	if strings.HasPrefix(text, "```") {
		text = strings.ReplaceAll(strings.ReplaceAll(text, "```json", ""), "```", "")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &obj); err != nil {
		return "", "", fmt.Errorf("daily post draft: %w", err)
	}
	if obj == nil {
		return "", "", errors.New("daily post draft: expected an object")
	}
	field := func(key string) (string, error) {
		raw, ok := obj[key]
		if !ok {
			return "", fmt.Errorf("daily post draft: no value for key %q", key)
		}
		var s *string
		if err := json.Unmarshal(raw, &s); err != nil || s == nil {
			return "", fmt.Errorf("daily post draft: %q is not a string", key)
		}
		return *s, nil
	}
	if title, err = field("title"); err != nil {
		return "", "", err
	}
	if body, err = field("body"); err != nil {
		return "", "", err
	}
	return title, body, nil
}
