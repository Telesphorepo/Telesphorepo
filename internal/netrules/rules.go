package netrules

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/netrules/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Rule kinds.
const (
	RuleCIDR = "cidr" // Value is a CIDR
	RuleList = "list" // Value is the name of an IP list; the rule keeps the list's id
)

const (
	maxRules     = 200
	maxNoteChars = 200
)

// ErrInvalid is a list of allowed networks that does not validate.
var ErrInvalid = errors.New("invalid allowed networks")

// ErrPublic refuses allowed networks on a public repo: anyone downloads from it already.
var ErrPublic = errors.New("a public repository takes no allowed networks: anyone downloads from it; make it restricted first")

// Rule is one allowed network of a repo: a CIDR or the name of an IP list.
type Rule struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
	Note  string `json:"note"`
}

// Rules are the allowed networks of a repo, as GET and PUT /api/repos/{name}/ip-rules carry them.
type Rules struct {
	Read []Rule `json:"read"`
}

// rule is one stored allowed network: a CIDR, or the id of an IP list.
type rule struct {
	cidr   string
	listID int64
	note   string
}

// repoRules is the in-memory copy of a repo's allowed networks with the CIDRs compiled.
type repoRules struct {
	list  []rule
	cidrs *ipSet
	lists []int64
}

func newRepoRules(list []rule) *repoRules {
	rr := &repoRules{list: list}
	var prefixes []netip.Prefix
	for _, r := range list {
		if r.listID != 0 {
			rr.lists = append(rr.lists, r.listID)
		} else if p, err := netip.ParsePrefix(r.cidr); err == nil {
			prefixes = append(prefixes, p)
		}
	}
	rr.cidrs = newIPSet(prefixes)
	return rr
}

func (s *Service) loadRules(ctx context.Context) error {
	s.rulesMu.Lock()
	defer s.rulesMu.Unlock()
	return s.reloadRulesLocked(ctx)
}

// reloadRulesLocked reads the allowed networks of every repo into memory and indexes them.
// Caller holds rulesMu.
func (s *Service) reloadRulesLocked(ctx context.Context) error {
	m := map[int64]*repoRules{}
	var after int64
	for {
		id, err := s.rq.NextNetworkRepo(ctx, after)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return fmt.Errorf("load allowed networks: %w", err)
		}
		rows, err := s.rq.ListRepoNetworks(ctx, id)
		if err != nil {
			return fmt.Errorf("load allowed networks of repo %d: %w", id, err)
		}
		list := make([]rule, 0, len(rows))
		for _, r := range rows {
			list = append(list, rule{cidr: r.Cidr.String, listID: r.ListID.Int64, note: r.Note})
		}
		m[id] = newRepoRules(list)
		after = id
	}
	s.rules = m
	s.reach.Store(buildReach(m))
	return nil
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

// RepoRules returns the allowed networks of a repo (an empty list when it has none).
func (s *Service) RepoRules(repoID int64) Rules {
	s.rulesMu.RLock()
	rr := s.rules[repoID]
	s.rulesMu.RUnlock()
	if rr == nil {
		return Rules{Read: []Rule{}}
	}
	return Rules{Read: s.view(rr.list)}
}

// normalize validates a list of allowed networks and returns it in stored form: a CIDR is
// masked, an address becomes a /32 or /128, a list name is lower-cased, must exist and becomes
// its id.
func (s *Service) normalize(in []Rule) ([]rule, error) {
	if len(in) > maxRules {
		return nil, fmt.Errorf("%w: at most %d allowed networks", ErrInvalid, maxRules)
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
				return nil, fmt.Errorf("%w: %q is not a CIDR (e.g. 203.0.113.0/24)", ErrInvalid, clipText(v, 64))
			}
		case RuleList:
			v = strings.ToLower(v)
			st := (*s.lists.Load())[v]
			if st == nil {
				return nil, fmt.Errorf("%w: there is no IP list %q", ErrInvalid, clipText(v, 64))
			}
			listID = st.id
		default:
			return nil, fmt.Errorf("%w: a kind is %q or %q", ErrInvalid, RuleCIDR, RuleList)
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
			return nil, fmt.Errorf("%w: %s is listed twice", ErrInvalid, v)
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

// AfterRulesChange registers fn to run after each committed SetRules, with the repo's id, after
// the in-memory snapshot changed. fn runs on the request that made the change and must be quick
// (the edge module marks the repo's access object). It runs before the server serves.
func (s *Service) AfterRulesChange(fn func(ctx context.Context, repoID int64)) {
	s.afterRules = append(s.afterRules, fn)
}

// SetRules validates in and replaces the allowed networks of repo with it, records
// network.rules.update and then runs the AfterRulesChange functions. It returns the stored
// networks. A validation error wraps ErrInvalid; networks on a public repo are ErrPublic.
func (s *Service) SetRules(ctx context.Context, repo *repos.Repo, in Rules) (Rules, error) {
	out, err := s.setRules(ctx, repo, in)
	if err == nil {
		for _, fn := range s.afterRules {
			fn(ctx, repo.ID)
		}
	}
	return out, err
}

func (s *Service) setRules(ctx context.Context, repo *repos.Repo, in Rules) (Rules, error) {
	repoID := repo.ID
	// The list names are checked under rulesMu, which a list delete or rename also holds.
	s.rulesMu.Lock()
	defer s.rulesMu.Unlock()
	list, err := s.normalize(in.Read)
	if err != nil {
		return Rules{}, err
	}
	err = s.d.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		if len(list) > 0 {
			// Read in the write transaction, so a repo made public at the same time cannot end up
			// with networks: repos.Service refuses to make public a repo that has rows here.
			public, err := q.RepoIsPublic(ctx, repoID)
			if err != nil {
				return err
			}
			if public {
				return ErrPublic
			}
		}
		if err := q.DeleteRepoNetworks(ctx, repoID); err != nil {
			return err
		}
		for _, r := range list {
			if err := q.InsertRepoNetwork(ctx, dbq.InsertRepoNetworkParams{RepoID: repoID,
				Cidr: sql.NullString{String: r.cidr, Valid: r.listID == 0}, ListID: sql.NullInt64{Int64: r.listID, Valid: r.listID != 0},
				Note: r.note}); err != nil {
				return err
			}
		}
		return s.audit.RecordTx(ctx, tx, audit.Event{Action: ActionRulesUpdate, RepoID: repo.ID, Repo: repo.Name,
			Target: repo.Name, Detail: map[string]any{"networks": len(list)}})
	})
	if err != nil {
		return Rules{}, err
	}
	rr := newRepoRules(list)
	if len(list) == 0 {
		delete(s.rules, repoID)
	} else {
		s.rules[repoID] = rr
	}
	s.reach.Store(buildReach(s.rules))
	return Rules{Read: s.view(rr.list)}, nil
}

