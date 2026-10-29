package achievements

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Ported from AchievementRouteTests.swift.

type listResp struct {
	Summary struct {
		Unlocked, Total, HiddenRemaining int
	} `json:"summary"`
	Categories []struct {
		Key, Title string
	} `json:"categories"`
	Achievements []struct {
		Key           string  `json:"key"`
		Title         string  `json:"title"`
		IconKey       string  `json:"iconKey"`
		State         string  `json:"state"`
		CurrentTier   *string `json:"currentTier"`
		IsPerformance bool    `json:"isPerformance"`
		Progress      *struct {
			Current, Target int
		} `json:"progress"`
		Tiers []struct {
			Tier         string  `json:"tier"`
			Threshold    *int    `json:"threshold"`
			XP           int     `json:"xp"`
			UnlockID     *string `json:"unlockId"`
			ShowcaseRank *int    `json:"showcaseRank"`
		} `json:"tiers"`
	} `json:"achievements"`
}

func TestRoutesAre404WhenFlagOff(t *testing.T) {
	e := newEnv(t, "ACHIEVEMENTS_ENABLED", "0")
	_, tok := e.user("achr-off")
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/achievements", nil},
		{http.MethodGet, "/v1/achievements/unseen", nil},
		{http.MethodPost, "/v1/achievements/seen", map[string]any{"ids": []string{}}},
		{http.MethodPut, "/v1/achievements/showcase", map[string]any{"items": []any{}}},
	} {
		if r := e.Do(c.method, c.path, tok, c.body); r.Status != http.StatusNotFound {
			t.Fatalf("%s %s: %d", c.method, c.path, r.Status)
		}
	}
	if r := e.Do(http.MethodGet, "/v1/achievements", "", nil); r.Status != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", r.Status)
	}
}

