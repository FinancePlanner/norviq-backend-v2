package research_test

import (
	"context"
	"strings"
	"testing"

	"norviq-api/internal/auth"
	"norviq-api/internal/features/research"
	"norviq-api/internal/server"
	"norviq-api/internal/testutil"

	"github.com/google/uuid"
)

type env struct{ *testutil.App }

func newEnv(t *testing.T) *env {
	t.Helper()
	return &env{testutil.NewApp(t, []server.Feature{research.Feature{}})}
}

func (e *env) user() (uuid.UUID, string) {
	id := e.CreateUser("research_" + uuid.NewString()[:8] + "@example.com")
	return id, e.Token(id)
}

func (e *env) pat(userID uuid.UUID, scopes ...string) string {
	e.T.Helper()
	token := auth.PATPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err := e.Deps.DB.Exec(context.Background(),
		`INSERT INTO personal_access_tokens (id, user_id, name, token_hash, scopes, expires_at, created_at)
		 VALUES ($1, $2, 'test', $3, $4, now() + interval '1 day', now())`,
		uuid.New(), userID, auth.SHA256Hex(token), scopes)
	if err != nil {
		e.T.Fatal(err)
	}
	return token
}

func expect(t *testing.T, r *testutil.Response, status int, reason string) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("status %d, want %d: %s", r.Status, status, r.Body)
	}
	if reason != "" {
		var body struct{ Reason string }
		r.JSON(t, &body)
		if body.Reason != reason {
			t.Fatalf("reason %q, want %q", body.Reason, reason)
		}
	}
}

func TestLifecycle(t *testing.T) {
	e := newEnv(t)
	_, tok := e.user()
	r := e.Do("POST", "/v1/research", tok, map[string]any{
		"symbol": " aapl ", "title": "  ", "thesis": "  Services margin  ", "risks": "China",
		"reference_links": []string{" https://a.example/x ", "", "  "},
	})
	expect(t, r, 201, "")
	want := `"symbol":"AAPL","thesis":"Services margin","risks":"China","referenceLinks":["https://a.example/x"]}`
	if !strings.HasSuffix(string(r.Body), want) || strings.Contains(string(r.Body), "title") {
		t.Fatalf("created = %s", r.Body)
	}
	var note research.NoteResponse
	r.JSON(t, &note)
	if note.ID != strings.ToUpper(note.ID) {
		t.Fatalf("id = %s", note.ID)
	}
	var stored string
	if err := e.Deps.DB.QueryRow(context.Background(), `SELECT reference_links FROM research_notes WHERE id = $1`, note.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != `["https:\/\/a.example\/x"]` {
		t.Fatalf("stored = %s", stored)
	}

	expect(t, e.Do("POST", "/v1/research", tok, map[string]any{"symbol": "msft", "thesis": "Azure"}), 201, "")

	r = e.Do("GET", "/v1/research?symbol=%20aapl", tok, nil)
	expect(t, r, 200, "")
	var list []research.NoteResponse
	r.JSON(t, &list)
	if len(list) != 1 || list[0].Symbol != "AAPL" {
		t.Fatalf("filtered = %s", r.Body)
	}
	r = e.Do("GET", "/v1/research?limit=1", tok, nil)
	r.JSON(t, &list)
	if len(list) != 1 || list[0].Symbol != "MSFT" {
		t.Fatalf("limited = %s", r.Body)
	}
	r = e.Do("GET", "/v1/research?limit=abc&symbol=", tok, nil)
	r.JSON(t, &list)
	if len(list) != 2 {
		t.Fatalf("malformed limit = %s", r.Body)
	}

	r = e.Do("PUT", "/v1/research/"+note.ID, tok, map[string]any{"symbol": "aapl", "thesis": "Wearables", "referenceLinks": []string{}})
	expect(t, r, 200, "")
	if string(r.Body) != `{"id":"`+note.ID+`","symbol":"AAPL","thesis":"Wearables"}` {
		t.Fatalf("updated = %s", r.Body)
	}
	expect(t, e.Do("GET", "/v1/research/"+strings.ToLower(note.ID), tok, nil), 200, "")
	expect(t, e.Do("DELETE", "/v1/research/"+note.ID, tok, nil), 204, "")
	expect(t, e.Do("GET", "/v1/research/"+note.ID, tok, nil), 404, "Research note not found.")
}

func TestErrors(t *testing.T) {
	e := newEnv(t)
	uid, tok := e.user()
	_, other := e.user()
	expect(t, e.Do("GET", "/v1/research/nope", tok, nil), 400, "Invalid research ID")
	expect(t, e.Do("DELETE", "/v1/research/nope", tok, nil), 400, "Invalid research ID")
	expect(t, e.Do("POST", "/v1/research", tok, map[string]any{"symbol": " ", "thesis": "x"}), 400, "Symbol is required.")
	expect(t, e.Do("POST", "/v1/research", tok, map[string]any{"symbol": "x", "thesis": " "}), 400, "thesis is required.")
	r := e.Do("POST", "/v1/research", tok, map[string]any{"symbol": "x"})
	if r.Status != 400 {
		t.Fatalf("missing thesis: %d %s", r.Status, r.Body)
	}

	r = e.Do("POST", "/v1/research", tok, map[string]any{"symbol": "x", "thesis": "y"})
	var note research.NoteResponse
	r.JSON(t, &note)
	expect(t, e.Do("GET", "/v1/research/"+note.ID, other, nil), 404, "Research note not found.")
	expect(t, e.Do("PUT", "/v1/research/"+note.ID, other, map[string]any{"symbol": "x", "thesis": "y"}), 404, "Research note not found.")
	// The body is decoded before the note is looked up.
	if r := e.Do("PUT", "/v1/research/"+uuid.NewString(), tok, map[string]any{"symbol": "x"}); r.Status != 400 {
		t.Fatalf("decode first: %d %s", r.Status, r.Body)
	}
	expect(t, e.Do("PUT", "/v1/research/"+note.ID, tok, map[string]any{"symbol": "", "thesis": "y"}), 400, "Symbol is required.")

	// Scopes.
	read := e.pat(uid, "research:read")
	expect(t, e.Do("GET", "/v1/research", read, nil), 200, "")
	if r := e.Do("POST", "/v1/research", read, map[string]any{"symbol": "x", "thesis": "y"}); r.Status != 403 {
		t.Fatalf("read-only PAT wrote: %d", r.Status)
	}
	expect(t, e.Do("GET", "/v1/research", "", nil), 401, "")
}

func TestUnreadableStoredLinksAreOmitted(t *testing.T) {
	e := newEnv(t)
	uid, tok := e.user()
	id := uuid.New()
	_, err := e.Deps.DB.Exec(context.Background(),
		`INSERT INTO research_notes (id, user_id, symbol, thesis, reference_links, created_at, updated_at) VALUES ($1, $2, 'X', 't', 'not json', now(), now())`, id, uid)
	if err != nil {
		t.Fatal(err)
	}
	r := e.Do("GET", "/v1/research/"+id.String(), tok, nil)
	expect(t, r, 200, "")
	if strings.Contains(string(r.Body), "referenceLinks") {
		t.Fatalf("body = %s", r.Body)
	}
}
