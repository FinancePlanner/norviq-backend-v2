// Package research ports StockController's research note routes
// (/v1/research, research:read / research:write).
package research

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/achievementhooks"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/platform/swiftjson"
	"norviq-api/internal/server"
	"norviq-api/internal/store/researchdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// Feature mounts the routes.
type Feature struct {
	// Now overrides the clock (tests).
	Now func() time.Time
}

// Name implements server.Feature.
func (Feature) Name() string { return "research" }

type handlers struct {
	q     *researchdb.Queries
	clock func() time.Time
}

// Register mounts the routes as StockController.boot(routes:) built them.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	h := &handlers{q: researchdb.New(d.DB), clock: f.Now}
	protected := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard()}
	read := append(append([]echo.MiddlewareFunc{}, protected...), auth.RequireScope(auth.ScopeResearchRead))
	write := append(append([]echo.MiddlewareFunc{}, protected...), auth.RequireScope(auth.ScopeResearchWrite))
	r.V1.GET("/research", h.list, read...)
	r.V1.POST("/research", h.create, write...)
	r.V1.GET("/research/:researchId", h.get, read...)
	r.V1.PUT("/research/:researchId", h.update, write...)
	r.V1.DELETE("/research/:researchId", h.delete, write...)
	return nil
}

// now is Fluent's Date() for @Timestamp fields, at Postgres precision.
func (h *handlers) now() time.Time {
	t := time.Now()
	if h.clock != nil {
		t = h.clock()
	}
	return t.UTC().Truncate(time.Microsecond)
}

// NoteRequest is ResearchNoteRequest.
type NoteRequest struct {
	Symbol         string    `json:"symbol"`
	Title          *string   `json:"title"`
	Thesis         string    `json:"thesis"`
	Risks          *string   `json:"risks"`
	Catalysts      *string   `json:"catalysts"`
	ReferenceLinks *[]string `json:"referenceLinks"`
}

// NoteResponse is ResearchNoteResponse.
type NoteResponse struct {
	ID             string    `json:"id"`
	Symbol         string    `json:"symbol"`
	Title          *string   `json:"title,omitempty"`
	Thesis         string    `json:"thesis"`
	Risks          *string   `json:"risks,omitempty"`
	Catalysts      *string   `json:"catalysts,omitempty"`
	ReferenceLinks *[]string `json:"referenceLinks,omitempty"`
}

// MARK: - StockController helpers

var upper = cases.Upper(language.Und)

// normalizeSymbol is StockController.normalizeSymbol.
func normalizeSymbol(raw string) (string, error) {
	s := upper.String(portfolios.TrimSwift(raw))
	if s == "" {
		return "", httpx.BadRequest("Symbol is required.")
	}
	return s, nil
}

func requireNonEmpty(raw, field string) (string, error) {
	s := portfolios.TrimSwift(raw)
	if s == "" {
		return "", httpx.BadRequest(field + " is required.")
	}
	return s, nil
}

func emptyToNil(raw *string) *string {
	if raw == nil {
		return nil
	}
	s := portfolios.TrimSwift(*raw)
	if s == "" {
		return nil
	}
	return &s
}

