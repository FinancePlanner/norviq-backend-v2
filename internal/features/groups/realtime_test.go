package groups_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/features/boards"
	"norviq-api/internal/features/groups"
	"norviq-api/internal/platform/realtime"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"
)

// Ports GroupsRealtimeRouteTests (GroupsRealtimeTests.swift) and checks the
// SSE framing of GET /v1/realtime/stream.

func nextEvent(t *testing.T, sub *realtime.Subscription) *realtime.Envelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		sig, ok := sub.Next(ctx)
		if !ok {
			return nil
		}
		if sig.Kind == realtime.SignalEvent {
			return &sig.Envelope
		}
	}
}

func (e *env) privateGroup(owner user, name, topic string) groups.GroupDetail {
	return e.create(owner, name, topic, nil, groups.VisibilityPrivate)
}

func TestMessageFanOut(t *testing.T) {
	e := newEnv(t)
	ana, bo := e.member("rtana"), e.member("rtbo")
	g := e.privateGroup(ana, "Dividend club", groups.TopicCommunity)
	ch := g.Channels[0].ID
	e.join(g.Group.ID, bo, ana)

	live := e.bus.Subscribe(bo.id)
	expect(t, e.post(ch, ana, "Ex-div date is Friday", "rt-1"), http.StatusCreated)
	env := nextEvent(t, live)
	if env == nil || env.Event != groups.EventMessageCreated || env.AuthorID == nil || *env.AuthorID != ana.id {
		t.Fatalf("envelope: %+v", env)
	}
	var ev groups.GroupRealtimeEvent
	if err := json.Unmarshal([]byte(env.Data), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Message == nil || ev.Message.Body == nil || *ev.Message.Body != "Ex-div date is Friday" || ev.ChannelID == nil || *ev.ChannelID != ch {
		t.Fatalf("event: %s", env.Data)
	}
	e.bus.Unsubscribe(live)

	to, sent := e.pushes.all()
	if len(to) != 1 || to[0] != bo.id {
		t.Fatalf("push: %v", to)
	}
	p := sent[0]
	gid, cid := g.Group.ID.String(), ch.String()
	if p.Category != groups.PushMessage || p.Title != "Dividend club · #general" || p.Body != "@grp_rtana: Ex-div date is Friday" ||
		p.Payload["deepLink"] != "financeplan://groups/"+gid+"/channels/"+cid || p.ThreadID != "group-channel-"+cid {
		t.Fatalf("push: %+v", p)
	}

	// Without Redis there is no throttle.
	e.post(ch, ana, "and pays on the 30th", "rt-2")
	if to, _ := e.pushes.all(); len(to) != 2 {
		t.Fatalf("pushes %d", len(to))
	}
}

func TestPushRulesAndReadSync(t *testing.T) {
	e := newEnv(t)
	ana, bo := e.member("mute"), e.member("mutebo")
	g := e.privateGroup(ana, "Quiet room", groups.TopicCustom)
	ch := g.Channels[0].ID
	e.join(g.Group.ID, bo, ana)
	expect(t, e.do(http.MethodPut, "/v1/groups/"+g.Group.ID.String()+"/notifications", bo, map[string]any{"pushEnabled": false}), http.StatusOK)

	e.post(ch, ana, "hello", "m-1")
	if to, _ := e.pushes.all(); len(to) != 0 {
		t.Fatalf("muted member pushed: %v", to)
	}

	reader := e.bus.Subscribe(bo.id)
	expect(t, e.do(http.MethodPost, "/v1/group-channels/"+ch.String()+"/read", bo, map[string]any{"seq": 1}), http.StatusNoContent)
	if env := nextEvent(t, reader); env == nil || env.Event != groups.EventChannelRead || !strings.Contains(env.Data, `"seq":1`) {
		t.Fatalf("read sync: %+v", env)
	}
	e.bus.Unsubscribe(reader)
}

func TestStreamFraming(t *testing.T) {
	t.Setenv("SOCIAL_ENABLED", "1")
	t.Setenv("GROUPS_ENABLED", "1")
	bus := realtime.NewBus()
	p := &fakePusher{}
	app := testutil.NewApp(t, []server.Feature{
		boards.Feature{Push: p, Discord: &fakeDiscord{}},
		groups.Feature{
			Push: p, Discord: &fakeDiscord{}, Bus: bus, WaitPush: func(done <-chan struct{}) { <-done },
			Heartbeat: 150 * time.Millisecond, MaxStream: 400 * time.Millisecond,
		},
	})
	e := &env{t: t, app: app, pushes: p, bus: bus}
	ana, bo := e.member("sse"), e.member("ssebo")
	g := e.privateGroup(ana, "Stream", groups.TopicCommunity)
	e.join(g.Group.ID, bo, ana)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, app.Server.URL+"/v1/realtime/stream", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+bo.token)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := app.Server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream; charset=utf-8" ||
		resp.Header.Get("Cache-Control") != "no-cache" || resp.Header.Get("X-Accel-Buffering") != "no" ||
		resp.Header.Get("Content-Encoding") != "" {
		t.Fatalf("headers: %d %v", resp.StatusCode, resp.Header)
	}
	r := bufio.NewReader(resp.Body)
	frame := func() string {
		t.Helper()
		var b strings.Builder
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				t.Fatalf("read: %v (%q)", err, b.String())
			}
			b.WriteString(line)
			if line == "\n" {
				return b.String()
			}
		}
	}
	if f := frame(); f != "retry: 2000\n\n" {
		t.Fatalf("first frame %q", f)
	}
	if f := frame(); !strings.HasPrefix(f, "event: ready\ndata: {\"type\":\"ready\"") || !strings.Contains(f, `"serverTime":"`) {
		t.Fatalf("ready frame %q", f)
	}
	// Wait for the subscription before publishing.
	for range 100 {
		if bus.ConnectionCount(bo.id) == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	expect(t, e.post(g.Channels[0].ID, ana, "a/b", "s-1"), http.StatusCreated)
	var sawEvent, sawPing, sawReconnect bool
	for !sawReconnect {
		f := frame()
		switch {
		case strings.HasPrefix(f, "event: message.created\ndata: "):
			if !strings.Contains(f, `"body":"a\/b"`) {
				t.Fatalf("event frame %q", f)
			}
			sawEvent = true
		case f == ": ping\n\n":
			sawPing = true
		case f == "event: reconnect\ndata: {}\n\n":
			sawReconnect = true
		default:
			t.Fatalf("unexpected frame %q", f)
		}
	}
	if !sawEvent || !sawPing {
		t.Fatalf("event %v ping %v", sawEvent, sawPing)
	}
	if _, err := r.ReadString('\n'); err == nil {
		t.Fatal("stream still open after reconnect")
	}
}
