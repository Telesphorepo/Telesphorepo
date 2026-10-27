package edge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/settings"
	"github.com/Telesphorepo/Telesphorepo/internal/storage"
)

// The check of the Cloudflare API token (edge.cloudflare_token): which permissions of the deploy
// and of the pointer cache the token holds, before a deploy needs them. The exact check reads the
// token's own policies (GET /accounts/{account}/tokens/{id} for an account token, GET
// /user/tokens/{id} for a user token) and evaluates them as Cloudflare does: a deny comes before
// an allow, and a policy counts only on the resources it names. It matches permission groups by
// ID, since Cloudflare calls their names cosmetic. When the token cannot read its own details, or
// its policies hold no group that this server knows for a permission, harmless reads (probes) show
// what they can, and a read never proves Edit. Cache: Purge is always probed with a purge of an
// unused tag, which proves it.
//
// The check runs on a save of a new token (the Test of the settings group), at start and then
// every hour (after 10 minutes when it could not finish), when the token, the account or the route
// hosts change, and at the start of each deploy. Its last result lives in memory.

const (
	tokenRecheck = time.Hour
	tokenRetry   = 10 * time.Minute
)

// The resource keys of Cloudflare's token policies.
const (
	resAccount = "com.cloudflare.api.account."
	resZone    = "com.cloudflare.api.account.zone."
)

// The permission that the exact check needs, as Cloudflare's token forms name it.
const (
	readPermAccount = "Account & Billing › Account API Tokens: Read"
	readPermUser    = "User › API Tokens: Read"
)

// The states of a permission.
const (
	permPresent  = "present"
	permMissing  = "missing"
	permUnproven = "unproven"
)

// tokenPerm is a permission that the deploy or the pointer cache needs.
type tokenPerm struct {
	key  string
	name string // as Cloudflare's token form names it: category › name: level
	zone bool   // needed on the zone of every route host, else on the account
	opt  bool   // only the pointer cache needs it
	// groups are the permission groups that grant it: ID -> Cloudflare's API name.
	groups map[string]string
}

var tokenPerms = []tokenPerm{
	// Workers Admin is the role the form offers; Workers Scripts Write is the permission it replaces.
	{key: "workers", name: "Developer Platform › Workers: Admin", groups: map[string]string{
		"98d78cd2433d4c3687191bc0244ef948": "Workers Admin", "e086da7e2179491d91ee5f35b3ca210a": "Workers Scripts Write"}},
	{key: "r2", name: "Developer Platform › Workers R2 Storage: Edit", groups: map[string]string{"bf7481a1826f439697cb59a20b22293e": "Workers R2 Storage Write"}},
	{key: "queues", name: "Developer Platform › Queues: Edit", groups: map[string]string{"366f57075ffc42689627bcf8242a1b6d": "Queues Write"}},
	{key: "routes", name: "Developer Platform › Workers Routes: Edit", zone: true, groups: map[string]string{"28f4b596e7d643029c524985477ae49a": "Workers Routes Write"}},
	{key: "zone", name: "DNS & Zones › Zone: Read", zone: true, groups: map[string]string{"c8fed203ed3043cba015a93ad1616f1f": "Zone Read"}},
	{key: "purge", name: "Cache & Performance › Cache: Purge", zone: true, opt: true, groups: map[string]string{"e17beae8b8cb423a99b1730f21238bed": "Cache Purge"}},
}

// stepPerms are the permissions of tokenPerms that each step of a deploy needs.
var stepPerms = map[string][]string{
	stepZones: {"zone"}, stepBuckets: {"r2"}, stepNotification: {"r2"}, stepQueue: {"queues"}, stepConsumer: {"queues"},
	stepUpload: {"workers"}, stepTriggers: {"workers"}, stepRoutes: {"routes"},
}

// permJSON is one permission of the token check.
type permJSON struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Optional bool   `json:"optional"`
	State    string `json:"state"` // present, missing or unproven
	Why      string `json:"why"`
}

