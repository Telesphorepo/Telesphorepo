package auth

import (
	"context"
	"database/sql"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
)

// Change is what one committed change of the auth rows touched: the users whose disabled state,
// server permissions or repository grants changed (or who are new), and the public IDs of the read
// tokens that were created or deleted. The edge module writes its access objects from it.
type Change struct {
	Users  []int64
	Tokens []string
}

// AfterChange registers fn to run after each committed change of users, server permissions,
// repository grants and read tokens. fn runs on the request that made the change and must be
// quick. It runs before the server serves.
func (s *Service) AfterChange(fn func(ctx context.Context, c Change)) {
	s.notices = append(s.notices, fn)
}

// changed calls the AfterChange functions with c, unless c is empty.
func (s *Service) changed(ctx context.Context, c Change) {
	if len(c.Users) == 0 && len(c.Tokens) == 0 {
		return
	}
	for _, fn := range s.notices {
		fn(ctx, c)
	}
}

// TouchTokens sets last_used_at of the read tokens of publicIDs to at, unless a later use is
// stored already. The edge module reports with it the read tokens that the edge Worker served.
func (s *Service) TouchTokens(ctx context.Context, publicIDs []string, at time.Time) error {
	for _, id := range publicIDs {
		if err := s.wq.TouchTokenByPublicID(ctx, dbq.TouchTokenByPublicIDParams{
			LastUsedAt: sql.NullInt64{Int64: at.Unix(), Valid: true}, PublicID: id}); err != nil {
			return err
		}
	}
	return nil
}
