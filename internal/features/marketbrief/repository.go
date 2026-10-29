package marketbrief

import (
	"context"
	"errors"
	"time"

	"norviq-api/internal/store/marketbriefdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository is MarketBriefRepository.
type Repository interface {
	Exists(ctx context.Context, tradingDate, slot string) (bool, error)
	// Save writes all languages of one slot in one transaction: a reader
	// never sees en without pt-PT.
	Save(ctx context.Context, briefs []Response, model string, generatedAt time.Time) error
	Delete(ctx context.Context, tradingDate, slot string) error
	Latest(ctx context.Context, language string) (*Response, error)
	Find(ctx context.Context, tradingDate, slot, language string) (*Response, error)
	// Previous is the brief stored just before (tradingDate, slot) in this
	// language: the same day's morning behind an evening, otherwise the
	// latest earlier day.
	Previous(ctx context.Context, tradingDate, slot, language string) (*Ref, error)
}

// DBRepository is DatabaseMarketBriefRepository.
type DBRepository struct{ DB *pgxpool.Pool }

func (r DBRepository) q() *marketbriefdb.Queries { return marketbriefdb.New(r.DB) }

// Exists implements Repository.
func (r DBRepository) Exists(ctx context.Context, tradingDate, slot string) (bool, error) {
	n, err := r.q().CountMarketBriefSlot(ctx, marketbriefdb.CountMarketBriefSlotParams{TradingDate: tradingDate, Slot: slot})
	return n > 0, err
}

// Save implements Repository.
func (r DBRepository) Save(ctx context.Context, briefs []Response, model string, generatedAt time.Time) error {
	rows := make([]marketbriefdb.InsertMarketBriefParams, 0, len(briefs))
	for _, b := range briefs {
		if b.TradingDate == nil || b.Slot == nil {
			return errIncompleteBrief
		}
		payload, err := encodePayload(b)
		if err != nil {
			return err
		}
		rows = append(rows, marketbriefdb.InsertMarketBriefParams{
			ID: uuid.New(), TradingDate: *b.TradingDate, Slot: *b.Slot, Language: b.Language,
			Payload: payload, Model: model, Degraded: b.Degraded, GeneratedAt: generatedAt,
		})
	}
	return pgx.BeginFunc(ctx, r.DB, func(tx pgx.Tx) error {
		q := marketbriefdb.New(tx)
		for i := range rows {
			rows[i].CreatedAt = time.Now()
			if err := q.InsertMarketBrief(ctx, rows[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

// Delete implements Repository.
func (r DBRepository) Delete(ctx context.Context, tradingDate, slot string) error {
	return r.q().DeleteMarketBriefSlot(ctx, marketbriefdb.DeleteMarketBriefSlotParams{TradingDate: tradingDate, Slot: slot})
}

// Latest implements Repository: by trading day, not by write time, so a
// --replace run never makes an old brief the current card. Same day:
// "evening" sorts before "morning", and evening is the later brief.
func (r DBRepository) Latest(ctx context.Context, language string) (*Response, error) {
	row, err := r.q().LatestMarketBrief(ctx, language)
	return decodeRow(row, err)
}

// Find implements Repository.
func (r DBRepository) Find(ctx context.Context, tradingDate, slot, language string) (*Response, error) {
	row, err := r.q().FindMarketBrief(ctx, marketbriefdb.FindMarketBriefParams{TradingDate: tradingDate, Slot: slot, Language: language})
	return decodeRow(row, err)
}

func decodeRow(row marketbriefdb.MarketBrief, err error) (*Response, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	resp, err := decodePayload(row.Payload)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// Previous implements Repository.
func (r DBRepository) Previous(ctx context.Context, tradingDate, slot, language string) (*Ref, error) {
	q := r.q()
	var found *Ref
	if slot == SlotEvening {
		row, err := q.MarketBriefOnDay(ctx, marketbriefdb.MarketBriefOnDayParams{Language: language, TradingDate: tradingDate, Slot: SlotMorning})
		switch {
		case err == nil:
			found = &Ref{Slot: row.Slot, TradingDate: row.TradingDate}
		case !errors.Is(err, pgx.ErrNoRows):
			return nil, err
		}
	}
	if found == nil {
		row, err := q.MarketBriefBeforeDay(ctx, marketbriefdb.MarketBriefBeforeDayParams{Language: language, TradingDate: tradingDate})
		switch {
		case err == nil:
			found = &Ref{Slot: row.Slot, TradingDate: row.TradingDate}
		case !errors.Is(err, pgx.ErrNoRows):
			return nil, err
		}
	}
	if found == nil || !validSlot(found.Slot) {
		return nil, nil
	}
	return found, nil
}

// isConstraintFailure is DatabaseError.isConstraintFailure: any integrity
// constraint violation (SQLSTATE class 23).
func isConstraintFailure(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && len(pgErr.Code) == 5 && pgErr.Code[:2] == "23"
}
