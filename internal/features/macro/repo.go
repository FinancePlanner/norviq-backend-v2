package macro

import (
	"context"
	"errors"
	"sort"
	"time"

	"norviq-api/internal/store/macrodb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Point is MacroSeriesPointRecord (one vintage of one period).
type Point struct {
	Country     string
	SeriesKey   string
	PeriodDate  string
	Value       float64
	Unit        string
	Source      string
	VintageDate time.Time
}

// SnapshotRecord is MacroSnapshotRecord.
type SnapshotRecord struct {
	Country   string
	AsOf      string
	Source    string
	Payload   string
	FetchedAt time.Time
}

// Repo is DatabaseMacroRepository.
type Repo struct{ Q *macrodb.Queries }

// InsertSnapshotIfNew appends a snapshot unless one exists for (country,
// asOf, source), and reports whether it inserted.
func (r Repo) InsertSnapshotIfNew(ctx context.Context, s SnapshotRecord) (bool, error) {
	exists, err := r.Q.MacroSnapshotExists(ctx, macrodb.MacroSnapshotExistsParams{Country: s.Country, AsOf: s.AsOf, Source: s.Source})
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	err = r.Q.InsertMacroSnapshot(ctx, macrodb.InsertMacroSnapshotParams{
		ID: uuid.New(), Country: s.Country, AsOf: s.AsOf, Source: s.Source,
		Payload: s.Payload, FetchedAt: s.FetchedAt, CreatedAt: time.Now(),
	})
	return err == nil, err
}

// LatestSnapshot is the most recently fetched snapshot for country.
func (r Repo) LatestSnapshot(ctx context.Context, country string) (*SnapshotRecord, error) {
	row, err := r.Q.LatestMacroSnapshot(ctx, country)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &SnapshotRecord{Country: row.Country, AsOf: row.AsOf, Source: row.Source, Payload: row.Payload, FetchedAt: row.FetchedAt}, nil
}

// latestByPeriod keeps, per period, the row with the greatest vintage; the
// first row seen wins a tie, as Swift's strict `>` did.
func latestByPeriod(rows []macrodb.MacroSeriesPoint) map[string]macrodb.MacroSeriesPoint {
	out := map[string]macrodb.MacroSeriesPoint{}
	for i := range rows {
		row := &rows[i]
		if cur, ok := out[row.PeriodDate]; ok {
			if row.VintageDate.After(cur.VintageDate) {
				out[row.PeriodDate] = *row
			}
		} else {
			out[row.PeriodDate] = *row
		}
	}
	return out
}

// InsertPointsIfChanged is the vintage-safe insert: a point is written only
// when its period has no prior vintage (for that source) or the latest one
// holds a different value. Rows are never mutated, and there is no
// transaction (Fluent saved point by point). Returns the inserted count.
func (r Repo) InsertPointsIfChanged(ctx context.Context, points []Point) (int, error) {
	if len(points) == 0 {
		return 0, nil
	}
	type groupKey struct{ country, key, source string }
	groups := map[groupKey][]Point{}
	order := []groupKey{}
	for _, p := range points {
		k := groupKey{p.Country, p.SeriesKey, p.Source}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], p)
	}
	inserted := 0
	for _, k := range order {
		group := groups[k]
		periods := make([]string, len(group))
		for i, p := range group {
			periods[i] = p.PeriodDate
		}
		existing, err := r.Q.MacroPointsForPeriods(ctx, macrodb.MacroPointsForPeriodsParams{
			Country: k.country, SeriesKey: k.key, Source: k.source, Periods: periods,
		})
		if err != nil {
			return inserted, err
		}
		latest := latestByPeriod(existing)
		for _, p := range group {
			if cur, ok := latest[p.PeriodDate]; ok && cur.Value == p.Value {
				continue
			}
			if err := r.Q.InsertMacroPoint(ctx, macrodb.InsertMacroPointParams{
				ID: uuid.New(), Country: p.Country, SeriesKey: p.SeriesKey, PeriodDate: p.PeriodDate,
				Value: p.Value, Unit: p.Unit, Source: p.Source, VintageDate: p.VintageDate, CreatedAt: time.Now(),
			}); err != nil {
				return inserted, err
			}
			inserted++
		}
	}
	return inserted, nil
}

// Series is the latest vintage per period, ascending by period, capped to
// the most recent limit periods. Empty from/to are ignored.
func (r Repo) Series(ctx context.Context, country, seriesKey string, from, to *string, limit int) ([]Point, error) {
	params := macrodb.MacroSeriesRowsParams{Country: country, SeriesKey: seriesKey}
	if from != nil && *from != "" {
		params.FromPeriod = from
	}
	if to != nil && *to != "" {
		params.ToPeriod = to
	}
	rows, err := r.Q.MacroSeriesRows(ctx, params)
	if err != nil {
		return nil, err
	}
	latest := latestByPeriod(rows)
	ordered := make([]Point, 0, len(latest))
	for k := range latest {
		ordered = append(ordered, pointFromRow(latest[k]))
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].PeriodDate < ordered[j].PeriodDate })
	if len(ordered) > limit {
		ordered = ordered[len(ordered)-max(limit, 0):]
	}
	return ordered, nil
}

// LatestPoint is the newest period's newest vintage.
func (r Repo) LatestPoint(ctx context.Context, country, seriesKey string) (*Point, error) {
	row, err := r.Q.LatestMacroPoint(ctx, macrodb.LatestMacroPointParams{Country: country, SeriesKey: seriesKey})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p := pointFromRow(row)
	return &p, nil
}

// Freshness is the most recent snapshot fetch time per country, for the
// readiness check's degraded `macro` entry.
func (r Repo) Freshness(ctx context.Context) (map[string]time.Time, error) {
	rows, err := r.Q.MacroFreshness(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]time.Time{}
	for _, row := range rows {
		if _, ok := out[row.Country]; !ok {
			out[row.Country] = row.FetchedAt
		}
	}
	return out, nil
}

func pointFromRow(row macrodb.MacroSeriesPoint) Point {
	return Point{
		Country: row.Country, SeriesKey: row.SeriesKey, PeriodDate: row.PeriodDate, Value: row.Value,
		Unit: row.Unit, Source: row.Source, VintageDate: row.VintageDate,
	}
}
