package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Op is an operation on one repo. Package access gives the names that callers use
// (access.Read, access.Write, ...).
type Op int

const (
	OpMeta   Op = iota // read the repo record
	OpRead             // download
	OpWrite            // store a file
	OpDelete           // remove files
	OpManage           // change repo settings, grants, rules
	OpMint             // mint a publish session for the repo
)

// Scope is what a core bearer token allows on one repo: the operations, and the open publish
// session of the repo that Write and Delete need. Only the publish session of the bearer's own
// credential can be a scope session.
type Scope struct {
	Repo    string
	Ops     []Op
	Session int64
	// Open is the publish session of Session, loaded again on each request (nil without one).
	Open *PublishSession
}

// Allows reports whether the scope holds op.
func (s *Scope) Allows(op Op) bool { return slices.Contains(s.Ops, op) }

// ScopeOf returns the scope of p for repo, or nil.
func (p *Principal) ScopeOf(repo string) *Scope {
	for i := range p.Scopes {
		if p.Scopes[i].Repo == repo {
			return &p.Scopes[i]
		}
	}
	return nil
}

// bearerPrefix starts a core bearer token: "trb.<base64url payload>.<base64url HMAC-SHA256>".
const bearerPrefix = "trb."

// BearerKeyInfo is the HKDF info of the key that signs core bearer tokens.
const BearerKeyInfo = "telesphorepo bearer v1"

// maxBearerTTL is the longest life of a core bearer token.
const maxBearerTTL = 15 * time.Minute

// SetBearerKey sets the key that signs core bearer tokens (HKDF-SHA256 of the master key with
// BearerKeyInfo). Without it IssueBearer fails and no bearer token verifies.
func (s *Service) SetBearerKey(k []byte) { s.bearerKey = k }

// SessionLoader returns the principal of the open publish session id, or an error that wraps
// ErrPublishEnded when the session is unknown or ended. The publish module sets it.
type SessionLoader func(ctx context.Context, id int64) (*Principal, error)

// SetSessionLoader sets the loader of publish sessions that core bearer tokens name.
func (s *Service) SetSessionLoader(fn SessionLoader) { s.sessions = fn }

type bearerScope struct {
	Repo    string `json:"r"`
	Ops     []Op   `json:"o"`
	Session int64  `json:"x,omitempty"`
}

type bearerPayload struct {
	Cred    Cred          `json:"c"` // 0: anonymous
	User    int64         `json:"u,omitempty"`
	Token   int64         `json:"t,omitempty"` // CredReadToken
	Session int64         `json:"p,omitempty"` // CredPublish: the session of the credential
	Scopes  []bearerScope `json:"s"`
	Expires int64         `json:"e"`
}

// IssueBearer returns a token for p, limited to scopes, for ttl (at most 15 minutes), and its
// expiry. p is a password, a read token or a publish session; a nil p is the anonymous caller.
// A scope holds Read, Write and Delete only. Write and Delete stay only when p is a publish
// session and the scope session is that session, so a password, a read token or the anonymous
// caller gets Read only. Each request with the token loads the credential behind it again.
func (s *Service) IssueBearer(p *Principal, scopes []Scope, ttl time.Duration) (string, time.Time, error) {
	if len(s.bearerKey) == 0 {
		return "", time.Time{}, errors.New("auth: no bearer key")
	}
	if p != nil && !p.Is(CredPassword|CredReadToken|CredPublish) {
		return "", time.Time{}, errors.New("auth: a bearer token carries a password, a read token or a publish session")
	}
	ttl = min(ttl, maxBearerTTL)
	exp := time.Now().Add(ttl).Truncate(time.Second)
	pl := bearerPayload{Expires: exp.Unix(), Scopes: []bearerScope{}}
	if p != nil {
		pl.Cred, pl.User, pl.Token = p.Cred, p.UserID, p.TokenID
		if p.Is(CredPublish) {
			pl.Session = p.Publish.ID
		}
	}
	for _, sc := range scopes {
		writes := p.Is(CredPublish) && sc.Session != 0 && sc.Session == p.Publish.ID
		var ops []Op
		for _, op := range sc.Ops {
			switch {
			case op != OpRead && op != OpWrite && op != OpDelete:
			case op != OpRead && !writes:
			default:
				ops = append(ops, op)
			}
		}
		session := sc.Session
		if !writes {
			session = 0
		}
		if len(ops) > 0 {
			pl.Scopes = append(pl.Scopes, bearerScope{Repo: sc.Repo, Ops: ops, Session: session})
		}
	}
	body, err := json.Marshal(pl)
	if err != nil {
		return "", time.Time{}, err
	}
	enc := base64.RawURLEncoding.EncodeToString(body)
	return bearerPrefix + enc + "." + base64.RawURLEncoding.EncodeToString(s.bearerMAC(enc)), exp, nil
}

