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
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/publish/dbq"
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
		if r.Kind != repos.KindHosted {
			return nil, errorf(ErrInvalid, r.Name+" is a "+r.Kind+"; a CI pipeline publishes to a repository that takes uploads")
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

// CreateTrust stores a new policy created by user by and records ci.pipeline.create.
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
		if err := setTrustRepos(ctx, q, id, ids); err != nil {
			return err
		}
		return s.audit.RecordTx(ctx, tx, audit.Event{Action: ActionPipelineCreate, Target: f.name,
			Detail: map[string]any{"id": id, "issuer": f.issuer, "repos": in.Repos}})
	})
	if err != nil {
		return nil, err
	}
	return s.GetTrust(ctx, id)
}

// UpdateTrust changes a policy. A change to what the policy accepts (issuer, audience, claims,
// keys, repos, or disabling it) ends the open sessions it minted. It records ci.pipeline.update.
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
			if _, err := q.CloseTrustPublishSessions(ctx, dbq.CloseTrustPublishSessionsParams{
				ClosedAt: nullID(time.Now().Unix()), TrustID: nullID(id),
			}); err != nil {
				return err
			}
		}
		return s.audit.RecordTx(ctx, tx, audit.Event{Action: ActionPipelineUpdate, Target: f.name,
			Detail: map[string]any{"id": id, "sessions_closed": narrowed}})
	})
	if err != nil {
		return nil, err
	}
	return s.GetTrust(ctx, id)
}

// DeleteTrust ends the policy's open sessions, deletes it and records ci.pipeline.delete.
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
		return s.audit.RecordTx(ctx, tx, audit.Event{Action: ActionPipelineDelete, Target: strconv.FormatInt(id, 10), Detail: map[string]any{"id": id}})
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

// Trusts returns a keyset page of the policies by name: the names after after ("" first page)
// that start with prefix. next is the cursor of the next page, "" at the end. With all false only
// the policies that list at least one repo and only repos among repoIDs are returned.
func (s *Service) Trusts(ctx context.Context, all bool, repoIDs []int64, prefix, after string, limit int) ([]*Trust, string, error) {
	lower, upper := trustNameRange(prefix, after)
	var rows []dbq.PublishTrust
	var err error
	switch {
	case all:
		rows, err = s.rq.ListTrusts(ctx, dbq.ListTrustsParams{Lower: lower, Upper: upper, Limit: int64(limit)})
	case len(repoIDs) > 0:
		rows, err = s.rq.ListTrustsInRepos(ctx, dbq.ListTrustsInReposParams{Lower: lower, Upper: upper, RepoIds: repoIDs, Limit: int64(limit)})
	}
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

// trustNameRange is the range [lower, upper) of the policy names after after that start with
// prefix, under SQLite's byte-wise order. A name is valid UTF-8 (the JSON of the API), so no byte
// of it is 0xF5 or above.
func trustNameRange(prefix, after string) (lower, upper string) {
	lower, upper = prefix, "\xf5"
	if c := after + "\x00"; after != "" && c > lower {
		lower = c
	}
	b := []byte(prefix)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] < 0xf5 {
			b[i]++
			return lower, string(b[:i+1])
		}
	}
	return lower, upper
}

// PipelinesOf returns the names of the CI pipelines that publish to repo id, sorted.
func (s *Service) PipelinesOf(ctx context.Context, repoID int64) ([]string, error) {
	rows, err := s.rq.ListRepoTrusts(ctx, repoID)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(rows))
	for _, t := range rows {
		names = append(names, t.Name)
	}
	slices.Sort(names)
	return names, nil
}

