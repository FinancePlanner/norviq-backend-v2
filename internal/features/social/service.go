package social

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"sort"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/store/socialdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// DB is what the service needs from Postgres: queries plus transactions
// (*pgxpool.Pool and pgx.Tx both satisfy it).
type DB interface {
	socialdb.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Service is SocialService: the friends graph rules. Blocking is invisible:
// anything that names a user who blocked the caller, or whom the caller
// blocked, answers 404. Other domains may reuse its exported methods.
type Service struct {
	DB  DB
	Now func() time.Time
}

// NewService binds the service to db.
func NewService(db DB) *Service { return &Service{DB: db} }

func (s *Service) q() *socialdb.Queries { return socialdb.New(s.DB) }

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// tx runs fn in a transaction, as Fluent's db.transaction did.
func (s *Service) tx(ctx context.Context, fn func(q *socialdb.Queries) error) error {
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error { return fn(socialdb.New(tx)) })
}

// FriendshipStatus raw values (SocialFriendshipStatus).
const (
	StatusNone            = "none"
	StatusOutgoingPending = "outgoing_pending"
	StatusIncomingPending = "incoming_pending"
	StatusFriends         = "friends"
	StatusBlocked         = "blocked"
)

// Search visibility raw values (SocialSearchVisibility).
const (
	VisibilityEveryone         = "everyone"
	VisibilityFriendsOfFriends = "friends_of_friends"
	VisibilityNobody           = "nobody"
)

// UserSummary is SocialUserSummaryDTO.
type UserSummary struct {
	ID                apijson.UUID `json:"id"`
	Username          string       `json:"username"`
	DisplayName       *string      `json:"displayName,omitempty"`
	AvatarURL         *string      `json:"avatarUrl,omitempty"`
	FriendshipStatus  string       `json:"friendshipStatus"`
	MutualFriendCount *int         `json:"mutualFriendCount,omitempty"`
}

// PrivacySettings is SocialPrivacySettingsDTO.
type PrivacySettings struct {
	SearchVisibility       string `json:"searchVisibility"`
	DiscoverableByContacts bool   `json:"discoverableByContacts"`
	DiscoverableByX        bool   `json:"discoverableByX"`
	DiscoverableByFacebook bool   `json:"discoverableByFacebook"`
	ShowReturnPercent      bool   `json:"showReturnPercent"`
	ShowStreaks            bool   `json:"showStreaks"`
	ShowXP                 bool   `json:"showXP"`
	LeaderboardOptIn       bool   `json:"leaderboardOptIn"`
	ShowAchievements       bool   `json:"showAchievements"`
}

// DefaultPrivacy is SocialPrivacySettingsDTO.default: return % stays private
// until the user opts in.
var DefaultPrivacy = PrivacySettings{
	SearchVisibility:       VisibilityEveryone,
	DiscoverableByContacts: true,
	DiscoverableByX:        true,
	DiscoverableByFacebook: true,
	ShowReturnPercent:      false,
	ShowStreaks:            true,
	ShowXP:                 true,
	LeaderboardOptIn:       true,
	ShowAchievements:       true,
}

// privacyOf is SocialSettingsRecord.dto.
func privacyOf(r socialdb.SocialSetting) PrivacySettings {
	vis := r.SearchVisibility
	switch vis {
	case VisibilityEveryone, VisibilityFriendsOfFriends, VisibilityNobody:
	default:
		vis = VisibilityEveryone
	}
	return PrivacySettings{
		SearchVisibility:       vis,
		DiscoverableByContacts: r.DiscoverableByContacts,
		DiscoverableByX:        r.DiscoverableByX,
		DiscoverableByFacebook: r.DiscoverableByFacebook,
		ShowReturnPercent:      r.ShowReturnPercent,
		ShowStreaks:            r.ShowStreaks,
		ShowXP:                 r.ShowXp,
		LeaderboardOptIn:       r.LeaderboardOptIn,
		ShowAchievements:       r.ShowAchievements,
	}
}

// ---- Relationships ----------------------------------------------------------

// FriendIDs is SocialService.friendIds.
func (s *Service) FriendIDs(ctx context.Context, userID uuid.UUID) (map[uuid.UUID]struct{}, error) {
	ids, err := s.q().FriendIDs(ctx, userID)
	if err != nil {
		return nil, err
	}
	return toSet(ids), nil
}

