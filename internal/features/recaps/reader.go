package recaps

import (
	"context"
	"time"

	"norviq-api/internal/server"
	"norviq-api/internal/store/recapsdb"

	"github.com/google/uuid"
)

// Reader reads recaps for other features.
type Reader struct{ d *server.Deps }

// NewReader builds a Reader.
func NewReader(d *server.Deps) Reader { return Reader{d: d} }

// List is GET /v1/recaps for userID; empty while RECAP_ENABLED is off.
func (r Reader) List(ctx context.Context, userID uuid.UUID, now time.Time) (ListResponse, error) {
	svc := &Service{Q: recapsdb.New(r.d.DB), Config: ConfigFromEnv()}
	return svc.List(ctx, userID, now)
}
