package assistant

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"norviq-api/internal/platform/telegram"

	"github.com/google/uuid"
)

// MessagingCommands: slash commands, handled before the model sees the text.

const (
	cmdStart     = "/start"
	cmdHelp      = "/help"
	cmdUnlink    = "/unlink"
	cmdClear     = "/clear"
	cmdFinance   = "/finance"
	cmdPortfolio = "/portfolio"
	cmdBudget    = "/budget"
	cmdExpenses  = "/expenses"
	cmdNews      = "/news"
)

// dataCommands answer from the database: no model call, no turn spent.
var dataCommands = map[string]bool{cmdFinance: true, cmdPortfolio: true, cmdBudget: true, cmdExpenses: true, cmdNews: true}

// PublishedCommands is MessagingCommands.published(): the bot's command menu
// (no /start, which Telegram renders as a button anyway).
func PublishedCommands() []telegram.Command {
	return []telegram.Command{
		{Name: "finance", Description: "your money at a glance"},
		{Name: "portfolio", Description: "holdings, value, and today's move"},
		{Name: "budget", Description: "this month against your targets"},
		{Name: "expenses", Description: "what you have spent this month"},
		{Name: "news", Description: "signals and headlines on your holdings"},
		{Name: "clear", Description: "start a fresh conversation"},
		{Name: "help", Description: "what I can do"},
		{Name: "unlink", Description: "disconnect this chat from your account"},
	}
}

const helpText = `I'm Q. Ask me about your spending, portfolio, budget or the markets — the same assistant that lives in the Norviq app, and the same conversation. "Hey Q" and "@Q" both work, but you don't need either here.

I can also propose changes, like logging an expense. I never apply one without you tapping Confirm first.

These answer straight from your data, and never cost you an AI request:

/finance — your money at a glance
/portfolio — holdings, value, and today's move
/budget — this month against your targets
/expenses — what you have spent this month
/news — signals and headlines on your holdings

Add a question to any of them and I'll answer it myself instead — "/budget how do I cut it?".

/clear — start a fresh conversation
/help — this message
/unlink — disconnect this chat from your account`

// parseCommand extracts a leading command (lowercased, @botname stripped)
// and the rest of the text.
func parseCommand(text string) (command, argument string, ok bool) {
	trimmed := strings.TrimFunc(text, unicode.IsSpace)
	if !strings.HasPrefix(trimmed, "/") {
		return "", "", false
	}
	// split(separator: " ", maxSplits: 1, omittingEmptySubsequences: true)
	rest := strings.TrimLeft(trimmed, " ")
	head, tail, found := strings.Cut(rest, " ")
	if head == "" {
		return "", "", false
	}
	command = strings.ToLower(head)
	if at := strings.IndexRune(command, '@'); at >= 0 {
		command = command[:at]
	}
	if found {
		argument = strings.TrimFunc(strings.TrimLeft(tail, " "), unicode.IsSpace)
	}
	return command, argument, true
}

// runCommand is MessagingCommands.run. handled=false means the text is not a
// command this bot implements (it goes to the model); question is set when a
// data command carried one.
func (m *messenger) runCommand(ctx context.Context, in telegram.InboundMessage, userID uuid.UUID) (out telegram.Outbound, question *string, handled bool, err error) {
	command, argument, ok := parseCommand(in.Text)
	if !ok {
		return telegram.Outbound{}, nil, false, nil
	}
	if dataCommands[command] && argument != "" {
		return telegram.Outbound{}, &argument, false, nil
	}
	text := ""
	switch command {
	case cmdStart:
		text = "This chat is already connected to your Norviq account. " + helpText
	case cmdHelp:
		text = helpText
	case cmdUnlink:
		removed, err := m.unlink(ctx, userID, in.Platform)
		if err != nil {
			return telegram.Outbound{}, nil, false, err
		}
		text = "This chat was not connected."
		if removed {
			text = "Disconnected. This chat no longer reaches your Norviq account. You can reconnect any time from Settings."
		}
	case cmdClear:
		if err := m.startFresh(ctx, userID); err != nil {
			return telegram.Outbound{}, nil, false, err
		}
		text = "Fresh start — I've forgotten what we were talking about. Your earlier messages are still in the app."
	case cmdFinance:
		text, err = m.financeReply(ctx, userID)
	case cmdPortfolio:
		text, err = m.portfolioReply(ctx, userID)
	case cmdBudget:
		text, err = m.budgetReply(ctx, userID)
	case cmdExpenses:
		text, err = m.expensesReply(ctx, userID)
	case cmdNews:
		text, err = m.newsReply(ctx, userID)
	default:
		return telegram.Outbound{}, nil, false, nil
	}
	if err != nil {
		return telegram.Outbound{}, nil, false, err
	}
	return reply(text), nil, true, nil
}

// MARK: MessagingCommandReplies

const replyListLimit = 6

var errThesisFeedUnwired = errors.New("thesis watch feed source is not wired")

func moneySigned(v float64) string {
	if v > 0 {
		return "+" + moneyNumber(v)
	}
	return moneyNumber(v)
}