// AreFriends is SocialService.areFriends.
func (s *Service) AreFriends(ctx context.Context, a, b uuid.UUID) (bool, error) {
	return s.q().AreFriends(ctx, socialdb.AreFriendsParams{UserID: a, FriendID: b})
}

// BlockedEitherWay is SocialService.blockedEitherWay: everyone the viewer
// must not see (people they blocked, people who blocked them, and users a
// moderator suspended from social), never the viewer themself.
func (s *Service) BlockedEitherWay(ctx context.Context, viewer uuid.UUID) (map[uuid.UUID]struct{}, error) {
	q := s.q()
	rows, err := q.BlocksInvolving(ctx, viewer)
	if err != nil {
		return nil, err
	}
	hidden := map[uuid.UUID]struct{}{}
	for _, r := range rows {
		if r.BlockerID == viewer {
			hidden[r.BlockedID] = struct{}{}
		} else {
			hidden[r.BlockerID] = struct{}{}
		}
	}
	suspended, err := q.SuspendedUserIDs(ctx)
	if err != nil {
		return nil, err
	}
	for _, id := range suspended {
		hidden[id] = struct{}{}
	}
	delete(hidden, viewer)
	return hidden, nil
}

// IsSuspended is SocialService.isSuspended.
func (s *Service) IsSuspended(ctx context.Context, userID uuid.UUID) (bool, error) {
	return s.q().IsSuspended(ctx, userID)
}

// IsBlockedEitherWay is SocialService.isBlockedEitherWay.
func (s *Service) IsBlockedEitherWay(ctx context.Context, a, b uuid.UUID) (bool, error) {
	hidden, err := s.BlockedEitherWay(ctx, a)
	if err != nil {
		return false, err
	}
	_, ok := hidden[b]
	return ok, nil
}

// Summaries is SocialService.summaries: userIDs as viewer sees them, keyed
// by id. Users that no longer exist are simply absent.
func (s *Service) Summaries(ctx context.Context, userIDs []uuid.UUID, viewer uuid.UUID) (map[uuid.UUID]UserSummary, error) {
	ids := setKeys(toSet(userIDs))
	result := map[uuid.UUID]UserSummary{}
	if len(ids) == 0 {
		return result, nil
	}
	q := s.q()
	users, err := q.UsersByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	viewerFriends, err := s.FriendIDs(ctx, viewer)
	if err != nil {
		return nil, err
	}
	blocked, err := q.ViewerBlockedAmong(ctx, socialdb.ViewerBlockedAmongParams{Viewer: viewer, Ids: ids})
	if err != nil {
		return nil, err
	}
	requests, err := q.RequestsWithViewer(ctx, socialdb.RequestsWithViewerParams{Viewer: viewer, Ids: ids})
	if err != nil {
		return nil, err
	}
	mutual := map[uuid.UUID]int{}
	if len(viewerFriends) > 0 {
		rows, err := q.MutualFriendRows(ctx, socialdb.MutualFriendRowsParams{UserIds: ids, FriendIds: setKeys(viewerFriends)})
		if err != nil {
			return nil, err
		}
		for _, id := range rows {
			mutual[id]++
		}
	}
	blockedSet := toSet(blocked)
	for _, u := range users {
		id := u.ID
		status := StatusNone
		_, isBlocked := blockedSet[id]
		_, isFriend := viewerFriends[id]
		switch {
		case isBlocked:
			status = StatusBlocked
		case isFriend:
			status = StatusFriends
		case hasRequest(requests, viewer, id):
			status = StatusOutgoingPending
		case hasRequest(requests, id, viewer):
			status = StatusIncomingPending
		}
		username := "user"
		if u.Username != nil {
			username = *u.Username
		}
		sum := UserSummary{ID: apijson.NewUUID(id), Username: username, AvatarURL: u.AvatarUrl, FriendshipStatus: status}
		if n, ok := mutual[id]; ok && id != viewer {
			sum.MutualFriendCount = &n
		}
		result[id] = sum
	}
	return result, nil
}

