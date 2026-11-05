package achievements

import (
	"context"
	"fmt"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/store/achievementsdb"

	"github.com/google/uuid"
)

// showcaseLimit and unseenLimit are AchievementService's.
const (
	showcaseLimit = 3
	unseenLimit   = 20 // in UnseenAchievements
)

// Wire contract of /v1/achievements (AchievementDTOs.swift).

type tierDTO struct {
	Tier         string        `json:"tier"`
	Threshold    *int          `json:"threshold,omitempty"`
	XP           int           `json:"xp"`
	UnlockID     *apijson.UUID `json:"unlockId,omitempty"`
	UnlockedAt   *apijson.Time `json:"unlockedAt,omitempty"`
	ShowcaseRank *int          `json:"showcaseRank,omitempty"`
}

type progressDTO struct {
	Current int `json:"current"`
	Target  int `json:"target"`
}

type achievementDTO struct {
	Key           string       `json:"key"`
	Category      string       `json:"category"`
	Title         string       `json:"title"`
	Description   string       `json:"description"`
	IconKey       string       `json:"iconKey"`
	State         string       `json:"state"`
	CurrentTier   *string      `json:"currentTier,omitempty"`
	Progress      *progressDTO `json:"progress,omitempty"`
	IsPerformance bool         `json:"isPerformance"`
	Tiers         []tierDTO    `json:"tiers"`
}

type categoryDTO struct {
	Key   string `json:"key"`
	Title string `json:"title"`
}

type summaryDTO struct {
	Unlocked        int `json:"unlocked"`
	Total           int `json:"total"`
	HiddenRemaining int `json:"hiddenRemaining"`
}

// ResponseDTO is AchievementsResponseDTO.
type ResponseDTO struct {
	Summary      summaryDTO       `json:"summary"`
	Categories   []categoryDTO    `json:"categories"`
	Achievements []achievementDTO `json:"achievements"`
}

// UnlockDTO is AchievementUnlockDTO.
type UnlockDTO struct {
	ID          apijson.UUID `json:"id"`
	Key         string       `json:"key"`
	Tier        string       `json:"tier"`
	Title       string       `json:"title"`
	Description string       `json:"description"`
	IconKey     string       `json:"iconKey"`
	XP          int          `json:"xp"`
	UnlockedAt  apijson.Time `json:"unlockedAt"`
}

// ShowcaseItem is AchievementShowcaseItem.
type ShowcaseItem struct {
	Key  string `json:"key"`
	Tier string `json:"tier"`
}

// List is AchievementService.list.
func (s *Service) List(ctx context.Context, userID uuid.UUID, lang Language, now time.Time) (*ResponseDTO, error) {
	rows, err := s.q().UserAchievements(ctx, userID)
	if err != nil {
		return nil, err
	}
	rowsByKey := map[string]map[string]*Unlock{}
	hasLegacy := false
	for i := range rows {
		r := &rows[i]
		if rowsByKey[r.AchievementKey] == nil {
			rowsByKey[r.AchievementKey] = map[string]*Unlock{}
		}
		rowsByKey[r.AchievementKey][r.Tier] = r
		if strings.HasPrefix(r.AchievementKey, "legacy.") {
			hasLegacy = true
		}
	}

	definitions := make([]*Definition, 0, len(Catalog)+len(legacyDefinitions))
	for i := range Catalog {
		definitions = append(definitions, &Catalog[i])
	}
	if hasLegacy {
		for i := range legacyDefinitions {
			definitions = append(definitions, &legacyDefinitions[i])
		}
	}
	earnedCount := func(d *Definition) int {
		n := 0
		for _, t := range d.Tiers {
			if rowsByKey[d.Key][t.Tier] != nil {
				n++
			}
		}
		return n
	}

	// Progress only where it is cheap and allowed: never for performance,
	// hidden, sweep-only or legacy definitions, nor fully earned ones.
	measureKeys := map[string]bool{}
	for _, d := range definitions {
		if !d.IsPerformance && !d.IsHidden && !d.SweepOnly && !strings.HasPrefix(d.Key, "legacy.") && earnedCount(d) < len(d.Tiers) {
			measureKeys[d.Key] = true
		}
	}
	metrics := map[string]int{}
	if len(measureKeys) > 0 {
		metrics = measure(ctx, evalDB{db: s.DB}, s.Logger, userID, measureKeys, now).values
	}

	// An unearned hidden achievement is listed under an opaque key.
	hiddenOrdinals := map[string]int{}
	for i := range Catalog {
		if Catalog[i].IsHidden {
			hiddenOrdinals[Catalog[i].Key] = len(hiddenOrdinals) + 1
		}
	}

	out := &ResponseDTO{Achievements: make([]achievementDTO, 0, len(definitions))}
	for _, d := range definitions {
		isLegacy := strings.HasPrefix(d.Key, "legacy.")
		earned := earnedCount(d)
		maxed := earned == len(d.Tiers)
		if !isLegacy && !d.IsHidden {
			out.Summary.Total += len(d.Tiers)
			out.Summary.Unlocked += earned
		}
		if d.IsHidden && !maxed {
			out.Summary.HiddenRemaining++
		}

		if d.IsHidden && earned == 0 {
			tiers := make([]tierDTO, len(d.Tiers))
			for i, t := range d.Tiers {
				tiers[i] = tierDTO{Tier: t.Tier}
			}
			title, desc := "???", "Keep exploring."
			if lang == LangPT {
				desc = "Continua a explorar."
			}
			out.Achievements = append(out.Achievements, achievementDTO{
				Key: fmt.Sprintf("hidden.%d", hiddenOrdinals[d.Key]), Category: string(d.Category),
				Title: title, Description: desc, IconKey: "lock", State: "hidden",
				IsPerformance: d.IsPerformance, Tiers: tiers,
			})
			continue
		}

		metric, hasMetric := metrics[d.Key]
		state := "locked"
		switch {
		case maxed:
			state = "earned"
		case earned > 0 || (hasMetric && metric > 0):
			state = "inProgress"
		}
		var currentTier *string
		for _, t := range d.Tiers {
			if rowsByKey[d.Key][t.Tier] != nil {
				tier := t.Tier
				currentTier = &tier
			}
		}
		var progress *progressDTO
		if !maxed && !d.IsPerformance && !d.IsHidden && hasMetric {
			for _, t := range d.Tiers {
				if rowsByKey[d.Key][t.Tier] == nil {
					progress = &progressDTO{Current: min(metric, t.Threshold), Target: t.Threshold}
					break
				}
			}
		}
		text := d.Copy(lang)
		tiers := make([]tierDTO, len(d.Tiers))
		for i, t := range d.Tiers {
			threshold := t.Threshold
			td := tierDTO{Tier: t.Tier, Threshold: &threshold, XP: t.XP}
			if r := rowsByKey[d.Key][t.Tier]; r != nil {
				id := apijson.NewUUID(r.ID)
				at := apijson.NewTime(r.UnlockedAt)
				td.UnlockID, td.UnlockedAt = &id, &at
				if r.ShowcaseRank != nil {
					rank := int(*r.ShowcaseRank)
					td.ShowcaseRank = &rank
				}
			}
			tiers[i] = td
		}
		out.Achievements = append(out.Achievements, achievementDTO{
			Key: d.Key, Category: string(d.Category), Title: text.Title, Description: text.Description,
			IconKey: d.IconKey, State: state, CurrentTier: currentTier, Progress: progress,
			IsPerformance: d.IsPerformance, Tiers: tiers,
		})
	}

	out.Categories = make([]categoryDTO, 0, len(allCategories))
	for _, c := range allCategories {
		if c == CatClassic && !hasLegacy {
			continue
		}
		out.Categories = append(out.Categories, categoryDTO{Key: string(c), Title: c.Title(lang)})
	}
	return out, nil
}

