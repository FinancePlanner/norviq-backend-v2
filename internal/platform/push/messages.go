package push

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"norviq-api/internal/apijson"

	"github.com/google/uuid"
)

// The builders below reproduce, field for field, the alerts and payloads of
// APNSPushNotificationSender's send… methods. Payload keys are the Swift
// Codable property names; nil optionals are omitted.

// TargetHit is sendTargetHit. targetID may be nil.
func TargetHit(targetID *uuid.UUID, symbol, scenario string, targetPrice, currentPrice float64) Notification {
	p := map[string]any{
		"schemaVersion": 1,
		"type":          "target_hit",
		"symbol":        symbol,
		"scenario":      scenario,
		"deepLink":      "financeplan://stocks/" + symbol,
		"targetPrice":   targetPrice,
		"currentPrice":  currentPrice,
	}
	if targetID != nil {
		p["targetId"] = apijson.FormatUUID(*targetID)
	}
	return Notification{
		Kind:     "target_hit",
		Title:    "Target hit: " + symbol,
		Body:     fmt.Sprintf("Scenario %s reached %s vs %s.", strings.ToUpper(scenario), FormatPrice(currentPrice, "USD"), FormatPrice(targetPrice, "USD")),
		ThreadID: "target-" + strings.ToUpper(symbol),
		Category: "TARGET_ALERT",
		Payload:  p,
	}
}

// BudgetAlert is sendBudgetAlert. snapshotID may be nil.
func BudgetAlert(snapshotID *uuid.UUID, threshold int, remainingAmount float64) Notification {
	p := map[string]any{
		"schemaVersion":   1,
		"type":            "budget_alert",
		"threshold":       threshold,
		"deepLink":        "financeplan://budget",
		"remainingAmount": remainingAmount,
	}
	thread := "budget-general"
	if snapshotID != nil {
		id := apijson.FormatUUID(*snapshotID)
		p["snapshotId"] = id
		thread = "budget-" + id
	}
	return Notification{
		Kind:     "budget_alert",
		Title:    "Budget Alert",
		Body:     fmt.Sprintf("You have reached %d%% of your monthly budget. Remaining: %s.", threshold, FormatPrice(remainingAmount, "USD")),
		ThreadID: thread,
		Category: "BUDGET_ALERT",
		Payload:  p,
	}
}

// EarningsReminder is sendEarningsReminder.
func EarningsReminder(symbol, earningsDate string, leadDays int) Notification {
	sym := strings.ToUpper(symbol)
	when := "in " + strconv.Itoa(leadDays) + " days"
	if leadDays == 1 {
		when = "tomorrow"
	}
	return Notification{
		Kind:     "earnings_reminder",
		Title:    "Earnings soon: " + sym,
		Body:     fmt.Sprintf("%s reports earnings %s (%s).", sym, when, earningsDate),
		ThreadID: fmt.Sprintf("earnings-%s-%s-%d", sym, earningsDate, leadDays),
		Category: "EARNINGS_REMINDER",
		Payload: map[string]any{
			"schemaVersion": 1,
			"type":          "earnings_reminder",
			"symbol":        sym,
			"earningsDate":  earningsDate,
			"leadDays":      leadDays,
			"deepLink":      "financeplan://stocks/" + sym,
		},
	}
}

// TaxOpportunity is the part of TaxOpportunityResponse sendTaxOpportunity
// reads. EstimatedBenefit is the Decimal amount as a JSON number.
type TaxOpportunity struct {
	ID               string
	InstrumentID     string
	Symbol           string
	EstimatedBenefit json.Number
	Currency         string
}

// TaxOpportunityAlert is sendTaxOpportunity.
func TaxOpportunityAlert(o TaxOpportunity) Notification {
	benefit, _ := o.EstimatedBenefit.Float64()
	return Notification{
		Kind:     "tax_opportunity",
		Title:    "Tax opportunity: " + o.Symbol,
		Body:     fmt.Sprintf("Estimated benefit %s. Review before acting.", FormatPrice(benefit, o.Currency)),
		ThreadID: "tax-" + o.InstrumentID,
		Category: "TAX_OPPORTUNITY",
		Payload: map[string]any{
			"schemaVersion":    1,
			"type":             "tax_harvest_opportunity",
			"opportunityId":    o.ID,
			"instrumentId":     o.InstrumentID,
			"symbol":           o.Symbol,
			"estimatedBenefit": o.EstimatedBenefit,
			"currency":         o.Currency,
			"deepLink":         "financeplan://tax/opportunities/" + o.ID,
		},
	}
}

// AutomationAlert is sendAutomationAlert (AutomationPushMessage): the APNS
// channel of a published notification event.
func AutomationAlert(eventID uuid.UUID, kind, title, body string, deepLink *string, data map[string]string) Notification {
	if data == nil {
		data = map[string]string{}
	}
	p := map[string]any{
		"schemaVersion": 1,
		"type":          kind,
		"eventId":       apijson.FormatUUID(eventID),
		"data":          data,
	}
	if deepLink != nil {
		p["deepLink"] = *deepLink
	}
	return Notification{
		Kind:     "automation " + kind,
		Title:    title,
		Body:     body,
		ThreadID: "automation-" + kind,
		Category: "WEALTH_AUTOMATION",
		Payload:  p,
	}
}

