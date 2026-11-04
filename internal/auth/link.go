package auth

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"time"
)

// The query parameters of a signed download link: the expiry (unix seconds) and the signature
// "<user id>.<repo id>.<mac>". No format reads either of them.
const (
	LinkExpParam = "texp"
	LinkSigParam = "tsig"
)

// Link is what a signed download link grants: one file path of the repo RepoID until Expires.
type Link struct {
	RepoID  int64
	Expires time.Time
}

// LinkVerifier checks the signed download link of r (its path and its query) and returns the
// CredLink principal of the link (LinkPrincipal). A link that does not verify, or that expired,
// returns ErrBadCredentials. The links module sets it.
type LinkVerifier func(ctx context.Context, r *http.Request) (*Principal, error)

// SetLinkVerifier sets the verifier of signed download links (Read.Link).
func (s *Service) SetLinkVerifier(fn LinkVerifier) { s.links = fn }

// link verifies a signed download link under the failed-attempt limits of the client address.
func (s *Service) link(ctx context.Context, ip netip.Addr, r *http.Request) (*Principal, error) {
	keys := limitKeys(ip, "")[:1]
	if d := s.limits.blocked(keys...); d > 0 {
		return nil, &rateLimitedError{d}
	}
	if s.links == nil {
		return nil, ErrBadCredentials
	}
	p, err := s.links(ctx, r)
	if errors.Is(err, ErrBadCredentials) {
		s.limits.fail(keys...)
	}
	return p, err
}

// LinkPrincipal returns the CredLink principal of a verified link of the user userID to the repo
// repoID: the user's current name, kind and server permissions, so the access engine decides
// with the user's grants now. A user who is gone or disabled returns ErrBadCredentials.
func (s *Service) LinkPrincipal(ctx context.Context, userID, repoID int64, expires time.Time) (*Principal, error) {
	u, err := s.principalByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	u.Cred = CredLink
	u.Link = &Link{RepoID: repoID, Expires: expires}
	return u, nil
}
