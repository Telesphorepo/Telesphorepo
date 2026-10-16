package netrules

import (
	"context"
	"database/sql"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/netrules/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// TrustedProxyList is the prefix of a TR_TRUSTED_PROXIES entry that names an IP list, e.g.
// "list:cloudflare".
const TrustedProxyList = "list:"

// TrustedProxies is the setting trusted_proxies: the TCP peers whose X-Forwarded-For and forwarded
// proto the server reads, as literal entries. The IP lists that trusted proxies include are rows of
// trusted_proxy_lists (SetTrustedLists).
var TrustedProxies = settings.List(&settings.Def{Key: "trusted_proxies", Label: "Trusted proxies",
	Help: "The server reads the client address from X-Forwarded-For, and the scheme from X-Forwarded-Proto or Forwarded, only when the connection comes from one of these proxies. An entry is a CIDR, an address (for example 10.89.0.5) or \"private\" (loopback, RFC 1918, ULA and link-local). Add an IP list, for example the ranges that Cloudflare publishes, on the Reverse proxy page. With no entry and no list (the default), the server trusts no proxy and uses the TCP peer address.",
	Normalize: func(v any) any {
		l := v.([]string)
		out := make([]string, 0, len(l))
		for _, s := range l {
			if k := strings.ToLower(s); k == Private || strings.HasPrefix(k, TrustedProxyList) {
				s = k
			}
			out = append(out, s)
		}
		return out
	},
	Validate: func(v any) error {
		for _, s := range v.([]string) {
			if s == Private {
				continue
			}
			if name, ok := strings.CutPrefix(s, TrustedProxyList); ok {
				if !repos.NameRE.MatchString(name) {
					return fmt.Errorf("%q does not name an IP list", s)
				}
				continue
			}
			if _, err := netip.ParsePrefix(s); err == nil {
				continue
			}
			if _, err := netip.ParseAddr(s); err == nil {
				continue
			}
			return fmt.Errorf("%q is not a CIDR, an address or \"private\"", s)
		}
		return nil
	}})

// validate refuses a change of trusted_proxies with a "list:<name>" entry: an IP list joins the
// trusted proxies as a row (PUT /api/network/trusted-proxies). Only TR_TRUSTED_PROXIES may name
// lists; Init resolves them.
func (m *mod) validate(d *settings.Draft, mode settings.Mode) error {
	if mode != settings.ModeChange {
		return nil
	}
	for _, e := range TrustedProxies.Get(d.Next) {
		if strings.HasPrefix(e, TrustedProxyList) {
			d.Fail(TrustedProxies.Name(), "add an IP list on the Reverse proxy page")
			return nil
		}
	}
	return nil
}

// resolveEnvLists resolves the "list:<name>" entries of trusted_proxies (set by
// TR_TRUSTED_PROXIES) to list ids, once at startup. An unknown name is an error.
func (s *Service) resolveEnvLists() error {
	var ids []int64
	for _, e := range TrustedProxies.Get(s.set()) {
		name, ok := strings.CutPrefix(e, TrustedProxyList)
		if !ok {
			continue
		}
		st := (*s.lists.Load())[name]
		if st == nil {
			return fmt.Errorf("%s names the IP list %s, which does not exist", TrustedProxies.Def().Env(), strconv.Quote(name))
		}
		ids = append(ids, st.id)
	}
	s.envLists = ids
	return nil
}

// loadTrusted reads the rows of trusted_proxy_lists into memory.
func (s *Service) loadTrusted(ctx context.Context) error {
	ids, err := s.rq.ListTrustedProxyLists(ctx)
	if err != nil {
		return fmt.Errorf("load trusted proxy lists: %w", err)
	}
	s.trustedLists.Store(&ids)
	return nil
}

// trusts reports whether the trusted proxies include list id (a row or an env entry).
func (s *Service) trusts(id int64) bool {
	return slices.Contains(*s.trustedLists.Load(), id) || slices.Contains(s.envLists, id)
}

// TrustedList is an IP list that the trusted proxies include.
type TrustedList struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Trusted is what GET /api/network/trusted-proxies answers: the lists that rows include, and the
// names of the lists that TR_TRUSTED_PROXIES includes.
type Trusted struct {
	Lists    []TrustedList `json:"lists"`
	EnvLists []string      `json:"env_lists"`
}

// TrustedLists returns the IP lists that the trusted proxies include.
func (s *Service) TrustedLists() Trusted {
	out := Trusted{Lists: []TrustedList{}, EnvLists: []string{}}
	for _, id := range *s.trustedLists.Load() {
		if st := s.listByID(id); st != nil {
			out.Lists = append(out.Lists, TrustedList{ID: id, Name: st.name})
		}
	}
	for _, id := range s.envLists {
		if st := s.listByID(id); st != nil {
			out.EnvLists = append(out.EnvLists, st.name)
		}
	}
	return out
}

// SetTrustedLists replaces the IP lists that the trusted proxies include with ids, in one
// transaction, and records network.trusted.update. An unknown id is an *InvalidError.
func (s *Service) SetTrustedLists(ctx context.Context, ids []int64) (Trusted, error) {
	s.rulesMu.Lock()
	defer s.rulesMu.Unlock()
	ids = slices.Compact(slices.Sorted(slices.Values(ids)))
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		st := s.listByID(id)
		if st == nil {
			return Trusted{}, invalid("there is no IP list with id %d", id)
		}
		names = append(names, st.name)
	}
	err := s.d.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		if err := q.DeleteTrustedProxyLists(ctx); err != nil {
			return err
		}
		for _, id := range ids {
			if err := q.InsertTrustedProxyList(ctx, id); err != nil {
				return err
			}
		}
		return s.audit.RecordTx(ctx, tx, audit.Event{Action: ActionTrustedUpdate, Target: "trusted_proxies",
			Detail: map[string]any{"lists": names}})
	})
	if err != nil {
		return Trusted{}, err
	}
	s.trustedLists.Store(&ids)
	return s.TrustedLists(), nil
}
