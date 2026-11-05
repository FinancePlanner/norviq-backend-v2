package research

import (
	"context"

	"norviq-api/internal/server"
	"norviq-api/internal/store/researchdb"

	"github.com/google/uuid"
)

// Reader reads research notes for other features.
type Reader struct{ d *server.Deps }

// NewReader builds a Reader.
func NewReader(d *server.Deps) Reader { return Reader{d: d} }

// Recent lists the user's latest notes.
func (r Reader) Recent(ctx context.Context, userID uuid.UUID, limit int) ([]NoteResponse, error) {
	notes, err := researchdb.New(r.d.DB).ListResearchNotes(ctx, researchdb.ListResearchNotesParams{UserID: userID, Limit: int32(limit)}) // #nosec G115 -- callers pass a small constant
	if err != nil {
		return nil, err
	}
	out := make([]NoteResponse, 0, len(notes))
	for i := range notes {
		out = append(out, toResponse(notes[i]))
	}
	return out, nil
}
