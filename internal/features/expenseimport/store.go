package expenseimport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/crypto"
	"norviq-api/internal/store/expenseimportdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ExpenseImportSessionStore: in-progress imports, encrypted, handed back
// only to their owner.

const (
	sessionLifetime        = time.Hour
	maxOpenSessionsPerUser = 3
)

// StoredSheet is ExpenseImportSessionStore.StoredSheet. The JSON is what
// Swift's JSONEncoder wrote, so either backend can read the other's rows.
type StoredSheet struct {
	Name         string         `json:"name"`
	Index        int            `json:"index"`
	RowCount     int            `json:"rowCount"`
	HeaderRow    *int           `json:"headerRow,omitempty"`
	DataStartRow int            `json:"dataStartRow"`
	DataEndRow   int            `json:"dataEndRow"`
	ExcludedRows []int          `json:"excludedRows"`
	Columns      []StoredColumn `json:"columns"`
	Rows         []StoredRow    `json:"rows"`
	Notes        []string       `json:"notes"`
	Score        float64        `json:"score"`
}

// StoredRow is StoredSheet.Row: 1-based worksheet row, letter -> text.
type StoredRow struct {
	Row   int               `json:"row"`
	Cells map[string]string `json:"cells"`
}

// StoredColumn is ExpenseImportSessionStore.StoredColumn.
type StoredColumn struct {
	Letter        string   `json:"letter"`
	Header        *string  `json:"header,omitempty"`
	Kind          string   `json:"kind"`
	Samples       []string `json:"samples"`
	DistinctCount int      `json:"distinctCount"`
	Role          string   `json:"role"`
	Confidence    float64  `json:"confidence"`
}

// StoredMapping is ExpenseImportSessionStore.StoredMapping.
type StoredMapping struct {
	SelectedSheet    *string            `json:"selectedSheet,omitempty"`
	ColumnRoles      map[string]string  `json:"columnRoles"`
	Categories       []StoredCategory   `json:"categories"`
	AmountSign       string             `json:"amountSign"`
	DateOrder        string             `json:"dateOrder"`
	DecimalSeparator string             `json:"decimalSeparator"`
	Currency         *string            `json:"currency,omitempty"`
	BaseCurrency     *string            `json:"baseCurrency,omitempty"`
	ExchangeRates    map[string]float64 `json:"exchangeRates"`
	SkippedRows      []int              `json:"skippedRows"`
	Notes            []string           `json:"notes"`
	Warnings         []string           `json:"warnings"`
}

// StoredCategory is StoredMapping.Category.
type StoredCategory struct {
	SourceValue    string  `json:"sourceValue"`
	Pillar         *string `json:"pillar,omitempty"`
	CategoryID     *string `json:"categoryId,omitempty"`
	CategoryName   *string `json:"categoryName,omitempty"`
	CreateCategory bool    `json:"createCategory"`
	Confidence     float64 `json:"confidence"`
	Source         string  `json:"source"`
}

// Session is an ExpenseImportSession row.
type Session = expenseimportdb.ExpenseImportSession

// SessionStore is ExpenseImportSessionStore.
type SessionStore struct {
	Queries *expenseimportdb.Queries
	PII     *crypto.PII
	Now     func() time.Time
}