// AddPipelinesOf adds repo to to every CI pipeline that publishes to repo from, so each of them
// publishes to both, and records ci.pipeline.update for each pipeline that changed. It returns the
// names of the pipelines of from. A merge gives the pipelines of its source to its target.
func (s *Service) AddPipelinesOf(ctx context.Context, from, to *repos.Repo) ([]string, error) {
	var names []string
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		rows, err := q.ListRepoTrusts(ctx, from.ID)
		if err != nil {
			return err
		}
		names = names[:0]
		for _, t := range rows {
			names = append(names, t.Name)
		}
		if len(rows) == 0 {
			return nil
		}
		if _, err := q.CopyTrustRepos(ctx, dbq.CopyTrustReposParams{ToID: to.ID, FromID: from.ID}); err != nil {
			return err
		}
		for _, t := range rows {
			if err := s.audit.RecordTx(ctx, tx, audit.Event{Action: ActionPipelineUpdate, Target: t.Name,
				Detail: map[string]any{"id": t.ID, "added_repo": to.Name, "from_repo": from.Name}}); err != nil {
				return err
			}
		}
		return nil
	})
	slices.Sort(names)
	return names, err
}

// RepoPipelines returns the trust policies that list repo, each with what its claims allow in
// the words of its CI provider (the CI pipelines of the access report).
func (s *Service) RepoPipelines(ctx context.Context, repoID int64) ([]access.Pipeline, error) {
	rows, err := s.rq.ListRepoTrusts(ctx, repoID)
	if err != nil {
		return nil, err
	}
	out := make([]access.Pipeline, 0, len(rows))
	for _, t := range rows {
		claims, err := decodeClaims(t.Claims)
		if err != nil {
			claims = nil
		}
		out = append(out, access.Pipeline{ID: t.ID, Rule: t.Name, Issuer: t.Issuer, Disabled: t.Disabled != 0,
			Pipeline: s.ci.Describe(t.Issuer, claims)})
	}
	return out, nil
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

// trustKeys is what a check of a CI token reads of a trust policy.
type trustKeys struct {
	ID                             int64
	Issuer, Audience, Claims, Jwks string
}

// verifyCIToken checks the token's signature and issuer against one policy's keys, and its
// expiry unless anyAge.
func (s *Service) verifyCIToken(ctx context.Context, t trustKeys, raw string, anyAge bool) (*gooidc.IDToken, error) {
	cfg := &gooidc.Config{SkipClientIDCheck: true, SupportedSigningAlgs: signingAlgs, SkipExpiryCheck: anyAge}
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

// ciMatch is a CI identity token that a trust policy allows for a repo.
type ciMatch struct {
	trust  int64
	idt    *gooidc.IDToken
	claims map[string]any
	seen   [32]byte // the replay key: issuer and jti, or the whole token
}

// matchTrust checks raw against one policy t: signature, issuer, audience and claims, and with
// fresh also exp and an iat at most 10 min old. It returns the match, or why t refuses the token.
func (s *Service) matchTrust(ctx context.Context, t trustKeys, raw string, fresh bool) (*ciMatch, string) {
	tok, err := s.verifyCIToken(ctx, t, raw, !fresh)
	if err != nil {
		return nil, err.Error()
	}
	aud := t.Audience
	if aud == "" {
		aud = s.baseURL()
	}
	if aud == "" {
		return nil, noBaseURL
	}
	if !slices.Contains(tok.Audience, aud) {
		return nil, "the token's audience is not " + aud
	}
	if fresh {
		if why := staleReason(tok, time.Now()); why != "" {
			return nil, why
		}
	}
	var all map[string]any
	if err := tok.Claims(&all); err != nil {
		return nil, "the token's claims are unreadable"
	}
	policy, err := decodeClaims(t.Claims)
	if err != nil || len(policy) == 0 || !claimsMatch(policy, all) {
		return nil, "the token's claims do not match a trust policy"
	}
	m := &ciMatch{trust: t.ID, idt: tok, claims: all}
	if jti, _ := all["jti"].(string); jti != "" {
		m.seen = sha256.Sum256([]byte(tok.Issuer + "\n" + jti))
	} else {
		m.seen = sha256.Sum256([]byte(raw))
	}
	return m, ""
}

// staleReason returns why a verified token is too old to mint a new session ("" when it is
// fresh): its iat must be at most 10 min old and its exp ahead.
func staleReason(tok *gooidc.IDToken, now time.Time) string {
	if tok.IssuedAt.IsZero() || now.Sub(tok.IssuedAt) > maxTokenAge || tok.IssuedAt.After(now.Add(clockSkew)) {
		return "the token was not issued in the last 10 minutes"
	}
	if !tok.Expiry.IsZero() && !now.Before(tok.Expiry) {
		return "the token has expired"
	}
	return ""
}

// noBaseURL is the refusal reason of a policy without an audience while base_url is empty.
const noBaseURL = "the server has no public address to check the token's audience against"

// ciToken trims raw and checks its form; it returns the token and its unverified issuer.
func ciToken(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxCITokenBytes {
		return "", "", errorf(ErrInvalid, "token must be a CI identity token (JWT)")
	}
	issuer, ok := unverifiedIssuer(raw)
	if !ok {
		return "", "", errorf(ErrDenied, "token is not a valid identity token")
	}
	return raw, issuer, nil
}

// verifyCI checks raw against the enabled policies of its issuer that list the repo that resolve
// returns (matchTrust; fresh also checks exp and the age of iat). An unknown repo is refused like
// a repo that no policy lists.
func (s *Service) verifyCI(ctx context.Context, raw string, repoName string, resolve func(context.Context) (*repos.Repo, error), fresh bool) (*repos.Repo, *ciMatch, error) {
	raw, issuer, err := ciToken(raw)
	if err != nil {
		return nil, nil, err
	}
	fix := ". An administrator adds or changes the CI pipeline on Settings › CI pipelines."
	refused := errorf(ErrDenied, "no trust policy allows this token to publish to "+repoName+fix)
	repo, err := resolve(ctx)
	if errors.Is(err, repos.ErrNotFound) {
		return nil, nil, refused
	}
	if err != nil {
		return nil, nil, err
	}
	rows, err := s.rq.ListMatchingTrusts(ctx, dbq.ListMatchingTrustsParams{Issuer: issuer, RepoID: repo.ID})
	if err != nil {
		return nil, nil, err
	}
	reason := ""
	for _, t := range rows {
		m, why := s.matchTrust(ctx, trustKeys{t.ID, t.Issuer, t.Audience, t.Claims, t.Jwks}, raw, fresh)
		if m != nil {
			return repo, m, nil
		}
		reason = why
	}
	return nil, nil, ciRefusal("no trust policy allows this token to publish to "+repoName, reason, refused)
}

// ciRefusal is the refusal of a CI token with the reason of the last policy that refused it.
func ciRefusal(msg, reason string, none error) error {
	fix := ". An administrator adds or changes the CI pipeline on Settings › CI pipelines."
	if reason == noBaseURL {
		fix = ". An administrator sets the public address on Settings › General, or an audience on the CI pipeline on Settings › CI pipelines."
	}
	if reason == "" {
		return none
	}
	return errorf(ErrDenied, msg+": "+reason+fix)
}

// checkCI verifies a CI token without a repo (Minter.Check): an enabled policy of its issuer
// allows it (signature, issuer, audience, claims, exp, the age of iat). It records no use.
func (s *Service) checkCI(ctx context.Context, raw string) error {
	raw, issuer, err := ciToken(raw)
	if err != nil {
		return err
	}
	rows, err := s.rq.ListIssuerTrusts(ctx, issuer)
	if err != nil {
		return err
	}
	reason := ""
	for _, t := range rows {
		m, why := s.matchTrust(ctx, trustKeys{t.ID, t.Issuer, t.Audience, t.Claims, t.Jwks}, raw, true)
		if m != nil {
			return nil
		}
		reason = why
	}
	return ciRefusal("no trust policy allows this token", reason,
		errorf(ErrDenied, "no trust policy allows this token. An administrator adds the CI pipeline on Settings › CI pipelines."))
}

// ciSession mints the session of a CI identity token for repo: the token must pass a trust
// policy (verifyCI) and access.Engine.Decide with Mint. Without reuse (the mint route) a token
// mints once, for one repo. With reuse (the Minter) a token mints once for each repo, and a token
// that minted a session for the repo before gets that session back while it is open
// (Minted.Token is then ""): that check skips exp and the age of iat, because the open session
// bounds the time. A first mint needs a fresh token. revision is the commit that the job names
// (MintCred.Revision).
func (s *Service) ciSession(ctx context.Context, r *http.Request, raw, revision, repoName string, resolve func(context.Context) (*repos.Repo, error), reuse bool) (*Minted, error) {
	repo, m, err := s.verifyCI(ctx, raw, repoName, resolve, !reuse)
	if err != nil {
		return nil, err
	}
	p := &auth.Principal{Cred: auth.CredPublish, Publish: &auth.PublishSession{RepoID: repo.ID, Via: ViaOIDC, Subject: m.idt.Subject}}
	if d := s.access.Decide(ctx, r, p, repo, access.Mint, perm.RepoPublish); !d.Allowed {
		return nil, &RefusedError{d}
	}
	if reuse {
		if old, err := s.reused(ctx, m.seen, repo); old != nil || err != nil {
			return old, err
		}
		if why := staleReason(m.idt, time.Now()); why != "" {
			return nil, errorf(ErrDenied, "no trust policy allows this token to publish to "+repoName+": "+why)
		}
	}
	var out *Minted
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		if !reuse {
			seen, err := q.SeenOIDCToken(ctx, m.seen[:])
			if err != nil {
				return err
			}
			if seen {
				return errorf(ErrDenied, "this identity token was already used")
			}
		}
		// The build record and the session share the transaction.
		build, _, err := s.builds.Record(ctx, tx, m.idt.Issuer, m.claims, revision, time.Now())
		if err != nil {
			return err
		}
		out, err = s.mint(ctx, q, repo, ViaOIDC, 0, 0, m.trust, build, m.idt.Subject)
		if err != nil {
			return err
		}
		n, err := q.InsertSeenOIDCToken(ctx, dbq.InsertSeenOIDCTokenParams{IDHash: m.seen[:], RepoID: repo.ID,
			ExpiresAt: m.idt.Expiry.Unix(), SessionID: sql.NullInt64{Int64: out.ID, Valid: true}})
		if err != nil {
			return err
		}
		if n == 0 {
			return errorf(ErrDenied, "this identity token was already used")
		}
		return nil
	})
	if err != nil {
		if reuse && errors.Is(err, ErrDenied) {
			if old, rerr := s.reused(ctx, m.seen, repo); old != nil || rerr != nil {
				return old, rerr
			}
		}
		return nil, err
	}
	return out, nil
}