// checkPublic is the check of every repo update (repos.Service.CheckChange): a repo with allowed
// networks is not made public. It reads in the update's transaction.
func (s *Service) checkPublic(ctx context.Context, tx *sql.Tx, old, new *repos.Repo) error {
	if !new.AnonymousRead || old.AnonymousRead {
		return nil
	}
	has, err := dbq.New(tx).HasRepoNetworks(ctx, new.ID)
	if err != nil {
		return err
	}
	if has {
		return fmt.Errorf("%w: %s has allowed networks; remove them before making it public", repos.ErrInvalid, new.Name)
	}
	return nil
}

// Forget drops the in-memory networks of a deleted repo (its rows cascade with the repo).
func (s *Service) Forget(repoID int64) {
	s.rulesMu.Lock()
	if _, ok := s.rules[repoID]; ok {
		delete(s.rules, repoID)
		s.reach.Store(buildReach(s.rules))
	}
	s.rulesMu.Unlock()
}

// Allows reports whether a is in the allowed networks of repo id (access.Networks).
func (s *Service) Allows(repoID int64, a netip.Addr) bool {
	s.rulesMu.RLock()
	rr := s.rules[repoID]
	s.rulesMu.RUnlock()
	if rr == nil || !a.IsValid() {
		return false
	}
	a = a.Unmap()
	if rr.cidrs.contains(a) {
		return true
	}
	for _, l := range rr.lists {
		if s.listSet(l).contains(a) {
			return true
		}
	}
	return false
}

// Reach returns the ids of the repos whose allowed networks hold a (access.Networks): one binary
// search of the CIDR index and one of each IP list that a repo names.
func (s *Service) Reach(a netip.Addr) []int64 {
	if !a.IsValid() {
		return nil
	}
	return s.reach.Load().lookup(a.Unmap(), s.listSet)
}

// Has reports whether repo id has allowed networks (access.Networks).
func (s *Service) Has(repoID int64) bool {
	s.rulesMu.RLock()
	defer s.rulesMu.RUnlock()
	return s.rules[repoID] != nil
}

// Describe returns the allowed networks of repo id for the access report (access.Networks).
func (s *Service) Describe(repoID int64) access.NetworkReport {
	titles := map[string]string{}
	for _, l := range s.Lists() {
		titles[l.Name] = l.Title
	}
	out := access.NetworkReport{Lists: []access.NetworkList{}, CIDRs: []access.NetworkCIDR{}}
	var parts []string
	for _, r := range s.RepoRules(repoID).Read {
		switch r.Kind {
		case RuleList:
			t := cmp.Or(titles[r.Value], r.Value)
			out.Lists = append(out.Lists, access.NetworkList{Name: r.Value, Title: t})
			parts = append(parts, t)
		case RuleCIDR:
			out.CIDRs = append(out.CIDRs, access.NetworkCIDR{CIDR: r.Value, Note: r.Note})
		}
	}
	switch len(out.CIDRs) {
	case 0:
	case 1:
		parts = append(parts, out.CIDRs[0].CIDR)
	default:
		parts = append(parts, fmt.Sprintf("%d address ranges", len(out.CIDRs)))
	}
	out.Summary = joinAnd(parts)
	return out
}

// joinAnd joins items as "a", "a and b", "a, b and c".
func joinAnd(items []string) string {
	if len(items) <= 1 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
