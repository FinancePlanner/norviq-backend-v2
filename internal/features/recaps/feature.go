// Package recaps ports RecapController (/v1/recaps), RecapService,
// RecapPeriod, RecapFactsCollector, RecapDeckBuilder and
// PersonalityClassifier. Decks are generated on first read; there is no
// background job.
package recaps

import (
	"context"
	"errors"
	"os"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/portfolios"
	"norviq-api/internal/server"
	"norviq-api/internal/store/recapsdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/labstack/echo/v5"
)

// Feature mounts /v1/recaps.
type Feature struct {
	// Now overrides the clock (tests).
	Now func() time.Time
	// ScheduleAchievementCheck is req.scheduleAchievementCheck(for:), run
	// after a recap is first opened. nil is a no-op.
	ScheduleAchievementCheck func(userID uuid.UUID)
}

// Name implements server.Feature.
func (Feature) Name() string { return "recaps" }

// Config is RecapConfig: only the literal "true" turns a flag on. Read per
// request.
type Config struct {
	Enabled       bool
	YearlyEnabled bool
}

// ConfigFromEnv is RecapConfig.fromEnvironment().
func ConfigFromEnv() Config {
	return Config{Enabled: os.Getenv("RECAP_ENABLED") == "true", YearlyEnabled: os.Getenv("RECAP_YEARLY_ENABLED") == "true"}
}

// enabledMiddleware is RecapEnabledMiddleware: 404 ahead of authentication
// while RECAP_ENABLED is off.
func enabledMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if !ConfigFromEnv().Enabled {
			return httpx.NotFound("")
		}
		return next(c)
	}
}

// Register implements server.Feature.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	h := &handler{q: recapsdb.New(d.DB), now: f.Now, achievements: f.ScheduleAchievementCheck}
	if h.now == nil {
		h.now = time.Now
	}
	g := r.V1.Group("/recaps", enabledMiddleware, d.Auth.Scoped(), auth.Guard())
	read := auth.RequireScope(auth.ScopePlanningRead)
	write := auth.RequireScope(auth.ScopePlanningWrite)
	g.GET("", h.list, read)
	g.GET("/:kind/:key", h.show, read)
	g.POST("/:id/opened", h.opened, write)
	g.POST("/:id/completed", h.completed, write)
	return nil
}

type handler struct {
	q            recapsdb.Querier
	now          func() time.Time
	achievements func(uuid.UUID)
}

// service is the controller's service(_:): 404 while disabled.
func (h *handler) service() (*Service, error) {
	cfg := ConfigFromEnv()
	if !cfg.Enabled {
		return nil, httpx.NotFound("")
	}
	return &Service{Q: h.q, Config: cfg}, nil
}

func (h *handler) list(c *echo.Context) error {
	svc, err := h.service()
	if err != nil {
		return err
	}
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	out, err := svc.List(c.Request().Context(), s.UserID, h.now())
	if err != nil {
		return err
	}
	return httpx.OK(c, out)
}

func (h *handler) show(c *echo.Context) error {
	svc, err := h.service()
	if err != nil {
		return err
	}
	period, ok := ParsePeriod(c.Param("kind"), c.Param("key"))
	if !ok {
		return httpx.NotFound("")
	}
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	out, err := svc.Recap(c.Request().Context(), s.UserID, period, h.now())
	if err != nil {
		return err
	}
	if out == nil {
		return httpx.NotFound("")
	}
	return httpx.OK(c, out)
}

func (h *handler) stamp(c *echo.Context, opened bool) error {
	svc, err := h.service()
	if err != nil {
		return err
	}
	id, ok := portfolios.ParseSwiftUUID(c.Param("id"))
	if !ok {
		return httpx.NotFound("")
	}
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	found, err := svc.Stamp(c.Request().Context(), id, s.UserID, h.now(), opened)
	if err != nil {
		return err
	}
	if !found {
		return httpx.NotFound("")
	}
	if opened && h.achievements != nil {
		h.achievements(s.UserID)
	}
	return httpx.NoContent(c)
}

