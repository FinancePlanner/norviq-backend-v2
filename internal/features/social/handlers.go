package social

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/achievementhooks"
	"norviq-api/internal/platform/push"
	"norviq-api/internal/platform/users"
	"norviq-api/internal/store/socialdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// uuidParam is SocialController.uuidParameter: 400 "Invalid <name>".
func uuidParam(c *echo.Context, name string) (uuid.UUID, error) {
	raw := c.Param(name)
	id, err := uuid.Parse(raw)
	if err != nil || len(raw) != 36 {
		return uuid.Nil, httpx.BadRequest("Invalid " + name)
	}
	return id, nil
}

// codeParam is SocialController.codeParameter: 404 "Invite not found"
// unless 4...64 ASCII letters, digits, '-' or '_'.
func codeParam(c *echo.Context) (string, error) {
	code := c.Param("code")
	n := utf8.RuneCountInString(code)
	ok := n >= 4 && n <= 64
	for _, r := range code {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			ok = false
		}
	}
	if !ok {
		return "", httpx.NotFound("Invite not found")
	}
	return code, nil
}

// requireVisible is SocialController.requireVisible: 404 for missing users
// and for blocks in either direction.
func (h *handlers) requireVisible(ctx context.Context, target, viewer uuid.UUID) error {
	_, err := socialdb.New(h.svc.DB).UserBasic(ctx, target)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NotFound("User not found")
	}
	if err != nil {
		return err
	}
	blocked, err := h.svc.IsBlockedEitherWay(ctx, viewer, target)
	if err != nil {
		return err
	}
	if blocked {
		return httpx.NotFound("User not found")
	}
	return nil
}

// GET /v1/social/config: always answers so the app can tell "off" from
// "broken".
func (h *handlers) config(c *echo.Context) error {
	cfg := ConfigFromEnv()
	if cfg.Enabled {
		// Keeps this user's contact hash current.
		if _, err := h.svc.EnsureSettings(c.Request().Context(), sessionUser(c), cfg); err != nil {
			return err
		}
	}
	return httpx.OK(c, cfg.DTO())
}

// userList is SocialUserSearchResponse / SocialFriendsListResponse.
type searchResponse struct {
	Users      []UserSummary `json:"users"`
	NextCursor *string       `json:"nextCursor,omitempty"`
}

type friendsResponse struct {
	Friends    []UserSummary `json:"friends"`
	NextCursor *string       `json:"nextCursor,omitempty"`
}

func (h *handlers) search(c *echo.Context) error {
	q := ""
	if v := httpx.QueryString(c, "q"); v != nil {
		q = *v
	}
	users, err := h.svc.Search(c.Request().Context(), q, sessionUser(c))
	if err != nil {
		return err
	}
	return httpx.OK(c, searchResponse{Users: users})
}

// profileDTO is SocialProfileDTO.
type profileDTO struct {
	User       UserSummary      `json:"user"`
	StreakDays *int             `json:"streakDays,omitempty"`
	XPLevel    *int             `json:"xpLevel,omitempty"`
	BadgeCount *int             `json:"badgeCount,omitempty"`
	JoinedAt   *apijson.Time    `json:"joinedAt,omitempty"`
	Showcase   *[]ShowcaseEntry `json:"showcase,omitempty"`
}

