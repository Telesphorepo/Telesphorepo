package netrules

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Rule scopes.
const (
	ScopeRead    = "read"
	ScopePublish = "publish"
)

// Rule kinds.
const (
	RuleCIDR = "cidr" // Value is a CIDR
	RuleList = "list" // Value is the name of an IP list
)

const (
	maxRulesPerScope = 200
	maxNoteChars     = 200
)

// ErrInvalid is a rule set that does not validate.
var ErrInvalid = errors.New("invalid IP rules")

// Rule is one entry of an allow-list: a CIDR or the name of an IP list.
type Rule struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
	Note  string `json:"note"`
}

// Rules are the two allow-lists of a repo. An empty list leaves its scope unrestricted.
type Rules struct {
	Read    []Rule `json:"read"`
	Publish []Rule `json:"publish"`
}

// repoRules is the in-memory copy of a repo's rules with the CIDRs parsed.
type repoRules struct {
	rules         Rules
	read, publish matcher
}

// matcher is one compiled allow-list.
type matcher struct {
	prefixes []netip.Prefix
	lists    []string
	empty    bool
}

func compile(list []Rule) matcher {
	m := matcher{empty: len(list) == 0}
	for _, r := range list {
		switch r.Kind {
		case RuleCIDR:
			if p, err := netip.ParsePrefix(r.Value); err == nil {
				m.prefixes = append(m.prefixes, p)
			}
		case RuleList:
			m.lists = append(m.lists, r.Value)
		}
	}
	return m
}

func newRepoRules(r Rules) *repoRules {
	return &repoRules{rules: r, read: compile(r.Read), publish: compile(r.Publish)}
}

// DeniedError is a request from an address that a repo's rules do not allow.
type DeniedError struct {
	Scope string
	Repo  *repos.Repo
	IP    netip.Addr
	Actor audit.Actor
}

func (e *DeniedError) Error() string {
	return "your address is not allowed to " + e.Scope + " this repository"
}

func (s *Service) loadRules(ctx context.Context) error {
	s.rulesMu.Lock()
	defer s.rulesMu.Unlock()
	return s.reloadRulesLocked(ctx)
}

// reloadRulesLocked reads every repo's rules into memory. Caller holds rulesMu.
func (s *Service) reloadRulesLocked(ctx context.Context) error {
	m := map[int64]*repoRules{}
	var after int64
	for {
		id, err := s.rq.NextIPRuleRepo(ctx, after)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return fmt.Errorf("load IP rules: %w", err)
		}
		rr, err := s.readRules(ctx, s.rq, id)
		if err != nil {
			return err
		}
		m[id] = rr
		after = id
	}
	s.rules = m
	return nil
}

func (s *Service) readRules(ctx context.Context, q *dbq.Queries, repoID int64) (*repoRules, error) {
	rows, err := q.ListRepoIPRules(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("load IP rules of repo %d: %w", repoID, err)
	}
	rules := Rules{Read: []Rule{}, Publish: []Rule{}}
	for _, r := range rows {
		x := Rule{Kind: r.Kind, Value: r.Value, Note: r.Note}
		if r.Scope == ScopeRead {
			rules.Read = append(rules.Read, x)
		} else {
			rules.Publish = append(rules.Publish, x)
		}
	}
	return newRepoRules(rules), nil
}

// RepoRules returns the rules of a repo (empty lists when it has none).
func (s *Service) RepoRules(repoID int64) Rules {
	s.rulesMu.RLock()
	rr := s.rules[repoID]
	s.rulesMu.RUnlock()
	if rr == nil {
		return Rules{Read: []Rule{}, Publish: []Rule{}}
	}
	return rr.rules
}

// normalize validates one scope's list and returns it in canonical form: a CIDR is masked, an
// address becomes a /32 or /128, a list name is lower-cased and must exist.
func (s *Service) normalize(scope string, in []Rule) ([]Rule, error) {
	if len(in) > maxRulesPerScope {
		return nil, fmt.Errorf("%w: at most %d %s rules", ErrInvalid, maxRulesPerScope, scope)
	}
	out := make([]Rule, 0, len(in))
	seen := map[string]bool{}
	for _, r := range in {
		v := strings.TrimSpace(r.Value)
		switch r.Kind {
		case RuleCIDR:
			if p, err := netip.ParsePrefix(v); err == nil {
				v = p.Masked().String()
			} else if a, err := netip.ParseAddr(v); err == nil {
				a = a.Unmap()
				v = netip.PrefixFrom(a, a.BitLen()).String()
			} else {
				return nil, fmt.Errorf("%w: %s rule %q is not a CIDR (e.g. 203.0.113.0/24)", ErrInvalid, scope, clipText(v, 64))
			}
		case RuleList:
			v = strings.ToLower(v)
			if !s.Exists(v) {
				return nil, fmt.Errorf("%w: %s rule: there is no IP list %q", ErrInvalid, scope, clipText(v, 64))
			}
		default:
			return nil, fmt.Errorf("%w: %s rule kind must be %q or %q", ErrInvalid, scope, RuleCIDR, RuleList)
		}
		note := strings.TrimSpace(r.Note)
		if !utf8.ValidString(note) || utf8.RuneCountInString(note) > maxNoteChars {
			return nil, fmt.Errorf("%w: a note has at most %d characters", ErrInvalid, maxNoteChars)
		}
		for _, c := range note {
			if unicode.IsControl(c) {
				return nil, fmt.Errorf("%w: a note must not contain control characters", ErrInvalid)
			}
		}
		key := r.Kind + "\x00" + v
		if seen[key] {
			return nil, fmt.Errorf("%w: %s rule %s is listed twice", ErrInvalid, scope, v)
		}
		seen[key] = true
		out = append(out, Rule{Kind: r.Kind, Value: v, Note: note})
	}
	return out, nil
}

