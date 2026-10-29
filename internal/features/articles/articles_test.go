package articles

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// stubVerifier is StubTickerVerifier.
type stubVerifier struct {
	missing, unavailable map[string]bool
}

func (s *stubVerifier) Check(_ context.Context, symbol string) TickerCheck {
	if s.missing[symbol] {
		return TickerMissing
	}
	if s.unavailable[symbol] {
		return TickerUnknown
	}
	return TickerExists
}

type nopDiscord struct{}

func (nopDiscord) Send(context.Context, string) error { return nil }

type kit struct {
	t        *testing.T
	app      *testutil.App
	verifier *stubVerifier
}

type member struct {
	id    uuid.UUID
	token string
}

func newKit(t *testing.T, enabled bool) *kit {
	t.Helper()
	if enabled {
		t.Setenv("ARTICLES_ENABLED", "true")
	} else {
		t.Setenv("ARTICLES_ENABLED", "")
	}
	v := &stubVerifier{missing: map[string]bool{"ZZZZ": true}, unavailable: map[string]bool{}}
	app := testutil.NewApp(t, []server.Feature{Feature{Verifier: v, Discord: nopDiscord{}}})
	return &kit{t: t, app: app, verifier: v}
}

func (k *kit) exec(sql string, args ...any) {
	k.t.Helper()
	if _, err := k.app.Deps.DB.Exec(context.Background(), sql, args...); err != nil {
		k.t.Fatalf("exec %s: %v", sql, err)
	}
}

// register is a user with a username, not yet past the guidelines.
func (k *kit) register(name string) member {
	k.t.Helper()
	id := k.app.CreateUser("art+" + name + "@example.com")
	k.exec(`UPDATE users SET username = $2 WHERE id = $1`, id, "art_"+name)
	return member{id: id, token: k.app.Token(id)}
}

// member is registered and past the community guidelines.
func (k *kit) member(name string) member {
	m := k.register(name)
	k.exec(`INSERT INTO community_guidelines_acceptances (id, user_id, accepted_at) VALUES ($1, $2, now())`, uuid.New(), m.id)
	return m
}

func (k *kit) admin(name string) member {
	m := k.member(name)
	k.app.Deps.Admin.IDs[m.id] = struct{}{}
	return m
}

// credential is a market:read PAT owned by owner.
func (k *kit) credential(owner member) (uuid.UUID, string) {
	return k.credentialWithID(owner, uuid.New())
}

func (k *kit) credentialWithID(owner member, id uuid.UUID) (uuid.UUID, string) {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	token := auth.PATPrefix + hex.EncodeToString(b)
	k.exec(`INSERT INTO personal_access_tokens (id, user_id, name, token_hash, scopes, expires_at, created_at)
		VALUES ($1, $2, 'articles-test', $3, $4, now() + interval '1 hour', now())`,
		id, owner.id, auth.SHA256Hex(token), []string{"market:read"})
	return id, token
}

type reply struct {
	status int
	header http.Header
	body   []byte
}

func (r reply) code() string {
	var e struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(r.body, &e)
	return e.Code
}

func (r reply) reason() string {
	var e struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(r.body, &e)
	return e.Reason
}

func (k *kit) send(method, path, token string, body any, headers map[string]string) reply {
	k.t.Helper()
	var rd io.Reader
	ct := ""
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
		ct = "application/json"
	default:
		raw, _ := json.Marshal(b)
		rd = bytes.NewReader(raw)
		ct = "application/json"
	}
	req, err := http.NewRequestWithContext(context.Background(), method, k.app.Server.URL+path, rd)
	if err != nil {
		k.t.Fatal(err)
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for n, v := range headers {
		req.Header.Set(n, v)
	}
	resp, err := k.app.Server.Client().Do(req)
	if err != nil {
		k.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return reply{status: resp.StatusCode, header: resp.Header, body: data}
}

func (k *kit) json(r reply, v any) {
	k.t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		k.t.Fatalf("decode %d %s: %v", r.status, r.body, err)
	}
}

