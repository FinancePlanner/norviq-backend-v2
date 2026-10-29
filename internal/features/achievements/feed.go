package achievements

import (
	"context"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/store/achievementsdb"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// SocialFeedService constants.
const (
	feedDefaultLimit = 20
	feedMaxLimit     = 50
	feedWindow       = 90 * 24 * time.Hour
	// Congrats pushes for one activity are at least this far apart.
	congratsPushThrottle = 10 * time.Minute
)

type feedAchievementDTO struct {
	Key     string `json:"key"`
	Tier    string `json:"tier"`
	Title   string `json:"title"`
	IconKey string `json:"iconKey"`
}

type feedItemDTO struct {
	ID                  apijson.UUID       `json:"id"`
	Actor               userSummaryDTO     `json:"actor"`
	Achievement         feedAchievementDTO `json:"achievement"`
	CreatedAt           apijson.Time       `json:"createdAt"`
	CongratsCount       int                `json:"congratsCount"`
	ViewerCongratulated bool               `json:"viewerCongratulated"`
}

type feedResponseDTO struct {
	Items      []feedItemDTO `json:"items"`
	NextCursor *string       `json:"nextCursor,omitempty"`
}

// visibleActivity is SocialFeedService.visibleActivity: visible activity
// newest first, after cur, or the one activityID. limit rows as asked.
func visibleActivity(ctx context.Context, q achievementsdb.Querier, viewer uuid.UUID, activityID *uuid.UUID, cur *cursor, limit int) ([]achievementsdb.VisibleActivityRow, error) {
	blocked, err := blockedEitherWay(ctx, q, viewer)
	if err != nil {
		return nil, err
	}
	blockedIDs := make([]uuid.UUID, 0, len(blocked))
	for id := range blocked {
		blockedIDs = append(blockedIDs, id)
	}
	p := achievementsdb.VisibleActivityParams{
		Viewer: viewer, Kind: activityKind, WindowStart: time.Now().Add(-feedWindow),
		Known: catalogKeys(), Performance: performanceKeys(), Blocked: blockedIDs,
		ActivityID: activityID, RowLimit: int32(min(limit, feedMaxLimit+1)), //nolint:gosec // clamped to 51
	}
	if p.Performance == nil {
		p.Performance = []string{}
	}
	if cur != nil {
		at := cur.bound()
		p.CursorAt, p.CursorID = &at, cur.id
	}
	return q.VisibleActivity(ctx, p)
}

// GET /v1/social/feed?limit=&cursor=
func (h *handlers) feed(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	requested, err := queryLimit(c, "limit")
	if err != nil {
		return err
	}
	limit := min(max(httpx.IntOr(requested, feedDefaultLimit), 1), feedMaxLimit)
	var cur *cursor
	if raw := httpx.QueryString(c, "cursor"); raw != nil {
		parsed, ok := parseCursor(*raw)
		if ok {
			cur = &parsed
		} else if *raw != "" {
			return httpx.BadRequest("Invalid cursor")
		}
	}
	lang := LanguageFrom(c.Request().Header.Get("Accept-Language"))
	ctx := c.Request().Context()

	rows, err := visibleActivity(ctx, h.q, s.UserID, nil, cur, limit+1)
	if err != nil {
		return err
	}
	page := rows
	if len(page) > limit {
		page = page[:limit]
	}
	actorIDs := make([]uuid.UUID, len(page))
	ids := make([]uuid.UUID, len(page))
	for i, r := range page {
		actorIDs[i], ids[i] = r.ActorID, r.ID
	}
	actors, err := summaries(ctx, h.q, actorIDs, s.UserID)
	if err != nil {
		return err
	}
	counts := map[uuid.UUID]int{}
	mine := map[uuid.UUID]bool{}
	if len(ids) > 0 {
		congrats, err := h.q.CongratsForActivities(ctx, ids)
		if err != nil {
			return err
		}
		for _, cg := range congrats {
			counts[cg.ActivityID]++
			if cg.FromUserID == s.UserID {
				mine[cg.ActivityID] = true
			}
		}
	}
	out := feedResponseDTO{Items: make([]feedItemDTO, 0, len(page))}
	for _, r := range page {
		actor, ok := actors[r.ActorID]
		d := Lookup(r.AchievementKey)
		if !ok || d == nil {
			continue
		}
		out.Items = append(out.Items, feedItemDTO{
			ID: apijson.NewUUID(r.ID), Actor: actor,
			Achievement: feedAchievementDTO{Key: r.AchievementKey, Tier: r.Tier, Title: d.Copy(lang).Title, IconKey: d.IconKey},
			CreatedAt:   apijson.NewTime(r.CreatedAt), CongratsCount: counts[r.ID], ViewerCongratulated: mine[r.ID],
		})
	}
	if len(rows) > limit && len(page) > 0 {
		last := page[len(page)-1]
		next := encodeCursor(last.CreatedAt, last.ID)
		out.NextCursor = &next
	}
	return httpx.OK(c, out)
}

// uuidParam is SocialController.uuidParameter.
func uuidParam(c *echo.Context, name string) (uuid.UUID, error) {
	id, ok := parseSwiftUUID(c.Param(name))
	if !ok {
		return uuid.UUID{}, httpx.BadRequest("Invalid " + name)
	}
	return id, nil
}

// POST /v1/social/feed/:activityId/congrats: 204 whether or not this call
// created the congrats. The actor is pushed only for a new one, at most once
// per 10 minutes per activity.
func (h *handlers) congratulate(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	activityID, err := uuidParam(c, "activityId")
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	actors, err := h.q.ActivityActor(ctx, activityID)
	if err != nil {
		return err
	}
	if len(actors) > 0 && actors[0] == s.UserID {
		return httpx.BadRequest("You cannot congratulate yourself")
	}
	rows, err := visibleActivity(ctx, h.q, s.UserID, &activityID, nil, 1)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return httpx.NotFound("Activity not found")
	}
	row := rows[0]
	now := time.Now()
	recent, err := h.q.RecentCongratsCount(ctx, achievementsdb.RecentCongratsCountParams{ActivityID: activityID, Since: now.Add(-congratsPushThrottle)})
	if err != nil {
		return err
	}
	inserted, err := h.q.InsertCongrats(ctx, achievementsdb.InsertCongratsParams{ID: uuid.New(), ActivityID: activityID, FromUserID: s.UserID, CreatedAt: now})
	if err != nil {
		return err
	}
	if len(inserted) > 0 && recent == 0 {
		h.pushCongrats(ctx, s.UserID, row)
	}
	return httpx.NoContent(c)
}