// tokenJSON is token of GET /api/edge/status: the last check of the token (null without a token).
type tokenJSON struct {
	CheckedAt *time.Time `json:"checked_at"` // null before the first check of this token
	Running   bool       `json:"running"`
	Kind      string     `json:"kind"`  // account or user; "" when Cloudflare took the token as neither
	Exact     bool       `json:"exact"` // the token's policies were read
	// ReadMissing is the permission that the exact check lacks ("" when it ran, or failed otherwise);
	// ExactWhy says why the exact check did not run.
	ReadMissing string     `json:"read_missing"`
	ExactWhy    string     `json:"exact_why"`
	Problem     string     `json:"problem"` // the token itself fails: invalid, disabled, expired, or no answer
	ExpiresOn   *time.Time `json:"expires_on"`
	Permissions []permJSON `json:"permissions"`
}

// tokenGate is the last check of the token.
type tokenGate struct {
	mu      sync.Mutex
	key     string // the token, the account and the route hosts it was checked for
	res     *tokenJSON
	done    bool // the check finished: Cloudflare answered
	at      time.Time
	running bool
}

// tokenKey is the key of a check of token in account for hosts.
func tokenKey(token, account string, hosts []string) string {
	return hashOf([]byte(token + "\x00" + account + "\x00" + strings.Join(hosts, "\n")))
}

// tokenInput is what a check needs from the settings.
type tokenInput struct {
	token, account, jurisdiction string
	hosts                        []string
	key                          string
}

func (m *mod) tokenInputOf(s *settings.Snapshot) tokenInput {
	in := tokenInput{token: CloudflareToken.Get(s), hosts: m.routeHosts(s)}
	in.account, in.jurisdiction, _ = r2Account(storage.S3Endpoint.Get(s))
	in.key = tokenKey(in.token, in.account, in.hosts)
	return in
}

// checkToken starts the check of the token when it is due.
func (m *mod) checkToken(ctx context.Context) {
	in := m.tokenInputOf(m.h.Settings.Current())
	if in.token == "" || in.account == "" {
		return
	}
	g := &m.tok
	g.mu.Lock()
	every := tokenRetry
	if g.done {
		every = tokenRecheck
	}
	due := !g.running && (g.key != in.key || time.Since(g.at) >= every)
	if due {
		g.running = true
	}
	g.mu.Unlock()
	if !due {
		return
	}
	m.h.Go("edge.token-check", func(ctx context.Context) {
		res, done := m.tokenCheck(ctx, in)
		g.mu.Lock()
		g.running = false
		g.mu.Unlock()
		if ctx.Err() == nil {
			m.keepToken(in.key, res, done)
		}
	})
}

// keepToken keeps res as the last check of the token key.
func (m *mod) keepToken(key string, res *tokenJSON, done bool) {
	g := &m.tok
	g.mu.Lock()
	g.key, g.res, g.done, g.at = key, res, done, time.Now()
	g.mu.Unlock()
}

// tokenNow runs the check for the settings s now and keeps its result: the start of a deploy.
func (m *mod) tokenNow(ctx context.Context, s *settings.Snapshot) *tokenJSON {
	in := m.tokenInputOf(s)
	if in.token == "" || in.account == "" {
		return nil
	}
	res, done := m.tokenCheck(ctx, in)
	if ctx.Err() == nil {
		m.keepToken(in.key, res, done)
	}
	return res
}

// tokenStatus is token of GET /api/edge/status for the settings s: nil without a token or an R2
// account.
func (m *mod) tokenStatus(s *settings.Snapshot) *tokenJSON {
	in := m.tokenInputOf(s)
	if in.token == "" || in.account == "" {
		return nil
	}
	g := &m.tok
	g.mu.Lock()
	defer g.mu.Unlock()
	out := tokenJSON{Running: g.running, Permissions: []permJSON{}}
	if g.res != nil && g.key == in.key {
		out = *g.res
		out.Running = g.running
	}
	return &out
}

// tokenMissing returns the names of the permissions that step needs and that res found missing.
func tokenMissing(res *tokenJSON, step string) []string {
	if res == nil {
		return nil
	}
	var out []string
	for _, p := range res.Permissions {
		for _, k := range stepPerms[step] {
			if p.Key == k && p.State == permMissing {
				out = append(out, p.Name+" ("+strings.TrimSuffix(p.Why, ".")+")")
			}
		}
	}
	return out
}

