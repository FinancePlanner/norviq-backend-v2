package realtime

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Ports RealtimeBusTests (GroupsRealtimeTests.swift).

func collect(s *Subscription) []Signal {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var out []Signal
	for {
		sig, ok := s.Next(ctx)
		if !ok {
			return out
		}
		out = append(out, sig)
	}
}

func envelope(name string) Envelope { return Envelope{Event: name, Data: "{}"} }

func TestRouting(t *testing.T) {
	bus := NewBus()
	ana, bo := uuid.New(), uuid.New()
	phone, laptop, other := bus.Subscribe(ana), bus.Subscribe(ana), bus.Subscribe(bo)
	bus.Publish(envelope("message.created"), map[uuid.UUID]struct{}{ana: {}})
	bus.FinishAll()
	for _, s := range []*Subscription{phone, laptop} {
		got := collect(s)
		if len(got) != 1 || got[0].Kind != SignalEvent || got[0].Envelope.Event != "message.created" {
			t.Fatalf("got %+v", got)
		}
	}
	if got := collect(other); len(got) != 0 {
		t.Fatalf("other got %+v", got)
	}
}

func TestConnectionCap(t *testing.T) {
	bus := NewBus()
	user := uuid.New()
	var subs []*Subscription
	for range MaxConnectionsPerUser + 1 {
		subs = append(subs, bus.Subscribe(user))
	}
	if n := bus.ConnectionCount(user); n != MaxConnectionsPerUser {
		t.Fatalf("count %d", n)
	}
	// The first stream is finished, so collecting it returns at once.
	if got := collect(subs[0]); len(got) != 0 {
		t.Fatalf("oldest got %+v", got)
	}
	bus.Unsubscribe(subs[1])
	if n := bus.ConnectionCount(user); n != MaxConnectionsPerUser-1 {
		t.Fatalf("count after unsubscribe %d", n)
	}
	bus.FinishAll()
	if n := bus.ConnectionCount(user); n != 0 {
		t.Fatalf("count after finish %d", n)
	}
}

func TestBufferKeepsNewest(t *testing.T) {
	s := newSubscription(uuid.New())
	for i := range BufferSize + 10 {
		s.Yield(Signal{Kind: SignalEvent, Envelope: Envelope{Event: string(rune('a' + i%26))}})
	}
	s.Finish()
	got := collect(s)
	if len(got) != BufferSize {
		t.Fatalf("buffered %d", len(got))
	}
	if got[0].Envelope.Event != string(rune('a'+10%26)) {
		t.Fatalf("oldest kept %q", got[0].Envelope.Event)
	}
}
