package auth

import (
	"context"
	"crypto/ed25519"
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
	Repo string
	// RepoID is the repo that Repo names now, resolved through redirects when the token is
	// verified (0: no repo of the name). Scopes match a repo by id, so a token keeps working
	// after a rename of its repo.
	RepoID  int64
	Ops     []Op
	Session int64
	// Open is the publish session of Session, loaded again on each request (nil without one).
	Open *PublishSession
}

// Allows reports whether the scope holds op.
func (s *Scope) Allows(op Op) bool { return slices.Contains(s.Ops, op) }

// ScopeOf returns the scope of p for the repo id, or nil.
func (p *Principal) ScopeOf(repo int64) *Scope {
	for i := range p.Scopes {
		if p.Scopes[i].RepoID != 0 && p.Scopes[i].RepoID == repo {
			return &p.Scopes[i]
		}
	}
	return nil
}

// For returns the principal that the access engine uses for repo: for a core bearer token whose
// scope of repo has an open publish session, a copy that carries that session (CredPublish);
// else p.
func (p *Principal) For(repo int64) *Principal {
	if p == nil || p.Scopes == nil {
		return p
	}
	sc := p.ScopeOf(repo)
	if sc == nil || sc.Open == nil {
		return p
	}
	q := *p
	q.Cred, q.Publish = CredPublish, sc.Open
	return &q
}

// InGroup returns the principal that reads the members of group: for a core bearer token whose
// scope of group holds Read, a copy without scopes, so each member is checked against the rights
// of the credential behind the token (the same as that credential over HTTP Basic); else p. The
// copy only reads members of a group that the token reads: the engine never sees it for any
// other request.
func (p *Principal) InGroup(group int64) *Principal {
	if p == nil || p.Scopes == nil {
		return p
	}
	if sc := p.ScopeOf(group); sc == nil || !sc.Allows(OpRead) {
		return p
	}
	q := *p
	q.Scopes = nil
	return &q
}

// bearerPrefix starts a core bearer token: "trb.<payload>.<HMAC-SHA256>.<Ed25519>", each part
// base64url without padding. The HMAC and the Ed25519 signature both sign the ASCII bytes of the
// base64url payload part. The server checks both; the edge Worker checks the Ed25519 signature
// with the public key of EdgePublicKey, so the HMAC key never leaves the server.
const bearerPrefix = "trb."

// BearerKeyInfo is the HKDF info of the key that signs core bearer tokens.
const BearerKeyInfo = "telesphorepo bearer v1"

// EdgeKeyInfo is the HKDF info of the seed of the Ed25519 key that adds the edge signature to
// core bearer tokens.
const EdgeKeyInfo = "telesphorepo edge v1"

// SetEdgeKey sets the Ed25519 key of the edge signature (ed25519.NewKeyFromSeed of the HKDF-SHA256
// of the master key with EdgeKeyInfo). Without it IssueBearer fails.
func (s *Service) SetEdgeKey(k ed25519.PrivateKey) { s.edgeKey = k }

// EdgePublicKey returns the public key of the edge signature, nil before SetEdgeKey.
func (s *Service) EdgePublicKey() ed25519.PublicKey {
	if s.edgeKey == nil {
		return nil
	}
	return s.edgeKey.Public().(ed25519.PublicKey)
}

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

// DeviceLoader returns the CredDevice principal of device id: the device is not revoked and not
// expired, its user is enabled, and the principal carries the user's current permissions. A
// device that is not usable returns ErrBadCredentials. The publish module sets it.
type DeviceLoader func(ctx context.Context, id int64) (*Principal, error)

// SetDeviceLoader sets the loader of the devices that core bearer tokens carry.
func (s *Service) SetDeviceLoader(fn DeviceLoader) { s.devices = fn }

// DevicePrincipal returns the CredDevice principal of device id (DeviceLoader), for a token
// exchange that verified a proof of the device.
func (s *Service) DevicePrincipal(ctx context.Context, id int64) (*Principal, error) {
	if s.devices == nil {
		return nil, ErrBadCredentials
	}
	return s.devices(ctx, id)
}

type bearerScope struct {
	Repo    string `json:"r"`
	Ops     []Op   `json:"o"`
	Session int64  `json:"x,omitempty"`
}

type bearerPayload struct {
	Cred  Cred  `json:"c"` // 0: anonymous
	User  int64 `json:"u,omitempty"`
	Token int64 `json:"t,omitempty"` // CredReadToken: the row ID
	// TokenPublic is the public ID of the read token (CredReadToken), the key of its edge access
	// object.
	TokenPublic string        `json:"tp,omitempty"`
	Session     int64         `json:"p,omitempty"` // CredPublish: the session of the credential
	Device      int64         `json:"d,omitempty"` // CredDevice
	Scopes      []bearerScope `json:"s"`
	Expires     int64         `json:"e"`
	Upload      string        `json:"up,omitempty"` // an upload token: the upload it is bound to
}

// IssueBearer returns a token for p, limited to scopes, for ttl (at most 15 minutes), and its
// expiry. p is a password, a read token, a publish session or a device; a nil p is the anonymous
// caller. A scope holds Read, Write and Delete only. Write and Delete stay only with the scope
// session of the credential: the publish session of p, or for a device a session in sc.Open that
// the device minted for the scope's repo. So a password, a read token or the anonymous caller gets
// Read only. Each request with the token loads the credential behind it again.
func (s *Service) IssueBearer(p *Principal, scopes []Scope, ttl time.Duration) (string, time.Time, error) {
	return s.issueBearer(p, scopes, ttl, "")
}