// refused reports whether err is Cloudflare's refusal of a permission: 403 or error 10000.
func refused(err error) bool {
	ce := asCall(err)
	return ce != nil && (ce.Status == http.StatusForbidden || ce.has(10000))
}

// cfPolicy is a policy of a token's details.
type cfPolicy struct {
	Effect           string                     `json:"effect"`
	Resources        map[string]json.RawMessage `json:"resources"`
	PermissionGroups []struct {
		ID string `json:"id"`
	} `json:"permission_groups"`
}

// cfTokenDetails is the answer of GET /accounts/{account}/tokens/{id} and GET /user/tokens/{id}.
type cfTokenDetails struct {
	Status    string     `json:"status"`
	ExpiresOn string     `json:"expires_on"`
	NotBefore string     `json:"not_before"`
	Policies  []cfPolicy `json:"policies"`
}

// policyCovers reports whether res names the account (zone "") or the zone of the account: the
// account itself or every account; the zone itself or every zone, flat or nested under the
// account.
func policyCovers(res map[string]json.RawMessage, account, zone string) bool {
	for k, v := range res {
		star := strings.TrimSpace(string(v)) == `"*"`
		isAccount := k == resAccount+account || k == resAccount+"*"
		if zone == "" {
			if isAccount && star {
				return true
			}
			continue
		}
		if (k == resZone+zone || k == resZone+"*") && star {
			return true
		}
		var inner map[string]json.RawMessage
		if isAccount && json.Unmarshal(v, &inner) == nil && policyCovers(inner, account, zone) {
			return true
		}
	}
	return false
}

// evalPolicies returns the state of p on the account (zone "") or a zone, by the policies: a deny
// that covers it removes every allow.
func evalPolicies(pols []cfPolicy, p *tokenPerm, account, zone, where string) (string, string) {
	allowed := false
	for _, pol := range pols {
		has := false
		for _, g := range pol.PermissionGroups {
			if _, ok := p.groups[g.ID]; ok {
				has = true
			}
		}
		if !has || !policyCovers(pol.Resources, account, zone) {
			continue
		}
		if pol.Effect == "deny" {
			return permMissing, "A deny policy of the token removes it on " + where + "."
		}
		allowed = true
	}
	if allowed {
		return permPresent, "A policy of the token grants it on " + where + "."
	}
	return permMissing, "No policy of the token grants it on " + where + "."
}

// hostZone is the zone of a route host, or why it is not known.
type hostZone struct {
	host string
	zone zoneRef
	err  error  // the lookup failed
	none string // no zone of the account holds the host
}