func (h *handler) opened(c *echo.Context) error    { return h.stamp(c, true) }
func (h *handler) completed(c *echo.Context) error { return h.stamp(c, false) }

// Response is RecapResponse.
type Response struct {
	ID          apijson.UUID  `json:"id"`
	Deck        Deck          `json:"deck"`
	GeneratedAt apijson.Time  `json:"generatedAt"`
	OpenedAt    *apijson.Time `json:"openedAt,omitempty"`
	CompletedAt *apijson.Time `json:"completedAt,omitempty"`
}

// Summary is RecapSummary.
type Summary struct {
	Kind        string        `json:"kind"`
	Key         string        `json:"key"`
	PeriodLabel string        `json:"periodLabel"`
	ID          *apijson.UUID `json:"id,omitempty"`
	Generated   bool          `json:"generated"`
	OpenedAt    *apijson.Time `json:"openedAt,omitempty"`
	CompletedAt *apijson.Time `json:"completedAt,omitempty"`
}

// ListResponse is RecapListResponse.
type ListResponse struct {
	Recaps []Summary `json:"recaps"`
}

// Service is RecapService.
type Service struct {
	Q      recapsdb.Querier
	Config Config
}

func (s *Service) serves(kind string) bool {
	switch kind {
	case KindMonth:
		return s.Config.Enabled
	case KindYear:
		return s.Config.Enabled && s.Config.YearlyEnabled
	}
	return false
}

// loadTimeZone is TimeZone(identifier:), which rejected "" and "Local".
func loadTimeZone(id string) *time.Location {
	if id == "" || id == "Local" {
		return nil
	}
	loc, err := time.LoadLocation(id)
	if err != nil {
		return nil
	}
	return loc
}

// timeZone is RecapTimeZoneResolver.timeZone(for:on:): the zone of the
// latest check-in by local date, UTC when missing or unknown.
func (s *Service) timeZone(ctx context.Context, userID uuid.UUID) (*time.Location, error) {
	rows, err := s.Q.LatestCheckInTimeZone(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return time.UTC, nil
	}
	if loc := loadTimeZone(rows[0]); loc != nil {
		return loc, nil
	}
	return time.UTC, nil
}