func (h *handlers) profile(c *echo.Context) error {
	ctx := c.Request().Context()
	viewer := sessionUser(c)
	target, err := uuidParam(c, "userId")
	if err != nil {
		return err
	}
	if err := h.requireVisible(ctx, target, viewer); err != nil {
		return err
	}
	summary, err := h.svc.Summary(ctx, target, viewer)
	if err != nil {
		return err
	}
	out := profileDTO{User: summary}
	u, err := socialdb.New(h.svc.DB).UserBasic(ctx, target)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return err
	default:
		out.JoinedAt = &apijson.Time{Time: u.CreatedAt}
	}
	stats, err := h.xp.ProfileStats(ctx, h.svc, target, viewer, ZoneFromHeader(c.Request().Header))
	if err != nil {
		return err
	}
	out.StreakDays, out.XPLevel = stats.StreakDays, stats.XPLevel
	if AchievementsEnabled() {
		access, err := h.svc.resolveAchievementVisibility(ctx, viewer, target)
		if err != nil {
			return err
		}
		if access.canSee {
			rows, err := h.svc.visibleAchievementRows(ctx, target, access)
			if err != nil {
				return err
			}
			n := len(rows)
			out.BadgeCount = &n
			// A pointer: an empty showcase still encodes as [].
			list := showcase(rows, achievementLanguagePT(c.Request().Header.Get("Accept-Language")))
			out.Showcase = &list
		}
	}
	return httpx.OK(c, out)
}

func (h *handlers) friends(c *echo.Context) error {
	ctx := c.Request().Context()
	userID := sessionUser(c)
	hidden, err := h.svc.BlockedEitherWay(ctx, userID)
	if err != nil {
		return err
	}
	ids, err := h.svc.FriendIDs(ctx, userID)
	if err != nil {
		return err
	}
	for id := range hidden {
		delete(ids, id)
	}
	summaries, err := h.svc.Summaries(ctx, setKeys(ids), userID)
	if err != nil {
		return err
	}
	list := make([]UserSummary, 0, len(summaries))
	for _, s := range summaries {
		list = append(list, s)
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Username < list[j].Username })
	return httpx.OK(c, friendsResponse{Friends: list})
}