// SetRules validates in and replaces every rule of repoID with it. It returns the stored
// rules. A validation error wraps ErrInvalid.
func (s *Service) SetRules(ctx context.Context, repoID int64, in Rules) (Rules, error) {
	// The list names are checked under rulesMu, which a list delete or rename also holds.
	s.rulesMu.Lock()
	defer s.rulesMu.Unlock()
	read, err := s.normalize(ScopeRead, in.Read)
	if err != nil {
		return Rules{}, err
	}
	pub, err := s.normalize(ScopePublish, in.Publish)
	if err != nil {
		return Rules{}, err
	}
	err = s.d.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		if err := q.DeleteRepoIPRules(ctx, repoID); err != nil {
			return err
		}
		for _, l := range []struct {
			scope string
			rules []Rule
		}{{ScopeRead, read}, {ScopePublish, pub}} {
			for _, r := range l.rules {
				if err := q.InsertRepoIPRule(ctx, dbq.InsertRepoIPRuleParams{RepoID: repoID, Scope: l.scope, Kind: r.Kind, Value: r.Value, Note: r.Note}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return Rules{}, err
	}
	out := Rules{Read: read, Publish: pub}
	if len(read) == 0 && len(pub) == 0 {
		delete(s.rules, repoID)
	} else {
		s.rules[repoID] = newRepoRules(out)
	}
	return out, nil
}

// Forget drops the in-memory rules of a deleted repo (its rows cascade with the repo).
func (s *Service) Forget(repoID int64) {
	s.rulesMu.Lock()
	delete(s.rules, repoID)
	s.rulesMu.Unlock()
}

// allows reports whether m lets a through: an empty list lets every address through.
func (s *Service) allows(m matcher, a netip.Addr) bool {
	if m.empty {
		return true
	}
	if !a.IsValid() {
		return false
	}
	a = a.Unmap()
	if contains(m.prefixes, a) {
		return true
	}
	for _, l := range m.lists {
		if contains(s.listRanges(l), a) {
			return true
		}
	}
	return false
}

// allowed reports whether ip may do scope on repoID.
func (s *Service) allowed(repoID int64, scope string, ip netip.Addr) bool {
	s.rulesMu.RLock()
	rr := s.rules[repoID]
	s.rulesMu.RUnlock()
	if rr == nil {
		return true
	}
	if scope == ScopePublish {
		return s.allows(rr.publish, ip)
	}
	return s.allows(rr.read, ip)
}

// Check returns a *DeniedError when the rules of repo do not allow ip for scope, else nil. It
// applies to every caller, admins included.
func (s *Service) Check(repo *repos.Repo, scope string, ip netip.Addr, actor audit.Actor) *DeniedError {
	if s.allowed(repo.ID, scope, ip) {
		return nil
	}
	return &DeniedError{Scope: scope, Repo: repo, IP: ip, Actor: actor}
}

// CheckRequest is Check for the client address of r.
func (s *Service) CheckRequest(r *http.Request, repo *repos.Repo, scope string, actor audit.Actor) *DeniedError {
	return s.Check(repo, scope, httpx.ClientIP(r), actor)
}

// Refuse answers 403 {"error": "your address is not allowed to <scope> this repository"} and
// records the audit event ip.denied with the client address.
func (s *Service) Refuse(w http.ResponseWriter, r *http.Request, e *DeniedError) {
	s.RecordDenied(r, e)
	httpx.Error(w, http.StatusForbidden, e.Error())
}

// RecordDenied records the audit event ip.denied for e.
func (s *Service) RecordDenied(r *http.Request, e *DeniedError) {
	s.audit.Record(r.Context(), r, audit.Event{
		Actor: e.Actor, Action: audit.IPDenied, RepoID: e.Repo.ID, Repo: e.Repo.Name,
		Target: e.Repo.Name, Detail: map[string]any{"scope": e.Scope},
	})
}

// DeniedRepos returns the repos whose read rules do not allow ip (search leaves them out).
func (s *Service) DeniedRepos(ip netip.Addr) map[int64]bool {
	s.rulesMu.RLock()
	defer s.rulesMu.RUnlock()
	var out map[int64]bool
	for id, rr := range s.rules {
		if !s.allows(rr.read, ip) {
			if out == nil {
				out = map[int64]bool{}
			}
			out[id] = true
		}
	}
	return out
}
