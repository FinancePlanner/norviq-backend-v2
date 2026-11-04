package assistant

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"norviq-api/internal/server"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) turnCounts(t *testing.T) []int {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []int
	for line := range bytes.Lines(l.b.Bytes()) {
		var e struct {
			Msg      string `json:"msg"`
			InFlight int    `json:"in_flight"`
		}
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatal(err)
		}
		if e.Msg == "telegram_turn_started" {
			out = append(out, e.InFlight)
		}
	}
	return out
}

// Each dispatched turn logs how many are running. No flood has been observed,
// and this is how one would be: it neither caps nor drops an update.
func TestDispatchLogsInFlightTurns(t *testing.T) {
	logs := &lockedBuffer{}
	th := newTGHarness(t, nil, nil, func(d *server.Deps) {
		d.Logger = slog.New(slog.NewJSONHandler(logs, nil))
	})

	// Holding the stub's lock parks every turn at its first Telegram call, so
	// the first turn is still running when the second one starts.
	th.tg.mu.Lock()
	th.text(101, "first")
	th.text(102, "second")
	counts := logs.turnCounts(t)
	th.tg.mu.Unlock()
	if len(counts) != 2 || counts[0] != 1 || counts[1] != 2 {
		t.Fatalf("in_flight per turn = %v, want [1 2]", counts)
	}

	// Both chats still got an answer: the log drops nothing.
	chats := map[string]bool{}
	for _, c := range th.tg.sent(t, 2) {
		chats[fmt.Sprint(c.body["chat_id"])] = true
	}
	if !chats["101"] || !chats["102"] {
		t.Fatalf("answered chats %v, want 101 and 102", chats)
	}
}
