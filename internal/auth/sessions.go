package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
)

const sessionCookie = "tr_session"

func hashSession(v string) ([]byte, bool) {
	b, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil || len(b) != 32 {
		return nil, false
	}
	sum := sha256.Sum256([]byte(v))
	return sum[:], true
}

// LoginResult is the outcome of the password step of a UI sign-in. Exactly one of User and
// SecondFactor is set.
type LoginResult struct {
	// User is set when a session started. SetupOnly is true when the user has no second factor
	// yet, so the session reaches only the setup routes.
	User      *User
	SetupOnly bool
	// SecondFactor lists the factor kinds the user can finish the sign-in with ("totp",
	// "passkey", "recovery"). The pending sign-in lives in the tr_login cookie.
	SecondFactor []string
}

// Login checks a password for UI sign-in (spec §13). An admin signs in with a second factor: an
// admin with one gets a pending sign-in (cookie tr_login) that LoginSecondFactor completes, an
// admin without one gets a setup-only session. Other users get a session from the password;
// they need a factor only to approve a device or to create a manual publish session. In dev mode
// an admin without a factor gets a full session. SSO accounts are refused like a wrong
// password; local admins get ErrPasswordLoginDisabled when admin_password_login is off.
func (s *Service) Login(w http.ResponseWriter, r *http.Request, username, password string) (*LoginResult, error) {
	ctx := r.Context()
	p, err := s.authenticate(ctx, s.clientIP(r), username, password, false)
	if err != nil {
		var rl *rateLimitedError
		if errors.As(err, &rl) {
			setRetryAfter(w, rl.wait)
			return nil, ErrRateLimited
		}
		if errors.Is(err, ErrBadCredentials) {
			s.recordSignInFailed(r, username, "password")
		}
		return nil, err
	}
	if p.IsAdmin && !s.cfg().AdminPasswordLogin {
		s.recordSignInFailed(r, username, "admin password sign-in is disabled")
		return nil, ErrPasswordLoginDisabled
	}
	setupOnly := false
	if p.IsAdmin {
		kinds, has, err := s.factorKinds(ctx, p.UserID)
		if err != nil {
			return nil, err
		}
		if has {
			if err := s.startPendingLogin(w, r, p.UserID); err != nil {
				return nil, err
			}
			return &LoginResult{SecondFactor: kinds}, nil
		}
		setupOnly = !s.dev
	}
	if err := s.StartSession(w, r, p.UserID, s.cfg().SessionTTL); err != nil {
		return nil, err
	}
	u, err := s.GetUser(ctx, p.Username)
	if err != nil {
		return nil, err
	}
	s.RecordSignIn(r, p.UserID, p.Username, "password")
	return &LoginResult{User: u, SetupOnly: setupOnly}, nil
}

// RecordSignIn writes a sign-in to the audit log. method is "password", "totp", "passkey",
// "recovery" or "sso".
func (s *Service) RecordSignIn(r *http.Request, userID int64, username, method string) {
	s.audit.Record(r.Context(), r, audit.Event{
		Actor: audit.Actor{UserID: userID, Name: username}, Action: audit.SignIn,
		Target: username, Detail: map[string]any{"method": method},
	})
}

// recordSignInFailed writes a failed sign-in to the audit log; username is the name that was
// tried, reason says which step failed.
func (s *Service) recordSignInFailed(r *http.Request, username, reason string) {
	s.audit.Record(r.Context(), r, audit.Event{
		Actor: audit.Actor{Name: username}, Action: audit.SignInFailed,
		Target: username, Detail: map[string]any{"reason": reason},
	})
}

// RecordCLI writes a user change made with the telesphorepo CLI to the audit log; the actor is
// "cli" and there is no IP address.
func (s *Service) RecordCLI(ctx context.Context, action, username string, detail map[string]any) {
	s.audit.Record(ctx, nil, audit.Event{Actor: audit.Actor{Name: "cli"}, Action: action, Target: username, Detail: detail})
}

// RecordSignInFailed writes a refused SSO sign-in to the audit log.
func (s *Service) RecordSignInFailed(r *http.Request, username, reason string) {
	s.recordSignInFailed(r, username, reason)
}

// SetupOnly reports whether p is a setup-only UI session: a local admin without a second
// factor, outside dev mode. Such a session reaches only the setup routes.
func (s *Service) SetupOnly(ctx context.Context, p *Principal) (bool, error) {
	if p == nil || !p.Session || p.Kind != KindLocal || !p.IsAdmin || s.dev {
		return false, nil
	}
	has, err := s.rq.HasSecondFactor(ctx, p.UserID)
	if err != nil {
		return false, err
	}
	return has == 0, nil
}

// Username returns the name of user id; "" when the user no longer exists.
func (s *Service) Username(ctx context.Context, id int64) (string, error) {
	u, err := s.rq.GetPrincipal(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return u.Username, nil
}

// StartSession creates a session for userID with an absolute ttl and sets the tr_session cookie.
func (s *Service) StartSession(w http.ResponseWriter, r *http.Request, userID int64, ttl time.Duration) error {
	ctx := r.Context()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	val := base64.RawURLEncoding.EncodeToString(raw)
	h, _ := hashSession(val)
	now := time.Now()
	exp := now.Add(ttl)
	if err := s.wq.CreateSession(ctx, dbq.CreateSessionParams{
		IDHash: h, UserID: userID, CreatedAt: now.Unix(), ExpiresAt: exp.Unix()}); err != nil {
		return err
	}
	// Opportunistic bounded cleanup of expired sessions (served by sessions_expires).
	s.wq.DeleteExpiredSessions(ctx, now.Unix())

	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: val, Path: "/",
		MaxAge: int(ttl / time.Second), Expires: exp,
		HttpOnly: true, Secure: s.cfg().CookieSecure, SameSite: http.SameSiteStrictMode,
	})
	return nil
}

func (s *Service) Logout(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if h, ok := hashSession(c.Value); ok {
			if err := s.wq.DeleteSession(r.Context(), h); err != nil {
				return err
			}
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.cfg().CookieSecure, SameSite: http.SameSiteStrictMode,
	})
	return nil
}

// sessionPrincipal resolves a cookie value; unknown, expired or disabled-user sessions are
// anonymous (nil, nil).
func (s *Service) sessionPrincipal(ctx context.Context, val string) (*Principal, error) {
	h, ok := hashSession(val)
	if !ok {
		return nil, nil
	}
	row, err := s.rq.GetSessionPrincipal(ctx, h)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if time.Now().Unix() >= row.ExpiresAt {
		s.wq.DeleteSession(ctx, h)
		return nil, nil
	}
	if row.Disabled {
		return nil, nil
	}
	return &Principal{UserID: row.UserID, Username: row.Username, IsAdmin: row.IsAdmin, Kind: row.Kind, Session: true}, nil
}
