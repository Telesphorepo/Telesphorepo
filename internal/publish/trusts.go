package publish

import (
	"context"
	"crypto"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

const (
	// maxTokenAge is how old a CI token's iat may be.
	maxTokenAge = 10 * time.Minute
	// clockSkew is how far in the future a CI token's iat may be.
	clockSkew = time.Minute
	// maxCITokenBytes bounds a CI identity token.
	maxCITokenBytes = 16 << 10
	oidcTimeout     = 10 * time.Second
)

// signingAlgs are the JWS algorithms accepted on CI identity tokens.
var signingAlgs = []string{
	gooidc.RS256, gooidc.RS384, gooidc.RS512,
	gooidc.ES256, gooidc.ES384, gooidc.ES512,
	gooidc.PS256, gooidc.PS384, gooidc.PS512,
	gooidc.EdDSA,
}

// Trust is a CI trust policy. Audience "" means the current base_url. Claims maps a claim name
// to its allowed values, each exact or a "*" glob ("*" matches any text, "/" included); every
// listed claim must match. JWKS "" means the server fetches the issuer's keys.
type Trust struct {
	ID        int64               `json:"id"`
	Name      string              `json:"name"`
	Issuer    string              `json:"issuer"`
	Audience  string              `json:"audience"`
	Claims    map[string][]string `json:"claims"`
	JWKS      string              `json:"jwks"`
	Repos     []string            `json:"repos"`
	CreatedAt time.Time           `json:"created_at"`
	Disabled  bool                `json:"disabled"`
}

// TrustInput creates a policy, or changes the fields that are set (PATCH). Name, Issuer and
// Claims are required on create.
type TrustInput struct {
	Name     *string             `json:"name"`
	Issuer   *string             `json:"issuer"`
	Audience *string             `json:"audience"`
	Claims   map[string][]string `json:"claims"`
	JWKS     *string             `json:"jwks"`
	Repos    []string            `json:"repos"`
	Disabled *bool               `json:"disabled"`
}

// trustFields are the stored fields of a policy.
type trustFields struct {
	name, issuer, audience, claims, jwks string
	disabled                             bool
}

func (in *TrustInput) apply(f *trustFields) error {
	if in.Name != nil {
		f.name = strings.TrimSpace(*in.Name)
	}
	if in.Issuer != nil {
		f.issuer = strings.TrimSpace(*in.Issuer)
	}
	if in.Audience != nil {
		f.audience = strings.TrimSpace(*in.Audience)
	}
	if in.Claims != nil {
		b, err := json.Marshal(in.Claims) // map keys come out sorted
		if err != nil {
			return errorf(ErrInvalid, "invalid claims")
		}
		f.claims = string(b)
	}
	if in.JWKS != nil {
		f.jwks = strings.TrimSpace(*in.JWKS)
	}
	if in.Disabled != nil {
		f.disabled = *in.Disabled
	}
	return f.validate()
}

func (f *trustFields) validate() error {
	if !validName(f.name) {
		return errorf(ErrInvalid, "name must be 1 to 100 characters")
	}
	if u, err := url.Parse(f.issuer); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return errorf(ErrInvalid, "issuer must be an http(s) URL")
	}
	if len(f.audience) > 1024 {
		return errorf(ErrInvalid, "audience is too long")
	}
	claims, err := decodeClaims(f.claims)
	if err != nil || len(claims) == 0 {
		return errorf(ErrInvalid, "claims must name at least one claim, each with at least one allowed value")
	}
	for name, values := range claims {
		if name == "" || len(values) == 0 {
			return errorf(ErrInvalid, "claims must name at least one claim, each with at least one allowed value")
		}
		for _, v := range values {
			if v == "" || !utf8.ValidString(v) {
				return errorf(ErrInvalid, "claim "+name+" has an empty allowed value")
			}
		}
	}
	if f.jwks != "" {
		if _, err := parseJWKS(f.jwks); err != nil {
			return err
		}
	}
	return nil
}

func decodeClaims(s string) (map[string][]string, error) {
	var m map[string][]string
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, err
	}
	return m, nil
}

// parseJWKS returns the signing keys of a pasted JWKS.
func parseJWKS(s string) ([]crypto.PublicKey, error) {
	var set jose.JSONWebKeySet
	if err := json.Unmarshal([]byte(s), &set); err != nil {
		return nil, errorf(ErrInvalid, "jwks is not a valid JWKS document")
	}
	var keys []crypto.PublicKey
	for _, k := range set.Keys {
		if k.Use == "enc" {
			continue
		}
		pub := k.Public()
		if pub.Valid() {
			keys = append(keys, pub.Key)
		}
	}
	if len(keys) == 0 {
		return nil, errorf(ErrInvalid, "jwks holds no signing keys")
	}
	return keys, nil
}