func (s *SessionStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func expiredReason() error {
	return httpx.NotFound("That import has expired. Upload the file again.")
}

// Create stores a new ready session, pruning the user's oldest open ones.
func (s *SessionStore) Create(ctx context.Context, q *expenseimportdb.Queries, userID uuid.UUID, fileName string, sheets []StoredSheet, mapping StoredMapping, aiAvailable bool, aiModel *string, aiConfidence *float64) (Session, error) {
	if err := s.pruneExcess(ctx, q, userID); err != nil {
		return Session{}, err
	}
	sheetsBlob, err := s.encode(sheets)
	if err != nil {
		return Session{}, err
	}
	mappingBlob, err := s.encode(mapping)
	if err != nil {
		return Session{}, err
	}
	rows := 0
	for _, sh := range sheets {
		rows += len(sh.Rows)
	}
	now := s.now().UTC().Truncate(time.Microsecond)
	p := expenseimportdb.InsertSessionParams{
		ID:               uuid.New(),
		UserID:           userID,
		FileName:         prefixRunes(fileName, 255),
		SheetCount:       int64(len(sheets)),
		RowCount:         int64(rows),
		AiAvailable:      aiAvailable,
		AiModel:          aiModel,
		AiConfidence:     aiConfidence,
		SheetsEncrypted:  sheetsBlob,
		MappingEncrypted: mappingBlob,
		ExpiresAt:        now.Add(sessionLifetime),
		Now:              &now,
	}
	if err := q.InsertSession(ctx, p); err != nil {
		return Session{}, err
	}
	return Session{
		ID: p.ID, UserID: userID, Status: "ready", FileName: p.FileName, SheetCount: p.SheetCount, RowCount: p.RowCount,
		AiAvailable: aiAvailable, AiModel: aiModel, AiConfidence: aiConfidence, SheetsEncrypted: sheetsBlob,
		MappingEncrypted: mappingBlob, ExpiresAt: p.ExpiresAt, CreatedAt: &now, UpdatedAt: &now,
	}, nil
}

// Load returns a ready, unexpired session the user owns: 404 for missing,
// foreign, expired and discarded alike, 409 once committed.
func (s *SessionStore) Load(ctx context.Context, id, userID uuid.UUID) (Session, error) {
	row, err := s.Queries.GetUserSession(ctx, expenseimportdb.GetUserSessionParams{ID: id, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, expiredReason()
	}
	if err != nil {
		return Session{}, err
	}
	if row.Status == "committed" {
		return Session{}, httpx.Conflict("This import was already applied.")
	}
	if row.Status != "ready" || !row.ExpiresAt.After(s.now()) {
		return Session{}, expiredReason()
	}
	return row, nil
}

// Sheets decrypts a session's sheets.
func (s *SessionStore) Sheets(session Session) ([]StoredSheet, error) {
	var out []StoredSheet
	if err := s.decode(session.SheetsEncrypted, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Mapping decrypts a session's mapping.
func (s *SessionStore) Mapping(session Session) (StoredMapping, error) {
	var out StoredMapping
	if err := s.decode(session.MappingEncrypted, &out); err != nil {
		return StoredMapping{}, err
	}
	return out, nil
}

// UpdateMapping re-encrypts and saves the mapping.
func (s *SessionStore) UpdateMapping(ctx context.Context, q *expenseimportdb.Queries, mapping StoredMapping, session Session) error {
	blob, err := s.encode(mapping)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	return q.UpdateSessionMapping(ctx, expenseimportdb.UpdateSessionMappingParams{MappingEncrypted: blob, Now: &now, ID: session.ID})
}

// MarkCommitted flips the session to committed.
func (s *SessionStore) MarkCommitted(ctx context.Context, q *expenseimportdb.Queries, session Session) error {
	now := s.now().UTC()
	return q.MarkSessionCommitted(ctx, expenseimportdb.MarkSessionCommittedParams{Now: &now, ID: session.ID})
}

// Discard deletes the user's session (no error when there is none).
func (s *SessionStore) Discard(ctx context.Context, id, userID uuid.UUID) error {
	return s.Queries.DiscardUserSession(ctx, expenseimportdb.DiscardUserSessionParams{ID: id, UserID: userID})
}

func (s *SessionStore) pruneExcess(ctx context.Context, q *expenseimportdb.Queries, userID uuid.UUID) error {
	open, err := q.ListOpenSessions(ctx, userID)
	if err != nil {
		return err
	}
	if len(open) < maxOpenSessionsPerUser {
		return nil
	}
	for _, id := range open[maxOpenSessionsPerUser-1:] {
		if err := q.DeleteSession(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *SessionStore) encode(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, httpx.Abort(http.StatusInternalServerError, "Could not store the import.")
	}
	return s.PII.EncryptString(string(b))
}

func (s *SessionStore) decode(payload []byte, v any) error {
	text, err := s.PII.DecryptString(payload)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(text), v)
}