// tokenCheck checks the token of in. done: Cloudflare answered every call that the verdict rests on.
func (m *mod) tokenCheck(ctx context.Context, in tokenInput) (*tokenJSON, bool) {
	c := &cfClient{base: cfAPI, token: in.token}
	out := &tokenJSON{Permissions: []permJSON{}}
	now := time.Now().UTC()
	defer func() { out.CheckedAt = &now }()
	acct := "/accounts/" + seg(in.account)

	// The verify answers the token's ID: as an account token, else as a user token.
	var ver struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	_, aerr := c.get(ctx, acct+"/tokens/verify", nil, nil, &ver)
	out.Kind = "account"
	if aerr != nil {
		_, uerr := c.get(ctx, "/user/tokens/verify", nil, nil, &ver)
		out.Kind = "user"
		if uerr != nil {
			out.Kind = ""
			ace, uce := asCall(aerr), asCall(uerr)
			if ace == nil || uce == nil || ace.Transport != "" || uce.Transport != "" || ace.Status >= 500 || uce.Status >= 500 {
				out.Problem = "The check of the token got no answer from Cloudflare: " + c.redact(uerr.Error()) + "."
				return out, false
			}
			out.Problem = fmt.Sprintf("Cloudflare refused the token as a token of the account %s and as a user token (%s). Set an account API token of the account of the R2 buckets, or a user API token that reaches it.", in.account, c.redact(uce.Error()))
			return out, true
		}
	}
	if ver.Status != "active" {
		out.Problem = fmt.Sprintf("The token is %s in Cloudflare. Set an active API token.", ver.Status)
		return out, true
	}

	// The exact check: the token's own policies.
	var det cfTokenDetails
	detPath := acct + "/tokens/" + seg(ver.ID)
	readPerm := readPermAccount
	if out.Kind == "user" {
		detPath, readPerm = "/user/tokens/"+seg(ver.ID), readPermUser
	}
	if _, err := c.get(ctx, detPath, nil, nil, &det); err == nil {
		out.Exact = true
	} else if refused(err) || asCall(err) != nil && asCall(err).Status == http.StatusUnauthorized {
		out.ReadMissing = readPerm
		out.ExactWhy = "The token cannot read its own permissions (" + c.redact(err.Error()) + "), so the server checks what harmless reads can show."
	} else {
		out.ExactWhy = "Reading the token's permissions failed (" + c.redact(err.Error()) + "), so the server checks what harmless reads can show."
	}
	if out.Exact {
		if t, err := time.Parse(time.RFC3339, det.ExpiresOn); err == nil {
			t = t.UTC()
			out.ExpiresOn = &t
			if !t.After(now) {
				out.Problem = "The token expired " + t.Format("2006-01-02 15:04 UTC") + ". Set a new API token."
				return out, true
			}
		}
		if t, err := time.Parse(time.RFC3339, det.NotBefore); err == nil && t.After(now) {
			out.Problem = "The token is valid only from " + t.UTC().Format("2006-01-02 15:04 UTC") + "."
			return out, true
		}
		if det.Status != "" && det.Status != "active" {
			out.Problem = fmt.Sprintf("The token is %s in Cloudflare. Set an active API token.", det.Status)
			return out, true
		}
	}

	// The zone of each route host, as the deploy finds it.
	r := &deployRun{m: m, p: &deployPlan{Account: in.account}, c: c}
	var zones []hostZone
	for _, h := range in.hosts {
		hz := hostZone{host: h}
		for _, name := range zoneNames(h) {
			z, err := r.lookupZone(ctx, name, in.account)
			if err != nil {
				hz.err = err
				break
			}
			if z != nil {
				hz.zone = zoneRef{ID: z.ID, Name: z.Name}
				break
			}
		}
		if hz.err == nil && hz.zone.ID == "" {
			hz.none = "no zone of the account " + in.account + " that the token can see holds " + h
		}
		zones = append(zones, hz)
	}

	done := true
	for i := range tokenPerms {
		p := &tokenPerms[i]
		res := permJSON{Key: p.key, Name: p.name, Optional: p.opt}
		exact := ""
		if out.Exact {
			exact, res.Why = m.exactState(&det, p, in.account, zones)
		}
		if exact == permPresent && p.key != "purge" {
			res.State = exact
			out.Permissions = append(out.Permissions, res)
			continue
		}
		// Without the exact check, when it finds the permission missing (a group this server does
		// not know may grant it), and always for the purge, the reads show what they can.
		probe, why, ok := m.probePerm(ctx, c, in, p, zones)
		done = done && ok
		switch {
		case exact == "":
			res.State, res.Why = probe, why
		case p.key == "purge" && probe != permUnproven:
			// A purge proves the permission, a refused one its absence.
			res.State, res.Why = probe, why
		case exact == permMissing && probe == permPresent:
			res.State, res.Why = permPresent, why
		case exact == permMissing && probe == permUnproven && strings.HasPrefix(why, "A read of"):
			res.State, res.Why = permUnproven, res.Why+" "+why
		default:
			res.State = exact
		}
		out.Permissions = append(out.Permissions, res)
	}
	return out, done
}