func hasRequest(rows []socialdb.SocialFriendRequest, from, to uuid.UUID) bool {
	for _, r := range rows {
		if r.FromUserID == from && r.ToUserID == to {
			return true
		}
	}
	return false
}

// Summary is SocialService.summary: 404 "User not found" when absent.
func (s *Service) Summary(ctx context.Context, userID, viewer uuid.UUID) (UserSummary, error) {
	m, err := s.Summaries(ctx, []uuid.UUID{userID}, viewer)
	if err != nil {
		return UserSummary{}, err
	}
	sum, ok := m[userID]
	if !ok {
		return UserSummary{}, httpx.NotFound("User not found")
	}
	return sum, nil
}

// ---- Settings ---------------------------------------------------------------

// EnsureSettings is SocialService.ensureSettings: the user's settings row,
// created on first use, with the email hash kept current.
func (s *Service) EnsureSettings(ctx context.Context, userID uuid.UUID, cfg Config) (socialdb.SocialSetting, error) {
	q := s.q()
	rec, err := q.SettingsByUser(ctx, userID)
	exists := true
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		exists = false
		rec = newSettingsRecord(userID, s.now())
	case err != nil:
		return rec, err
	}
	var expected *string
	if cfg.ContactPepper != nil {
		u, err := q.UserBasic(ctx, userID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return rec, err
		default:
			if h, ok := ContactHash(u.Email, *cfg.ContactPepper); ok {
				expected = &h
			}
		}
	}
	if exists && equalPtr(rec.EmailHash, expected) {
		return rec, nil
	}
	rec.EmailHash = expected
	if !exists {
		err = q.InsertSettings(ctx, socialdb.InsertSettingsParams{
			ID: rec.ID, UserID: userID, SearchVisibility: rec.SearchVisibility,
			DiscoverableByContacts: rec.DiscoverableByContacts, DiscoverableByX: rec.DiscoverableByX,
			DiscoverableByFacebook: rec.DiscoverableByFacebook, ShowReturnPercent: rec.ShowReturnPercent,
			ShowStreaks: rec.ShowStreaks, ShowXp: rec.ShowXp, LeaderboardOptIn: rec.LeaderboardOptIn,
			ShowAchievements: rec.ShowAchievements, EmailHash: rec.EmailHash, CreatedAt: rec.CreatedAt,
		})
		return rec, err
	}
	now := s.now()
	rec.UpdatedAt = &now
	return rec, q.UpdateSettingsEmailHash(ctx, socialdb.UpdateSettingsEmailHashParams{EmailHash: expected, UpdatedAt: &now, ID: rec.ID})
}

func newSettingsRecord(userID uuid.UUID, now time.Time) socialdb.SocialSetting {
	d := DefaultPrivacy
	return socialdb.SocialSetting{
		ID: uuid.New(), UserID: userID, SearchVisibility: d.SearchVisibility,
		DiscoverableByContacts: d.DiscoverableByContacts, DiscoverableByX: d.DiscoverableByX,
		DiscoverableByFacebook: d.DiscoverableByFacebook, ShowReturnPercent: d.ShowReturnPercent,
		ShowStreaks: d.ShowStreaks, ShowXp: d.ShowXP, LeaderboardOptIn: d.LeaderboardOptIn,
		ShowAchievements: d.ShowAchievements, CreatedAt: now, UpdatedAt: &now,
	}
}