type input map[string]any

func articleInput() input {
	return input{
		"title":        "Why 2027 could reprice NextDecade",
		"bodyMarkdown": strings.Repeat("Revenue visibility is unusually long. ", 12),
		"bulletPoints": []string{"First LNG from Train 1 is targeted for 1H 2027."},
		"tickers":      []string{"$next"},
		"disclosure":   "I hold a position in $NEXT.",
		"source":       "web",
	}
}

func (i input) with(key string, v any) input {
	out := input{}
	for k, val := range i {
		out[k] = val
	}
	out[key] = v
	return out
}

func (k *kit) publish(m member, in input) articleDetail {
	k.t.Helper()
	r := k.send("POST", "/v1/articles", m.token, in, nil)
	if r.status != http.StatusOK {
		k.t.Fatalf("publish: %d %s", r.status, r.body)
	}
	var d articleDetail
	k.json(r, &d)
	return d
}

func expectStatus(t *testing.T, r reply, want int) {
	t.Helper()
	if r.status != want {
		t.Fatalf("status %d, want %d: %s", r.status, want, r.body)
	}
}

// --- ArticlesRouteTests ------------------------------------------------------

func TestFlagOffEveryRoute404s(t *testing.T) {
	k := newKit(t, false)
	m := k.member("off")
	expectStatus(t, k.send("GET", "/v1/articles", m.token, nil, nil), 404)
	expectStatus(t, k.send("POST", "/v1/articles", m.token, articleInput(), nil), 404)
	expectStatus(t, k.send("GET", "/v1/articles", "", nil, nil), 404)
	expectStatus(t, k.send("POST", "/v1/articles/images", "", nil, nil), 404)
	expectStatus(t, k.send("GET", "/v1/articles/images/"+uuid.NewString(), "", nil, nil), 404)
}

func TestPublishAndRead(t *testing.T) {
	k := newKit(t, true)
	m := k.member("pub")
	d := k.publish(m, articleInput())
	a := d.Article
	if len(a.Tickers) != 1 || a.Tickers[0] != "NEXT" || a.Slug != "why-2027-could-reprice-nextdecade" ||
		len(a.Code) != 8 || a.Author.Username == nil || *a.Author.Username != "art_pub" || a.WordCount != 60 || !d.ViewerIsAuthor {
		t.Fatalf("detail %+v", d)
	}
	if a.Status != "published" || a.Source != "web" || a.EditedAt != nil || a.CoverImageID != nil {
		t.Fatalf("summary %+v", a)
	}
	expectStatus(t, k.send("GET", "/v1/articles/"+a.Code, m.token, nil, nil), 200)
	expectStatus(t, k.send("GET", "/v1/articles/"+strings.ToUpper(a.Code), m.token, nil, nil), 200)
	var byID articleDetail
	k.json(k.send("GET", "/v1/articles/"+a.ID.String(), m.token, nil, nil), &byID)
	if byID.Article.Code != a.Code {
		t.Fatalf("by id %+v", byID)
	}
	// Raw wire: nil optionals are absent, the author has no avatar.
	raw := k.send("GET", "/v1/articles/"+a.Code, m.token, nil, nil)
	if bytes.Contains(raw.body, []byte("editedAt")) || bytes.Contains(raw.body, []byte("avatarURL")) || bytes.Contains(raw.body, []byte("coverImageId")) {
		t.Fatalf("nil optionals encoded: %s", raw.body)
	}
	expectStatus(t, k.send("GET", "/v1/articles/nope1234", m.token, nil, nil), 404)
	if r := k.send("GET", "/v1/articles/short", m.token, nil, nil); r.reason() != "Article not found" {
		t.Fatalf("reason %q", r.reason())
	}
}