// repoIDs resolves repo names to ids, in name order without duplicates.
func (s *Service) repoIDs(ctx context.Context, names []string) ([]int64, error) {
	names = slices.Compact(slices.Sorted(slices.Values(names)))
	ids := make([]int64, 0, len(names))
	for _, n := range names {
		r, err := s.repos.ByName(ctx, n)
		if errors.Is(err, repos.ErrNotFound) {
			return nil, errorf(ErrInvalid, "unknown repo "+n)
		}
		if err != nil {
			return nil, err
		}
		ids = append(ids, r.ID)
	}
	return ids, nil
}

func setTrustRepos(ctx context.Context, q *dbq.Queries, trustID int64, ids []int64) error {
	if err := q.DeleteTrustRepos(ctx, trustID); err != nil {
		return err
	}
	for _, id := range ids {
		if err := q.InsertTrustRepo(ctx, dbq.InsertTrustRepoParams{TrustID: trustID, RepoID: id}); err != nil {
			return err
		}
	}
	return nil
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// CreateTrust stores a new policy created by user by.
func (s *Service) CreateTrust(ctx context.Context, by int64, in TrustInput) (*Trust, error) {
	f := trustFields{claims: "{}"}
	if err := in.apply(&f); err != nil {
		return nil, err
	}
	ids, err := s.repoIDs(ctx, in.Repos)
	if err != nil {
		return nil, err
	}
	var id int64
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		var err error
		id, err = q.CreateTrust(ctx, dbq.CreateTrustParams{
			Name: f.name, Issuer: f.issuer, Audience: f.audience, Claims: f.claims, Jwks: f.jwks,
			CreatedBy: nullID(by), CreatedAt: time.Now().Unix(), Disabled: boolInt(f.disabled),
		})
		if isUnique(err) {
			return errorf(ErrConflict, "a trust policy with this name already exists")
		}
		if err != nil {
			return err
		}
		return setTrustRepos(ctx, q, id, ids)
	})
	if err != nil {
		return nil, err
	}
	return s.GetTrust(ctx, id)
}

// UpdateTrust changes a policy. A change to what the policy accepts (issuer, audience, claims,
// keys, repos, or disabling it) ends the open sessions it minted.
func (s *Service) UpdateTrust(ctx context.Context, id int64, in TrustInput) (*Trust, error) {
	cur, err := s.rq.GetTrust(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errorf(ErrNotFound, "trust policy not found")
	}
	if err != nil {
		return nil, err
	}
	old := trustFields{cur.Name, cur.Issuer, cur.Audience, cur.Claims, cur.Jwks, cur.Disabled != 0}
	f := old
	if err := in.apply(&f); err != nil {
		return nil, err
	}
	var ids []int64
	if in.Repos != nil {
		if ids, err = s.repoIDs(ctx, in.Repos); err != nil {
			return nil, err
		}
	}
	narrowed := in.Repos != nil || f.issuer != old.issuer || f.audience != old.audience ||
		f.claims != old.claims || f.jwks != old.jwks || (f.disabled && !old.disabled)
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		n, err := q.UpdateTrust(ctx, dbq.UpdateTrustParams{
			Name: f.name, Issuer: f.issuer, Audience: f.audience, Claims: f.claims, Jwks: f.jwks,
			Disabled: boolInt(f.disabled), ID: id,
		})
		if isUnique(err) {
			return errorf(ErrConflict, "a trust policy with this name already exists")
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return errorf(ErrNotFound, "trust policy not found")
		}
		if in.Repos != nil {
			if err := setTrustRepos(ctx, q, id, ids); err != nil {
				return err
			}
		}
		if narrowed {
			_, err = q.CloseTrustPublishSessions(ctx, dbq.CloseTrustPublishSessionsParams{
				ClosedAt: nullID(time.Now().Unix()), TrustID: nullID(id),
			})
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetTrust(ctx, id)
}

// DeleteTrust ends the policy's open sessions and deletes it.
func (s *Service) DeleteTrust(ctx context.Context, id int64) error {
	return s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		if _, err := q.CloseTrustPublishSessions(ctx, dbq.CloseTrustPublishSessionsParams{
			ClosedAt: nullID(time.Now().Unix()), TrustID: nullID(id),
		}); err != nil {
			return err
		}
		n, err := q.DeleteTrust(ctx, id)
		if err != nil {
			return err
		}
		if n == 0 {
			return errorf(ErrNotFound, "trust policy not found")
		}
		return nil
	})
}