// unlockDTO is the wire form of a stored unlock; nil for an unknown key.
func unlockDTO(r *Unlock, d *Definition, lang Language) UnlockDTO {
	text := d.Copy(lang)
	return UnlockDTO{
		ID: apijson.NewUUID(r.ID), Key: r.AchievementKey, Tier: r.Tier, Title: text.Title,
		Description: text.Description, IconKey: d.IconKey, XP: d.xpFor(r.Tier), UnlockedAt: apijson.NewTime(r.UnlockedAt),
	}
}

// Unseen is AchievementService.unseen: live and sweep unlocks not yet seen,
// oldest first, at most 20.
func (s *Service) Unseen(ctx context.Context, userID uuid.UUID, lang Language) ([]UnlockDTO, error) {
	rows, err := s.q().UnseenAchievements(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]UnlockDTO, 0, len(rows))
	for i := range rows {
		if d := lookupWithLegacy(rows[i].AchievementKey); d != nil {
			out = append(out, unlockDTO(&rows[i], d, lang))
		}
	}
	return out, nil
}

// MarkSeen is AchievementService.markSeen.
func (s *Service) MarkSeen(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now()
	return s.q().MarkAchievementsSeen(ctx, achievementsdb.MarkAchievementsSeenParams{Now: &now, UserID: userID, Ids: ids})
}

// SetShowcase is AchievementService.setShowcase: replaces the caller's
// showcase (ranks 1…n in order) and returns the refreshed listing.
func (s *Service) SetShowcase(ctx context.Context, userID uuid.UUID, items []ShowcaseItem, lang Language, now time.Time) (*ResponseDTO, error) {
	if len(items) > showcaseLimit {
		return nil, httpx.BadRequest(fmt.Sprintf("At most %d showcase items.", showcaseLimit))
	}
	seen := map[string]bool{}
	for _, it := range items {
		seen[it.Key+"|"+it.Tier] = true
	}
	if len(seen) != len(items) {
		return nil, httpx.BadRequest("Duplicate showcase items.")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	tq := achievementsdb.New(tx)
	matches := make([]uuid.UUID, 0, len(items))
	for _, it := range items {
		rows, err := tq.FindUserAchievement(ctx, achievementsdb.FindUserAchievementParams{UserID: userID, AchievementKey: it.Key, Tier: it.Tier})
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return nil, httpx.BadRequest("Achievement not earned.")
		}
		matches = append(matches, rows[0].ID)
	}
	if err := tq.ClearShowcase(ctx, userID); err != nil {
		return nil, err
	}
	for i, id := range matches {
		rank := int64(i + 1)
		if err := tq.SetShowcaseRank(ctx, achievementsdb.SetShowcaseRankParams{Rank: &rank, ID: id}); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.List(ctx, userID, lang, now)
}

// Digest is one achievement in the short form other features read.
type Digest struct {
	Title       string  `json:"title"`
	Category    string  `json:"category"`
	State       string  `json:"state"`
	CurrentTier *string `json:"currentTier,omitempty"`
}

// Digest lists achievements without their artwork and tier ladders.
func (s *Service) Digest(ctx context.Context, userID uuid.UUID, lang Language, now time.Time) ([]Digest, error) {
	full, err := s.List(ctx, userID, lang, now)
	if err != nil {
		return nil, err
	}
	out := make([]Digest, 0, len(full.Achievements))
	for i := range full.Achievements {
		a := &full.Achievements[i]
		out = append(out, Digest{Title: a.Title, Category: a.Category, State: a.State, CurrentTier: a.CurrentTier})
	}
	return out, nil
}