func TestValidationErrors(t *testing.T) {
	k := newKit(t, true)
	m := k.member("val")
	expectStatus(t, k.send("POST", "/v1/articles", m.token, articleInput().with("title", "short"), nil), 400)
	expectStatus(t, k.send("POST", "/v1/articles", m.token, articleInput().with("bulletPoints", []string{}), nil), 400)
	r := k.send("POST", "/v1/articles", m.token, articleInput().with("tickers", []string{"ZZZZ"}), nil)
	if r.status != 400 || r.reason() != "We couldn't find the ticker ZZZZ." {
		t.Fatalf("unknown ticker: %d %s", r.status, r.body)
	}
	missing := articleInput()
	delete(missing, "disclosure")
	r = k.send("POST", "/v1/articles", m.token, missing, nil)
	want := `No such key 'disclosure' at path ''. No value associated with key CodingKeys(stringValue: "disclosure", intValue: nil) ("disclosure").`
	if r.status != 400 || r.reason() != want {
		t.Fatalf("missing key: %d %s", r.status, r.body)
	}
	r = k.send("POST", "/v1/articles", m.token, articleInput().with("coverImageId", "nope"), nil)
	if r.status != 400 || r.reason() != "Data corrupted at path 'coverImageId'. Attempted to decode UUID from invalid UUID string." {
		t.Fatalf("bad uuid: %d %s", r.status, r.body)
	}
	// An unrecognized source is accepted and stored as web.
	d := k.publish(m, articleInput().with("source", "carrier-pigeon"))
	if d.Article.Source != "web" {
		t.Fatalf("source %q", d.Article.Source)
	}
	d = k.publish(m, articleInput().with("source", "ios").with("title", "Another article about NEXT"))
	if d.Article.Source != "ios" {
		t.Fatalf("source %q", d.Article.Source)
	}
}

func TestUnavailableTickerIsAccepted(t *testing.T) {
	k := newKit(t, true)
	k.verifier.missing = map[string]bool{}
	k.verifier.unavailable = map[string]bool{"NEXT": true}
	k.publish(k.member("unav"), articleInput())
}

func TestPublishingNeedsGuidelines(t *testing.T) {
	k := newKit(t, true)
	r := k.send("POST", "/v1/articles", k.register("fresh").token, articleInput(), nil)
	if r.status != 403 || r.code() != "guidelines_required" {
		t.Fatalf("%d %s", r.status, r.body)
	}
}

func TestDailyLimit(t *testing.T) {
	k := newKit(t, true)
	m := k.member("cap")
	for _, n := range []string{"1", "2", "3"} {
		k.publish(m, articleInput().with("title", "Article number "+n+" about NEXT"))
	}
	// Every ticker now "doesn't exist": a 429 rather than a 400 proves the cap
	// is checked before any market-data lookup is spent.
	k.verifier.missing = map[string]bool{"NEXT": true}
	r := k.send("POST", "/v1/articles", m.token, articleInput().with("title", "Article number 4 about NEXT"), nil)
	if r.status != 429 || r.code() != "article_daily_limit" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// Admins are exempt.
	a := k.admin("cap_admin")
	k.verifier.missing = map[string]bool{}
	for _, n := range []string{"1", "2", "3", "4"} {
		k.publish(a, articleInput().with("title", "Admin article "+n+" about NEXT"))
	}
}

