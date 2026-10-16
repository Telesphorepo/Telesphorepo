package auth

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/token"
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
	kind, err := s.rq.GetUserKind(ctx, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", ErrNotFound
		}
		return nil, "", err
	}
	if kind != KindLocal {
		return nil, "", ErrSSOAccount
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
		pub, sec, tok := token.Mint(token.ReadToken)
		id, err := s.wq.CreateToken(ctx, dbq.CreateTokenParams{
			UserID: userID, PublicID: pub, SecretHash: token.Hash(sec), Name: name, CreatedAt: now.Unix(), ExpiresAt: exp})
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
		t.ID = id
		_ = s.audit.Record(ctx, audit.Event{Action: audit.TokenCreate, Target: name, Detail: map[string]any{"token_id": id}})
		return t, tok, nil
	}
}

func (s *Service) ListTokens(ctx context.Context, userID int64) ([]*Token, error) {
	rows, err := s.rq.ListTokens(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]*Token, len(rows))
	for i, r := range rows {
		out[i] = &Token{
			ID: r.ID, Name: r.Name, CreatedAt: time.Unix(r.CreatedAt, 0),
			ExpiresAt: optTime(r.ExpiresAt), LastUsedAt: optTime(r.LastUsedAt),
		}
	}
	return out, nil
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
	var n int64
	var err error
	if p.Perms.Has(perm.PeopleManage) {
		n, err = s.wq.DeleteToken(ctx, id)
	} else {
		n, err = s.wq.DeleteUserToken(ctx, dbq.DeleteUserTokenParams{ID: id, UserID: p.UserID})
	}
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	_ = s.audit.Record(ctx, audit.Event{Action: audit.TokenDelete, Target: strconv.FormatInt(id, 10), Detail: map[string]any{"token_id": id}})
	return nil
}

// VerifyReadToken is the Verifier of read tokens (token.ReadToken); the owner is the principal.
// A read token never writes: a write route accepts only publish sessions (route policy).
func (s *Service) VerifyReadToken(ctx context.Context, publicID, secret string) (*Principal, error) {
	if publicID == "" {
		return nil, ErrBadCredentials
	}
	row, err := s.rq.GetTokenAuth(ctx, publicID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBadCredentials
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	if !token.Check(row.SecretHash, secret) || row.Disabled || row.Kind != KindLocal ||
		(row.ExpiresAt.Valid && now >= row.ExpiresAt.Int64) {
		return nil, ErrBadCredentials
	}
	if !row.LastUsedAt.Valid || now-row.LastUsedAt.Int64 >= 60 {
		s.touchToken(row.TokenID, now)
	}
	return &Principal{Cred: CredReadToken, UserID: row.UserID, Username: row.Username, Perms: perm.Joined(row.Perms), TokenID: row.TokenID, Kind: row.Kind}, nil
}

// touchToken updates last_used_at in the background, at most once per minute per token. Wait
// waits for these writes.
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
	s.background.Add(1)
	go func() {
		defer s.background.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		s.wq.TouchToken(ctx, dbq.TouchTokenParams{LastUsedAt: sql.NullInt64{Int64: now, Valid: true}, ID: id})
	}()
}
