// Package realtime is RealtimeBus.swift: the in-process fan-out behind
// GET /v1/realtime/stream. Delivery is best effort; a client that misses an
// event catches up over REST when its next stream opens, so nothing here is
// durable.
//
// Like Vapor's InProcessRealtimeBus, it is only correct with one API replica:
// nothing is published to Redis, so the stream and every route that publishes
// to it must be served by the same process.
package realtime

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

const (
	// MaxConnectionsPerUser: a user with more tabs and devices loses the
	// oldest one.
	MaxConnectionsPerUser = 5
	// BufferSize: a slow client drops the oldest events past this.
	BufferSize = 256
)

// Envelope is one event on a user's stream. Data is the encoded JSON
// payload; AuthorID lets each connection drop events from people hidden
// from it.
type Envelope struct {
	Event    string
	Data     string
	AuthorID *uuid.UUID
}

// SignalKind distinguishes forwarded events from the connection's own
// heartbeat and close signals.
type SignalKind int

const (
	SignalEvent SignalKind = iota
	SignalHeartbeat
	SignalReconnect
)

// Signal is RealtimeSignal.
type Signal struct {
	Kind     SignalKind
	Envelope Envelope
}

// Subscription is one open connection: an AsyncStream buffering the newest
// BufferSize signals.
type Subscription struct {
	UserID       uuid.UUID
	ConnectionID uuid.UUID

	mu       sync.Mutex
	items    []Signal
	finished bool
	notify   chan struct{}
}

func newSubscription(userID uuid.UUID) *Subscription {
	return &Subscription{UserID: userID, ConnectionID: uuid.New(), notify: make(chan struct{}, 1)}
}

// Yield buffers s, dropping the oldest past BufferSize. A finished stream
// ignores it.
func (s *Subscription) Yield(sig Signal) {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return
	}
	s.items = append(s.items, sig)
	if len(s.items) > BufferSize {
		s.items = s.items[len(s.items)-BufferSize:]
	}
	s.mu.Unlock()
	s.wake()
}

// Finish ends the stream once its buffer drains.
func (s *Subscription) Finish() {
	s.mu.Lock()
	s.finished = true
	s.mu.Unlock()
	s.wake()
}

func (s *Subscription) wake() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// Next waits for the next signal. It returns false once the stream is
// finished and drained, or ctx ends.
func (s *Subscription) Next(ctx context.Context) (Signal, bool) {
	for {
		s.mu.Lock()
		if len(s.items) > 0 {
			sig := s.items[0]
			s.items = s.items[1:]
			s.mu.Unlock()
			return sig, true
		}
		done := s.finished
		s.mu.Unlock()
		if done {
			return Signal{}, false
		}
		select {
		case <-s.notify:
		case <-ctx.Done():
			return Signal{}, false
		}
	}
}

// Bus is InProcessRealtimeBus.
type Bus struct {
	mu          sync.Mutex
	connections map[uuid.UUID][]*Subscription
}

// NewBus returns an empty bus.
func NewBus() *Bus { return &Bus{connections: map[uuid.UUID][]*Subscription{}} }

// Subscribe opens a connection for userID, closing the oldest past
// MaxConnectionsPerUser.
func (b *Bus) Subscribe(userID uuid.UUID) *Subscription {
	sub := newSubscription(userID)
	b.mu.Lock()
	defer b.mu.Unlock()
	open := b.connections[userID]
	for len(open) >= MaxConnectionsPerUser {
		open[0].Finish()
		open = open[1:]
	}
	b.connections[userID] = append(append([]*Subscription{}, open...), sub)
	return sub
}

// Unsubscribe finishes sub and forgets it.
func (b *Bus) Unsubscribe(sub *Subscription) {
	sub.Finish()
	b.mu.Lock()
	defer b.mu.Unlock()
	open := b.connections[sub.UserID]
	kept := make([]*Subscription, 0, len(open))
	for _, s := range open {
		if s.ConnectionID != sub.ConnectionID {
			kept = append(kept, s)
		}
	}
	if len(kept) == 0 {
		delete(b.connections, sub.UserID)
	} else {
		b.connections[sub.UserID] = kept
	}
}

// Publish yields env to every connection of userIDs.
func (b *Bus) Publish(env Envelope, userIDs map[uuid.UUID]struct{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id := range userIDs {
		for _, s := range b.connections[id] {
			s.Yield(Signal{Kind: SignalEvent, Envelope: env})
		}
	}
}

// FinishAll ends every open stream, so a shutting-down pod lets its clients
// reconnect to the new one straight away.
func (b *Bus) FinishAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, subs := range b.connections {
		for _, s := range subs {
			s.Finish()
		}
	}
	b.connections = map[uuid.UUID][]*Subscription{}
}

// ConnectionCount is the number of open connections for userID.
func (b *Bus) ConnectionCount(userID uuid.UUID) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.connections[userID])
}

var (
	sharedMu sync.Mutex
	shared   = map[any]*Bus{}
)

// Shared returns the process bus for key (the server's Deps), creating it
// on first use, as Application.realtimeBus did.
func Shared(key any) *Bus {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	b, ok := shared[key]
	if !ok {
		b = NewBus()
		shared[key] = b
	}
	return b
}

// FinishAll ends every stream on every shared bus (RealtimeShutdown). The
// HTTP server calls it when shutdown begins.
func FinishAll() {
	sharedMu.Lock()
	buses := make([]*Bus, 0, len(shared))
	for _, b := range shared {
		buses = append(buses, b)
	}
	sharedMu.Unlock()
	for _, b := range buses {
		b.FinishAll()
	}
}
