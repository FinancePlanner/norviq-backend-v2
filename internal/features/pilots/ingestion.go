package pilots

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"norviq-api/internal/store/pilotsdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Ingestion is PilotIngestionService: pulls a pilot's disclosures, stores
// the new ones, and writes a new book version only when something was new.
// Re-running on unchanged data is a no-op (unique pilot_id, source_key).
type Ingestion struct {
	DB          *pgxpool.Pool
	Politicians DisclosureSource
	Funds       DisclosureSource // nil: funds are not ingested
}

// Ingest returns the new version, or 0 when unchanged.
func (s *Ingestion) Ingest(ctx context.Context, pilot pilotsdb.Pilot, now time.Time) (int, error) {
	var source DisclosureSource
	if pilotKind(pilot.Kind) == KindPolitician {
		source = s.Politicians
	} else {
		source = s.Funds
	}
	if source == nil {
		return 0, nil
	}
	// Network fetch happens outside any transaction.
	rows, err := source.Disclosures(ctx, identityOf(pilot))
	if err != nil {
		return 0, err
	}

	// Inserts, version rebuild and lastIngestedAt commit together.
	var outcome int
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		q := pilotsdb.New(tx)
		if err := q.LockPilot(ctx, pilot.ID); err != nil {
			return err
		}
		inserted := 0
		for _, row := range rows {
			ids, err := q.InsertDisclosure(ctx, pilotsdb.InsertDisclosureParams{
				PilotID: pilot.ID, SourceKey: row.SourceKey, Symbol: row.Symbol, Side: row.Side, Instrument: row.Instrument,
				TransactionDate: row.TransactionDate, DisclosureDate: row.DisclosureDate,
				AmountMin: row.AmountMin, AmountMax: row.AmountMax, Shares: row.Shares, MarketValue: row.MarketValue,
				Period: row.Period, DiscoveredAt: now,
			})
			if err != nil {
				return err
			}
			inserted += len(ids)
		}

		latest, err := q.LatestBookVersion(ctx, pilot.ID)
		hasLatest := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if inserted > 0 || !hasLatest {
			// Discovery order (then id) is the replay order for same-day rows.
			records, err := q.DisclosuresInDiscoveryOrder(ctx, pilot.ID)
			if err != nil {
				return err
			}
			entries := make([]BookEntry, 0, len(records))
			for i := range records {
				r := &records[i]
				if !validSide(r.Side) || !validInstrument(r.Instrument) {
					continue
				}
				entries = append(entries, BookEntry{
					Symbol: r.Symbol, Side: r.Side, Instrument: r.Instrument, TransactionDate: r.TransactionDate,
					AmountMin: r.AmountMin, AmountMax: r.AmountMax, MarketValue: r.MarketValue, Period: r.Period,
				})
			}
			var book Book
			if pilotKind(pilot.Kind) == KindPolitician {
				book = PoliticianBook(entries, now)
			} else {
				book = FundBook(entries)
			}
			if len(book.Weights) > 0 {
				next := 1
				if hasLatest {
					next = int(latest.Version) + 1
				}
				weights, err := json.Marshal(book.Weights)
				if err != nil {
					return err
				}
				if err := q.InsertBookVersion(ctx, pilotsdb.InsertBookVersionParams{
					ID: uuid.New(), PilotID: pilot.ID, Version: int32(next), ComputedAt: now,
					Weights: weights, SkippedPuts: int32(book.SkippedPuts), //nolint:gosec // int4 column, as Vapor
				}); err != nil {
					return err
				}
				outcome = next
			}
		}
		return q.StampPilotIngested(ctx, pilotsdb.StampPilotIngestedParams{ID: pilot.ID, LastIngestedAt: &now, UpdatedAt: ptrTime(time.Now())})
	})
	if err != nil {
		return 0, err
	}
	return outcome, nil
}

func ptrTime(t time.Time) *time.Time { return &t }

// decodeWeights reads a pilot_book_versions.weights jsonb.
func decodeWeights(raw []byte) map[string]float64 {
	w := map[string]float64{}
	_ = json.Unmarshal(raw, &w)
	return w
}