func TestListShape(t *testing.T) {
	e := newEnv(t)
	e.activate()
	u, tok := e.user("achr-shape")
	e.addStock(u, "AAPL", 10)
	e.latch(u, "first_holding_at")
	r := e.Do(http.MethodGet, "/v1/achievements", tok, nil)
	if r.Status != http.StatusOK {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	var list listResp
	r.JSON(t, &list)
	byKey := map[string]int{}
	for i, a := range list.Achievements {
		byKey[a.Key] = i
	}
	first := list.Achievements[byKey["start.first_holding"]]
	if first.State != "earned" || first.CurrentTier == nil || *first.CurrentTier != "" || first.Progress != nil || first.Tiers[0].UnlockID == nil {
		t.Fatalf("first holding %+v", first)
	}
	check := list.Achievements[byKey["discipline.check_in"]]
	if check.State != "locked" || check.Progress == nil || check.Progress.Current != 0 || check.Progress.Target != 7 {
		t.Fatalf("check in %+v", check)
	}
	founding, ok := byKey["hidden.founding_member"]
	if !ok || list.Achievements[founding].State != "earned" || list.Achievements[founding].Title != "Founding Member" {
		t.Fatal("founding member")
	}
	for _, a := range list.Achievements {
		if a.IsPerformance && a.Progress != nil {
			t.Fatal("performance progress")
		}
		if strings.HasPrefix(a.Key, "legacy.") {
			t.Fatal("legacy without legacy rows")
		}
	}
	var cats []string
	for _, c := range list.Categories {
		cats = append(cats, c.Key)
	}
	if strings.Join(cats, ",") != "getting_started,discipline,craft,planning,learning,community,performance,hidden" {
		t.Fatalf("categories %v", cats)
	}
	if list.Summary.Unlocked < 1 {
		t.Fatal("summary")
	}
	// The listing writes activity but never pushes.
	if e.count(`SELECT COUNT(*) FROM social_activity WHERE actor_id = $1`, u) == 0 || len(e.sender.all()) != 0 {
		t.Fatal("activity/push")
	}
	if row := e.find(u, "start.first_holding", ""); row.NotifiedAt != nil {
		t.Fatal("listing notified")
	}
	// Raw shape: nil optionals omitted, zero values sent.
	var raw map[string]any
	_ = json.Unmarshal(r.Body, &raw)
	ach := raw["achievements"].([]any)
	checkRaw := ach[byKey["discipline.check_in"]].(map[string]any)
	if _, has := checkRaw["currentTier"]; has {
		t.Fatal("currentTier should be omitted")
	}
	if checkRaw["isPerformance"] != false {
		t.Fatal("isPerformance must be sent")
	}
}

func TestHiddenMaskedUntilEarned(t *testing.T) {
	lowFounding(t, 0)
	e := newEnv(t)
	_, tok := e.user("achr-hidden")
	var list listResp
	e.Do(http.MethodGet, "/v1/achievements", tok, nil).JSON(t, &list)
	found := false
	for _, a := range list.Achievements {
		if strings.Contains(a.Key, "founding") {
			t.Fatal("hidden key leaked")
		}
		if a.State == "hidden" {
			found = true
			if a.Key != "hidden.1" || a.Title != "???" || a.IconKey != "lock" || a.Progress != nil {
				t.Fatalf("hidden %+v", a)
			}
			for _, tier := range a.Tiers {
				if tier.Threshold != nil || tier.XP != 0 {
					t.Fatal("hidden tier details")
				}
			}
		}
	}
	if !found || list.Summary.HiddenRemaining < 1 {
		t.Fatal("no hidden entry")
	}
	r := e.Do(http.MethodPut, "/v1/achievements/showcase", tok, map[string]any{"items": []map[string]string{{"key": "hidden.1", "tier": ""}}})
	if r.Status != http.StatusBadRequest {
		t.Fatalf("opaque key accepted: %d", r.Status)
	}
}

func TestPortugueseCopy(t *testing.T) {
	e := newEnv(t)
	_, tok := e.user("achr-pt")
	var list listResp
	e.do(http.MethodGet, "/v1/achievements", tok, "pt-PT", nil).JSON(t, &list)
	for _, a := range list.Achievements {
		if a.Key == "start.first_holding" && a.Title != "Primeira posição" {
			t.Fatalf("title %q", a.Title)
		}
	}
	if list.Categories[0].Title != "Primeiros passos" {
		t.Fatal("category title")
	}
	if LanguageFrom(",pt") != LangPT || LanguageFrom(" ,pt") != LangEN || LanguageFrom("en-US,pt") != LangEN || LanguageFrom("PT-br") != LangPT {
		t.Fatal("language parsing")
	}
}

func TestUnseenThenSeen(t *testing.T) {
	e := newEnv(t)
	u, tok := e.user("achr-unseen")
	other, _ := e.user("achr-unseen2")
	now := time.Now()
	e.insertUnlock(u, "start.first_holding", "", SourceLive, false, now.Add(-10*time.Second))
	e.insertUnlock(u, "start.first_budget", "", SourceLive, false, now.Add(-5*time.Second))
	otherID := e.insertUnlock(other, "start.first_holding", "", SourceLive, false, now)

	var unseen []UnlockDTO
	e.Do(http.MethodGet, "/v1/achievements/unseen", tok, nil).JSON(t, &unseen)
	if len(unseen) != 2 || unseen[0].Key != "start.first_holding" || unseen[1].Key != "start.first_budget" || unseen[0].XP != 25 {
		t.Fatalf("unseen %+v", unseen)
	}
	r := e.Do(http.MethodPost, "/v1/achievements/seen", tok, map[string]any{"ids": []string{unseen[0].ID.String(), otherID.String()}})
	if r.Status != http.StatusNoContent {
		t.Fatalf("seen %d %s", r.Status, r.Body)
	}
	var after []UnlockDTO
	e.Do(http.MethodGet, "/v1/achievements/unseen", tok, nil).JSON(t, &after)
	if len(after) != 1 || after[0].Key != "start.first_budget" {
		t.Fatalf("after %+v", after)
	}
	if row := e.find(other, "start.first_holding", ""); row.SeenAt != nil {
		t.Fatal("other user's row changed")
	}
	if r := e.Do(http.MethodPost, "/v1/achievements/seen", tok, map[string]any{}); r.Status != http.StatusBadRequest {
		t.Fatalf("missing ids: %d", r.Status)
	}
}

func TestBackfilledNotUnseen(t *testing.T) {
	e := newEnv(t)
	u, tok := e.user("achr-backfill")
	e.markForBackfill(u)
	e.addStock(u, "AAPL", 10)
	e.insertUnlock(u, "start.first_budget", "", SourceBackfill, false, time.Now())
	e.Do(http.MethodGet, "/v1/achievements", tok, nil)
	if len(e.unlocks(u)) < 2 {
		t.Fatal("backfill did not run")
	}
	var unseen []UnlockDTO
	e.Do(http.MethodGet, "/v1/achievements/unseen", tok, nil).JSON(t, &unseen)
	if len(unseen) != 0 {
		t.Fatalf("unseen %+v", unseen)
	}
}

func TestShowcaseValidationAndRanks(t *testing.T) {
	e := newEnv(t)
	u, tok := e.user("achr-showcase")
	keys := []string{"start.first_holding", "start.first_budget", "start.first_goal", "hidden.founding_member"}
	for _, k := range keys {
		e.insertUnlock(u, k, "", SourceLive, true, time.Now())
	}
	put := func(items ...[2]string) *listResp {
		body := []map[string]string{}
		for _, it := range items {
			body = append(body, map[string]string{"key": it[0], "tier": it[1]})
		}
		r := e.Do(http.MethodPut, "/v1/achievements/showcase", tok, map[string]any{"items": body})
		if r.Status != http.StatusOK {
			return nil
		}
		var l listResp
		r.JSON(t, &l)
		return &l
	}
	four := [][2]string{{keys[0], ""}, {keys[1], ""}, {keys[2], ""}, {keys[3], ""}}
	if put(four...) != nil || put(four[0], four[0]) != nil || put([2]string{"discipline.check_in", "gold"}) != nil {
		t.Fatal("invalid showcase accepted")
	}
	ranks := func(l *listResp) map[string]int {
		out := map[string]int{}
		for _, a := range l.Achievements {
			for _, tr := range a.Tiers {
				if tr.ShowcaseRank != nil {
					out[a.Key] = *tr.ShowcaseRank
				}
			}
		}
		return out
	}
	res := put(four[1], four[0])
	if got := ranks(res); len(got) != 2 || got["start.first_budget"] != 1 || got["start.first_holding"] != 2 {
		t.Fatalf("ranks %v", got)
	}
	var again listResp
	e.Do(http.MethodGet, "/v1/achievements", tok, nil).JSON(t, &again)
	if got := ranks(&again); got["start.first_budget"] != 1 || got["start.first_holding"] != 2 {
		t.Fatalf("ranks after %v", got)
	}
	if got := ranks(put()); len(got) != 0 {
		t.Fatalf("cleared %v", got)
	}
	r := e.Do(http.MethodPut, "/v1/achievements/showcase", tok, map[string]any{"items": []map[string]string{{"key": "x"}, {"key": "y"}}})
	if r.Status != http.StatusBadRequest {
		t.Fatal("missing tier accepted")
	}
}
