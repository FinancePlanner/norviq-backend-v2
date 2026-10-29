package groups

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/platform/community"
	"norviq-api/internal/platform/realtime"
	"norviq-api/internal/store/groupsdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// RealtimeStreamController.swift.
const (
	heartbeatInterval     = 20 * time.Second
	maxConnectionDuration = 600 * time.Second
	hiddenRefreshInterval = 60 * time.Second
)

// encodeEvent is RealtimeStreamController.encode: JSONEncoder.backendAPI,
// which also wrote "/" as "\/".
func encodeEvent(e GroupRealtimeEvent) string {
	raw, err := apijson.Marshal(e)
	if err != nil {
		return "{}"
	}
	return string(bytes.ReplaceAll(raw, []byte("/"), []byte(`\/`)))
}

// publisher is GroupsRealtime: publishes Groups events after the change
// committed.
type publisher struct {
	bus *realtime.Bus
	db  groupsdb.DBTX
	log *slog.Logger
}

// toMembersOf publishes to every member of groupID.
func (p *publisher) toMembersOf(ctx context.Context, e GroupRealtimeEvent, groupID uuid.UUID, authorID *uuid.UUID) {
	rows, err := groupsdb.New(p.db).MembersOfGroup(ctx, groupID)
	if err != nil {
		p.log.Warn("groups.realtime members lookup failed", "error", err)
		return
	}
	ids := map[uuid.UUID]struct{}{}
	for _, r := range rows {
		ids[r.UserID] = struct{}{}
	}
	p.to(e, ids, authorID)
}

func (p *publisher) to(e GroupRealtimeEvent, userIDs map[uuid.UUID]struct{}, authorID *uuid.UUID) {
	if len(userIDs) == 0 {
		return
	}
	p.bus.Publish(realtime.Envelope{Event: e.Type, Data: encodeEvent(e), AuthorID: authorID}, userIDs)
}

func uuidPtr(id uuid.UUID) *apijson.UUID { u := apijson.NewUUID(id); return &u }

func one(id uuid.UUID) map[uuid.UUID]struct{} { return map[uuid.UUID]struct{}{id: {}} }

// announce tells every member something about the group changed.
func (h *handler) announce(ctx context.Context, typ string, groupID uuid.UUID, channelID *uuid.UUID) {
	e := GroupRealtimeEvent{Type: typ, GroupID: uuidPtr(groupID)}
	if channelID != nil {
		e.ChannelID = uuidPtr(*channelID)
	}
	h.pub.toMembersOf(ctx, e, groupID, nil)
}

func (h *handler) removed(userID, groupID uuid.UUID, reason string) {
	r := reason
	h.pub.to(GroupRealtimeEvent{Type: EventMembershipRemoved, GroupID: uuidPtr(groupID), Reason: &r}, one(userID), nil)
}

// unwrapGzip returns the writer under echo's gzip middleware: Vapor
// disabled compression on the stream, since gzip holds small frames back.
func unwrapGzip(w http.ResponseWriter) http.ResponseWriter {
	if _, ok := w.(*echo.Response); ok {
		return w
	}
	if u, ok := w.(interface{ Unwrap() http.ResponseWriter }); ok {
		return u.Unwrap()
	}
	return w
}

// stream is GET /v1/realtime/stream: one server-sent-events stream per
// signed-in client. There is no replay; every connection opens with `ready`
// and the client catches up over REST.
func (h *handler) stream(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	w := unwrapGzip(c.Response())
	hdr := w.Header()
	hdr.Del("Vary")
	hdr.Set("Content-Type", "text/event-stream; charset=utf-8")
	hdr.Set("Cache-Control", "no-cache")
	hdr.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	_ = rc.Flush()

	ctx := c.Request().Context()
	sub := h.bus.Subscribe(v.UserID)
	clockCtx, stopClock := context.WithCancel(ctx)
	go runClock(clockCtx, sub, h.heartbeat, h.maxDuration)
	defer func() {
		stopClock()
		h.bus.Unsubscribe(sub)
	}()

	write := func(s string) error {
		if _, err := io.WriteString(w, s); err != nil {
			return err
		}
		return rc.Flush()
	}
	frame := func(event, data string) error { return write("event: " + event + "\ndata: " + data + "\n\n") }

	if err := write("retry: 2000\n\n"); err != nil {
		return nil
	}
	now := apijson.NewTime(h.s.now())
	if err := frame(EventReady, encodeEvent(GroupRealtimeEvent{Type: EventReady, ServerTime: &now})); err != nil {
		return nil
	}
	hidden, err := h.s.hidden(ctx, v.UserID)
	if err != nil {
		h.log.Debug("realtime.stream closed", "error", err)
		return nil
	}
	loadedAt := time.Now()
	for {
		sig, ok := sub.Next(ctx)
		if !ok {
			return nil
		}
		switch sig.Kind {
		case realtime.SignalHeartbeat:
			err = write(": ping\n\n")
		case realtime.SignalReconnect:
			err = frame(EventReconnect, "{}")
		default:
			if time.Since(loadedAt) > hiddenRefreshInterval {
				if hidden, err = h.s.hidden(ctx, v.UserID); err != nil {
					break
				}
				loadedAt = time.Now()
			}
			if a := sig.Envelope.AuthorID; a != nil {
				if _, skip := hidden[*a]; skip {
					continue
				}
			}
			err = frame(sig.Envelope.Event, sig.Envelope.Data)
		}
		if err != nil {
			h.log.Debug("realtime.stream closed", "error", err)
			return nil
		}
	}
}

// runClock yields a heartbeat every interval, and after maxDuration a
// reconnect, then finishes the stream.
func runClock(ctx context.Context, sub *realtime.Subscription, interval, maxDuration time.Duration) {
	start := time.Now()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if time.Since(start) >= maxDuration {
			sub.Yield(realtime.Signal{Kind: realtime.SignalReconnect})
			sub.Finish()
			return
		}
		sub.Yield(realtime.Signal{Kind: realtime.SignalHeartbeat})
	}
}
