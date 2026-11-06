package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"norviq-api/internal/platform/ai"
	"norviq-api/internal/testutil"

	"github.com/labstack/echo/v5"
	"github.com/redis/go-redis/v9"
)

const (
	largeBusyReason = "Too many large uploads right now. Try again in a moment."
	// largeImage alone puts a form over the 4 MB line.
	largeImage = 5 << 20
)

// largeTurnRig parks large image turns inside the model until released.
type largeTurnRig struct {
	*turnHarness
	token, conv string
	contentType string
	form        []byte
	entered     chan struct{}

	mu   sync.Mutex
	gate chan struct{}
}

type turnReply struct {
	status int
	body   []byte
	err    error
}

func newLargeTurnRig(t *testing.T) *largeTurnRig {
	t.Helper()
	return newLargeTurnRigWith(t, imagesOn)
}

func newLargeTurnRigWith(t *testing.T, env map[string]string) *largeTurnRig {
	t.Helper()
	th := newTurnHarness(t, env)
	user, conv := th.proConversation("large@example.com")
	r := &largeTurnRig{turnHarness: th, token: th.Token(user), conv: conv, entered: make(chan struct{})}
	r.contentType, r.form = th.multipartForm(parkedTurnText, fakePNG(largeImage))
	th.client.before = func(ctx context.Context, messages []ai.Message) error {
		r.mu.Lock()
		gate := r.gate
		r.mu.Unlock()
		if gate == nil || !parkedTurn(messages) {
			return nil
		}
		select {
		case r.entered <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-gate:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// Before the harness's server closes, which waits for every parked turn.
	t.Cleanup(r.open)
	return r
}

// parkedTurnText marks the rig's own large turns. The gate re-encodes every
// image, so a form's size no longer shows in what the model receives.
const parkedTurnText = "park"

func parkedTurn(messages []ai.Message) bool {
	last := lastMessage(messages)
	return last.Role == "user" && last.Content != nil && strings.HasPrefix(*last.Content, parkedTurnText)
}

func (r *largeTurnRig) open() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gate != nil {
		close(r.gate)
		r.gate = nil
	}
}

func (r *largeTurnRig) arm() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gate == nil {
		r.gate = make(chan struct{})
	}
}

// script tops up the model's answers.
func (r *largeTurnRig) script(n int) {
	r.client.mu.Lock()
	defer r.client.mu.Unlock()
	r.client.replies = nil
	for range n {
		r.client.replies = append(r.client.replies, say("ok"))
	}
}

func (r *largeTurnRig) path(route string) string {
	return "/v1/ai/assistant/conversations/" + r.conv + "/" + route
}

