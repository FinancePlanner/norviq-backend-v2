package assistant

import (
	"strings"
	"testing"
	"time"
)

// Ported from AIWatchIntentClassifierTests.swift.

func TestWatchConditional(t *testing.T) {
	i := classifyWatch("Watch NVDA and tell me when it drops below 100")
	if i == nil {
		t.Fatal("no intent")
	}
	if i.subject != "NVDA" || i.condition == nil || *i.condition != "it drops below 100" || i.title != "Watch NVDA" ||
		i.intervalMinutes != 60 || i.scheduleHuman != "Every hour" || i.spec != "Watch NVDA and tell me when it drops below 100" {
		t.Fatalf("intent = %+v", i)
	}
	if got := i.confirmationText(); got != "Got it — I'll watch NVDA and ping you when it drops below 100." {
		t.Fatalf("confirmation = %q", got)
	}
}

func TestWatchSecondPerson(t *testing.T) {
	i := classifyWatch("keep an eye on my grocery spending and let me know if it goes over 400")
	if i == nil || i.confirmationText() != "Got it — I'll watch your grocery spending and ping you when it goes over 400." {
		t.Fatalf("intent = %+v", i)
	}
}

func TestWatchEveryMorning(t *testing.T) {
	i := classifyWatch("Every morning check my budget and tell me what's left")
	if i == nil || i.condition != nil || i.intervalMinutes != 1440 || i.scheduleHuman != "Every day at 8:00" ||
		i.hour == nil || *i.hour != 8 || i.title != "Check my budget and tell me what's left" {
		t.Fatalf("intent = %+v", i)
	}
	if !strings.HasPrefix(i.confirmationText(), "Got it — I'll check your budget and tell you what's left every day at 8:00") {
		t.Fatalf("confirmation = %q", i.confirmationText())
	}
}

func TestWatchSchedules(t *testing.T) {
	daily := classifyWatch("send me a spending recap every day at 7:30 pm")
	if daily == nil || daily.scheduleHuman != "Every day at 19:30" {
		t.Fatalf("daily = %+v", daily)
	}
	weekly := classifyWatch("every Monday summarise my portfolio") //nolint:misspell // user text, British spelling
	if weekly == nil || weekly.scheduleHuman != "Every Monday at 9:00" || *weekly.weekday != 2 || weekly.intervalMinutes != 7*1440 {
		t.Fatalf("weekly = %+v", weekly)
	}
	hours := classifyWatch("check my portfolio every 4 hours")
	if hours == nil || hours.intervalMinutes != 240 || hours.scheduleHuman != "Every 4 hours" {
		t.Fatalf("hours = %+v", hours)
	}
	if m := classifyWatch("check AAPL every 5 minutes"); m == nil || m.intervalMinutes != 60 {
		t.Fatalf("minimum = %+v", m)
	}
}

func TestWatchPingMe(t *testing.T) {
	i := classifyWatch("Ping me when my savings rate drops under 20%")
	if i == nil || *i.condition != "my savings rate drops under 20%" ||
		i.confirmationText() != "Got it — I'll keep an eye on it and ping you when your savings rate drops under 20%." {
		t.Fatalf("intent = %+v", i)
	}
}

func TestWatchNegatives(t *testing.T) {
	for _, text := range []string{
		"How much did I spend on groceries?", "Tell me if I overspent this month", "How much do I spend every day?",
		"What is my portfolio worth?", "Add a 12 euro lunch expense", "/dd NVDA", "watch out",
	} {
		if i := classifyWatch(text); i != nil {
			t.Errorf("%q classified as %+v", text, i)
		}
	}
}

func TestWatchFirstRun(t *testing.T) {
	i := classifyWatch("every morning check my budget")
	loc, err := time.LoadLocation("Europe/Lisbon")
	if err != nil {
		t.Skip("no tzdata")
	}
	now := time.Unix(1_790_157_600, 0)
	first := i.firstRunAt(now, loc)
	l := first.In(loc)
	if l.Hour() != 8 || l.Minute() != 0 || !first.After(now) || first.Sub(now) >= 24*time.Hour {
		t.Fatalf("first = %v", l)
	}
}