func (s *Service) toTrust(ctx context.Context, t dbq.PublishTrust) (*Trust, error) {
	rs, err := s.rq.ListTrustRepos(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(rs))
	for i, r := range rs {
		names[i] = r.Name
	}
	slices.Sort(names)
	claims, err := decodeClaims(t.Claims)
	if err != nil {
		return nil, err
	}
	return &Trust{
		ID: t.ID, Name: t.Name, Issuer: t.Issuer, Audience: t.Audience, Claims: claims, JWKS: t.Jwks,
		Repos: names, CreatedAt: unixTime(t.CreatedAt), Disabled: t.Disabled != 0,
	}, nil
}

// GetTrust returns one policy with its repos.
func (s *Service) GetTrust(ctx context.Context, id int64) (*Trust, error) {
	t, err := s.rq.GetTrust(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errorf(ErrNotFound, "trust policy not found")
	}
	if err != nil {
		return nil, err
	}
	return s.toTrust(ctx, t)
}

// Trusts returns a keyset page of the policies by name: names after after ("" first page).
// next is the cursor of the next page, "" at the end.
func (s *Service) Trusts(ctx context.Context, after string, limit int) ([]*Trust, string, error) {
	rows, err := s.rq.ListTrusts(ctx, dbq.ListTrustsParams{After: after, Limit: int64(limit)})
	if err != nil {
		return nil, "", err
	}
	out := make([]*Trust, 0, len(rows))
	for _, r := range rows {
		t, err := s.toTrust(ctx, r)
		if err != nil {
			return nil, "", err
		}
		out = append(out, t)
	}
	next := ""
	if len(rows) == limit && limit > 0 {
		next = rows[len(rows)-1].Name
	}
	return out, next, nil
}

// --- token exchange ---

// unverifiedIssuer reads the iss claim of a JWT without checking it; it only picks the
// policies to verify the token against.
func unverifiedIssuer(raw string) (string, bool) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return "", false
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	var c struct {
		Iss string `json:"iss"`
	}
	if json.Unmarshal(b, &c) != nil || c.Iss == "" {
		return "", false
	}
	return c.Iss, true
}

// provider returns the discovered provider of a CI issuer, cached per issuer (its key set
// refetches the keys when a token names an unknown key). Failures are not cached.
func (s *Service) provider(ctx context.Context, issuer string) (*gooidc.Provider, error) {
	s.provMu.Lock()
	p, ok := s.provs[issuer]
	s.provMu.Unlock()
	if ok {
		return p, nil
	}
	// The provider keeps this context for later key fetches without its deadline, so the
	// client carries the timeout.
	cctx := gooidc.ClientContext(context.WithoutCancel(ctx), &http.Client{Timeout: oidcTimeout})
	dctx, cancel := context.WithTimeout(cctx, oidcTimeout)
	defer cancel()
	p, err := gooidc.NewProvider(dctx, issuer)
	if err != nil {
		return nil, err
	}
	s.provMu.Lock()
	s.provs[issuer] = p
	s.provMu.Unlock()
	return p, nil
}

// verifyCIToken checks the token's signature, issuer and expiry against one policy's keys.
func (s *Service) verifyCIToken(ctx context.Context, t dbq.ListMatchingTrustsRow, raw string) (*gooidc.IDToken, error) {
	cfg := &gooidc.Config{SkipClientIDCheck: true, SupportedSigningAlgs: signingAlgs}
	var v *gooidc.IDTokenVerifier
	if t.Jwks != "" {
		keys, err := parseJWKS(t.Jwks)
		if err != nil {
			return nil, err
		}
		v = gooidc.NewVerifier(t.Issuer, &gooidc.StaticKeySet{PublicKeys: keys}, cfg)
	} else {
		p, err := s.provider(ctx, t.Issuer)
		if err != nil {
			s.log.WarnContext(ctx, "CI issuer discovery failed", "issuer", t.Issuer, "error", err.Error())
			return nil, errors.New("the issuer's keys could not be fetched")
		}
		v = p.Verifier(cfg)
	}
	ctx, cancel := context.WithTimeout(ctx, oidcTimeout)
	defer cancel()
	return v.Verify(ctx, raw)
}

