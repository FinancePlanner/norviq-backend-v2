package joblock

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// NewSessionWithDial is NewSession with a test dialer.
func NewSessionWithDial(dial func(ctx context.Context) (*pgx.Conn, error)) *Session {
	return newSession(dial)
}

// BackendPID is the session connection's Postgres backend, for a test that
// kills it from the server side.
func (s *Session) BackendPID() uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn.PgConn().PID()
}

// BreakConn closes the session's connection from the client side, leaving it
// in place for the next lock to find.
func (s *Session) BreakConn(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		_ = s.conn.Close(ctx)
	}
}
