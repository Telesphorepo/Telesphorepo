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

// Login checks a password for UI sign-in and starts a session. SSO accounts are refused like a
// wrong password; local admins get ErrPasswordLoginDisabled when admin_password_login is off.
func (s *Service) Login(w http.ResponseWriter, r *http.Request, username, password string) (*User, error) {
	ctx := r.Context()
	p, err := s.authenticate(ctx, s.clientIP(r), username, password, false)
	if err != nil {
		var rl *rateLimitedError
		if errors.As(err, &rl) {
			setRetryAfter(w, rl.wait)
			return nil, ErrRateLimited
		}
		return nil, err
	}
	if p.IsAdmin && !s.cfg().AdminPasswordLogin {
		return nil, ErrPasswordLoginDisabled
	}
	if err := s.StartSession(w, r, p.UserID, s.cfg().SessionTTL); err != nil {
		return nil, err
	}
	return s.GetUser(ctx, p.Username)
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
	return &Principal{UserID: row.UserID, Username: row.Username, IsAdmin: row.IsAdmin, Kind: row.Kind}, nil
}