// claimValues returns a claim's value as strings: a string, number or boolean, or a list of
// them.
func claimValues(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case bool:
		return []string{strconv.FormatBool(x)}
	case float64:
		return []string{strconv.FormatFloat(x, 'f', -1, 64)}
	case []any:
		var out []string
		for _, e := range x {
			if _, nested := e.([]any); !nested {
				out = append(out, claimValues(e)...)
			}
		}
		return out
	}
	return nil
}

// globMatch matches s against a pattern in which "*" stands for any text, "/" included.
func globMatch(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for _, mid := range parts[1 : len(parts)-1] {
		i := strings.Index(s, mid)
		if i < 0 {
			return false
		}
		s = s[i+len(mid):]
	}
	return strings.HasSuffix(s, parts[len(parts)-1])
}

// claimsMatch reports whether every claim the policy lists has a value that one of its
// patterns matches.
func claimsMatch(policy map[string][]string, claims map[string]any) bool {
	for name, patterns := range policy {
		ok := false
		for _, v := range claimValues(claims[name]) {
			for _, p := range patterns {
				if globMatch(p, v) {
					ok = true
				}
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// OIDCSession exchanges a CI identity token for a publish session for repo. The token must
// verify against an enabled policy of its issuer that lists the repo (signature, issuer,
// audience, exp, iat at most 10 min old, claims), and must not have been used before.
func (s *Service) OIDCSession(ctx context.Context, ip netip.Addr, raw, repoName string) (*Minted, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxCITokenBytes {
		return nil, errorf(ErrInvalid, "token must be a CI identity token (JWT)")
	}
	issuer, ok := unverifiedIssuer(raw)
	if !ok {
		return nil, errorf(ErrDenied, "token is not a valid identity token")
	}
	refused := errorf(ErrDenied, "no trust policy allows this token to publish to "+repoName)
	repo, err := s.repos.ByName(ctx, repoName)
	if errors.Is(err, repos.ErrNotFound) {
		return nil, refused
	}
	if err != nil {
		return nil, err
	}
	trusts, err := s.rq.ListMatchingTrusts(ctx, dbq.ListMatchingTrustsParams{Issuer: issuer, RepoID: repo.ID})
	if err != nil {
		return nil, err
	}
	now := time.Now()
	reason := ""
	var match *dbq.ListMatchingTrustsRow
	var idt *gooidc.IDToken
	var claims map[string]any
	for i := range trusts {
		t := trusts[i]
		tok, err := s.verifyCIToken(ctx, t, raw)
		if err != nil {
			reason = err.Error()
			continue
		}
		aud := t.Audience
		if aud == "" {
			aud = s.baseURL()
		}
		if !slices.Contains(tok.Audience, aud) {
			reason = "the token's audience is not " + aud
			continue
		}
		if tok.IssuedAt.IsZero() || now.Sub(tok.IssuedAt) > maxTokenAge || tok.IssuedAt.After(now.Add(clockSkew)) {
			reason = "the token was not issued in the last 10 minutes"
			continue
		}
		var all map[string]any
		if err := tok.Claims(&all); err != nil {
			reason = "the token's claims are unreadable"
			continue
		}
		policy, err := decodeClaims(t.Claims)
		if err != nil || len(policy) == 0 || !claimsMatch(policy, all) {
			reason = "the token's claims do not match a trust policy"
			continue
		}
		match, idt, claims = &t, tok, all
		break
	}
	if match == nil {
		if reason != "" {
			return nil, errorf(ErrDenied, "no trust policy allows this token to publish to "+repoName+": "+reason)
		}
		return nil, refused
	}
	if repo.LockedBy != "" {
		return nil, errorf(ErrConflict, repo.LockedBy)
	}
	if err := s.ipAllowed(repo, ip, audit.Actor{Name: idt.Subject}); err != nil {
		return nil, err
	}

	var seen [32]byte
	if jti, _ := claims["jti"].(string); jti != "" {
		seen = sha256.Sum256([]byte(idt.Issuer + "\n" + jti))
	} else {
		seen = sha256.Sum256([]byte(raw))
	}
	var m *Minted
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		n, err := q.InsertSeenOIDCToken(ctx, dbq.InsertSeenOIDCTokenParams{IDHash: seen[:], ExpiresAt: idt.Expiry.Unix()})
		if err != nil {
			return err
		}
		if n == 0 {
			return errorf(ErrDenied, "this identity token was already used")
		}
		m, err = s.mint(ctx, q, repo, ViaOIDC, 0, 0, match.ID, idt.Subject)
		return err
	})
	if err != nil {
		return nil, err
	}
	return m, nil
}