// Settings is SocialService.settings: privacy settings for many users;
// users without a row get DefaultPrivacy.
func (s *Service) Settings(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]PrivacySettings, error) {
	result := map[uuid.UUID]PrivacySettings{}
	if len(userIDs) == 0 {
		return result, nil
	}
	rows, err := s.q().SettingsForUsers(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	for _, id := range userIDs {
		result[id] = DefaultPrivacy
	}
	for _, r := range rows {
		result[r.UserID] = privacyOf(r)
	}
	return result, nil
}

// ---- Search -----------------------------------------------------------------

// Search is SocialService.search: username prefix search. Usernames are
// stored lowercased and limited to [a-z0-9_], so anything else is dropped.
func (s *Service) Search(ctx context.Context, rawQuery string, viewer uuid.UUID) ([]UserSummary, error) {
	var b strings.Builder
	n := 0
	for _, r := range strings.ToLower(rawQuery) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			if n == 30 {
				break
			}
			b.WriteRune(r)
			n++
		}
	}
	query := b.String()
	out := []UserSummary{}
	if len(query) < 2 {
		return out, nil
	}
	escaped := strings.ReplaceAll(query, "_", `\_`)
	candidates, err := s.q().SearchUserIDs(ctx, socialdb.SearchUserIDsParams{Pattern: ptr(escaped + "%"), Viewer: viewer})
	if err != nil {
		return nil, err
	}
	hidden, err := s.BlockedEitherWay(ctx, viewer)
	if err != nil {
		return nil, err
	}
	visibleIDs := make([]uuid.UUID, 0, len(candidates))
	for _, id := range candidates {
		if _, ok := hidden[id]; !ok {
			visibleIDs = append(visibleIDs, id)
		}
	}
	settings, err := s.Settings(ctx, visibleIDs)
	if err != nil {
		return nil, err
	}
	summaries, err := s.Summaries(ctx, visibleIDs, viewer)
	if err != nil {
		return nil, err
	}
	for _, id := range visibleIDs {
		sum, ok := summaries[id]
		if !ok {
			continue
		}
		keep := sum.FriendshipStatus == StatusFriends
		if !keep {
			vis := VisibilityEveryone
			if st, ok := settings[id]; ok {
				vis = st.SearchVisibility
			}
			switch vis {
			case VisibilityEveryone:
				keep = true
			case VisibilityFriendsOfFriends:
				keep = sum.MutualFriendCount != nil && *sum.MutualFriendCount > 0
			}
		}
		if keep {
			out = append(out, sum)
		}
		if len(out) == 20 {
			break
		}
	}
	return out, nil
}

// ---- Friend requests --------------------------------------------------------

// FriendRequest is SocialFriendRequestDTO.
type FriendRequest struct {
	ID        apijson.UUID `json:"id"`
	From      UserSummary  `json:"from"`
	To        UserSummary  `json:"to"`
	CreatedAt apijson.Time `json:"createdAt"`
}

// FriendRequests is SocialFriendRequestsResponse.
type FriendRequests struct {
	Incoming []FriendRequest `json:"incoming"`
	Outgoing []FriendRequest `json:"outgoing"`
}

// Requests is SocialService.requests.
func (s *Service) Requests(ctx context.Context, viewer uuid.UUID) (FriendRequests, error) {
	out := FriendRequests{Incoming: []FriendRequest{}, Outgoing: []FriendRequest{}}
	rows, err := s.q().RequestsForViewer(ctx, viewer)
	if err != nil {
		return out, err
	}
	hidden, err := s.BlockedEitherWay(ctx, viewer)
	if err != nil {
		return out, err
	}
	var visible []socialdb.SocialFriendRequest
	var ids []uuid.UUID
	for _, r := range rows {
		_, hf := hidden[r.FromUserID]
		_, ht := hidden[r.ToUserID]
		if !hf && !ht {
			visible = append(visible, r)
			ids = append(ids, r.FromUserID, r.ToUserID)
		}
	}
	summaries, err := s.Summaries(ctx, ids, viewer)
	if err != nil {
		return out, err
	}
	for _, r := range visible {
		dto, ok := requestDTO(r, summaries)
		if !ok {
			continue
		}
		if r.ToUserID == viewer {
			out.Incoming = append(out.Incoming, dto)
		}
		if r.FromUserID == viewer {
			out.Outgoing = append(out.Outgoing, dto)
		}
	}
	return out, nil
}

// requestDTO is SocialService.dto(for:summaries:).
func requestDTO(r socialdb.SocialFriendRequest, summaries map[uuid.UUID]UserSummary) (FriendRequest, bool) {
	from, ok1 := summaries[r.FromUserID]
	to, ok2 := summaries[r.ToUserID]
	if !ok1 || !ok2 {
		return FriendRequest{}, false
	}
	return FriendRequest{ID: apijson.NewUUID(r.ID), From: from, To: to, CreatedAt: apijson.NewTime(r.CreatedAt)}, true
}

