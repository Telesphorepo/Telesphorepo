package auth

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ProofHeader carries a fresh second-factor proof on the routes that need one (route policy
// FreshFactor): "totp <6 digits>", "passkey <base64url of the assertion JSON>" (answering
// POST /api/security/passkeys/assert) or "grant <grant>" (an SSO account: the grant of a fresh
// sign-in at the identity provider).
const ProofHeader = "X-Telesphorepo-Proof"

const (
	// chGrant is the auth_challenges kind of an SSO grant; data is what the grant is for.
	chGrant  = "device_grant"
	grantTTL = 5 * time.Minute
)

var (
	// ErrProofMissing: the request has no proof, or one of a kind the account cannot give.
	ErrProofMissing = fmt.Errorf("%w: send the header %s with \"totp <code>\" or \"passkey <assertion>\"", ErrInvalid, ProofHeader)
	// ErrGrantRequired: an SSO account confirms with the grant of a fresh SSO sign-in.
	ErrGrantRequired = fmt.Errorf("%w: sign in again with single sign-on to confirm this", ErrBadCredentials)
	// ErrGrantUsed: the grant is unknown, used, expired or of another person.
	ErrGrantUsed = fmt.Errorf("%w: the single sign-on approval is used, expired or for another code; sign in again", ErrBadCredentials)
)

// Proof is a checked fresh second-factor proof. Data is what a grant was issued for (e.g. the
// user code of a device enrollment); "" for a TOTP code or a passkey.
type Proof struct {
	Kind string // "totp", "passkey", "grant" or "none" (dev mode, a person without a factor)
	Data string
}

// CheckProof checks the fresh second-factor proof header of the UI session principal p. A local
// account proves with a TOTP code or a passkey assertion; an SSO account with a grant, which is
// used up. In dev mode a local account without a factor passes without a proof. Failures count
// against the username like failed passwords. Errors: ErrNoSecondFactor, ErrProofMissing,
// ErrBadSecondFactor, ErrGrantRequired, ErrGrantUsed, a rate-limit error or an internal error.
func (s *Service) CheckProof(ctx context.Context, p *Principal, header string) (*Proof, error) {
	kind, value, _ := strings.Cut(strings.TrimSpace(header), " ")
	value = strings.TrimSpace(value)
	if p.Kind == KindOIDC {
		if kind != "grant" || value == "" {
			return nil, ErrGrantRequired
		}
		row, err := s.takeChallenge(ctx, chGrant, ceremonyID(value))
		if errors.Is(err, sql.ErrNoRows) || (err == nil && row.UserID.Int64 != p.UserID) {
			return nil, ErrGrantUsed
		}
		if err != nil {
			return nil, err
		}
		return &Proof{Kind: "grant", Data: string(row.Data)}, nil
	}
	has, err := s.rq.HasSecondFactor(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	if has == 0 {
		if s.dev {
			return &Proof{Kind: "none"}, nil // dev mode: the factor is optional; one that exists is still checked
		}
		return nil, ErrNoSecondFactor
	}
	var in SecondFactorInput
	switch kind {
	case "totp":
		in.TOTPCode = value
	case "passkey":
		b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(value, "="))
		if err != nil || len(b) == 0 {
			return nil, ErrProofMissing
		}
		in.Passkey = b
	default:
		return nil, ErrProofMissing
	}
	if value == "" {
		return nil, ErrProofMissing
	}
	keys := []string{userLimitKey(p.Username)}
	if d := s.limits.begin(keys...); d > 0 {
		return nil, &rateLimitedError{d}
	}
	ok, err := s.checkFactor(ctx, p.UserID, in)
	s.limits.end(err == nil && !ok, keys...)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrBadSecondFactor
	}
	return &Proof{Kind: kind}, nil
}

// IssueGrant stores a single-use grant (5 minutes) that lets the SSO account userID confirm one
// action for data, e.g. the approval of the device enrollment with that user code. The SSO
// callback calls it after a fresh sign-in at the identity provider.
func (s *Service) IssueGrant(ctx context.Context, userID int64, data string) (string, error) {
	grant, _, err := newSecret()
	if err != nil {
		return "", err
	}
	if err := s.putChallenge(ctx, chGrant, ceremonyID(grant), userID, []byte(data), grantTTL); err != nil {
		return "", err
	}
	return grant, nil
}
