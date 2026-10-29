package articles

import (
	"math"
	"time"
)

// Foundation keeps a Date as a Double of seconds since 2001-01-01, and
// PostgresNIO binds one as Int64((date - 2000-01-01) * 1e6), truncating. A
// whole-millisecond Date (an article's publishedAt, a feed cursor) can
// therefore land in Postgres one microsecond early. Articles inserted or
// paged by v2 go through the same arithmetic, so cursors and stored rows from
// either backend compare equal.

const (
	referenceDateUnix = 978307200 // 2001-01-01T00:00:00Z
	psqlStartRef      = -31622400.0
)

var psqlStart = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// swiftSeconds is Date.timeIntervalSince1970 for t.
func swiftSeconds(t time.Time) float64 {
	ref := float64(t.Unix()-referenceDateUnix) + float64(t.Nanosecond())/1e9
	return ref + referenceDateUnix
}

// pgBind is the instant Postgres stores for Date(timeIntervalSince1970: s).
func pgBind(s float64) time.Time {
	ref := s - referenceDateUnix
	micros := int64((ref - psqlStartRef) * 1e6)
	return psqlStart.Add(time.Duration(micros) * time.Microsecond).UTC()
}

// fromSeconds is Date(timeIntervalSince1970: s) as a Go time (nanosecond
// precision is enough: responses format to whole seconds).
func fromSeconds(s float64) time.Time {
	sec := math.Floor(s)
	return time.Unix(int64(sec), int64(math.Round((s-sec)*1e9))).UTC()
}

// wholeMillis is Article.init's publishedAt: now rounded down to the
// millisecond, as seconds.
func wholeMillis(now time.Time) float64 {
	return math.Floor(swiftSeconds(now)*1000) / 1000
}

// cursorMillis is encodeCursor's `Int64((publishedAt * 1000).rounded())`.
func cursorMillis(t time.Time) int64 {
	return int64(math.Round(swiftSeconds(t) * 1000))
}
