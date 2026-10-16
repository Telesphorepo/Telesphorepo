package auth

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
)

const (
	// chSetupCode is the auth_challenges kind of a setup code; data is the repo id.
	chSetupCode = "setup_code"
	// SetupCodeTTL is the life of a setup code.
	SetupCodeTTL = 10 * time.Minute
	// SetupCodePrefix starts every setup code: "trsu_" and 32 random bytes (base64url).
	SetupCodePrefix = "trsu_"
)

// SetupCode is a setup code that a person made for one repo. A code that expired is still
// returned once, so the answer can name its repo.
type SetupCode struct {
	UserID    int64
	RepoID    int64
	ExpiresAt time.Time
}

// Expired reports whether the code expired before now.
func (c *SetupCode) Expired(now time.Time) bool { return !now.Before(c.ExpiresAt) }

// IssueSetupCode stores a single-use setup code of userID for repoID (SetupCodeTTL) and returns
// it with its expiry. The server keeps only its SHA-256.
func (s *Service) IssueSetupCode(ctx context.Context, userID, repoID int64) (string, time.Time, error) {
	v, _, err := newSecret()
	if err != nil {
		return "", time.Time{}, err
	}
	code := SetupCodePrefix + v
	if err := s.putChallenge(ctx, chSetupCode, ceremonyID(code), userID, []byte(strconv.FormatInt(repoID, 10)), SetupCodeTTL); err != nil {
		return "", time.Time{}, err
	}
	return code, time.Now().Add(SetupCodeTTL).Truncate(time.Second), nil
}

// TakeSetupCode takes the setup code: it deletes the code and returns it, also when it expired.
// ErrNotFound: the code is unknown or used.
func (s *Service) TakeSetupCode(ctx context.Context, code string) (*SetupCode, error) {
	row, err := s.wq.TakeChallengeAnyAge(ctx, dbq.TakeChallengeAnyAgeParams{IDHash: ceremonyID(code), Kind: chSetupCode})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	repoID, err := strconv.ParseInt(string(row.Data), 10, 64)
	if err != nil || !row.UserID.Valid {
		return nil, ErrNotFound
	}
	return &SetupCode{UserID: row.UserID.Int64, RepoID: repoID, ExpiresAt: time.Unix(row.ExpiresAt, 0)}, nil
}

// RestoreSetupCode puts a setup code that TakeSetupCode took back, with its expiry, after a
// setup that failed for another reason than the code.
func (s *Service) RestoreSetupCode(ctx context.Context, code string, c *SetupCode) error {
	now := time.Now()
	return s.wq.CreateChallenge(context.WithoutCancel(ctx), dbq.CreateChallengeParams{
		IDHash: ceremonyID(code), Kind: chSetupCode, UserID: sql.NullInt64{Int64: c.UserID, Valid: true},
		Data: []byte(strconv.FormatInt(c.RepoID, 10)), CreatedAt: now.Unix(), ExpiresAt: c.ExpiresAt.Unix()})
}

// UserPrincipal returns the principal of the enabled user id with the current name, kind and
// server permissions, and no credential kind. ErrBadCredentials: the user is gone or disabled.
func (s *Service) UserPrincipal(ctx context.Context, id int64) (*Principal, error) {
	return s.principalByID(ctx, id)
}