func TestThirdPartyReadsAnonymously(t *testing.T) {
	k := newKit(t, true)
	admin := k.admin("anon_admin")
	_, token := k.credential(admin)
	own := k.publish(admin, articleInput()).Article.Code
	expectStatus(t, k.send("POST", "/v1/articles/"+own+"/vote", admin.token, nil, nil), 200)
	hidden := k.publish(admin, articleInput().with("title", "Hidden article about NEXT")).Article.Code
	k.exec(`UPDATE articles SET status = 'hidden' WHERE code = $1`, hidden)
	deleted := k.publish(admin, articleInput().with("title", "Deleted article about NEXT")).Article.Code
	expectStatus(t, k.send("DELETE", "/v1/articles/"+deleted, admin.token, nil, nil), 204)

	expectStatus(t, k.send("GET", "/v1/articles/"+hidden, admin.token, nil, nil), 200)
	expectStatus(t, k.send("GET", "/v1/articles/"+deleted, admin.token, nil, nil), 200)
	expectStatus(t, k.send("GET", "/v1/articles/"+hidden, token, nil, nil), 404)
	expectStatus(t, k.send("GET", "/v1/articles/"+deleted, token, nil, nil), 404)
	r := k.send("GET", "/v1/articles/"+own, token, nil, nil)
	expectStatus(t, r, 200)
	var d articleDetail
	k.json(r, &d)
	if d.Article.UpvoteCount != 1 || d.ViewerUpvoted || d.ViewerIsAuthor {
		t.Fatalf("%+v", d)
	}
	// A credential can't write.
	r = k.send("POST", "/v1/articles/"+own+"/vote", token, nil, nil)
	if r.status != 403 || r.reason() != "This endpoint requires a first-party session." {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// No token at all is a 401.
	expectStatus(t, k.send("GET", "/v1/articles", "", nil, nil), 401)
}

func TestScopeRequired(t *testing.T) {
	k := newKit(t, true)
	m := k.member("scope")
	token := auth.PATPrefix + "noscope0123456789"
	k.exec(`INSERT INTO personal_access_tokens (id, user_id, name, token_hash, scopes, expires_at, created_at)
		VALUES ($1, $2, 'x', $3, $4, now() + interval '1 hour', now())`, uuid.New(), m.id, auth.SHA256Hex(token), []string{"portfolio:read"})
	r := k.send("GET", "/v1/articles", token, nil, nil)
	if r.status != 403 || !strings.HasPrefix(r.reason(), "insufficient_scope") {
		t.Fatalf("%d %s", r.status, r.body)
	}
}

func TestUTCDay(t *testing.T) {
	for in, want := range map[int64]string{0: "1970-01-01", 1_791_503_999: "2026-10-08", 1_791_504_000: "2026-10-09"} {
		if got := utcDay(time.Unix(in, 0).In(time.FixedZone("X", -5*3600))); got != want {
			t.Fatalf("utcDay(%d) = %s, want %s", in, got, want)
		}
	}
}

func codes(items []articleSummary) []string {
	out := []string{}
	for i := range items {
		out = append(out, items[i].Code)
	}
	return out
}

func TestFeed(t *testing.T) {
	k := newKit(t, true)
	ana, bo := k.member("ana"), k.member("bo")
	first := k.publish(ana, articleInput().with("title", "First article about NEXT")).Article.Code
	second := k.publish(bo, articleInput().with("title", "Second article on NVDA").with("tickers", []string{"NVDA"})).Article.Code
	third := k.publish(ana, articleInput().with("title", "Third article on NVDA too").with("tickers", []string{"NVDA", "NEXT"})).Article.Code

	list := func(q string, m member) listResponse {
		var l listResponse
		k.json(k.send("GET", "/v1/articles"+q, m.token, nil, nil), &l)
		return l
	}
	eq := func(got []articleSummary, want ...string) {
		t.Helper()
		if strings.Join(codes(got), ",") != strings.Join(want, ",") {
			t.Fatalf("got %v want %v", codes(got), want)
		}
	}
	eq(list("", ana).Items, third, second, first)
	eq(list("?ticker=nvda", ana).Items, third, second)
	eq(list("?ticker=%24NVDA", ana).Items, third, second)
	eq(list("?author=art_ana", bo).Items, third, first)
	eq(list("?author=nobody", bo).Items)
	page1 := list("?limit=2", ana)
	if page1.NextCursor == nil {
		t.Fatal("no cursor")
	}
	page2 := list("?limit=2&cursor="+*page1.NextCursor, ana)
	eq(page2.Items, first)
	if page2.NextCursor != nil {
		t.Fatal("unexpected cursor")
	}
	// A malformed cursor is ignored; limit clamps; junk limit defaults.
	eq(list("?cursor=garbage", ana).Items, third, second, first)
	eq(list("?limit=0", ana).Items, third)
	eq(list("?limit=abc", ana).Items, third, second, first)
	r := k.send("GET", "/v1/articles?ticker=1ABC", ana.token, nil, nil)
	if r.status != 400 || r.reason() != `"1ABC" isn't a ticker symbol.` {
		t.Fatalf("%d %s", r.status, r.body)
	}
	r = k.send("GET", "/v1/articles?ticker=%24", ana.token, nil, nil)
	if r.status != 400 || r.reason() != "Tag one to five tickers." {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// The raw wire keeps an empty items array and omits a nil cursor.
	raw := k.send("GET", "/v1/articles?author=nobody", ana.token, nil, nil)
	if string(raw.body) != `{"items":[]}` {
		t.Fatalf("%s", raw.body)
	}
}

// Equal timestamps page by id. Rows are stored the way PostgresNIO binds a
// whole-millisecond Date (truncated to the microsecond below), and so is the
// cursor, so the tie-break holds. A row written outside both backends at the
// exact millisecond falls between the cursor's < and = and is skipped, as in
// Vapor.
func TestCursorEqualTimestamps(t *testing.T) {
	k := newKit(t, true)
	m := k.member("tie")
	for _, n := range []string{"1", "2", "3"} {
		k.publish(m, articleInput().with("title", "Tied article "+n+" on NEXT"))
	}
	page := func(exact bool) (all, p2 listResponse) {
		at := pgBind(1790856000.123)
		if exact {
			at = time.Date(2026, 10, 1, 12, 0, 0, 123_000_000, time.UTC)
		}
		k.exec(`UPDATE articles SET published_at = $1`, at)
		var p1 listResponse
		k.json(k.send("GET", "/v1/articles", m.token, nil, nil), &all)
		k.json(k.send("GET", "/v1/articles?limit=1", m.token, nil, nil), &p1)
		if p1.NextCursor == nil || !strings.HasPrefix(*p1.NextCursor, "1790856000123_") {
			t.Fatalf("cursor %v", p1.NextCursor)
		}
		k.json(k.send("GET", "/v1/articles?limit=5&cursor="+*p1.NextCursor, m.token, nil, nil), &p2)
		return all, p2
	}
	all, p2 := page(false)
	if len(all.Items) != 3 || len(p2.Items) != 2 || p2.Items[0].Code != all.Items[1].Code {
		t.Fatalf("all %v p2 %v", codes(all.Items), codes(p2.Items))
	}
	if pgBind(1790856000.123).Nanosecond() != 122_999_000 {
		t.Skip("float path landed exactly on the millisecond")
	}
	if _, p2 = page(true); len(p2.Items) != 0 {
		t.Fatalf("exact-ms rows: %v", codes(p2.Items))
	}
}

func TestEditAndDelete(t *testing.T) {
	k := newKit(t, true)
	ana, bo := k.member("ed_ana"), k.member("ed_bo")
	code := k.publish(ana, articleInput()).Article.Code
	r := k.send("PATCH", "/v1/articles/"+code, bo.token, articleInput().with("title", "Hijacked title here"), nil)
	if r.status != 403 || r.reason() != "You can only edit your own articles." {
		t.Fatalf("%d %s", r.status, r.body)
	}
	var up articleDetail
	r = k.send("PATCH", "/v1/articles/"+code, ana.token, articleInput().with("title", "A better title for NEXT").with("source", "ios"), nil)
	k.json(r, &up)
	if up.Article.Slug != "a-better-title-for-next" || up.Article.EditedAt == nil || up.Article.Code != code || up.Article.Source != "web" {
		t.Fatalf("%+v", up)
	}
	r = k.send("DELETE", "/v1/articles/"+code, bo.token, nil, nil)
	if r.status != 403 || r.reason() != "You can only delete your own articles." {
		t.Fatalf("%d %s", r.status, r.body)
	}
	expectStatus(t, k.send("DELETE", "/v1/articles/"+code, ana.token, nil, nil), 204)
	expectStatus(t, k.send("GET", "/v1/articles/"+code, bo.token, nil, nil), 404)
	// The author no longer sees it either (deleted: admins only).
	expectStatus(t, k.send("DELETE", "/v1/articles/"+code, ana.token, nil, nil), 404)
	var l listResponse
	k.json(k.send("GET", "/v1/articles", bo.token, nil, nil), &l)
	if len(l.Items) != 0 {
		t.Fatalf("%v", codes(l.Items))
	}
	// An admin may delete anyone's article.
	admin := k.admin("ed_admin")
	other := k.publish(bo, articleInput()).Article.Code
	expectStatus(t, k.send("DELETE", "/v1/articles/"+other, admin.token, nil, nil), 204)
}

func (k *kit) sanction(m member, kind string) {
	k.exec(`INSERT INTO community_sanctions (id, user_id, kind, reason, expires_at, created_at)
		VALUES ($1, $2, $3, 'Testing', now() + interval '1 hour', now())`, uuid.New(), m.id, kind)
}

func TestMutedCannotPublishButCanReport(t *testing.T) {
	k := newKit(t, true)
	ana, bo := k.member("mute_ana"), k.member("mute_bo")
	code := k.publish(bo, articleInput()).Article.Code
	k.sanction(ana, "mute")
	r := k.send("POST", "/v1/articles", ana.token, articleInput(), nil)
	if r.status != 403 || r.code() != "community_muted" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	r = k.send("POST", "/v1/articles/"+code+"/vote", ana.token, nil, nil)
	if r.status != 403 || r.code() != "community_muted" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	expectStatus(t, k.send("POST", "/v1/articles/"+code+"/report", ana.token, input{"reason": "spam"}, nil), 204)
}

func TestBannedIsTurnedAway(t *testing.T) {
	k := newKit(t, true)
	m := k.member("ban")
	k.sanction(m, "ban")
	r := k.send("POST", "/v1/articles", m.token, articleInput(), nil)
	if r.status != 403 || r.code() != "community_banned" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// Reads don't go through the community gate.
	expectStatus(t, k.send("GET", "/v1/articles", m.token, nil, nil), 200)
}

// --- ArticleEngagementTests --------------------------------------------------

func TestVotes(t *testing.T) {
	k := newKit(t, true)
	ana, bo := k.member("v_ana"), k.member("v_bo")
	code := k.publish(ana, articleInput()).Article.Code
	vote := func(method string) voteResponse {
		var v voteResponse
		k.json(k.send(method, "/v1/articles/"+code+"/vote", bo.token, nil, nil), &v)
		return v
	}
	if v := vote("POST"); v != (voteResponse{1, true}) {
		t.Fatalf("%+v", v)
	}
	if v := vote("POST"); v != (voteResponse{1, true}) {
		t.Fatalf("%+v", v)
	}
	var d articleDetail
	k.json(k.send("GET", "/v1/articles/"+code, bo.token, nil, nil), &d)
	if !d.ViewerUpvoted {
		t.Fatal("not upvoted")
	}
	if v := vote("DELETE"); v != (voteResponse{0, false}) {
		t.Fatalf("%+v", v)
	}
	if v := vote("DELETE"); v != (voteResponse{0, false}) {
		t.Fatalf("%+v", v)
	}
	expectStatus(t, k.send("POST", "/v1/articles/nope2345/vote", bo.token, nil, nil), 404)
}

func (k *kit) viewCount(m member, code string) int64 {
	var d articleDetail
	k.json(k.send("GET", "/v1/articles/"+code, m.token, nil, nil), &d)
	return d.Article.ViewCount
}

var (
	alice = map[string]string{"X-Norviq-Viewer": "0123456789abcdef0123456789abcdef"}
	bob   = map[string]string{"X-Norviq-Viewer": "FEDCBA9876543210fedcba9876543210"}
)

func TestViewsCountOncePerDay(t *testing.T) {
	k := newKit(t, true)
	ana, bo := k.member("w_ana"), k.member("w_bo")
	code := k.publish(ana, articleInput()).Article.Code
	expectStatus(t, k.send("POST", "/v1/articles/"+code+"/view", bo.token, nil, nil), 204)
	expectStatus(t, k.send("POST", "/v1/articles/"+code+"/view", bo.token, nil, nil), 204)
	expectStatus(t, k.send("POST", "/v1/articles/"+code+"/view", bo.token, nil, alice), 204)
	expectStatus(t, k.send("POST", "/v1/articles/"+code+"/view", ana.token, nil, nil), 204)
	if n := k.viewCount(bo, code); n != 2 {
		t.Fatalf("views %d", n)
	}
}

func TestListedCredentialCountsViewers(t *testing.T) {
	webID := uuid.New()
	// The listed ids are read once at startup, as Vapor's configure did.
	t.Setenv(viewerCredentialsEnv, " "+strings.ToLower(webID.String())+" ,, junk")
	k := newKit(t, true)
	ana := k.member("lv_ana")
	_, web := k.credentialWithID(ana, webID)
	code := k.publish(ana, articleInput()).Article.Code
	for _, h := range []map[string]string{alice, alice, bob, bob} {
		expectStatus(t, k.send("POST", "/v1/articles/"+code+"/view", web, nil, h), 204)
	}
	// No viewer key, or a malformed one: nothing to dedupe on.
	expectStatus(t, k.send("POST", "/v1/articles/"+code+"/view", web, nil, nil), 204)
	expectStatus(t, k.send("POST", "/v1/articles/"+code+"/view", web, nil, map[string]string{"X-Norviq-Viewer": "nothex-nothex-nothex"}), 204)
	expectStatus(t, k.send("POST", "/v1/articles/"+code+"/view", web, nil, map[string]string{"X-Norviq-Viewer": "abc"}), 204)
	if n := k.viewCount(ana, code); n != 2 {
		t.Fatalf("views %d", n)
	}
	var keys []string
	rows, _ := k.app.Deps.DB.Query(context.Background(), `SELECT viewer_key FROM article_views ORDER BY viewer_key`)
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		keys = append(keys, s)
	}
	rows.Close()
	if strings.Join(keys, ",") != "v:0123456789abcdef0123456789abcdef,v:fedcba9876543210fedcba9876543210" {
		t.Fatalf("keys %v", keys)
	}

	// Any other market:read credential counts nothing, however it rotates.
	_, other := k.credential(k.member("uv_bo"))
	for _, n := range []string{"0", "1", "2"} {
		expectStatus(t, k.send("POST", "/v1/articles/"+code+"/view", other, nil, map[string]string{"X-Norviq-Viewer": strings.Repeat(n, 32)}), 204)
	}
	if n := k.viewCount(ana, code); n != 2 {
		t.Fatalf("views %d", n)
	}
}

func TestParseCredentialIDs(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	if len(parseCredentialIDs("")) != 0 || len(parseCredentialIDs(" , ")) != 0 {
		t.Fatal("empty")
	}
	got := parseCredentialIDs(" " + strings.ToLower(a.String()) + " ,, " + strings.ToUpper(b.String()) + " ,not-a-uuid")
	if _, ok := got[a]; !ok || len(got) != 2 {
		t.Fatalf("%v", got)
	}
	if _, ok := got[b]; !ok {
		t.Fatalf("%v", got)
	}
}

func TestViewLimiterSkipsListedCredential(t *testing.T) {
	web := uuid.New()
	h := &handler{viewerCreds: map[uuid.UUID]struct{}{web: {}}}
	// Stands in for the Redis limiter: refuses everyone it sees.
	refusing := func(echo.HandlerFunc) echo.HandlerFunc {
		return func(*echo.Context) error { return httpx.Abort(http.StatusTooManyRequests, "") }
	}
	mw := h.viewRateLimit(refusing)
	e := echo.New()
	run := func(scope *uuid.UUID) error {
		c := e.NewContext(httptest.NewRequestWithContext(context.Background(), "POST", "/", nil), httptest.NewRecorder())
		s := &auth.Session{UserID: uuid.New()}
		if scope != nil {
			s.Scope = &auth.ScopeContext{TokenID: *scope, Kind: auth.KindPAT}
		}
		c.Set("norviq.session", s) // auth's context key
		return mw(func(*echo.Context) error { return nil })(c)
	}
	if err := run(&web); err != nil {
		t.Fatalf("listed credential was limited: %v", err)
	}
	other := uuid.New()
	for _, scope := range []*uuid.UUID{&other, nil} {
		var he *httpx.Error
		if err := run(scope); !errors.As(err, &he) || he.Status != http.StatusTooManyRequests {
			t.Fatalf("not limited: %v", err)
		}
	}
}

func TestReports(t *testing.T) {
	k := newKit(t, true)
	ana, bo := k.member("r_ana"), k.member("r_bo")
	d := k.publish(ana, articleInput())
	expectStatus(t, k.send("POST", "/v1/articles/"+d.Article.Code+"/report", bo.token, input{"reason": "scam", "note": "  Pump and dump  "}, nil), 204)
	var target, reason, status, note string
	if err := k.app.Deps.DB.QueryRow(context.Background(),
		`SELECT target_id, reason, status, note FROM social_reports WHERE target_type = 'article'`).Scan(&target, &reason, &status, &note); err != nil {
		t.Fatal(err)
	}
	if target != apijson.FormatUUID(d.Article.ID.Std()) || reason != "scam" || status != "open" || note != "Pump and dump" {
		t.Fatalf("%s %s %s %q", target, reason, status, note)
	}
	r := k.send("POST", "/v1/articles/"+d.Article.Code+"/report", bo.token, input{"reason": "boring"}, nil)
	if r.status != 400 || r.reason() != "Data corrupted at path 'reason'. Cannot initialize BoardReportReason from invalid String value boring." {
		t.Fatalf("%d %s", r.status, r.body)
	}
	r = k.send("POST", "/v1/articles/"+d.Article.Code+"/report", bo.token, input{"reason": "spam", "note": strings.Repeat("x", 1001)}, nil)
	if r.status != 400 || r.reason() != "Keep the details under 1,000 characters." {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// A blank note is stored as NULL.
	expectStatus(t, k.send("POST", "/v1/articles/"+d.Article.Code+"/report", bo.token, input{"reason": "other", "note": "   "}, nil), 204)
	var nulls int
	_ = k.app.Deps.DB.QueryRow(context.Background(), `SELECT count(*) FROM social_reports WHERE note IS NULL`).Scan(&nulls)
	if nulls != 1 {
		t.Fatalf("nulls %d", nulls)
	}
}

// --- Vapor's trie router -----------------------------------------------------

func TestImagesIsNeverARef(t *testing.T) {
	k := newKit(t, true)
	m := k.member("trie")
	for _, c := range []struct{ method, path string }{
		{"GET", "/v1/articles/images"},
		{"POST", "/v1/articles/images/view"},
		{"POST", "/v1/articles/images/vote"},
		{"POST", "/v1/articles/images/report"},
	} {
		r := k.send(c.method, c.path, "", nil, nil)
		if r.status != 404 || r.reason() != "Not Found" {
			t.Fatalf("%s %s: %d %s", c.method, c.path, r.status, r.body)
		}
	}
	// DELETE has no constant images route, so the :ref handler answers.
	r := k.send("DELETE", "/v1/articles/images/vote", m.token, nil, nil)
	if r.status != 404 || r.reason() != "Article not found" {
		t.Fatalf("%d %s", r.status, r.body)
	}
}

// --- helpers for images ------------------------------------------------------

func pngBytes(w, h uint32) []byte {
	b := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 13, 0x49, 0x48, 0x44, 0x52}
	b = binary.BigEndian.AppendUint32(b, w)
	b = binary.BigEndian.AppendUint32(b, h)
	return append(b, 8, 6, 0, 0, 0, 0, 0, 0, 0)
}
