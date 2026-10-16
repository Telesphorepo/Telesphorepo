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
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
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

// LoginResult is the outcome of the password step of a UI sign-in. Exactly one of Session and
// SecondFactor is set.
type LoginResult struct {
	// Session is set when a session started. Its SecondFactorRequired is true when the user has
	// no second factor yet, so the session reaches only the setup routes.
	Session *SessionView
	// SecondFactor lists the factor kinds the user can finish the sign-in with ("totp",
	// "passkey", "recovery"). The pending sign-in lives in the tr_login cookie.
	SecondFactor []string
}

// Login checks a password for UI sign-in. A person who needs the second factor (they hold a
// permission with SecondFactor) signs in with it: with a factor they get a pending sign-in
// (cookie tr_login) that LoginSecondFactor
// completes, without one a setup-only session. Other users get a session from the password;
// they need a factor only to approve a device or to create a manual publish session. In dev mode
// a person without a factor gets a full session. SSO accounts are refused like a wrong
// password; a person who needs the second factor gets ErrPasswordLoginDisabled when
// signin.password_login is "publishers".
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
	needs, err := userNeedsFactor(ctx, s.rq, p.UserID)
	if err != nil {
		return nil, err
	}
	if needs && PasswordLogin.Get(s.set()) == PasswordLoginPublishers {
		s.recordSignInFailed(r, username, "password sign-in is off for people who manage")
		return nil, ErrPasswordLoginDisabled
	}
	setupOnly := false
	if needs {
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
	if err := s.StartSession(w, r, p.UserID, settings.SessionTTL.Get(s.set())); err != nil {
		return nil, err
	}
	v, err := s.View(ctx, p.UserID, setupOnly)
	if err != nil {
		return nil, err
	}
	s.RecordSignIn(r, p.UserID, p.Username, "password")
	return &LoginResult{Session: v}, nil
}

// RecordSignIn writes a sign-in of user to the audit log. method is "password", "totp",
// "passkey" or "recovery".
func (s *Service) RecordSignIn(r *http.Request, userID int64, username, method string) {
	ctx := audit.WithActor(r.Context(), audit.Actor{Kind: audit.ActorUser, UserID: userID, Name: username, IP: httpx.ClientIP(r)})
	_ = s.audit.Record(ctx, audit.Event{Action: audit.SignInSuccess, Target: username, Detail: map[string]any{"method": method}})
}

// recordSignInFailed writes a failed sign-in to the audit log; username is the name that was
// tried, reason says which step failed.
func (s *Service) recordSignInFailed(r *http.Request, username, reason string) {
	ctx := audit.WithActor(r.Context(), audit.Actor{Kind: audit.ActorAnonymous, Name: username, IP: httpx.ClientIP(r)})
	_ = s.audit.Record(ctx, audit.Event{Action: audit.SignInFailed, Target: username, Detail: map[string]any{"reason": reason}})
}

// setupOnly reports whether a UI session of user is setup-only: a local account that needs the
// second factor (it holds a server permission with SecondFactor, or such a repository permission
// on some repo) and has none, outside dev mode. Such a session (CredSetupSession) reaches only
// the setup routes.
func (s *Service) setupOnly(ctx context.Context, userID int64, kind string, server perm.Set) (bool, error) {
	if kind != KindLocal || s.dev {
		return false, nil
	}
	if !needsFactor(server, false) {
		repoManaging, err := s.rq.HasRepoPermIn(ctx, dbq.HasRepoPermInParams{UserID: userID, Perms: managingRepoPerms()})
		if err != nil || !repoManaging {
			return false, err
		}
	}
	has, err := s.rq.HasSecondFactor(ctx, userID)
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
		HttpOnly: true, Secure: settings.CookieSecure.Get(s.set()), SameSite: http.SameSiteStrictMode,
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
		HttpOnly: true, Secure: settings.CookieSecure.Get(s.set()), SameSite: http.SameSiteStrictMode,
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
	perms := perm.Joined(row.Perms)
	setup, err := s.setupOnly(ctx, row.UserID, row.Kind, perms)
	if err != nil {
		return nil, err
	}
	cred := CredSession
	if setup {
		cred = CredSetupSession
	}
	return &Principal{Cred: cred, UserID: row.UserID, Username: row.Username, Perms: perms, Kind: row.Kind}, nil
}