func (s *Service) userCreatedAt(ctx context.Context, userID uuid.UUID) (*time.Time, error) {
	rows, err := s.Q.RecapUserCreatedAt(ctx, userID)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

func response(r *recapsdb.Recap) (*Response, error) {
	deck, err := decodePayload(r.Payload)
	if err != nil {
		return nil, err
	}
	return &Response{
		ID: apijson.NewUUID(r.ID), Deck: deck, GeneratedAt: apijson.NewTime(r.GeneratedAt),
		OpenedAt: apijson.TimePtr(r.OpenedAt), CompletedAt: apijson.TimePtr(r.CompletedAt),
	}, nil
}

// Recap is recap(userId:period:now:on:): the stored deck, else a freshly
// generated one once the period is available; nil means 404.
func (s *Service) Recap(ctx context.Context, userID uuid.UUID, p Period, now time.Time) (*Response, error) {
	if !s.serves(p.Kind) {
		return nil, nil
	}
	if existing, err := s.Q.RecapByPeriod(ctx, recapsdb.RecapByPeriodParams{UserID: userID, PeriodKind: p.Kind, PeriodKey: p.Key()}); err != nil {
		return nil, err
	} else if len(existing) > 0 {
		return response(&existing[0])
	}
	created, err := s.userCreatedAt(ctx, userID)
	if err != nil || created == nil {
		return nil, err
	}
	loc, err := s.timeZone(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !p.IsAvailable(now, loc, *created) {
		return nil, nil
	}
	facts, err := collector{q: s.Q}.collect(ctx, userID, p, loc)
	if err != nil {
		return nil, err
	}
	deck := BuildDeck(facts, p)
	payload, err := encodePayload(deck)
	if err != nil {
		return nil, err
	}
	rec := recapsdb.Recap{
		ID: uuid.New(), UserID: userID, PeriodKind: deck.Kind, PeriodKey: deck.Key, Payload: payload,
		Archetype: deck.Archetype, SchemaVersion: int64(deck.SchemaVersion), GeneratedAt: now,
	}
	err = s.Q.InsertRecap(ctx, recapsdb.InsertRecapParams{
		ID: rec.ID, UserID: rec.UserID, PeriodKind: rec.PeriodKind, PeriodKey: rec.PeriodKey,
		Payload: rec.Payload, Archetype: rec.Archetype, SchemaVersion: rec.SchemaVersion, GeneratedAt: rec.GeneratedAt,
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		// Someone else inserted first: serve theirs.
		existing, ferr := s.Q.RecapByPeriod(ctx, recapsdb.RecapByPeriodParams{UserID: userID, PeriodKind: rec.PeriodKind, PeriodKey: rec.PeriodKey})
		if ferr != nil {
			return nil, ferr
		}
		if len(existing) == 0 {
			return nil, err
		}
		return response(&existing[0])
	}
	if err != nil {
		return nil, err
	}
	return response(&rec)
}

// List is list(userId:now:on:): stored recaps of a served kind, plus an
// un-generated entry for the last closed month (and, in January with
// yearly on, the last closed year). Never generates.
func (s *Service) List(ctx context.Context, userID uuid.UUID, now time.Time) (ListResponse, error) {
	out := ListResponse{Recaps: []Summary{}}
	if !s.Config.Enabled {
		return out, nil
	}
	created, err := s.userCreatedAt(ctx, userID)
	if err != nil || created == nil {
		return out, err
	}
	loc, err := s.timeZone(ctx, userID)
	if err != nil {
		return out, err
	}
	records, err := s.Q.ListRecaps(ctx, userID)
	if err != nil {
		return out, err
	}
	stored := []Summary{}
	storedKeys := map[string]bool{}
	for i := range records {
		r := &records[i]
		p, ok := ParsePeriod(r.PeriodKind, r.PeriodKey)
		if !ok || !s.serves(r.PeriodKind) {
			continue
		}
		storedKeys[r.PeriodKind+"/"+r.PeriodKey] = true
		id := apijson.NewUUID(r.ID)
		stored = append(stored, Summary{
			Kind: r.PeriodKind, Key: r.PeriodKey, PeriodLabel: p.Label(), ID: &id, Generated: true,
			OpenedAt: apijson.TimePtr(r.OpenedAt), CompletedAt: apijson.TimePtr(r.CompletedAt),
		})
	}
	candidates := []Period{LastClosedMonth(now, loc)}
	if s.Config.YearlyEnabled && now.In(loc).Month() == time.January {
		candidates = append(candidates, LastClosedYear(now, loc))
	}
	for _, p := range candidates {
		if storedKeys[p.Kind+"/"+p.Key()] || !p.IsAvailable(now, loc, *created) {
			continue
		}
		out.Recaps = append(out.Recaps, Summary{Kind: p.Kind, Key: p.Key(), PeriodLabel: p.Label()})
	}
	out.Recaps = append(out.Recaps, stored...)
	return out, nil
}

// Stamp is markOpened / markCompleted: sets the timestamp the first time
// only; false when the recap is not this user's.
func (s *Service) Stamp(ctx context.Context, id, userID uuid.UUID, now time.Time, opened bool) (bool, error) {
	rows, err := s.Q.RecapByID(ctx, recapsdb.RecapByIDParams{ID: id, UserID: userID})
	if err != nil || len(rows) == 0 {
		return false, err
	}
	r := &rows[0]
	switch {
	case opened && r.OpenedAt == nil:
		err = s.Q.SetRecapOpened(ctx, recapsdb.SetRecapOpenedParams{ID: id, OpenedAt: &now})
	case !opened && r.CompletedAt == nil:
		err = s.Q.SetRecapCompleted(ctx, recapsdb.SetRecapCompletedParams{ID: id, CompletedAt: &now})
	}
	return err == nil, err
}
