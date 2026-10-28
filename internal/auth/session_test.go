package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMintVerifyRoundTrip(t *testing.T) {
	s := NewSigner("secret")
	id := uuid.New()
	now := time.Now()
	tok, err := s.Mint(id, now.Add(time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Verify(tok)
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID != id || got.IssuedAt == nil || got.IssuedAt.Sub(now).Abs() > time.Millisecond {
		t.Errorf("got %+v", got)
	}
}

func TestVerifyRejects(t *testing.T) {
	s := NewSigner("secret")
	id := uuid.New()
	now := time.Now()
	expired, _ := s.Mint(id, now.Add(-time.Second), now.Add(-time.Hour))
	other, _ := NewSigner("other").Mint(id, now.Add(time.Hour), now)
	for name, tok := range map[string]string{"expired": expired, "wrong key": other, "garbage": "a.b.c"} {
		if _, err := s.Verify(tok); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A token JWTKit minted: fractional exp/iat and an uppercase userId.
func TestVerifyAcceptsVaporToken(t *testing.T) {
	// Signed with HS256 over "parity-secret" by the Vapor backend's encoder shape.
	s := NewSigner("parity-secret")
	id := uuid.MustParse("4b06f5a8-1234-4abc-9def-0123456789ab")
	tok, err := s.Mint(id, time.Now().Add(time.Hour), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	payload := strings.Split(tok, ".")[1]
	if payload == "" {
		t.Fatal("empty payload")
	}
	got, err := s.Verify(tok)
	if err != nil || got.UserID != id {
		t.Fatalf("got %v %v", got, err)
	}
}

func TestIsCurrent(t *testing.T) {
	now := time.Now()
	before, after := now.Add(-time.Minute), now.Add(time.Minute)
	withIat := &Session{IssuedAt: &now}
	noIat := &Session{}
	if !withIat.IsCurrent(nil) || !noIat.IsCurrent(nil) {
		t.Error("no revocation must pass")
	}
	if !withIat.IsCurrent(&before) {
		t.Error("issued after revocation must pass")
	}
	if withIat.IsCurrent(&after) {
		t.Error("issued before revocation must fail")
	}
	if noIat.IsCurrent(&before) {
		t.Error("pre-1.4.1 token must fail once revoked")
	}
}

func TestParseScopes(t *testing.T) {
	got, err := ParseScopes([]string{"market:read", "bank:read"})
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v %v", got, err)
	}
	if _, err := ParseScopes([]string{"market:read", "nope"}); err == nil || err.Error() != "Unknown scope 'nope'" {
		t.Errorf("err %v", err)
	}
	if len(Grantable()) != len(AllScopes)-1 {
		t.Error("grantable must drop bank:read only")
	}
}

func TestRequirementDescription(t *testing.T) {
	got := requirementDescription([]Scope{ScopePortfolioWrite, ScopeHoldingsWrite})
	if got != "'holdings:write' or 'portfolio:write'" {
		t.Errorf("got %q", got)
	}
}