// post sends a turn. A plain io.Reader body has no known length, so it goes out
// chunked with no Content-Length.
func (r *largeTurnRig) post(ctx context.Context, route, contentType string, body io.Reader) turnReply {
	// A turn that should have been refused and parks instead fails, not hangs.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.Server.URL+r.path(route), body)
	if err != nil {
		return turnReply{err: err}
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+r.token)
	resp, err := r.Server.Client().Do(req)
	if err != nil {
		return turnReply{err: err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return turnReply{status: resp.StatusCode, body: data, err: err}
}

func chunked(data []byte) io.Reader { return struct{ io.Reader }{bytes.NewReader(data)} }

func (r *largeTurnRig) sendLarge(ctx context.Context, route string) turnReply {
	return r.post(ctx, route, r.contentType, bytes.NewReader(r.form))
}

// occupy parks one large turn on each route, retrying while a slot is still on its
// way back, and returns a func that lets them finish. The two parking is itself
// the proof that two slots are free.
func (r *largeTurnRig) occupy() (finish func() []turnReply) {
	r.T.Helper()
	r.arm()
	done := make(chan turnReply, 2)
	for _, route := range []string{"chat", "stream"} {
		deadline := time.Now().Add(15 * time.Second)
	retry:
		for {
			attempt := make(chan turnReply, 1)
			go func() { attempt <- r.sendLarge(context.Background(), route) }()
			select {
			case <-r.entered:
				go func() { done <- <-attempt }()
				break retry
			case got := <-attempt:
				if got.status != http.StatusServiceUnavailable {
					r.T.Fatalf("%s while occupying = %d %v %s", route, got.status, got.err, got.body)
				}
				if time.Now().After(deadline) {
					r.T.Fatalf("%s: slot never came back", route)
				}
				time.Sleep(20 * time.Millisecond)
			case <-time.After(30 * time.Second):
				r.T.Fatalf("%s never reached the model", route)
			}
		}
	}
	return func() []turnReply {
		r.open()
		return []turnReply{<-done, <-done}
	}
}

func refusedBusy(t *testing.T, name string, got turnReply) {
	t.Helper()
	if got.err != nil || got.status != http.StatusServiceUnavailable {
		t.Fatalf("%s = %d %v %s", name, got.status, got.err, got.body)
	}
	var envelope struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(got.body, &envelope); err != nil {
		t.Fatalf("%s: %v in %s", name, err, got.body)
	}
	if envelope.Reason != largeBusyReason {
		t.Fatalf("%s reason = %q", name, envelope.Reason)
	}
}

// slotsAreFree occupies both slots, shows a third is refused, and lets them go.
func (r *largeTurnRig) slotsAreFree(t *testing.T) {
	t.Helper()
	r.script(4)
	finish := r.occupy()
	refusedBusy(t, "third turn", r.sendLarge(context.Background(), "chat"))
	for i, got := range finish() {
		if got.err != nil || got.status != 200 {
			t.Fatalf("parked turn %d = %d %v %s", i, got.status, got.err, got.body)
		}
	}
}

func TestLargeTurnsTakeASlotAndAThirdIsRefused(t *testing.T) {
	r := newLargeTurnRig(t)
	ctx := context.Background()
	r.script(50)
	finish := r.occupy()
	calls := r.client.calls()

	refusedBusy(t, "chat", r.sendLarge(ctx, "chat"))
	stream := r.sendLarge(ctx, "stream")
	refusedBusy(t, "stream", stream) // a real status, not a frame inside a 200
	if bytes.Contains(stream.body, []byte("event:")) {
		t.Fatalf("stream refusal carried frames: %s", stream.body)
	}
	// No Content-Length, whatever it turns out to hold.
	smallType, smallForm := r.multipartForm("x", fakePNG(256))
	refusedBusy(t, "no Content-Length", r.post(ctx, "chat", smallType, chunked(smallForm)))

	// The line is over 4 MB, so exactly 4 MB takes no slot.
	overhead := len(smallForm) - 256
	exactType, exact := r.multipartForm("x", fakePNG(4<<20-overhead))
	if len(exact) != 4<<20 {
		t.Fatalf("form = %d bytes", len(exact))
	}
	overType, over := r.multipartForm("x", fakePNG(4<<20-overhead+1))
	refusedBusy(t, "one byte over", r.post(ctx, "chat", overType, bytes.NewReader(over)))
	if r.client.calls() != calls {
		t.Fatalf("refused turns reached the model: %d calls, was %d", r.client.calls(), calls)
	}

	if got := r.post(ctx, "chat", exactType, bytes.NewReader(exact)); got.status != 200 {
		t.Fatalf("exactly 4 MB = %d %v %s", got.status, got.err, got.body)
	}
	if got := r.post(ctx, "chat", smallType, bytes.NewReader(smallForm)); got.status != 200 {
		t.Fatalf("small image = %d %v %s", got.status, got.err, got.body)
	}
	if got := r.post(ctx, "chat", "application/json", strings.NewReader(`{"content":"hello"}`)); got.status != 200 {
		t.Fatalf("text turn = %d %v %s", got.status, got.err, got.body)
	}

	for i, got := range finish() {
		if got.err != nil || got.status != 200 {
			t.Fatalf("parked turn %d = %d %v %s", i, got.status, got.err, got.body)
		}
	}
	// With a slot free, no Content-Length is an ordinary turn.
	if got := r.post(ctx, "chat", smallType, chunked(smallForm)); got.status != 200 {
		t.Fatalf("no Content-Length, free slot = %d %v %s", got.status, got.err, got.body)
	}
}

func TestLargeTurnSlotsFreeOnEveryExit(t *testing.T) {
	r := newLargeTurnRig(t)
	ctx := context.Background()
	img := fakePNG(2 << 20)
	tooManyType, tooMany := r.multipartForm("x", img, img, img, img)
	for name, exit := range map[string]struct {
		script int
		run    func() turnReply
		status int
	}{
		"success, chat":    {script: 2, run: func() turnReply { return r.sendLarge(ctx, "chat") }, status: 200},
		"success, stream":  {script: 2, run: func() turnReply { return r.sendLarge(ctx, "stream") }, status: 200},
		"model fails":      {run: func() turnReply { return r.sendLarge(ctx, "chat") }, status: 500},
		"model fails, sse": {run: func() turnReply { return r.sendLarge(ctx, "stream") }, status: 200},
		"refused form": {run: func() turnReply {
			return r.post(ctx, "chat", tooManyType, bytes.NewReader(tooMany))
		}, status: 400},
	} {
		t.Run(name, func(t *testing.T) {
			r.script(exit.script)
			// Twice: a leak per turn then leaves no slot for the check.
			for range 2 {
				if got := exit.run(); got.err != nil || got.status != exit.status {
					t.Fatalf("status = %d %v %s", got.status, got.err, got.body)
				}
			}
			r.slotsAreFree(t)
		})
	}
}

func TestLargeTurnSlotsFreeWhenTheClientDrops(t *testing.T) {
	t.Run("during the turn", func(t *testing.T) {
		r := newLargeTurnRig(t)
		r.script(4)
		r.arm()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var wg sync.WaitGroup
		for _, route := range []string{"chat", "stream"} {
			wg.Go(func() { r.sendLarge(ctx, route) })
			select {
			case <-r.entered:
			case <-time.After(30 * time.Second):
				t.Fatalf("%s never reached the model", route)
			}
		}
		refusedBusy(t, "while both run", r.sendLarge(context.Background(), "chat"))
		cancel()
		wg.Wait()
		r.slotsAreFree(t)
	})

	t.Run("during the upload", func(t *testing.T) {
		r := newLargeTurnRig(t)
		spy := &limiterSpy{}
		r.Deps.Limiter.Redis = spy
		r.script(50)
		smallType, smallForm := r.multipartForm("x", fakePNG(256))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var wg sync.WaitGroup
		var pipes []*io.PipeWriter
		for i, route := range []string{"chat", "stream"} {
			pr, pw := io.Pipe()
			pipes = append(pipes, pw)
			wg.Go(func() { r.post(ctx, route, r.contentType, pr) })
			wg.Go(func() { _, _ = pw.Write(r.form[:1024]) })
			// One at a time and before any probe: a stalled upload refused now would
			// sit in net/http draining a body that never comes.
			spy.waitForImageTurns(t, int64(i+1))
		}
		// Both stalled, neither past 1 KB, and a further turn is refused: the slot was
		// taken before the body was read. A probe that beat a slot to it just finishes.
		deadline := time.Now().Add(15 * time.Second)
		for {
			got := r.post(context.Background(), "chat", smallType, chunked(smallForm))
			if got.status == http.StatusServiceUnavailable {
				break
			}
			if got.status != 200 || time.Now().After(deadline) {
				t.Fatalf("probe = %d %v %s", got.status, got.err, got.body)
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
		// The transport won't return until its write loop leaves the body.
		for _, pw := range pipes {
			_ = pw.CloseWithError(context.Canceled)
		}
		wg.Wait()
		r.slotsAreFree(t)
	})
}

// limiterSpy lets every request through and counts those reaching the image
// limiter, the step right before the slot.
type limiterSpy struct {
	redis.UniversalClient
	imageTurns atomic.Int64
}

func (s *limiterSpy) EvalSha(_ context.Context, _ string, keys []string, _ ...any) *redis.Cmd {
	if strings.HasPrefix(keys[0], imageTurnLimitKey) {
		s.imageTurns.Add(1)
	}
	return redis.NewCmdResult(int64(1), nil)
}

func (s *limiterSpy) waitForImageTurns(t *testing.T, n int64) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for s.imageTurns.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d turns reached the limiter", s.imageTurns.Load(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // and on through the one step after it
}

func TestOversizedJSONTurnIs413WithImagesOn(t *testing.T) {
	r := newLargeTurnRig(t)
	body := bytes.Repeat([]byte{' '}, 12<<20)
	for _, route := range []string{"chat", "stream"} {
		ctx := context.Background()
		for name, sent := range map[string]io.Reader{"sized": bytes.NewReader(body), "chunked": chunked(body)} {
			got := r.post(ctx, route, "application/json", sent)
			if got.err != nil || got.status != http.StatusRequestEntityTooLarge {
				t.Fatalf("%s %s = %d %v %.200s", route, name, got.status, got.err, got.body)
			}
		}
	}
	if r.client.calls() != 0 {
		t.Fatalf("model calls = %d", r.client.calls())
	}
}

// The raw form goes as soon as it is parsed; the images are their own copies.
func TestParseTurnBodyDropsTheRawForm(t *testing.T) {
	th := &turnHarness{App: &testutil.App{T: t}}
	img := fakePNG(256)
	contentType, form := th.multipartForm("hello", img)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", bytes.NewReader(form))
	req.Header.Set("Content-Type", contentType)
	c := echo.New().NewContext(req, httptest.NewRecorder())

	content, files, _, err := parseTurnBody(c)
	images := files.Images
	if err != nil {
		t.Fatal(err)
	}
	if req.Body != http.NoBody {
		t.Fatalf("request still holds the form: %T", req.Body)
	}
	for i := range form {
		form[i] = 0
	}
	if content != "hello" || len(images) != 1 || !bytes.Equal(images[0].Data, pngCore) {
		t.Fatalf("content %q, %d images", content, len(images))
	}
}

// A PDF can be 8 MB and the slot is taken before the body is read, so with
// documents on a 2 MB turn takes one; with them off it is below the 4 MB line.
func TestDocumentsEnabledLowersLargeTurnThreshold(t *testing.T) {
	for name, tc := range map[string]struct {
		env    map[string]string
		status int
	}{
		"documents on":  {docsOn, http.StatusServiceUnavailable},
		"documents off": {imagesOn, http.StatusOK},
	} {
		t.Run(name, func(t *testing.T) {
			r := newLargeTurnRigWith(t, tc.env)
			r.script(10)
			finish := r.occupy()
			contentType, form := r.multipartForm("x", fakePNG(2<<20))
			got := r.post(context.Background(), "chat", contentType, bytes.NewReader(form))
			if got.err != nil || got.status != tc.status {
				t.Fatalf("2 MB turn = %d %v %s", got.status, got.err, got.body)
			}
			if tc.status == http.StatusServiceUnavailable {
				refusedBusy(t, "2 MB turn", got)
			}
			for i, done := range finish() {
				if done.err != nil || done.status != 200 {
					t.Fatalf("parked turn %d = %d %v %s", i, done.status, done.err, done.body)
				}
			}
		})
	}
}
