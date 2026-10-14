package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	tokenPrefix    = "trp_"
	tokenIDHexLen  = 12
	tokenSecretLen = 43 // base64url(32 bytes)
	tokenLen       = len(tokenPrefix) + tokenIDHexLen + 1 + tokenSecretLen
)

func validTokenName(name string) bool {
	if name == "" || len(name) > 100 || !utf8.ValidString(name) || strings.TrimSpace(name) != name {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (s *Service) CreateToken(ctx context.Context, userID int64, name string, expiresIn time.Duration) (*Token, string, error) {
	if !validTokenName(name) || expiresIn < 0 {
		return nil, "", ErrInvalid
	}
	now := time.Now()
	t := &Token{Name: name, CreatedAt: time.Unix(now.Unix(), 0)}
	var exp sql.NullInt64
	if expiresIn > 0 {
		e := time.Unix(now.Add(expiresIn).Unix(), 0)
		t.ExpiresAt = &e
		exp = sql.NullInt64{Int64: e.Unix(), Valid: true}
	}
	for attempt := 0; ; attempt++ {
		idb := make([]byte, tokenIDHexLen/2)
		secret := make([]byte, 32)
		if _, err := rand.Read(idb); err != nil {
			return nil, "", err
		}
		if _, err := rand.Read(secret); err != nil {
			return nil, "", err
		}
		pub := hex.EncodeToString(idb)
		sec := base64.RawURLEncoding.EncodeToString(secret)
		sum := sha256.Sum256([]byte(sec))
		res, err := s.db.W.ExecContext(ctx,
			`INSERT INTO api_tokens (user_id, public_id, secret_hash, name, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
			userID, pub, sum[:], name, now.Unix(), exp)
		if err != nil {
			msg := err.Error()
			switch {
			case strings.Contains(msg, "api_tokens.public_id") && attempt < 3:
				continue
			case strings.Contains(msg, "UNIQUE constraint failed"):
				return nil, "", ErrExists
			case strings.Contains(msg, "FOREIGN KEY constraint failed"):
				return nil, "", ErrNotFound
			}
			return nil, "", err
		}
		t.ID, _ = res.LastInsertId()
		return t, tokenPrefix + pub + "_" + sec, nil
	}
}

func (s *Service) ListTokens(ctx context.Context, userID int64) ([]*Token, error) {
	rows, err := s.db.R.QueryContext(ctx,
		`SELECT id, name, created_at, expires_at, last_used_at FROM api_tokens WHERE user_id = ? ORDER BY name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Token{}
	for rows.Next() {
		t := &Token{}
		var created int64
		var exp, used sql.NullInt64
		if err := rows.Scan(&t.ID, &t.Name, &created, &exp, &used); err != nil {
			return nil, err
		}
		t.CreatedAt = time.Unix(created, 0)
		t.ExpiresAt, t.LastUsedAt = optTime(exp), optTime(used)
		out = append(out, t)
	}
	return out, rows.Err()
}

func optTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(v.Int64, 0)
	return &t
}

func (s *Service) DeleteToken(ctx context.Context, p *Principal, id int64) error {
	if p == nil {
		return ErrNotFound
	}
	var res sql.Result
	var err error
	if p.IsAdmin {
		res, err = s.db.W.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ?`, id)
	} else {
		res, err = s.db.W.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ? AND user_id = ?`, id, p.UserID)
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// verifyToken authenticates a "trp_<id>_<secret>" token; the owner is the principal.
func (s *Service) verifyToken(ctx context.Context, tok string) (*Principal, error) {
	if len(tok) != tokenLen || tok[len(tokenPrefix)+tokenIDHexLen] != '_' {
		return nil, ErrBadCredentials
	}
	pub := tok[len(tokenPrefix) : len(tokenPrefix)+tokenIDHexLen]
	sec := tok[len(tokenPrefix)+tokenIDHexLen+1:]
	if _, err := hex.DecodeString(pub); err != nil || strings.ToLower(pub) != pub {
		return nil, ErrBadCredentials
	}
	if b, err := base64.RawURLEncoding.DecodeString(sec); err != nil || len(b) != 32 {
		return nil, ErrBadCredentials
	}
	var (
		p         Principal
		hash      []byte
		exp, used sql.NullInt64
		disabled  bool
	)
	err := s.db.R.QueryRowContext(ctx, `
		SELECT t.id, t.secret_hash, t.expires_at, t.last_used_at, u.id, u.username, u.is_admin, u.disabled
		FROM api_tokens t JOIN users u ON u.id = t.user_id WHERE t.public_id = ?`, pub).
		Scan(&p.TokenID, &hash, &exp, &used, &p.UserID, &p.Username, &p.IsAdmin, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBadCredentials
	}
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(sec))
	now := time.Now().Unix()
	if subtle.ConstantTimeCompare(sum[:], hash) != 1 || disabled || (exp.Valid && now >= exp.Int64) {
		return nil, ErrBadCredentials
	}
	if !used.Valid || now-used.Int64 >= 60 {
		s.touchToken(p.TokenID, now)
	}
	return &p, nil
}

// touchToken updates last_used_at in the background, at most once per minute per token.
func (s *Service) touchToken(id, now int64) {
	s.touchMu.Lock()
	if last, ok := s.touched[id]; ok && now-last < 60 {
		s.touchMu.Unlock()
		return
	}
	s.touched[id] = now
	if len(s.touched) > 10000 {
		for k, v := range s.touched {
			if now-v >= 60 {
				delete(s.touched, k)
			}
		}
	}
	s.touchMu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		s.db.W.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, now, id)
	}()
}
