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
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/netrules/dbq"
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
	RuleList = "list" // Value is the name of an IP list; the rule keeps the list's id
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

// rule is one stored rule: a CIDR, or the id of an IP list.
type rule struct {
	cidr   string
	listID int64
	note   string
}

// repoRules is the in-memory copy of a repo's rules with the CIDRs parsed.
type repoRules struct {
	read, publish []rule
	readM, pubM   matcher
}

// matcher is one compiled allow-list.
type matcher struct {
	prefixes []netip.Prefix
	lists    []int64
	empty    bool
}

func compile(list []rule) matcher {
	m := matcher{empty: len(list) == 0}
	for _, r := range list {
		if r.listID != 0 {
			m.lists = append(m.lists, r.listID)
		} else if p, err := netip.ParsePrefix(r.cidr); err == nil {
			m.prefixes = append(m.prefixes, p)
		}
	}
	return m
}

func newRepoRules(read, publish []rule) *repoRules {
	return &repoRules{read: read, publish: publish, readM: compile(read), pubM: compile(publish)}
}

// DeniedError is a request from an address that a repo's rules do not allow.
type DeniedError struct {
	Scope string
	Repo  *repos.Repo
	IP    netip.Addr
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
	var read, publish []rule
	for _, r := range rows {
		x := rule{cidr: r.Cidr.String, listID: r.ListID.Int64, note: r.Note}
		if r.Scope == ScopeRead {
			read = append(read, x)
		} else {
			publish = append(publish, x)
		}
	}
	return newRepoRules(read, publish), nil
}

// view returns rules as the API shows them: a list rule names its list.
func (s *Service) view(list []rule) []Rule {
	out := make([]Rule, 0, len(list))
	for _, r := range list {
		if r.listID != 0 {
			name := ""
			if st := s.listByID(r.listID); st != nil {
				name = st.name
			}
			out = append(out, Rule{Kind: RuleList, Value: name, Note: r.note})
		} else {
			out = append(out, Rule{Kind: RuleCIDR, Value: r.cidr, Note: r.note})
		}
	}
	return out
}

// RepoRules returns the rules of a repo (empty lists when it has none).
func (s *Service) RepoRules(repoID int64) Rules {
	s.rulesMu.RLock()
	rr := s.rules[repoID]
	s.rulesMu.RUnlock()
	if rr == nil {
		return Rules{Read: []Rule{}, Publish: []Rule{}}
	}
	return Rules{Read: s.view(rr.read), Publish: s.view(rr.publish)}
}

// normalize validates one scope's list and returns it in stored form: a CIDR is masked, an
// address becomes a /32 or /128, a list name is lower-cased, must exist and becomes its id.
func (s *Service) normalize(scope string, in []Rule) ([]rule, error) {
	if len(in) > maxRulesPerScope {
		return nil, fmt.Errorf("%w: at most %d %s rules", ErrInvalid, maxRulesPerScope, scope)
	}
	out := make([]rule, 0, len(in))
	seen := map[string]bool{}
	for _, r := range in {
		v := strings.TrimSpace(r.Value)
		var listID int64
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
			st := (*s.lists.Load())[v]
			if st == nil {
				return nil, fmt.Errorf("%w: %s rule: there is no IP list %q", ErrInvalid, scope, clipText(v, 64))
			}
			listID = st.id
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
		if listID != 0 {
			out = append(out, rule{listID: listID, note: note})
		} else {
			out = append(out, rule{cidr: v, note: note})
		}
	}
	return out, nil
}

// SetRules validates in and replaces every rule of repo with it, and records
// network.rules.update. It returns the stored rules. A validation error wraps ErrInvalid.
func (s *Service) SetRules(ctx context.Context, repo *repos.Repo, in Rules) (Rules, error) {
	repoID := repo.ID
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
			rules []rule
		}{{ScopeRead, read}, {ScopePublish, pub}} {
			for _, r := range l.rules {
				if err := q.InsertRepoIPRule(ctx, dbq.InsertRepoIPRuleParams{RepoID: repoID, Scope: l.scope,
					Cidr: sql.NullString{String: r.cidr, Valid: r.listID == 0}, ListID: sql.NullInt64{Int64: r.listID, Valid: r.listID != 0},
					Note: r.note}); err != nil {
					return err
				}
			}
		}
		return s.audit.RecordTx(ctx, tx, audit.Event{Action: ActionRulesUpdate, RepoID: repo.ID, Repo: repo.Name,
			Target: repo.Name, Detail: map[string]any{"read": len(read), "publish": len(pub)}})
	})
	if err != nil {
		return Rules{}, err
	}
	rr := newRepoRules(read, pub)
	if len(read) == 0 && len(pub) == 0 {
		delete(s.rules, repoID)
	} else {
		s.rules[repoID] = rr
	}
	return Rules{Read: s.view(rr.read), Publish: s.view(rr.publish)}, nil
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
		return s.allows(rr.pubM, ip)
	}
	return s.allows(rr.readM, ip)
}

// RestrictsRead reports whether repo id has read rules (from the in-memory snapshot).
func (s *Service) RestrictsRead(repoID int64) bool {
	s.rulesMu.RLock()
	rr := s.rules[repoID]
	s.rulesMu.RUnlock()
	return rr != nil && !rr.readM.empty
}

// Check returns a *DeniedError when the rules of repo do not allow ip for scope, else nil. It
// applies to every caller, admins included.
func (s *Service) Check(repo *repos.Repo, scope string, ip netip.Addr) *DeniedError {
	if s.allowed(repo.ID, scope, ip) {
		return nil
	}
	return &DeniedError{Scope: scope, Repo: repo, IP: ip}
}

// CheckRequest is Check for the client address of r.
func (s *Service) CheckRequest(r *http.Request, repo *repos.Repo, scope string) *DeniedError {
	return s.Check(repo, scope, httpx.ClientIP(r))
}

// deniedEvent is the audit event network.denied of e; the audit log adds the actor and the
// client address.
func deniedEvent(e *DeniedError) audit.Event {
	return audit.Event{
		Action: ActionDenied, RepoID: e.Repo.ID, Repo: e.Repo.Name,
		Target: e.Repo.Name, Detail: map[string]any{"scope": e.Scope},
	}
}

// DeniedRepos returns the repos whose read rules do not allow ip (search leaves them out).
func (s *Service) DeniedRepos(ip netip.Addr) map[int64]bool {
	s.rulesMu.RLock()
	defer s.rulesMu.RUnlock()
	var out map[int64]bool
	for id, rr := range s.rules {
		if !s.allows(rr.readM, ip) {
			if out == nil {
				out = map[int64]bool{}
			}
			out[id] = true
		}
	}
	return out
}
