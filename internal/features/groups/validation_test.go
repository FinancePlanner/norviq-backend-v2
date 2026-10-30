package groups

import (
	"strings"
	"testing"
	"time"
)

// Ports GroupsValidationTests (GroupsTests.swift).

func sp(s string) *string { return &s }

func ip(n int) *int { return &n }

func TestTickers(t *testing.T) {
	ok := func(raw, topic, want string) {
		t.Helper()
		got, err := validateTicker(sp(raw), topic)
		if err != nil || got == nil || *got != want {
			t.Fatalf("ticker(%q) = %v, %v; want %q", raw, got, err, want)
		}
	}
	ok("$aapl", TopicStock, "AAPL")
	ok(" brk.b ", TopicStock, "BRK.B")
	ok("btc-usd", TopicCrypto, "BTC-USD")
	if got, err := validateTicker(sp(""), TopicStock); err != nil || got != nil {
		t.Fatalf("empty ticker: %v %v", got, err)
	}
	for _, c := range []struct{ raw, topic string }{{"AAPL!", TopicStock}, {"-AAPL", TopicStock}, {"AAPL", TopicCommunity}} {
		if _, err := validateTicker(sp(c.raw), c.topic); err == nil {
			t.Fatalf("ticker(%q, %s) should fail", c.raw, c.topic)
		}
	}
}

func TestChannelNames(t *testing.T) {
	for _, c := range []struct{ raw, want string }{{" Earnings Talk ", "earnings-talk"}, {"off_topic", "off-topic"}} {
		if got, err := validateChannelName(c.raw); err != nil || got != c.want {
			t.Fatalf("channelName(%q) = %q, %v", c.raw, got, err)
		}
	}
	for _, raw := range []string{"", "ação", strings.Repeat("a", 33)} {
		if _, err := validateChannelName(raw); err == nil {
			t.Fatalf("channelName(%q) should fail", raw)
		}
	}
}

func TestBounds(t *testing.T) {
	if got, err := validateGroupName("  $AAPL   holders "); err != nil || got != "$AAPL holders" {
		t.Fatalf("groupName: %q %v", got, err)
	}
	if _, err := validateGroupName("a"); err == nil {
		t.Fatal("short name accepted")
	}
	for _, e := range []string{"🍎", "👨‍👩‍👧"} {
		if got, err := validateIconEmoji(sp(e)); err != nil || got == nil || *got != e {
			t.Fatalf("icon %q: %v %v", e, got, err)
		}
	}
	if got, err := validateIconEmoji(sp(" ")); err != nil || got != nil {
		t.Fatalf("blank icon: %v %v", got, err)
	}
	for _, bad := range []string{"A", "7", "🍎🍏"} {
		if _, err := validateIconEmoji(sp(bad)); err == nil {
			t.Fatalf("icon %q accepted", bad)
		}
	}
	if _, err := validateMessageBody("   "); err == nil {
		t.Fatal("blank body accepted")
	}
	if _, err := validateMessageBody(strings.Repeat("x", 2001)); err == nil {
		t.Fatal("long body accepted")
	}
	if got, err := validateMessageBody(" hi \n"); err != nil || got != "hi" {
		t.Fatalf("body: %q %v", got, err)
	}
}

func TestInviteOptions(t *testing.T) {
	now := time.Unix(1_790_000_000, 0)
	exp, uses, err := validateInvite(defaultInvite(), now)
	if err != nil || exp == nil || !exp.Equal(now.Add(168*time.Hour)) || uses != nil {
		t.Fatalf("default: %v %v %v", exp, uses, err)
	}
	if exp, _, err := validateInvite(inviteOptions{MaxUses: ip(5)}, now); err != nil || exp != nil {
		t.Fatalf("never expires: %v %v", exp, err)
	}
	for _, o := range []inviteOptions{{ExpiresInHours: ip(0)}, {ExpiresInHours: ip(721)}, {ExpiresInHours: ip(1), MaxUses: ip(0)}} {
		if _, _, err := validateInvite(o, now); err == nil {
			t.Fatalf("invite %+v accepted", o)
		}
	}
}

func TestContentFilter(t *testing.T) {
	for _, s := range []string{"you f4gg0t", "N1GGGG3R!!", "Wéťback"} {
		if contentAllowed(s, nil) {
			t.Fatalf("%q allowed", s)
		}
	}
	for _, s := range []string{"Niger's bond yields", "Scunthorpe United", "pros and cons of $AAPL", "buy the dip"} {
		if !contentAllowed(s, nil) {
			t.Fatalf("%q rejected", s)
		}
	}
	if contentAllowed("total scam coin", []string{"scam coin"}) {
		t.Fatal("extra term missed")
	}
	if !contentAllowed("scam", []string{"scam coin"}) {
		t.Fatal("partial extra term rejected")
	}
}

func TestInviteCodes(t *testing.T) {
	for range 50 {
		c := generateInviteCode()
		if len(c) != 10 || !inviteCodeWellFormed(c) {
			t.Fatalf("bad code %q", c)
		}
	}
	if inviteCodeWellFormed("abc") || inviteCodeWellFormed("abc def!") {
		t.Fatal("malformed code accepted")
	}
}

func TestEncodeEventEscapesSlashes(t *testing.T) {
	raw := encodeEvent(GroupRealtimeEvent{Type: EventMessageCreated, Message: &GroupMessage{Body: sp("a/b")}})
	if !strings.Contains(raw, `a\/b`) {
		t.Fatalf("slash not escaped: %s", raw)
	}
}