func (s *Service) bearerMAC(payload string) []byte {
	m := hmac.New(sha256.New, s.bearerKey)
	m.Write([]byte(payload))
	return m.Sum(nil)
}

// verifyBearer checks a core bearer token and loads the credential behind it again: the user is
// enabled, the read token exists, the publish session is open. A scope session must be the
// publish session of the credential, on the repo that the scope names. A token that fails any
// check is ErrBadCredentials, or ErrPublishEnded for an ended publish session.
func (s *Service) verifyBearer(ctx context.Context, tok string) (*Principal, error) {
	enc, sig, ok := strings.Cut(strings.TrimPrefix(tok, bearerPrefix), ".")
	if !ok || len(s.bearerKey) == 0 {
		return nil, ErrBadCredentials
	}
	mac, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(mac, s.bearerMAC(enc)) {
		return nil, ErrBadCredentials
	}
	body, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return nil, ErrBadCredentials
	}
	var pl bearerPayload
	if json.Unmarshal(body, &pl) != nil || time.Now().Unix() >= pl.Expires {
		return nil, ErrBadCredentials
	}
	p, err := s.bearerPrincipal(ctx, pl)
	if err != nil {
		return nil, err
	}
	p.Scopes = []Scope{}
	for _, sc := range pl.Scopes {
		x := Scope{Repo: sc.Repo, Ops: sc.Ops, Session: sc.Session}
		if sc.Session != 0 {
			if !p.Is(CredPublish) || sc.Session != p.Publish.ID {
				return nil, ErrBadCredentials
			}
			repo, err := s.repos.ByName(ctx, sc.Repo)
			if errors.Is(err, repos.ErrNotFound) {
				return nil, ErrBadCredentials
			}
			if err != nil {
				return nil, err
			}
			if repo.ID != p.Publish.RepoID {
				return nil, ErrBadCredentials
			}
			x.Open = p.Publish
		}
		p.Scopes = append(p.Scopes, x)
	}
	return p, nil
}

// bearerPrincipal loads the credential of a bearer payload again.
func (s *Service) bearerPrincipal(ctx context.Context, pl bearerPayload) (*Principal, error) {
	switch pl.Cred {
	case 0:
		return &Principal{}, nil
	case CredPassword:
		p, err := s.principalByID(ctx, pl.User)
		if err != nil {
			return nil, err
		}
		p.Cred = pl.Cred
		return p, nil
	case CredReadToken:
		row, err := s.rq.GetTokenAuthByID(ctx, pl.Token)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrBadCredentials
		}
		if err != nil {
			return nil, err
		}
		if row.Disabled || row.Kind != KindLocal || (row.ExpiresAt.Valid && time.Now().Unix() >= row.ExpiresAt.Int64) {
			return nil, ErrBadCredentials
		}
		return &Principal{Cred: CredReadToken, UserID: row.UserID, Username: row.Username, Perms: perm.Joined(row.Perms), TokenID: row.TokenID, Kind: row.Kind}, nil
	case CredPublish:
		if s.sessions == nil {
			return nil, ErrPublishEnded
		}
		return s.sessions(ctx, pl.Session)
	}
	return nil, ErrBadCredentials
}
