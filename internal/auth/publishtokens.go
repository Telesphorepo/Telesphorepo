package auth

import (
	"context"
	"errors"
)

// publishTokenPrefix starts a publish-session token: "trs_<12 hex>_<secret>" (spec §7.6).
const publishTokenPrefix = "trs_"

// PublishTokens verifies a publish-session token ("trs_<12hex>_<secret>"). Set by the server.
// Verify returns an error that wraps ErrBadCredentials when the token is malformed, unknown,
// closed or expired; any other error is internal.
type PublishTokens interface {
	Verify(ctx context.Context, token string) (*PublishSession, error)
}

// PublishSession is an open publish session for one repo.
type PublishSession struct {
	ID, RepoID int64
	UserID     int64 // 0 for OIDC sessions
	Via        string
	Subject    string // the username (device, manual) or the CI token's sub (oidc)
	// Provenance is true when the session recorded build provenance (internal/provenance): the
	// files it writes point at it.
	Provenance bool
}

// SetPublishTokens sets the verifier of publish-session tokens. Without it a "trs_" password
// never authenticates.
func (s *Service) SetPublishTokens(p PublishTokens) { s.publish = p }

// verifyPublishToken authenticates a publish-session token. A session with a user carries that
// user's current name, kind and admin flag (the write gate checks the user's permission now); a
// session from a CI identity token has no user. On the JSON API such a principal reaches only
// /api/publish/ (see Identify).
func (s *Service) verifyPublishToken(ctx context.Context, tok string) (*Principal, error) {
	if s.publish == nil {
		return nil, ErrBadCredentials
	}
	ps, err := s.publish.Verify(ctx, tok)
	if err != nil {
		return nil, err
	}
	if ps == nil {
		return nil, ErrBadCredentials
	}
	p := &Principal{UserID: ps.UserID, Publish: ps}
	if ps.UserID != 0 {
		u, err := s.principalByID(ctx, ps.UserID)
		if err != nil {
			if errors.Is(err, ErrBadCredentials) {
				return nil, ErrBadCredentials
			}
			return nil, err
		}
		p.Username, p.Kind, p.Perms = u.Username, u.Kind, u.Perms
	}
	return p, nil
}
