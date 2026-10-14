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

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	val := base64.RawURLEncoding.EncodeToString(raw)
	h, _ := hashSession(val)
	now := time.Now()
	exp := now.Add(s.cfg().SessionTTL)
	if _, err := s.db.W.ExecContext(ctx,
		`INSERT INTO sessions (id_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		h, p.UserID, now.Unix(), exp.Unix()); err != nil {
		return nil, err
	}
	// Opportunistic bounded cleanup of expired sessions (served by sessions_expires).
	s.db.W.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash IN
		(SELECT id_hash FROM sessions WHERE expires_at <= ? LIMIT 100)`, now.Unix())

	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: val, Path: "/",
		MaxAge: int(s.cfg().SessionTTL / time.Second), Expires: exp,
		HttpOnly: true, Secure: s.cfg().CookieSecure, SameSite: http.SameSiteStrictMode,
	})
	return s.GetUser(ctx, p.Username)
}

func (s *Service) Logout(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if h, ok := hashSession(c.Value); ok {
			if _, err := s.db.W.ExecContext(r.Context(), `DELETE FROM sessions WHERE id_hash = ?`, h); err != nil {
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
	var (
		p        Principal
		exp      int64
		disabled bool
	)
	err := s.db.R.QueryRowContext(ctx, `
		SELECT s.expires_at, u.id, u.username, u.is_admin, u.disabled
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.id_hash = ?`, h).
		Scan(&exp, &p.UserID, &p.Username, &p.IsAdmin, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if time.Now().Unix() >= exp {
		s.db.W.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, h)
		return nil, nil
	}
	if disabled {
		return nil, nil
	}
	return &p, nil
}