// IssueUploadToken is IssueBearer for a token bound to one upload: it verifies only from the URL
// of that upload (Read.URLToken), never from an Authorization header. A registry puts it in the
// upload URL that it sends to another host, so the client needs no credential there.
func (s *Service) IssueUploadToken(p *Principal, scopes []Scope, upload string, ttl time.Duration) (string, time.Time, error) {
	if upload == "" {
		return "", time.Time{}, errors.New("auth: an upload token needs an upload")
	}
	return s.issueBearer(p, scopes, ttl, upload)
}

func (s *Service) issueBearer(p *Principal, scopes []Scope, ttl time.Duration, upload string) (string, time.Time, error) {
	if len(s.bearerKey) == 0 || s.edgeKey == nil {
		return "", time.Time{}, errors.New("auth: no bearer key")
	}
	if p != nil && !p.Is(CredPassword|CredReadToken|CredPublish|CredDevice) {
		return "", time.Time{}, errors.New("auth: a bearer token carries a password, a read token, a publish session or a device")
	}
	ttl = min(ttl, maxBearerTTL)
	exp := time.Now().Add(ttl).Truncate(time.Second)
	pl := bearerPayload{Expires: exp.Unix(), Scopes: []bearerScope{}, Upload: upload}
	if p != nil {
		pl.Cred, pl.User, pl.Token, pl.Device = p.Cred, p.UserID, p.TokenID, p.DeviceID
		if p.Is(CredReadToken) {
			pl.TokenPublic = p.TokenPublicID
		}
		if p.Is(CredPublish) {
			pl.Session = p.Publish.ID
		}
	}
	for _, sc := range scopes {
		writes := sc.Session != 0 && ((p.Is(CredPublish) && sc.Session == p.Publish.ID) ||
			(p.Is(CredDevice) && sc.Open != nil && sc.Open.ID == sc.Session && sc.Open.DeviceID == p.DeviceID))
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
	return s.signBearer(body), exp, nil
}

// signBearer returns the core bearer token of the payload body: its HMAC and its edge signature
// (see bearerPrefix). IssueBearer and IssueUploadToken both sign through it.
func (s *Service) signBearer(body []byte) string {
	enc := base64.RawURLEncoding.EncodeToString(body)
	b64 := base64.RawURLEncoding.EncodeToString
	return bearerPrefix + enc + "." + b64(s.bearerMAC(enc)) + "." + b64(ed25519.Sign(s.edgeKey, []byte(enc)))
}

func (s *Service) bearerMAC(payload string) []byte {
	m := hmac.New(sha256.New, s.bearerKey)
	m.Write([]byte(payload))
	return m.Sum(nil)
}

// verifyBearer checks a core bearer token and loads the credential behind it again: the user is
// enabled, the read token exists, the publish session is open, the device is usable. A scope
// session must be the publish session of the credential, or for a device an open session that the
// device minted, on the repo that the scope names. A token that fails any check is
// ErrBadCredentials, or ErrPublishEnded for an ended publish session. upload is the upload of the
// URL that carried tok ("" for an Authorization header): the token must be bound to it.
func (s *Service) verifyBearer(ctx context.Context, tok, upload string) (*Principal, error) {
	// The four-part form: payload, HMAC, edge signature. The server checks both signatures, so a
	// token is valid in exactly one spelling.
	parts := strings.Split(strings.TrimPrefix(tok, bearerPrefix), ".")
	if len(parts) != 3 || len(s.bearerKey) == 0 || s.edgeKey == nil {
		return nil, ErrBadCredentials
	}
	enc := parts[0]
	mac, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(mac, s.bearerMAC(enc)) {
		return nil, ErrBadCredentials
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !ed25519.Verify(s.EdgePublicKey(), []byte(enc), sig) {
		return nil, ErrBadCredentials
	}
	body, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return nil, ErrBadCredentials
	}
	var pl bearerPayload
	if json.Unmarshal(body, &pl) != nil || time.Now().Unix() >= pl.Expires || pl.Upload != upload {
		return nil, ErrBadCredentials
	}
	p, err := s.bearerPrincipal(ctx, pl)
	if err != nil {
		return nil, err
	}
	p.Scopes = []Scope{}
	for _, sc := range pl.Scopes {
		x := Scope{Repo: sc.Repo, Ops: sc.Ops, Session: sc.Session}
		// The scope names the repo as it was named at issue: a redirect of it now resolves to it.
		repo, err := s.repos.ByName(ctx, sc.Repo)
		switch {
		case errors.Is(err, repos.ErrNotFound):
		case err != nil:
			return nil, err
		default:
			x.RepoID = repo.ID
		}
		if sc.Session != 0 {
			var open *PublishSession
			switch {
			case p.Is(CredPublish) && sc.Session == p.Publish.ID:
				open = p.Publish
			case p.Is(CredDevice) && s.sessions != nil:
				sp, err := s.sessions(ctx, sc.Session)
				if err != nil {
					return nil, err
				}
				if sp.Publish.DeviceID != p.DeviceID {
					return nil, ErrBadCredentials
				}
				open = sp.Publish
			default:
				return nil, ErrBadCredentials
			}
			if x.RepoID == 0 || x.RepoID != open.RepoID {
				return nil, ErrBadCredentials
			}
			x.Open = open
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
		return &Principal{Cred: CredReadToken, UserID: row.UserID, Username: row.Username, Perms: perm.Joined(row.Perms), TokenID: row.TokenID, TokenPublicID: pl.TokenPublic, Kind: row.Kind}, nil
	case CredPublish:
		if s.sessions == nil {
			return nil, ErrPublishEnded
		}
		return s.sessions(ctx, pl.Session)
	case CredDevice:
		p, err := s.DevicePrincipal(ctx, pl.Device)
		if err != nil {
			return nil, err
		}
		if p.UserID != pl.User {
			return nil, ErrBadCredentials
		}
		return p, nil
	}
	return nil, ErrBadCredentials
}