func TestWatchArgumentsSortedAndSlashEscaped(t *testing.T) {
	i := classifyWatch("Ping me when my AAPL/MSFT spread widens")
	args := &watchArguments{
		Condition: i.condition, ConfirmationText: i.confirmationText(), FirstRunAt: "2026-10-29T18:00:00Z",
		IntervalMinutes: i.intervalMinutes, ScheduleHuman: i.scheduleHuman, Spec: i.spec, Subject: i.subject, Title: i.title,
	}
	enc, err := encodeSwift(args)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"condition":"my AAPL\/MSFT spread widens","confirmationText":"Got it — I'll keep an eye on it and ping you when your AAPL\/MSFT spread widens.","firstRunAt":"2026-10-29T18:00:00Z","intervalMinutes":60,"scheduleHuman":"Every hour","spec":"Ping me when my AAPL\/MSFT spread widens","subject":"my AAPL\/MSFT spread widens","title":"Ping me when my AAPL\/MSFT spread widens"}`
	if enc != want {
		t.Fatalf("encoded =\n%s\nwant\n%s", enc, want)
	}
	if _, first, err := decodeWatchArguments(enc); err != nil || first.Hour() != 18 {
		t.Fatalf("decode: %v %v", first, err)
	}
}

// Ported from MessagingTelegramTests.swift (AssistantAddress).

func TestStripAssistantAddress(t *testing.T) {
	cases := map[string]string{
		"Hey Q, what did I spend last month?": "what did I spend last month?",
		"@Q summarise my month":               "summarise my month", //nolint:misspell // user text
		"Q: how is my portfolio doing":        "how is my portfolio doing",
		"q - log a coffee":                    "log a coffee",
		"  hi Q!  budget please ":             "budget please ",
	}
	for in, want := range cases {
		if got := stripAssistantAddress(in); got != want {
			t.Errorf("strip(%q) = %q, want %q", in, got, want)
		}
	}
	for _, same := range []string{
		"Q3 earnings for AAPL", "QQQ price?", "Quick question about my budget", "Qualcomm outlook", "QS is down again",
		"Q7KMPX3R", "Q", "Hey Q", "Q???", "norviq:confirm:4B1C2D3E-0000-4000-8000-000000000001",
		"hey there", "ok sounds good", "what did I spend?",
	} {
		if got := stripAssistantAddress(same); got != same {
			t.Errorf("strip(%q) = %q, want unchanged", same, got)
		}
	}
}

// Ported from PositionMemoTests.swift (PositionMemoAsk).

func TestParseMemoAsk(t *testing.T) {
	a := parseMemoAsk("Make DD about $GRAB (A6I), my average price is 4.1 euros and im down 34% so far")
	if a == nil || a.askedSymbol != "A6I" || a.companionSymbol == nil || *a.companionSymbol != "GRAB" ||
		a.cost == nil || *a.cost != 4.1 || *a.costCurrency != "EUR" || a.statedPercent == nil || *a.statedPercent != -34 {
		t.Fatalf("ask = %+v", a)
	}
	for _, no := range []string{"what's my budget", "should I sell", "/expenses this month"} {
		if parseMemoAsk(no) != nil {
			t.Errorf("%q parsed as a memo", no)
		}
	}
	if s := parseMemoAsk("/dd GRAB"); s == nil || s.askedSymbol != "GRAB" {
		t.Fatalf("slash = %+v", s)
	}
	if s := parseMemoAsk("should I sell AAPL"); s == nil || s.askedSymbol != "AAPL" {
		t.Fatalf("stance = %+v", s)
	}
	e := parseMemoAsk("Write a due diligence memo. GRAB cost 4.10 EUR")
	if e == nil || e.askedSymbol != "GRAB" || *e.cost != 4.10 || *e.costCurrency != "EUR" {
		t.Fatalf("expanded = %+v", e)
	}
}

// Ported from AIAssistantToolNudgeTests.swift.

func TestLooksLikeUnfulfilledIntent(t *testing.T) {
	s := func(v string) *string { return &v }
	for _, yes := range []string{"Let me check your latest expenses for you.", "One moment.", "I'll pull that up"} {
		if !looksLikeUnfulfilledIntent(s(yes)) {
			t.Errorf("%q should nudge", yes)
		}
	}
	long := "Let me check " + strings.Repeat("x", 200)
	for _, no := range []*string{nil, s(""), s("Hello! How can I help?"), &long} {
		if looksLikeUnfulfilledIntent(no) {
			t.Errorf("%v should not nudge", no)
		}
	}
}

func TestSummaryCopy(t *testing.T) {
	args := parseArgs(`{"title":"Lunch","amount":12.5,"occurred_on":"2026-10-01"}`)
	if got := confirmationSummary("create_expense", args); got != "Add the expense 'Lunch' for 12.50 on 2026-10-01." {
		t.Fatalf("summary = %q", got)
	}
	if got := confirmationSummary("delete_goal", parseArgs(`{"id":"4b1c2d3e-0000"}`)); got != "Permanently delete goal 4b1c2d3e." {
		t.Fatalf("summary = %q", got)
	}
	if got := confirmationSummary("set_terminal_scenario", parseArgs(`{"ticker":"amzn","valueWanted":1000000}`)); got != "Set the terminal scenario for AMZN: value wanted 1000000." {
		t.Fatalf("summary = %q", got)
	}
	if got := confirmationSummary("nope", nil); got != "Apply this change to your Norviq data." {
		t.Fatalf("fallback = %q", got)
	}
	if completionMessage("create_goal") != "Goal created." || completionMessage("nope") != "Done." {
		t.Fatal("completion copy")
	}
	for i := range catalog {
		if _, ok := summaryCopy(catalog[i].name, actionArgs{}); !ok && catalog[i].name != recordTradesToolName {
			t.Errorf("%s has no summary copy", catalog[i].name)
		}
		if _, ok := completionCopy(catalog[i].name); !ok {
			t.Errorf("%s has no completion copy", catalog[i].name)
		}
	}
}

func TestSwiftDoubleDescription(t *testing.T) {
	for v, want := range map[float64]string{100: "100.0", 12.34: "12.34", -5.5: "-5.5", 0: "0.0", 1e16: "1e+16"} {
		if got := swiftDoubleDescription(v); got != want {
			t.Errorf("%v = %q, want %q", v, got, want)
		}
	}
}
