package social_test

import (
	"net/http"
	"testing"
	"time"

	"norviq-api/internal/features/social"

	"github.com/google/uuid"
)

func TestLevelCurve(t *testing.T) {
	for total, want := range map[int]int{-5: 1, 0: 1, 99: 1, 100: 2, 299: 2, 300: 3, 600: 4, 4500: 10, 4499: 9} {
		if got := social.Level(total); got != want {
			t.Errorf("Level(%d) = %d, want %d", total, got, want)
		}
	}
	if p := social.LevelProgress(150); p != 0.25 {
		t.Errorf("progress = %v", p)
	}
	if social.XPForBadgeTier("gold") <= social.XPForBadgeTier("bronze") {
		t.Error("tiers not ordered")
	}
}

func TestStreakMath(t *testing.T) {
	days := func(ds ...int) map[int]struct{} {
		m := map[int]struct{}{}
		for _, d := range ds {
			m[d] = struct{}{}
		}
		return m
	}
	cases := []struct {
		days        map[int]struct{}
		today, want int
	}{
		{days(), 10, 0},
		{days(8, 9, 10), 10, 3},
		{days(7, 8, 9), 10, 3}, // yesterday still counts
		{days(7, 8), 10, 0},
		{days(9, 10, 11), 10, 3}, // a day ahead (zone change) counts
		{days(1, 2, 5, 6, 7), 7, 3},
	}
	for _, c := range cases {
		if got := social.CurrentStreak(c.days, c.today); got != c.want {
			t.Errorf("CurrentStreak(%v, %d) = %d, want %d", c.days, c.today, got, c.want)
		}
	}
	if got := social.LongestStreak(days(1, 2, 3, 7, 8, 20)); got != 3 {
		t.Errorf("longest = %d", got)
	}
	if d1, ok := social.DayNumber("2026-01-02"); !ok {
		t.Error("DayNumber failed")
	} else if d0, _ := social.DayNumber("2026-01-01"); d1-d0 != 1 {
		t.Error("DayNumber not consecutive")
	}
}

func TestZoneFromHeader(t *testing.T) {
	h := http.Header{}
	if z := social.ZoneFromHeader(h); z.ID != "GMT" {
		t.Fatalf("default = %q", z.ID)
	}
	h.Set("X-Timezone", "Europe/Lisbon")
	if z := social.ZoneFromHeader(h); z.ID != "Europe/Lisbon" {
		t.Fatalf("lisbon = %q", z.ID)
	}
	for _, bad := range []string{"Local", "Mars/Olympus", string(make([]byte, 65))} {
		h.Set("X-Timezone", bad)
		if z := social.ZoneFromHeader(h); z.ID != "GMT" {
			t.Fatalf("%q = %q", bad, z.ID)
		}
	}
}

func TestGamificationFlagsOff(t *testing.T) {
	e := setup(t, social.Feature{}, "SOCIAL_LEADERBOARDS_ENABLED", "0")
	a := e.user("gam_off1")
	for _, p := range []string{"/v1/gamification/xp", "/v1/gamification/streaks", "/v1/social/leaderboards"} {
		if s := e.status(http.MethodGet, p, a, nil); s != http.StatusNotFound {
			t.Fatalf("%s = %d", p, s)
		}
	}
	if s := e.status(http.MethodPost, "/v1/gamification/check-in", a, nil); s != http.StatusNotFound {
		t.Fatalf("check-in = %d", s)
	}
	if s := e.status(http.MethodGet, "/v1/gamification/xp", uuid.Nil, nil); s != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", s)
	}
}

func TestCheckInIdempotentAndXP(t *testing.T) {
	e := setup(t, social.Feature{})
	a := e.user("gam_chk1")
	var res social.CheckInResponse
	e.call(http.MethodPost, "/v1/gamification/check-in", a, nil, "X-Timezone", "Europe/Lisbon").decode(t, &res)
	if res.Streak != 1 || res.XPAwarded != 10 || res.AlreadyCheckedIn {
		t.Fatalf("first = %+v", res)
	}
	e.call(http.MethodPost, "/v1/gamification/check-in", a, nil, "X-Timezone", "Europe/Lisbon").decode(t, &res)
	if res.Streak != 1 || res.XPAwarded != 0 || !res.AlreadyCheckedIn {
		t.Fatalf("second = %+v", res)
	}
	var zone string
	e.scalar(`SELECT time_zone FROM gamification_check_ins WHERE user_id = $1`, &zone, a)
	if zone != "Europe/Lisbon" {
		t.Fatalf("zone = %q", zone)
	}
	var sum social.XPSummary
	e.get("/v1/gamification/xp", a, &sum)
	if sum.Total != 10 || sum.WeekXP != 10 || sum.Level != 1 || sum.LevelProgress != 0.1 {
		t.Fatalf("summary = %+v", sum)
	}
	var hist social.XPHistory
	e.get("/v1/gamification/xp/events?limit=1", a, &hist)
	if len(hist.Events) != 1 || hist.Events[0].Type != social.XPCheckIn || hist.Events[0].Points != 10 || hist.NextCursor != nil {
		t.Fatalf("events = %+v", hist)
	}
	var st social.StreakSummary
	e.get("/v1/gamification/streaks", a, &st)
	if st.CheckInCurrent != 1 || st.CheckInLongest != 1 || st.LastCheckInDate == nil {
		t.Fatalf("streaks = %+v", st)
	}
}

