package joblock

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// NewSessionWithDial is NewSession with a test dialer.
func NewSessionWithDial(dial func(ctx context.Context) (*pgx.Conn, error)) *Session {
	return newSession(dial)
}

// BreakConn closes the session's connection as a network failure would,
// leaving it in place for the next lock to notice.
func (s *Session) BreakConn(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		_ = s.conn.Close(ctx)
	}
}