func moneyArrow(v float64) string {
	switch {
	case v > 0:
		return "▲"
	case v < 0:
		return "▼"
	}
	return "▬"
}

type replyPerformer struct {
	Symbol        string  `json:"symbol"`
	ChangePercent float64 `json:"changePercent"`
}

type replyDashboard struct {
	TotalValue         float64          `json:"totalValue"`
	DailyChange        float64          `json:"dailyChange"`
	DailyChangePercent float64          `json:"dailyChangePercent"`
	TopPerformers      []replyPerformer `json:"topPerformers"`
	BottomPerformers   []replyPerformer `json:"bottomPerformers"`
	SectorAllocation   []struct {
		Sector  string  `json:"sector"`
		Percent float64 `json:"percent"`
	} `json:"sectorAllocation"`
}

type replyInsights struct {
	SavingsRate     float64 `json:"savingsRate"`
	BudgetStreak    int     `json:"budgetStreak"`
	CashBuffer      float64 `json:"cashBuffer"`
	FinancialHealth struct {
		Score    int    `json:"score"`
		MaxScore int    `json:"maxScore"`
		Status   string `json:"status"`
	} `json:"financialHealth"`
}

func (m *messenger) dashboard(ctx context.Context, userID uuid.UUID) (replyDashboard, error) {
	var out replyDashboard
	v, err := m.h.src.Dashboard(ctx, userID)
	if err != nil {
		return out, err
	}
	insightDecode(v, &out)
	return out, nil
}

func (m *messenger) financeReply(ctx context.Context, userID uuid.UUID) (string, error) {
	d, err := m.dashboard(ctx, userID)
	if err != nil {
		return "", err
	}
	raw, err := m.h.src.DashboardInsights(ctx, userID)
	if err != nil {
		return "", err
	}
	var ins replyInsights
	insightDecode(raw, &ins)
	health := ins.FinancialHealth
	lines := []string{
		"**Your finances**",
		"",
		"Portfolio  **" + moneyNumber(d.TotalValue) + "**  " +
			moneyArrow(d.DailyChange) + " " + moneyPercent(d.DailyChangePercent) + " today",
		"Cash buffer  " + moneyNumber(ins.CashBuffer),
		"Savings rate  " + moneyPercent(ins.SavingsRate),
		"Health  " + strconv.Itoa(health.Score) + "/" + strconv.Itoa(health.MaxScore) + " — " + readableHealth(health.Status),
	}
	if ins.BudgetStreak > 0 {
		s := "s"
		if ins.BudgetStreak == 1 {
			s = ""
		}
		lines = append(lines, "Budget streak  "+strconv.Itoa(ins.BudgetStreak)+" month"+s)
	}
	lines = append(lines, "", "_/portfolio, /budget, /expenses for detail._")
	return strings.Join(lines, "\n"), nil
}

func readableHealth(status string) string {
	switch status {
	case "atRisk":
		return "at risk"
	case "needsAttention":
		return "needs attention"
	}
	return status // healthy, excellent
}

func (m *messenger) portfolioReply(ctx context.Context, userID uuid.UUID) (string, error) {
	d, err := m.dashboard(ctx, userID)
	if err != nil {
		return "", err
	}
	if d.TotalValue == 0 && len(d.TopPerformers) == 0 {
		return "**Portfolio**\n\nNo holdings yet. Add one in the app and this fills in.", nil
	}
	lines := []string{
		"**Portfolio**  " + moneyNumber(d.TotalValue),
		moneyArrow(d.DailyChange) + " " + moneySigned(d.DailyChange) + " (" + moneyPercent(d.DailyChangePercent) + ") today",
	}
	performer := func(p replyPerformer) string {
		return p.Symbol + "  " + moneyArrow(p.ChangePercent) + " " + moneyPercent(p.ChangePercent)
	}
	if len(d.TopPerformers) > 0 {
		lines = append(lines, "", "**Leading**")
		for _, p := range d.TopPerformers[:min(3, len(d.TopPerformers))] {
			lines = append(lines, performer(p))
		}
	}
	if len(d.BottomPerformers) > 0 {
		lines = append(lines, "", "**Lagging**")
		for _, p := range d.BottomPerformers[:min(3, len(d.BottomPerformers))] {
			lines = append(lines, performer(p))
		}
	}
	if len(d.SectorAllocation) > 0 {
		lines = append(lines, "", "**Sectors**")
		for _, s := range d.SectorAllocation[:min(replyListLimit, len(d.SectorAllocation))] {
			lines = append(lines, s.Sector+" — "+moneyPercent(s.Percent))
		}
	}
	return strings.Join(lines, "\n"), nil
}

func pillarName(p string) string {
	switch p {
	case "fundamentals":
		return "Fundamentals"
	case "futureYou":
		return "Future you"
	case "fun":
		return "Fun"
	}
	return p
}