// congratsMessage is SocialFeedService.congratsMessage: English copy; the
// tier is added for a tiered unlock.
func congratsMessage(viewerUsername string, row achievementsdb.VisibleActivityRow) (title, body string) {
	base := "achievement"
	if d := Lookup(row.AchievementKey); d != nil {
		base = d.EN.Title
	}
	name := base
	if row.Tier != "" {
		name = base + " (" + capitalized(row.Tier) + ")"
	}
	return "🎉 Congrats", viewerUsername + " cheered your " + name
}

// pushCongrats is best effort: a failure never fails the request.
func (h *handlers) pushCongrats(ctx context.Context, viewer uuid.UUID, row achievementsdb.VisibleActivityRow) {
	names, err := h.q.UsernameOf(ctx, viewer)
	if err != nil {
		h.svc.Logger.Warn("social.congrats.push failed", "error", err)
		return
	}
	if len(names) == 0 || names[0] == nil {
		return
	}
	devices, err := h.svc.capableDevices(ctx, row.ActorID)
	if err != nil {
		h.svc.Logger.Warn("social.congrats.push failed", "error", err)
		return
	}
	if len(devices) == 0 {
		return
	}
	title, body := congratsMessage(*names[0], row)
	_ = h.svc.Push.Sender.Send(ctx, achievementPush(kindAchievementCongrats, title, body), devices)
}

// GET /v1/social/users/:userId/achievements: 404 unless the viewer may see
// the owner's achievements, so nothing leaks.
func (h *handlers) friendAchievements(c *echo.Context) error {
	s, err := auth.Require(c)
	if err != nil {
		return err
	}
	owner, err := uuidParam(c, "userId")
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	access, err := resolveVisibility(ctx, h.q, s.UserID, owner)
	if err != nil {
		return err
	}
	if !access.canSee {
		return httpx.NotFound("User not found")
	}
	rows, err := visibleRows(ctx, h.q, owner, access)
	if err != nil {
		return err
	}
	return httpx.OK(c, visibleUnlocks(rows, LanguageFrom(c.Request().Header.Get("Accept-Language"))))
}