// encodeReferenceLinks stores the trimmed, non-empty links as a JSON array,
// written as Foundation's JSONEncoder wrote it ("/" escaped as "\/").
func encodeReferenceLinks(links *[]string) (*string, error) {
	if links == nil {
		return nil, nil
	}
	cleaned := []string{}
	for _, l := range *links {
		if t := portfolios.TrimSwift(l); t != "" {
			cleaned = append(cleaned, t)
		}
	}
	if len(cleaned) == 0 {
		return nil, nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(cleaned); err != nil {
		return nil, err
	}
	out := string(bytes.ReplaceAll(bytes.TrimSuffix(buf.Bytes(), []byte{'\n'}), []byte("/"), []byte(`\/`)))
	return &out, nil
}

// decodeReferenceLinks is `try? JSONDecoder().decode([String].self, ...)`.
func decodeReferenceLinks(raw *string) *[]string {
	if raw == nil {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(*raw), &out); err != nil || out == nil {
		return nil
	}
	return &out
}

func toResponse(n researchdb.ResearchNote) NoteResponse {
	return NoteResponse{
		ID:             apijson.FormatUUID(n.ID),
		Symbol:         n.Symbol,
		Title:          n.Title,
		Thesis:         n.Thesis,
		Risks:          n.Risks,
		Catalysts:      n.Catalysts,
		ReferenceLinks: decodeReferenceLinks(n.ReferenceLinks),
	}
}

// researchID is requireUUIDParameter(req, name: "researchId", reason:
// "Invalid research ID"): UUID(uuidString:), the hyphenated form only.
func researchID(c *echo.Context) (uuid.UUID, error) {
	raw := c.Param("researchId")
	if len(raw) == 36 {
		if id, err := uuid.Parse(raw); err == nil {
			return id, nil
		}
	}
	return uuid.UUID{}, httpx.BadRequest("Invalid research ID")
}

func (h *handlers) find(c *echo.Context, userID, id uuid.UUID) (researchdb.ResearchNote, error) {
	n, err := h.q.OwnedResearchNote(c.Request().Context(), researchdb.OwnedResearchNoteParams{ID: id, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return n, httpx.NotFound("Research note not found.")
	}
	return n, err
}

// MARK: - Handlers

func (h *handlers) list(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	// clampedLimit: max(1, min(limit ?? 50, 200)); a malformed limit is nil.
	limit := int32(max(1, min(httpx.IntOr(httpx.QueryOptionalInt(c, "limit"), 50), 200)))
	ctx := c.Request().Context()
	var notes []researchdb.ResearchNote
	symbol := ""
	if raw := httpx.QueryString(c, "symbol"); raw != nil {
		symbol = upper.String(portfolios.TrimSwift(*raw))
	}
	if symbol != "" {
		notes, err = h.q.ListResearchNotesBySymbol(ctx, researchdb.ListResearchNotesBySymbolParams{
			UserID: s.UserID, Symbol: symbol, Limit: limit,
		})
	} else {
		notes, err = h.q.ListResearchNotes(ctx, researchdb.ListResearchNotesParams{UserID: s.UserID, Limit: limit})
	}
	if err != nil {
		return err
	}
	out := make([]NoteResponse, 0, len(notes))
	for i := range notes {
		out = append(out, toResponse(notes[i]))
	}
	return httpx.OK(c, out)
}

func (h *handlers) create(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	var in NoteRequest
	if err := swiftjson.DecodeRequest(c, &in); err != nil {
		return err
	}
	symbol, err := normalizeSymbol(in.Symbol)
	if err != nil {
		return err
	}
	title := emptyToNil(in.Title)
	thesis, err := requireNonEmpty(in.Thesis, "thesis")
	if err != nil {
		return err
	}
	links, err := encodeReferenceLinks(in.ReferenceLinks)
	if err != nil {
		return err
	}
	now := h.now()
	note := researchdb.ResearchNote{
		ID:             uuid.New(),
		UserID:         s.UserID,
		Symbol:         symbol,
		Title:          title,
		Thesis:         thesis,
		Risks:          emptyToNil(in.Risks),
		Catalysts:      emptyToNil(in.Catalysts),
		ReferenceLinks: links,
		CreatedAt:      now,
		UpdatedAt:      &now,
	}
	if err := h.q.InsertResearchNote(c.Request().Context(), researchdb.InsertResearchNoteParams(note)); err != nil {
		return err
	}
	achievementhooks.Schedule(s.UserID)
	return httpx.JSON(c, http.StatusCreated, toResponse(note))
}

func (h *handlers) get(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := researchID(c)
	if err != nil {
		return err
	}
	note, err := h.find(c, s.UserID, id)
	if err != nil {
		return err
	}
	return httpx.OK(c, toResponse(note))
}

func (h *handlers) update(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := researchID(c)
	if err != nil {
		return err
	}
	var in NoteRequest
	if err := swiftjson.DecodeRequest(c, &in); err != nil {
		return err
	}
	note, err := h.find(c, s.UserID, id)
	if err != nil {
		return err
	}
	if note.Symbol, err = normalizeSymbol(in.Symbol); err != nil {
		return err
	}
	note.Title = emptyToNil(in.Title)
	if note.Thesis, err = requireNonEmpty(in.Thesis, "thesis"); err != nil {
		return err
	}
	note.Risks = emptyToNil(in.Risks)
	note.Catalysts = emptyToNil(in.Catalysts)
	if note.ReferenceLinks, err = encodeReferenceLinks(in.ReferenceLinks); err != nil {
		return err
	}
	now := h.now()
	note.UpdatedAt = &now
	err = h.q.UpdateResearchNote(c.Request().Context(), researchdb.UpdateResearchNoteParams{
		Symbol:         note.Symbol,
		Title:          note.Title,
		Thesis:         note.Thesis,
		Risks:          note.Risks,
		Catalysts:      note.Catalysts,
		ReferenceLinks: note.ReferenceLinks,
		UpdatedAt:      note.UpdatedAt,
		ID:             note.ID,
	})
	if err != nil {
		return err
	}
	achievementhooks.Schedule(s.UserID)
	return httpx.OK(c, toResponse(note))
}

func (h *handlers) delete(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	id, err := researchID(c)
	if err != nil {
		return err
	}
	note, err := h.find(c, s.UserID, id)
	if err != nil {
		return err
	}
	if err := h.q.DeleteResearchNote(c.Request().Context(), note.ID); err != nil {
		return err
	}
	return httpx.NoContent(c)
}