// Befriend is SocialService.befriend: writes both directions and clears
// pending requests between the two.
func (s *Service) Befriend(ctx context.Context, a, b uuid.UUID) error {
	now := s.now()
	return s.tx(ctx, func(q *socialdb.Queries) error {
		for _, p := range [][2]uuid.UUID{{a, b}, {b, a}} {
			if err := q.InsertFriendship(ctx, socialdb.InsertFriendshipParams{
				ID: uuid.New(), UserID: p[0], FriendID: p[1], CreatedAt: now,
			}); err != nil {
				return err
			}
		}
		return q.DeleteRequestsBetween(ctx, socialdb.DeleteRequestsBetweenParams{A: a, B: b})
	})
}

// Unfriend is SocialService.unfriend.
func (s *Service) Unfriend(ctx context.Context, a, b uuid.UUID) error {
	return s.q().DeleteFriendshipPair(ctx, socialdb.DeleteFriendshipPairParams{A: a, B: b})
}

// ---- Invites ----------------------------------------------------------------

// InviteLink is SocialInviteLinkDTO.
type InviteLink struct {
	Code      string        `json:"code"`
	URL       string        `json:"url"`
	ExpiresAt *apijson.Time `json:"expiresAt,omitempty"`
}

// inviteAlphabet is InviteCode.alphabet (Groups/GroupsService.swift).
const inviteAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"

// generateInviteCode is InviteCode.generate(length: 10).
func generateInviteCode() string {
	b := make([]byte, 10)
	limit := big.NewInt(int64(len(inviteAlphabet)))
	for i := range b {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			b[i] = 'x'
			continue
		}
		b[i] = inviteAlphabet[n.Int64()]
	}
	return string(b)
}

// Invite is SocialService.invite: one reusable invite code per user.
func (s *Service) Invite(ctx context.Context, userID uuid.UUID, cfg Config) (InviteLink, error) {
	q := s.q()
	existing, err := q.InviteByUser(ctx, userID)
	if err == nil {
		return inviteDTO(existing.Code, cfg), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return InviteLink{}, err
	}
	code := generateInviteCode()
	if err := q.InsertInvite(ctx, socialdb.InsertInviteParams{ID: uuid.New(), UserID: userID, Code: code, CreatedAt: s.now()}); err != nil {
		return InviteLink{}, err
	}
	return inviteDTO(code, cfg), nil
}

func inviteDTO(code string, cfg Config) InviteLink {
	base := strings.TrimSuffix(cfg.InviteBaseURL, "/")
	return InviteLink{Code: code, URL: base + "/i/" + code}
}

// InviteRedemptionWindow is SocialService.inviteRedemptionWindow: how new an
// account must be for redeeming an invite to credit the inviter.
const InviteRedemptionWindow = 7 * 24 * time.Hour

// RecordInviteRedemption is SocialService.recordInviteRedemption. It returns
// the inviter credited by this call, or nil.
func (s *Service) RecordInviteRedemption(ctx context.Context, code string, invitee uuid.UUID, now time.Time) (*uuid.UUID, error) {
	id, err := s.q().RecordInviteRedemption(ctx, socialdb.RecordInviteRedemptionParams{
		ID: uuid.New(), Now: now, Invitee: invitee, Code: code, Cutoff: now.Add(-InviteRedemptionWindow),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// Inviter is SocialService.inviter: the user behind code, unless either side
// blocked the other.
func (s *Service) Inviter(ctx context.Context, code string, viewer uuid.UUID) (uuid.UUID, error) {
	invite, err := s.q().InviteByCode(ctx, code)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, httpx.NotFound("Invite not found")
	}
	if err != nil {
		return uuid.Nil, err
	}
	blocked, err := s.IsBlockedEitherWay(ctx, invite.UserID, viewer)
	if err != nil {
		return uuid.Nil, err
	}
	if blocked {
		return uuid.Nil, httpx.NotFound("Invite not found")
	}
	return invite.UserID, nil
}

// ---- helpers ----------------------------------------------------------------

func toSet(ids []uuid.UUID) map[uuid.UUID]struct{} {
	m := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		m[id] = struct{}{}
	}
	return m
}

// setKeys returns the set's ids in a stable order.
func setKeys(m map[uuid.UUID]struct{}) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return strings.Compare(out[i].String(), out[j].String()) < 0 })
	return out
}

func equalPtr[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func ptr[T any](v T) *T { return &v }