func (h *handlers) unfriend(c *echo.Context) error {
	target, err := uuidParam(c, "userId")
	if err != nil {
		return err
	}
	if err := h.svc.Unfriend(c.Request().Context(), sessionUser(c), target); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

func (h *handlers) requests(c *echo.Context) error {
	res, err := h.svc.Requests(c.Request().Context(), sessionUser(c))
	if err != nil {
		return err
	}
	return httpx.OK(c, res)
}

// POST /v1/social/friend-requests. Idempotent; when the other person
// already asked, this accepts their request instead.
func (h *handlers) sendRequest(c *echo.Context) error {
	ctx := c.Request().Context()
	userID := sessionUser(c)
	m, err := decodeBody(c, []field{{name: "userId", kind: kUUID, required: true}})
	if err != nil {
		return err
	}
	target := m["userId"].(uuid.UUID)
	if target == userID {
		return httpx.BadRequest("You can't add yourself.")
	}
	if err := h.requireVisible(ctx, target, userID); err != nil {
		return err
	}
	friends, err := h.svc.AreFriends(ctx, userID, target)
	if err != nil {
		return err
	}
	if friends {
		return httpx.Conflict("You're already friends.")
	}
	q := socialdb.New(h.svc.DB)
	reverse, err := q.RequestByPair(ctx, socialdb.RequestByPairParams{FromUserID: target, ToUserID: userID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil {
		summaries, err := h.svc.Summaries(ctx, []uuid.UUID{userID, target}, userID)
		if err != nil {
			return err
		}
		dto, ok := requestDTO(reverse, summaries)
		if err := h.svc.Befriend(ctx, userID, target); err != nil {
			return err
		}
		achievementhooks.Schedule(userID, target)
		h.pushSocial(ctx, push.SocialFriendAccepted, target, userID)
		if !ok {
			return httpx.Abort(http.StatusInternalServerError, "Friend request missing")
		}
		return httpx.OK(c, dto)
	}

	request, err := q.RequestByPair(ctx, socialdb.RequestByPairParams{FromUserID: userID, ToUserID: target})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		request = socialdb.SocialFriendRequest{ID: uuid.New(), FromUserID: userID, ToUserID: target, CreatedAt: time.Now()}
		if err := q.InsertFriendRequest(ctx, socialdb.InsertFriendRequestParams{
			ID: request.ID, FromUserID: userID, ToUserID: target, CreatedAt: request.CreatedAt,
		}); err != nil {
			return err
		}
		h.pushSocial(ctx, push.SocialFriendRequest, target, userID)
	case err != nil:
		return err
	}
	summaries, err := h.svc.Summaries(ctx, []uuid.UUID{userID, target}, userID)
	if err != nil {
		return err
	}
	dto, ok := requestDTO(request, summaries)
	if !ok {
		return httpx.Abort(http.StatusInternalServerError, "Friend request missing")
	}
	return httpx.OK(c, dto)
}

// incomingRequest is SocialController.incomingRequest.
func (h *handlers) incomingRequest(c *echo.Context, userID uuid.UUID) (socialdb.SocialFriendRequest, error) {
	id, err := uuidParam(c, "requestId")
	if err != nil {
		return socialdb.SocialFriendRequest{}, err
	}
	r, err := socialdb.New(h.svc.DB).RequestByID(c.Request().Context(), id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && r.ToUserID != userID) {
		return r, httpx.NotFound("Friend request not found")
	}
	return r, err
}

func (h *handlers) accept(c *echo.Context) error {
	ctx := c.Request().Context()
	userID := sessionUser(c)
	r, err := h.incomingRequest(c, userID)
	if err != nil {
		return err
	}
	if err := h.svc.Befriend(ctx, userID, r.FromUserID); err != nil {
		return err
	}
	achievementhooks.Schedule(userID, r.FromUserID)
	h.pushSocial(ctx, push.SocialFriendAccepted, r.FromUserID, userID)
	return httpx.NoContent(c)
}

// decline: the requester is not told.
func (h *handlers) decline(c *echo.Context) error {
	r, err := h.incomingRequest(c, sessionUser(c))
	if err != nil {
		return err
	}
	if err := socialdb.New(h.svc.DB).DeleteFriendRequest(c.Request().Context(), r.ID); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

func (h *handlers) cancel(c *echo.Context) error {
	ctx := c.Request().Context()
	id, err := uuidParam(c, "requestId")
	if err != nil {
		return err
	}
	q := socialdb.New(h.svc.DB)
	r, err := q.RequestByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && r.FromUserID != sessionUser(c)) {
		return httpx.NotFound("Friend request not found")
	}
	if err != nil {
		return err
	}
	if err := q.DeleteFriendRequest(ctx, r.ID); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

func (h *handlers) createInvite(c *echo.Context) error {
	link, err := h.svc.Invite(c.Request().Context(), sessionUser(c), ConfigFromEnv())
	if err != nil {
		return err
	}
	return httpx.OK(c, link)
}

func (h *handlers) invitePreview(c *echo.Context) error {
	ctx := c.Request().Context()
	userID := sessionUser(c)
	code, err := codeParam(c)
	if err != nil {
		return err
	}
	inviter, err := h.svc.Inviter(ctx, code, userID)
	if err != nil {
		return err
	}
	sum, err := h.svc.Summary(ctx, inviter, userID)
	if err != nil {
		return err
	}
	return httpx.OK(c, sum)
}

// POST /v1/social/invites/:code/redeem: the inviter asked by sharing the
// link, so redeeming befriends straight away.
func (h *handlers) redeemInvite(c *echo.Context) error {
	ctx := c.Request().Context()
	userID := sessionUser(c)
	code, err := codeParam(c)
	if err != nil {
		return err
	}
	inviter, err := h.svc.Inviter(ctx, code, userID)
	if err != nil {
		return err
	}
	if inviter == userID {
		return httpx.BadRequest("That's your own invite link.")
	}
	already, err := h.svc.AreFriends(ctx, userID, inviter)
	if err != nil {
		return err
	}
	if !already {
		if err := h.svc.Befriend(ctx, userID, inviter); err != nil {
			return err
		}
		achievementhooks.Schedule(userID, inviter)
		h.pushSocial(ctx, push.SocialFriendAccepted, inviter, userID)
		// A new account joining through the link credits the inviter
		// (social.inviter). Best effort: the friendship is committed.
		credited, err := h.svc.RecordInviteRedemption(ctx, code, userID, time.Now())
		switch {
		case err != nil:
			h.log.Warn("social.invite redemption failed", "error", err)
		case credited != nil:
			achievementhooks.Schedule(*credited)
		}
	}
	sum, err := h.svc.Summary(ctx, inviter, userID)
	if err != nil {
		return err
	}
	return httpx.OK(c, struct {
		Inviter UserSummary `json:"inviter"`
	}{sum})
}

func (h *handlers) privacy(c *echo.Context) error {
	rec, err := h.svc.EnsureSettings(c.Request().Context(), sessionUser(c), ConfigFromEnv())
	if err != nil {
		return err
	}
	return httpx.OK(c, privacyOf(rec))
}

var searchVisibilities = []string{VisibilityEveryone, VisibilityFriendsOfFriends, VisibilityNobody}

// PUT /v1/social/privacy. Apps released before Facebook import (and before
// achievements) don't send those fields; the stored values are kept.
func (h *handlers) updatePrivacy(c *echo.Context) error {
	ctx := c.Request().Context()
	userID := sessionUser(c)
	m, err := decodeBody(c, []field{
		{name: "searchVisibility", kind: kEnum, required: true, enum: "SocialSearchVisibility", values: searchVisibilities},
		{name: "discoverableByContacts", kind: kBool, required: true},
		{name: "discoverableByX", kind: kBool, required: true},
		{name: "discoverableByFacebook", kind: kBool},
		{name: "showReturnPercent", kind: kBool, required: true},
		{name: "showStreaks", kind: kBool, required: true},
		{name: "showXP", kind: kBool, required: true},
		{name: "leaderboardOptIn", kind: kBool, required: true},
		{name: "showAchievements", kind: kBool},
	})
	if err != nil {
		return err
	}
	rec, err := h.svc.EnsureSettings(ctx, userID, ConfigFromEnv())
	if err != nil {
		return err
	}
	rec.SearchVisibility = str(m, "searchVisibility")
	rec.DiscoverableByContacts = boolean(m, "discoverableByContacts")
	rec.DiscoverableByX = boolean(m, "discoverableByX")
	if v := optBool(m, "discoverableByFacebook"); v != nil {
		rec.DiscoverableByFacebook = *v
	}
	rec.ShowReturnPercent = boolean(m, "showReturnPercent")
	rec.ShowStreaks = boolean(m, "showStreaks")
	rec.ShowXp = boolean(m, "showXP")
	rec.LeaderboardOptIn = boolean(m, "leaderboardOptIn")
	if v := optBool(m, "showAchievements"); v != nil {
		rec.ShowAchievements = *v
	}
	now := time.Now()
	if err := socialdb.New(h.svc.DB).UpdateSettingsPrivacy(ctx, socialdb.UpdateSettingsPrivacyParams{
		SearchVisibility: rec.SearchVisibility, DiscoverableByContacts: rec.DiscoverableByContacts,
		DiscoverableByX: rec.DiscoverableByX, DiscoverableByFacebook: rec.DiscoverableByFacebook,
		ShowReturnPercent: rec.ShowReturnPercent, ShowStreaks: rec.ShowStreaks, ShowXp: rec.ShowXp,
		LeaderboardOptIn: rec.LeaderboardOptIn, ShowAchievements: rec.ShowAchievements, UpdatedAt: &now, ID: rec.ID,
	}); err != nil {
		return err
	}
	return httpx.OK(c, privacyOf(rec))
}

func (h *handlers) blocks(c *echo.Context) error {
	ctx := c.Request().Context()
	userID := sessionUser(c)
	rows, err := socialdb.New(h.svc.DB).BlocksByBlocker(ctx, userID)
	if err != nil {
		return err
	}
	summaries, err := h.svc.Summaries(ctx, rows, userID)
	if err != nil {
		return err
	}
	out := []UserSummary{}
	for _, id := range rows {
		if s, ok := summaries[id]; ok {
			out = append(out, s)
		}
	}
	return httpx.OK(c, struct {
		Users []UserSummary `json:"users"`
	}{out})
}

// POST /v1/social/blocks/:userId ends the friendship and pending requests
// both ways. Idempotent.
func (h *handlers) block(c *echo.Context) error {
	ctx := c.Request().Context()
	userID := sessionUser(c)
	target, err := uuidParam(c, "userId")
	if err != nil {
		return err
	}
	if target == userID {
		return httpx.BadRequest("You can't block yourself.")
	}
	u, err := socialdb.New(h.svc.DB).UserBasic(ctx, target)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NotFound("User not found")
	}
	if err != nil {
		return err
	}
	// Same rule as the Boards block: the Daily post stays pinned.
	if users.IsSystemEmail(u.Email) {
		return httpx.BadRequest("That account can't be blocked.")
	}
	if err := h.svc.tx(ctx, func(q *socialdb.Queries) error {
		exists, err := q.BlockExists(ctx, socialdb.BlockExistsParams{BlockerID: userID, BlockedID: target})
		if err != nil {
			return err
		}
		if !exists {
			if err := q.InsertBlock(ctx, socialdb.InsertBlockParams{ID: uuid.New(), BlockerID: userID, BlockedID: target, CreatedAt: time.Now()}); err != nil {
				return err
			}
		}
		if err := q.DeleteFriendshipPair(ctx, socialdb.DeleteFriendshipPairParams{A: userID, B: target}); err != nil {
			return err
		}
		return q.DeleteRequestsBetween(ctx, socialdb.DeleteRequestsBetweenParams{A: userID, B: target})
	}); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

func (h *handlers) unblock(c *echo.Context) error {
	target, err := uuidParam(c, "userId")
	if err != nil {
		return err
	}
	if err := socialdb.New(h.svc.DB).DeleteBlock(c.Request().Context(), socialdb.DeleteBlockParams{
		BlockerID: sessionUser(c), BlockedID: target,
	}); err != nil {
		return err
	}
	return httpx.NoContent(c)
}

var (
	reportTargetTypes = []string{"user", "message"}
	reportReasons     = []string{"spam", "harassment", "hate", "scam", "impersonation", "inappropriate", "other"}
)

// POST /v1/social/reports: 202, queued for human review.
func (h *handlers) report(c *echo.Context) error {
	ctx := c.Request().Context()
	userID := sessionUser(c)
	m, err := decodeBody(c, []field{
		{name: "targetType", kind: kEnum, required: true, enum: "SocialReportTargetType", values: reportTargetTypes},
		{name: "targetId", kind: kString, required: true},
		{name: "reason", kind: kEnum, required: true, enum: "SocialReportReason", values: reportReasons},
		{name: "note", kind: kString},
	})
	if err != nil {
		return err
	}
	targetID := strings.TrimSpace(str(m, "targetId"))
	if targetID == "" || utf8.RuneCountInString(targetID) > 100 {
		return httpx.BadRequest("A report needs a target.")
	}
	var note *string
	if n := optStr(m, "note"); n != nil {
		t := strings.TrimSpace(*n)
		if utf8.RuneCountInString(t) > 1000 {
			return httpx.BadRequest("Keep the details under 1,000 characters.")
		}
		if t != "" {
			note = &t
		}
	}
	targetType, reason := str(m, "targetType"), str(m, "reason")
	q := socialdb.New(h.svc.DB)
	if err := q.InsertReport(ctx, socialdb.InsertReportParams{
		ID: uuid.New(), ReporterID: userID, TargetType: targetType, TargetID: targetID, Reason: reason, Note: note, CreatedAt: time.Now(),
	}); err != nil {
		return err
	}
	h.log.Info("social.report filed", "target_type", targetType, "reason", reason)
	h.notifyNewReport(ctx, q, targetType, targetID, reason)
	return c.NoContent(http.StatusAccepted)
}

// notifyNewReport is SocialModeration.notifyNewReport: pings the team's
// Discord. Best effort.
func (h *handlers) notifyNewReport(ctx context.Context, q *socialdb.Queries, targetType, targetID, reason string) {
	open, err := q.CountOpenReports(ctx)
	if err != nil {
		h.log.Warn("social.report notify failed", "error", err)
		return
	}
	target := targetID
	if targetType == "user" {
		if id, err := uuid.Parse(targetID); err == nil && len(targetID) == 36 {
			if u, err := q.UserBasic(ctx, id); err == nil && u.Username != nil {
				target = fmt.Sprintf("@%s (%s)", *u.Username, targetID)
			}
		}
	}
	msg := fmt.Sprintf("🚩 New social report: %s on %s %s. %d open. Review within 24 hours: GET /v1/admin/social/reports",
		reason, targetType, target, open)
	if err := h.discord.Send(ctx, msg); err != nil {
		h.log.Warn("social.report notify failed", "error", err)
	}
}

// ContactMatch is SocialContactMatch.
type ContactMatch struct {
	Hash string      `json:"hash"`
	User UserSummary `json:"user"`
}

// POST /v1/social/discovery/contacts/match: on-device hashes of the
// caller's contacts against users who allow it. Nothing submitted is stored.
func (h *handlers) matchContacts(c *echo.Context) error {
	ctx := c.Request().Context()
	userID := sessionUser(c)
	cfg := ConfigFromEnv()
	if !cfg.ContactsDiscovery {
		return httpx.NotFound("Contact matching is not available.")
	}
	m, err := decodeBody(c, []field{
		{name: "hashVersion", kind: kInt, required: true},
		{name: "items", kind: kArray, required: true, items: []field{
			{name: "hash", kind: kString, required: true},
			{name: "kind", kind: kEnum, required: true, enum: "SocialContactKind", values: []string{"email", "phone"}},
		}},
	})
	if err != nil {
		return err
	}
	if v, _ := m["hashVersion"].(int); v != ContactHashVersion {
		return httpx.Unprocessable("Unsupported contact hash version.")
	}
	items, _ := m["items"].([]map[string]any)
	if len(items) > 1000 {
		return httpx.Abort(http.StatusRequestEntityTooLarge, "Send at most 1,000 contacts per request.")
	}
	if _, err := h.svc.EnsureSettings(ctx, userID, cfg); err != nil {
		return err
	}
	seen := map[string]bool{}
	var hashes []string
	for _, it := range items {
		hash := str(it, "hash")
		if str(it, "kind") == "email" && contactHashWellFormed(hash) && !seen[hash] {
			seen[hash] = true
			hashes = append(hashes, hash)
		}
	}
	out := struct {
		Matches []ContactMatch `json:"matches"`
	}{[]ContactMatch{}}
	if len(hashes) == 0 {
		return httpx.OK(c, out)
	}
	rows, err := socialdb.New(h.svc.DB).ContactMatches(ctx, socialdb.ContactMatchesParams{Hashes: hashes, Viewer: userID})
	if err != nil {
		return err
	}
	hidden, err := h.svc.BlockedEitherWay(ctx, userID)
	if err != nil {
		return err
	}
	var visible []socialdb.ContactMatchesRow
	var ids []uuid.UUID
	for _, r := range rows {
		if _, ok := hidden[r.UserID]; !ok {
			visible = append(visible, r)
			ids = append(ids, r.UserID)
		}
	}
	summaries, err := h.svc.Summaries(ctx, ids, userID)
	if err != nil {
		return err
	}
	for _, r := range visible {
		if u, ok := summaries[r.UserID]; ok && r.EmailHash != nil {
			out.Matches = append(out.Matches, ContactMatch{Hash: *r.EmailHash, User: u})
		}
	}
	return httpx.OK(c, out)
}

// pushSocial is SocialController.push: best effort, a failed push never
// fails the request that caused it.
func (h *handlers) pushSocial(ctx context.Context, kind string, recipient, actor uuid.UUID) {
	devices, err := h.push.Devices.Active(ctx, recipient)
	if err != nil {
		h.log.Warn("social.push failed", "kind", kind, "error", err)
		return
	}
	if len(devices) == 0 {
		return
	}
	name := "Someone"
	u, err := socialdb.New(h.svc.DB).UserBasic(ctx, actor)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		h.log.Warn("social.push failed", "kind", kind, "error", err)
		return
	case u.Username != nil:
		name = "@" + *u.Username
	}
	var n push.Notification
	switch kind {
	case push.SocialFriendRequest:
		n = push.SocialEvent(kind, "New friend request", name+" wants to be friends on Norviq.")
	case push.SocialFriendAccepted:
		n = push.SocialEvent(kind, "You're now friends", name+" is now your friend on Norviq.")
	default:
		return
	}
	h.push.Sender.Send(ctx, n, devices)
}

// ---- GamificationController -------------------------------------------------

func (h *handlers) xpSummary(c *echo.Context) error {
	res, err := h.xp.Summary(c.Request().Context(), sessionUser(c), time.Now(), ZoneFromHeader(c.Request().Header))
	if err != nil {
		return err
	}
	return httpx.OK(c, res)
}

func (h *handlers) xpEvents(c *echo.Context) error {
	limit := httpx.IntOr(httpx.QueryOptionalInt(c, "limit"), 30)
	res, err := h.xp.Events(c.Request().Context(), sessionUser(c), httpx.QueryString(c, "cursor"), limit)
	if err != nil {
		return err
	}
	return httpx.OK(c, res)
}

func (h *handlers) streaks(c *echo.Context) error {
	res, err := h.xp.Streaks(c.Request().Context(), sessionUser(c), time.Now(), ZoneFromHeader(c.Request().Header))
	if err != nil {
		return err
	}
	return httpx.OK(c, res)
}

func (h *handlers) checkIn(c *echo.Context) error {
	userID := sessionUser(c)
	res, err := h.xp.CheckIn(c.Request().Context(), userID, time.Now(), ZoneFromHeader(c.Request().Header))
	if err != nil {
		return err
	}
	achievementhooks.Schedule(userID)
	return httpx.OK(c, res)
}

// POST /v1/gamification/streaks/budget: the app reports the budget streak
// it shows; the server re-derives it from expense data and keeps the lower.
func (h *handlers) reportBudgetStreak(c *echo.Context) error {
	ctx := c.Request().Context()
	userID := sessionUser(c)
	m, err := decodeBody(c, []field{{name: "months", kind: kInt, required: true}})
	if err != nil {
		return err
	}
	verified, err := h.xp.VerifiedBudgetStreak(ctx, userID)
	if err != nil {
		return err
	}
	if _, err := h.xp.RecordBudgetStreak(ctx, userID, m["months"].(int), verified); err != nil {
		return err
	}
	achievementhooks.Schedule(userID)
	res, err := h.xp.Streaks(ctx, userID, time.Now(), ZoneFromHeader(c.Request().Header))
	if err != nil {
		return err
	}
	return httpx.OK(c, res)
}

func (h *handlers) leaderboard(c *echo.Context) error {
	metric := MetricXP
	if v := httpx.QueryString(c, "metric"); v != nil {
		metric = *v
	}
	switch metric {
	case MetricReturnPercent, MetricXP, MetricCheckInStreak, MetricBudgetStreak:
	default:
		return httpx.BadRequest("Unknown metric.")
	}
	period := PeriodWeek
	if v := httpx.QueryString(c, "period"); v != nil {
		period = *v
	}
	if period != PeriodWeek && period != PeriodMonth {
		return httpx.BadRequest("Unknown period.")
	}
	res, err := leaderboards{svc: h.svc, xp: h.xp}.build(c.Request().Context(), metric, period, sessionUser(c), time.Now(),
		ZoneFromHeader(c.Request().Header))
	if err != nil {
		return err
	}
	return httpx.OK(c, res)
}
