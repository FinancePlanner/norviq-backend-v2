package groups

import (
	"context"

	"norviq-api/internal/server"
	"norviq-api/internal/store/groupsdb"

	"github.com/google/uuid"
)

// Membership is one group a user belongs to. It deliberately has no field that
// could carry a message.
type Membership struct {
	Name        string `json:"name"`
	Role        string `json:"role"`
	MemberCount int64  `json:"memberCount"`
}

// Reader reads group memberships for other features.
type Reader struct{ d *server.Deps }

// NewReader builds a Reader.
func NewReader(d *server.Deps) Reader { return Reader{d: d} }

// Memberships lists the groups the user belongs to.
func (r Reader) Memberships(ctx context.Context, userID uuid.UUID) ([]Membership, error) {
	q := groupsdb.New(r.d.DB)
	members, err := q.MembersOfUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(members))
	roles := make(map[uuid.UUID]string, len(members))
	for i := range members {
		ids[i] = members[i].GroupID
		roles[members[i].GroupID] = members[i].Role
	}
	found, err := q.GroupsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Membership, 0, len(found))
	for i := range found {
		out = append(out, Membership{Name: found[i].Name, Role: roles[found[i].ID], MemberCount: found[i].MemberCount})
	}
	return out, nil
}

// Enabled is whether groups are on.
func Enabled() bool { return configFromEnv().Enabled }
