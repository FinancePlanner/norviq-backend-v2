package achievements

import (
	"context"
	"fmt"
	"slices"
	"time"

	"norviq-api/internal/platform/push"
	"norviq-api/internal/store/achievementsdb"

	"github.com/google/uuid"
)

// capability is AchievementNotifier.capability: a device whose app can open
// achievement pushes.
const capability = "achievements_v1"

// SocialPushMessage kinds for achievement news.
const (
	kindAchievementUnlocked = "achievement_unlocked"
	kindAchievementCongrats = "achievement_congrats"
)

// achievementPush is sendSocialEvent for an achievement kind: same payload
// as push.SocialEvent, but SocialPushMessage.threadID is "achievements" for
// these kinds.
func achievementPush(kind, title, body string) push.Notification {
	n := push.SocialEvent(kind, title, body)
	n.ThreadID = "achievements"
	return n
}

func isCapable(d *push.Device) bool {
	return slices.Contains(push.DecodeCapabilities(d.CapabilitiesJson), capability)
}

// capableDevices is pushDeviceService.activeDevices filtered to devices
// with the achievements capability.
func (s *Service) capableDevices(ctx context.Context, userID uuid.UUID) ([]push.Device, error) {
	if s.Push == nil {
		return nil, nil
	}
	devices, err := s.Push.Devices.Active(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := devices[:0:0]
	for i := range devices {
		if isCapable(&devices[i]) {
			out = append(out, devices[i])
		}
	}
	return out, nil
}

// unlockMessage is AchievementNotifier.message(for:): English copy, the
// achievement's real title for one unlock (with its tier when tiered), or a
// count for several. ok is false for none.
func unlockMessage(unlocks []Unlock) (title, body string, ok bool) {
	switch len(unlocks) {
	case 0:
		return "", "", false
	case 1:
		return "Achievement unlocked", unlockTitle(&unlocks[0]), true
	}
	return "Achievements unlocked", fmt.Sprintf("%d achievements unlocked", len(unlocks)), true
}

func unlockTitle(u *Unlock) string {
	base := "New achievement"
	if d := lookupWithLegacy(u.AchievementKey); d != nil {
		base = d.EN.Title
	}
	if u.Tier == "" {
		return base
	}
	return base + " (" + capitalized(u.Tier) + ")"
}

// NotifyUnlocks is AchievementNotifier.notifyUnlocks: pushes the live and
// sweep unlocks among unlocks (one push per evaluation) and stamps their
// notified_at. Nothing without a capable device. Never fails (logged).
func (s *Service) NotifyUnlocks(ctx context.Context, unlocks []Unlock, userID uuid.UUID) {
	var news []Unlock
	for i := range unlocks {
		if unlocks[i].Source == SourceLive || unlocks[i].Source == SourceSweep {
			news = append(news, unlocks[i])
		}
	}
	title, body, ok := unlockMessage(news)
	if !ok {
		return
	}
	devices, err := s.capableDevices(ctx, userID)
	if err != nil {
		s.Logger.Warn("achievements.push failed", "error", err)
		return
	}
	if len(devices) == 0 {
		return
	}
	_ = s.Push.Sender.Send(ctx, achievementPush(kindAchievementUnlocked, title, body), devices)
	ids := make([]uuid.UUID, len(news))
	for i := range news {
		ids[i] = news[i].ID
	}
	now := time.Now()
	if err := s.q().MarkAchievementsNotified(ctx, achievementsdb.MarkAchievementsNotifiedParams{Now: &now, Ids: ids}); err != nil {
		s.Logger.Warn("achievements.push failed", "error", err)
	}
}

// hookEvaluate is the AchievementHooks run: evaluateBestEffort, then the
// unlock notification unless the server shut down meanwhile.
func (s *Service) hookEvaluate(ctx context.Context, userID uuid.UUID) {
	unlocks := s.EvaluateBestEffort(ctx, userID, time.Now())
	if len(unlocks) > 0 && ctx.Err() == nil {
		s.NotifyUnlocks(ctx, unlocks, userID)
	}
}