// exactState returns the state of p by the token's policies: on the account, or on the zone of
// every route host.
func (m *mod) exactState(det *cfTokenDetails, p *tokenPerm, account string, zones []hostZone) (string, string) {
	if !p.zone {
		return evalPolicies(det.Policies, p, account, "", "the account")
	}
	if len(zones) == 0 {
		return permUnproven, "The base URL names no route host yet."
	}
	var unknown []string
	for _, hz := range zones {
		if hz.zone.ID == "" {
			unknown = append(unknown, hz.host)
			continue
		}
		if st, why := evalPolicies(det.Policies, p, account, hz.zone.ID, "the zone "+hz.zone.Name); st != permPresent {
			return st, why
		}
	}
	if len(unknown) > 0 {
		// A policy of every zone covers a host whose zone the token cannot see.
		if st, why := evalPolicies(det.Policies, p, account, "*", "every zone"); st == permPresent {
			return st, why
		}
		return permUnproven, "The zone of " + strings.Join(unknown, ", ") + " is not known."
	}
	return permPresent, "A policy of the token grants it on the zone of every route host."
}

// probePerm returns the state of p by harmless reads (a purge of an unused tag for the purge). A
// read that went through shows read access only. ok: no read got no answer.
func (m *mod) probePerm(ctx context.Context, c *cfClient, in tokenInput, p *tokenPerm, zones []hostZone) (string, string, bool) {
	answered := func(err error) bool {
		ce := asCall(err)
		return ce != nil && ce.Transport == "" && ce.Status < 500
	}
	read := func(path string, hdr map[string]string, what string) (string, string, bool) {
		_, err := c.get(ctx, path, nil, hdr, nil)
		switch {
		case err == nil:
			return permUnproven, "A read of " + what + " went through: read access seen, Edit not provable.", true
		case refused(err):
			return permMissing, "Cloudflare refused a read of " + what + " (" + c.redact(err.Error()) + ").", true
		}
		return permUnproven, "The read of " + what + " failed: " + c.redact(err.Error()) + ".", answered(err)
	}
	acct := "/accounts/" + seg(in.account)
	switch p.key {
	case "workers":
		return read(acct+"/workers/scripts", nil, "the Workers scripts")
	case "r2":
		var hdr map[string]string
		if in.jurisdiction != "" {
			hdr = map[string]string{"cf-r2-jurisdiction": in.jurisdiction}
		}
		return read(acct+"/r2/buckets", hdr, "the R2 buckets")
	case "queues":
		return read(acct+"/queues", nil, "the queues")
	}
	if len(zones) == 0 {
		return permUnproven, "The base URL names no route host yet.", true
	}
	var found []zoneRef
	for _, hz := range zones {
		switch {
		case hz.err != nil && refused(hz.err) && p.key == "zone":
			return permMissing, "Cloudflare refused the lookup of the zone of " + hz.host + " (" + c.redact(hz.err.Error()) + ").", true
		case hz.err != nil:
			return permUnproven, "The lookup of the zone of " + hz.host + " failed: " + c.redact(hz.err.Error()) + ".", answered(hz.err)
		case hz.none != "" && p.key == "zone":
			return permUnproven, "Cloudflare found " + hz.none + ": the zone is in another account, or the token lacks this permission on it.", true
		case hz.none != "":
			return permUnproven, "The zone of " + hz.host + " is not known: " + hz.none + ".", true
		}
		if !slices.Contains(found, hz.zone) {
			found = append(found, hz.zone)
		}
	}
	switch p.key {
	case "zone":
		return permPresent, "The deploy's lookup of the zone of every route host went through.", true
	case "routes":
		state, why, ok := permUnproven, "", true
		for _, z := range found {
			if state, why, ok = read("/zones/"+seg(z.ID)+"/workers/routes", nil, "the Workers routes of the zone "+z.Name); state == permMissing || !ok {
				return state, why, ok
			}
		}
		return state, why, ok
	case "purge":
		for _, z := range found {
			if err := purgeTags(ctx, c, z, []string{"tp-probe/" + randomHex()}); err != nil {
				if refused(err) {
					return permMissing, "Cloudflare refused a purge by tag on the zone " + z.Name + " (" + c.redact(err.Error()) + ").", true
				}
				return permUnproven, "A purge by tag on the zone " + z.Name + " failed: " + c.redact(err.Error()) + ".", answered(err)
			}
		}
		return permPresent, "A purge of an unused tag went through on the zone of every route host.", true
	}
	return permUnproven, "", true
}