// reused returns the open session of repo that the CI token with replay key seen minted, or nil.
func (s *Service) reused(ctx context.Context, seen [32]byte, repo *repos.Repo) (*Minted, error) {
	id, err := s.rq.GetSeenOIDCSession(ctx, dbq.GetSeenOIDCSessionParams{IDHash: seen[:], RepoID: repo.ID})
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !id.Valid) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p, err := s.LoadSession(ctx, id.Int64)
	if errors.Is(err, auth.ErrBadCredentials) {
		return nil, errorf(ErrDenied, "the publish session of this identity token ended; request a new token")
	}
	if err != nil {
		return nil, err
	}
	return &Minted{ID: p.Publish.ID, Repo: repo.Name, Format: repo.Format, Kind: repo.Kind, RepoID: repo.ID,
		Subject: p.Publish.Subject, Via: ViaOIDC, Reused: true, Session: p.Publish}, nil
}

// trustCountCap is where the count of every pipeline stops (the Settings overview: "10,000+").
const trustCountCap = 10000

// CountTrusts counts the pipelines of a scope: every one (all, up to trustCountCap; more is true
// past it), or those whose every repo is among ids.
func (s *Service) CountTrusts(ctx context.Context, all bool, ids []int64) (n int64, more bool, err error) {
	if all {
		n, err = s.rq.CountTrustsUpTo(ctx, trustCountCap+1)
		return min(n, trustCountCap), n > trustCountCap, err
	}
	if len(ids) == 0 {
		return 0, false, nil
	}
	n, err = s.rq.CountTrustsWithin(ctx, ids)
	return n, false, err
}