func (m *messenger) budgetReply(ctx context.Context, userID uuid.UUID) (string, error) {
	summaries, err := m.h.actions.expenses.GetPillarPlanningSummaries(ctx, m.h.d.DB, userID, moneyMonthStart(m.h.now()))
	if err != nil {
		return "", err
	}
	if len(summaries) == 0 {
		return "**Budget**\n\nNo plan for this month yet. Set targets in the app and this fills in.", nil
	}
	lines := []string{"**Budget — this month**", ""}
	var target, actual float64
	for _, s := range summaries {
		remaining := s.TargetAmount - s.ActualAmount
		state := moneyNumber(remaining) + " left"
		if remaining < 0 {
			state = "**" + moneyNumber(-remaining) + " over**"
		}
		lines = append(lines, pillarName(string(s.Pillar))+"  "+
			moneyNumber(s.ActualAmount)+" / "+moneyNumber(s.TargetAmount)+"  — "+state)
	}
	for _, s := range summaries {
		target += s.TargetAmount
	}
	for _, s := range summaries {
		actual += s.ActualAmount
	}
	lines = append(lines, "", "Total  "+moneyNumber(actual)+" / "+moneyNumber(target))
	return strings.Join(lines, "\n"), nil
}

func (m *messenger) expensesReply(ctx context.Context, userID uuid.UUID) (string, error) {
	monthStart := moneyMonthStart(m.h.now())
	exp := m.h.actions.expenses
	recent, _, err := exp.GetExpenses(ctx, m.h.d.DB, userID, &monthStart, nil, replyListLimit, nil)
	if err != nil {
		return "", err
	}
	reports, err := exp.GetMonthlyReports(ctx, m.h.d.DB, userID, nil, nil)
	if err != nil {
		return "", err
	}
	lines := []string{"**Expenses — this month**"}
	if len(reports) > 0 {
		current := reports[len(reports)-1]
		lines = append(lines, "", "Spent  **"+moneyNumber(current.Actual)+"** of "+moneyNumber(current.Planned)+" planned")
	}
	if len(recent) == 0 {
		lines = append(lines, "", "Nothing logged this month yet.")
		return strings.Join(lines, "\n"), nil
	}
	lines = append(lines, "", "**Latest**")
	for i := range recent {
		item := &recent[i]
		lines = append(lines, item.OccurredOn+"  "+item.Title+" — "+moneyNumber(item.Amount))
	}
	return strings.Join(lines, "\n"), nil
}

type replySummary struct {
	WindowDays  int            `json:"windowDays"`
	TotalEvents int            `json:"totalEvents"`
	ByTopic     map[string]int `json:"byTopic"`
}

type replyFeed struct {
	Items []struct {
		Headline     string   `json:"headline"`
		Source       *string  `json:"source"`
		URL          string   `json:"url"`
		WhyItMatters *string  `json:"whyItMatters"`
		Symbols      []string `json:"symbols"`
	} `json:"items"`
}

func (m *messenger) newsReply(ctx context.Context, userID uuid.UUID) (string, error) {
	rawSummary, err := m.h.src.InsightsSummary(ctx, 7)
	if err != nil {
		return "", err
	}
	if m.h.src.ThesisFeed == nil {
		return "", errThesisFeedUnwired
	}
	rawFeed, err := m.h.src.ThesisFeed(ctx, userID, replyListLimit)
	if err != nil {
		return "", err
	}
	var summary replySummary
	insightDecode(rawSummary, &summary)
	var feed replyFeed
	insightDecode(rawFeed, &feed)

	lines := []string{"**News**"}
	if summary.TotalEvents > 0 {
		type kv struct {
			k string
			v int
		}
		topics := make([]kv, 0, len(summary.ByTopic))
		for k, v := range summary.ByTopic {
			topics = append(topics, kv{k, v})
		}
		// Swift's min(count:sortedBy:) over a Dictionary: ties fall in hash
		// order there; sorted by key here so the text is stable.
		sort.Slice(topics, func(i, j int) bool {
			if topics[i].v != topics[j].v {
				return topics[i].v > topics[j].v
			}
			return topics[i].k < topics[j].k
		})
		parts := make([]string, 0, 3)
		for _, t := range topics[:min(3, len(topics))] {
			parts = append(parts, t.k+" ("+strconv.Itoa(t.v)+")")
		}
		line := strconv.Itoa(summary.TotalEvents) + " signals in " + strconv.Itoa(summary.WindowDays) + " days"
		if len(parts) > 0 {
			line += " — " + strings.Join(parts, ", ")
		}
		lines = append(lines, "", line)
	}
	if len(feed.Items) == 0 {
		lines = append(lines, "", "Nothing on your holdings right now.")
		return strings.Join(lines, "\n"), nil
	}
	lines = append(lines, "", "**On your holdings**")
	for _, story := range feed.Items {
		tickers := strings.Join(story.Symbols[:min(3, len(story.Symbols))], ", ")
		lines = append(lines, "", "["+story.Headline+"]("+story.URL+")")
		footnote := tickers
		if story.Source != nil && *story.Source != "" {
			if footnote == "" {
				footnote = *story.Source
			} else {
				footnote += " · " + *story.Source
			}
		}
		if footnote != "" {
			lines = append(lines, "_"+footnote+"_")
		}
		if story.WhyItMatters != nil && *story.WhyItMatters != "" {
			lines = append(lines, *story.WhyItMatters)
		}
	}
	return strings.Join(lines, "\n"), nil
}
