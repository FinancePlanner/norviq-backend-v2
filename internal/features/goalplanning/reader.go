package goalplanning

import (
	"context"

	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"

	"github.com/google/uuid"
)

// Reader reads goals for other features.
type Reader struct{ h *handler }

// Reader builds a Reader with Register's defaults.
func (f Feature) Reader(d *server.Deps) (Reader, error) {
	svc, err := f.service(d)
	if err != nil {
		return Reader{}, err
	}
	ent := f.Entitlements
	if ent == nil {
		ent = entitlements.NewFromEnv(d.DB)
	}
	return Reader{h: &handler{d: d, svc: svc, ent: ent}}, nil
}

// Overview is GET /v1/financial-goals/overview for userID.
func (r Reader) Overview(ctx context.Context, userID uuid.UUID) (Overview, error) {
	return r.h.overviewFor(ctx, userID)
}