func TestSevenDayBonus(t *testing.T) {
	e := setup(t, social.Feature{})
	a := e.user("gam_bon1")
	today := time.Now().UTC()
	for i := 1; i <= 6; i++ {
		e.exec(`INSERT INTO gamification_check_ins (id, user_id, local_date, time_zone, created_at) VALUES ($1, $2, $3, 'GMT', now())`,
			uuid.New(), a, today.AddDate(0, 0, -i).Format("2006-01-02"))
	}
	var res social.CheckInResponse
	e.call(http.MethodPost, "/v1/gamification/check-in", a, nil).decode(t, &res)
	if res.Streak != 7 || res.XPAwarded != 60 {
		t.Fatalf("bonus = %+v", res)
	}
	var zone string
	e.scalar(`SELECT time_zone FROM gamification_check_ins WHERE user_id = $1 AND local_date = $2`, &zone, a, today.Format("2006-01-02"))
	if zone != "GMT" {
		t.Fatalf("default zone = %q", zone)
	}
}

func TestBudgetStreakKeepsTheLower(t *testing.T) {
	e := setup(t, social.Feature{})
	a := e.user("gam_bud1")
	var st social.StreakSummary
	e.call(http.MethodPost, "/v1/gamification/streaks/budget", a, map[string]any{"months": 12}).decode(t, &st)
	if st.BudgetMonths != 0 {
		t.Fatalf("unverified = %+v", st)
	}
	var xp int
	e.scalar(`SELECT count(*) FROM gamification_xp_events WHERE user_id = $1`, &xp, a)
	if xp != 0 {
		t.Fatalf("xp events = %d", xp)
	}
	if r := e.call(http.MethodPost, "/v1/gamification/streaks/budget", a, map[string]any{"months": "x"}); r.Status != 400 {
		t.Fatalf("bad body = %d %s", r.Status, r.Body)
	}
}

func TestLeaderboardFriendsOnly(t *testing.T) {
	e := setup(t, social.Feature{})
	a, b, c, d := e.user("gam_lb1"), e.user("gam_lb2"), e.user("gam_lb3"), e.user("gam_lb4")
	e.befriend(a, b)
	e.befriend(a, d)
	for _, u := range []uuid.UUID{a, b, c, d} {
		e.call(http.MethodPost, "/v1/gamification/check-in", u, nil)
	}
	e.exec(`INSERT INTO gamification_xp_events (id, user_id, type, points, dedupe_key, exclude_from_leaderboard, created_at)
		VALUES ($1, $2, 'badge_earned', 40, 'x', false, now()), ($3, $2, 'badge_earned', 500, 'y', true, now())`, uuid.New(), b, uuid.New())
	var lb social.Leaderboard
	e.get("/v1/social/leaderboards?metric=xp&period=week", a, &lb)
	if len(lb.Entries) != 3 || lb.Entries[0].User.ID.Std() != b || lb.Entries[0].Value != 50 || lb.Entries[0].Rank != 1 {
		t.Fatalf("board = %+v", lb.Entries)
	}
	for _, en := range lb.Entries {
		if en.User.ID.Std() == c {
			t.Fatal("non-friend on board")
		}
		if en.IsMe != (en.User.ID.Std() == a) {
			t.Fatalf("isMe = %+v", en)
		}
	}
	// Opt-out and blocks drop people.
	e.setPrivacy(d, func(m map[string]any) { m["leaderboardOptIn"] = false })
	e.call(http.MethodPost, "/v1/social/blocks/"+up(a), b, nil)
	e.get("/v1/social/leaderboards", a, &lb)
	if len(lb.Entries) != 1 || !lb.Entries[0].IsMe || lb.Metric != "xp" || lb.Period != "week" {
		t.Fatalf("after = %+v", lb)
	}
	e.get("/v1/social/leaderboards?metric=check_in_streak&period=month", a, &lb)
	if len(lb.Entries) != 1 || lb.Entries[0].Value != 1 {
		t.Fatalf("streak board = %+v", lb.Entries)
	}
	// Return % needs the owner to share it.
	e.get("/v1/social/leaderboards?metric=return_percent", a, &lb)
	if len(lb.Entries) != 0 {
		t.Fatalf("return board = %+v", lb.Entries)
	}
	for q, reason := range map[string]string{"metric=gold": "Unknown metric.", "period=year": "Unknown period."} {
		if r := e.call(http.MethodGet, "/v1/social/leaderboards?"+q, a, nil); r.Status != 400 || r.reason(t) != reason {
			t.Fatalf("%s = %d %s", q, r.Status, r.Body)
		}
	}
}