// RebalancingAlert is the part of StockPlanShared.RebalancingAlert
// sendRebalancingDrift reads.
type RebalancingAlert struct {
	ID                   string
	PortfolioID          string
	ModelID              string
	ScopeID              string
	ScopeName            string
	DriftBasisPoints     int
	ThresholdBasisPoints int
}

// RebalancingDrift is sendRebalancingDrift.
func RebalancingDrift(a RebalancingAlert, portfolioName string) Notification {
	drift := a.DriftBasisPoints
	if drift < 0 {
		drift = -drift
	}
	return Notification{
		Kind:     "rebalance_drift",
		Title:    "Portfolio drift: " + portfolioName,
		Body:     fmt.Sprintf("%s drifted %.2f%%, above your threshold. Review the plan before acting.", a.ScopeName, float64(drift)/100),
		ThreadID: "rebalance-" + a.PortfolioID + "-" + a.ScopeID,
		Category: "REBALANCE_DRIFT",
		Payload: map[string]any{
			"schemaVersion":        1,
			"type":                 "rebalance_drift",
			"alertId":              a.ID,
			"portfolioId":          a.PortfolioID,
			"modelId":              a.ModelID,
			"scopeName":            a.ScopeName,
			"driftBasisPoints":     a.DriftBasisPoints,
			"thresholdBasisPoints": a.ThresholdBasisPoints,
			"deepLink":             "financeplan://portfolios/" + a.PortfolioID + "/rebalancing",
		},
	}
}

// AssistantMessageCategory is AssistantPushMessage.category.
const AssistantMessageCategory = "assistant_message"

// AssistantMessage is sendAssistantMessage: a message the assistant posted
// into a thread on its own. sourceLabel is e.g. "Standing task".
func AssistantMessage(conversationID, messageID uuid.UUID, sourceLabel, title, body string) Notification {
	cid := apijson.FormatUUID(conversationID)
	return Notification{
		Kind:     AssistantMessageCategory,
		Title:    title,
		Body:     body,
		ThreadID: "assistant-" + cid,
		Category: AssistantMessageCategory,
		Payload: map[string]any{
			"schemaVersion":  1,
			"type":           AssistantMessageCategory,
			"conversationId": cid,
			"messageId":      apijson.FormatUUID(messageID),
			"sourceLabel":    sourceLabel,
			"deepLink":       "financeplan://assistant/conversations/" + cid,
		},
	}
}

// SocialPushMessage.Kind values.
const (
	SocialFriendRequest  = "friend_request"
	SocialFriendAccepted = "friend_accepted"
)

// SocialEvent is sendSocialEvent. kind is SocialFriendRequest or
// SocialFriendAccepted.
func SocialEvent(kind, title, body string) Notification {
	return Notification{
		Kind:     "social_event " + kind,
		Title:    title,
		Body:     body,
		ThreadID: "social",
		Category: kind,
		Payload:  map[string]any{"schemaVersion": 1, "type": kind},
	}
}

// BoardPushMessage.Kind values.
const (
	BoardReply  = "board_reply"
	BoardUpvote = "board_upvote"
)

// BoardEvent is sendBoardEvent: a reply or upvote, deep-linked to the post.
func BoardEvent(kind, title, body string, postID uuid.UUID, boardSlug string) Notification {
	pid := apijson.FormatUUID(postID)
	return Notification{
		Kind:     "board_event " + kind,
		Title:    title,
		Body:     body,
		ThreadID: "board-post-" + pid,
		Category: kind,
		Payload: map[string]any{
			"schemaVersion": 1,
			"type":          kind,
			"postId":        pid,
			"boardSlug":     boardSlug,
			"deepLink":      "financeplan://boards/" + boardSlug + "/posts/" + pid,
		},
	}
}

// MarketBriefCategory is MarketBriefPushMessage.category.
const MarketBriefCategory = "MARKET_BRIEF"

// MarketBrief is sendMarketBrief. slot is the MarketBriefSlot raw value.
func MarketBrief(slot, tradingDate, title, body string) Notification {
	return Notification{
		Kind:     "market_brief",
		Title:    title,
		Body:     body,
		ThreadID: "market-brief",
		Category: MarketBriefCategory,
		Payload: map[string]any{
			"schemaVersion": 1,
			"type":          "market_brief",
			"slot":          slot,
			"tradingDate":   tradingDate,
			"deepLink":      "financeplan://market/brief?slot=" + slot + "&date=" + tradingDate,
		},
	}
}

// currencySymbols are the en_US NumberFormatter symbols for the currencies
// Norviq handles; any other code renders as "CODE 1,234.00" like ICU.
var currencySymbols = map[string]string{
	"USD": "$", "EUR": "€", "GBP": "£", "JPY": "¥", "CAD": "CA$", "AUD": "A$",
	"BRL": "R$", "INR": "₹", "CNY": "CN¥", "HKD": "HK$", "NZD": "NZ$", "MXN": "MX$",
}

// FormatPrice is NumberFormatter(.currency) in en_US with exactly two
// fraction digits: "$1,234.50", "-€3.00", "CHF 12.00".
func FormatPrice(value float64, currency string) string {
	neg := value < 0
	if neg {
		value = -value
	}
	digits := strconv.FormatFloat(value, 'f', 2, 64)
	whole, frac, _ := strings.Cut(digits, ".")
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	number := b.String() + "." + frac
	symbol, ok := currencySymbols[strings.ToUpper(currency)]
	if !ok {
		symbol = strings.ToUpper(currency) + " "
	}
	if neg {
		return "-" + symbol + number
	}
	return symbol + number
}
