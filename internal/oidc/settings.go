package oidc

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"

	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// CallbackPath is the OIDC redirect endpoint, appended to the base URL when oidc.redirect_url
// is empty.
const CallbackPath = "/api/auth/oidc/callback"

// The settings of the group "sso".
var (
	Issuer = settings.String(&settings.Def{Key: "oidc.issuer", Label: "OIDC issuer URL",
		Help:      "The issuer URL of your identity provider, for example https://kc.example.com/realms/ops. When it is set, people can sign in with single sign-on. A save checks the discovery document of the issuer.",
		Normalize: settings.Trim, Validate: settings.HTTPURL})
	ClientID = settings.String(&settings.Def{Key: "oidc.client_id", Label: "Client ID",
		Help: "The identity provider gives this ID to the confidential client that you register there.", Normalize: settings.Trim})
	ClientSecret = settings.String(&settings.Def{Key: "oidc.client_secret", Secret: true, Label: "Client secret",
		Help: "The identity provider gives this secret with the client ID."})
	RedirectURL = settings.String(&settings.Def{Key: "oidc.redirect_url", Label: "Redirect URL",
		Help:      "Register this callback URL at the identity provider. The default is <public base URL>" + CallbackPath + ".",
		Normalize: settings.Trim, Validate: settings.HTTPURL})
	Scopes = settings.String(&settings.Def{Key: "oidc.scopes", Default: "openid profile email", Label: "Scopes",
		Help:      "The server asks for these space-separated scopes at sign-in. It always adds openid.",
		Normalize: func(v any) any { return strings.Join(strings.Fields(v.(string)), " ") }})
	PermissionClaim = settings.String(&settings.Def{Key: "oidc.permission_claim", Label: "Permission claim",
		Help:      "The claim of the ID token (then of UserInfo) whose values give server permissions, for example groups, or realm_access.roles for Keycloak realm roles. When it is set, single sign-on manages the server permissions of every SSO account at each sign-in. When it is empty, people manage the permissions on this server and a sign-in never changes them.",
		Normalize: settings.Trim,
		Validate: func(v any) error {
			s := v.(string)
			if s == "" {
				return nil
			}
			for _, seg := range strings.Split(s, ".") {
				if seg == "" {
					return errors.New("must be a claim name or a dotted path such as realm_access.roles")
				}
			}
			return nil
		}})
	PermissionMap = settings.Map(&settings.Def{Key: "oidc.permission_map", Label: "Permissions by claim value",
		Help: "Each value of the permission claim gives these server permissions, for example {\"repo-admins\": [\"repositories.all\"]}. A person gets the permissions of every value they have."})
	SessionTTL = settings.Duration(&settings.Def{Key: "oidc.session_ttl", Default: "12h", Label: "SSO session lifetime",
		Help: "After this time, a person signed in through single sign-on must sign in again."})
)

// connectionKeys are the settings whose change the discovery check of the issuer covers.
var connectionKeys = []string{Issuer.Name(), ClientID.Name(), ClientSecret.Name(), RedirectURL.Name(), Scopes.Name()}

// Config is the effective SSO configuration. SSO is on when OIDCIssuer is set.
type Config struct {
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string   // oidc.redirect_url, else the base URL + CallbackPath
	OIDCScopes       []string // always with openid
	PermissionClaim  string   // "" when the server manages the permissions
	PermissionMap    map[string][]string
	OIDCSessionTTL   time.Duration
	CookieSecure     bool
}

// OIDCEnabled reports whether SSO is on.
func (c *Config) OIDCEnabled() bool { return c.OIDCIssuer != "" }

// Managed reports whether single sign-on manages the server permissions of SSO accounts: SSO is
// on and the permission claim is set.
func (c *Config) Managed() bool { return c.OIDCEnabled() && c.PermissionClaim != "" }

// ConfigView is the SSO configuration of a settings snapshot.
var ConfigView = settings.NewView(func(s *settings.Snapshot) Config {
	c := Config{
		OIDCIssuer: Issuer.Get(s), OIDCClientID: ClientID.Get(s), OIDCClientSecret: ClientSecret.Get(s),
		OIDCRedirectURL: RedirectURL.Get(s), OIDCScopes: strings.Fields(Scopes.Get(s)),
		PermissionClaim: PermissionClaim.Get(s), PermissionMap: PermissionMap.Get(s),
		OIDCSessionTTL: SessionTTL.Get(s), CookieSecure: settings.CookieSecure.Get(s),
	}
	if c.OIDCRedirectURL == "" {
		if base := settings.BaseURL.Get(s); base != "" {
			c.OIDCRedirectURL = base + CallbackPath
		}
	}
	if !slices.Contains(c.OIDCScopes, "openid") {
		c.OIDCScopes = append([]string{"openid"}, c.OIDCScopes...)
	}
	return c
})

// group is the settings group "sso".
func (h *Handler) group() *settings.Group {
	return &settings.Group{ID: "sso", Label: "Single sign-on", Perm: PermManage, Page: "/settings/sign-in",
		Defs: []*settings.Def{Issuer.Def(), ClientID.Def(), ClientSecret.Def(), RedirectURL.Def(), Scopes.Def(),
			SessionTTL.Def(), PermissionClaim.Def(), PermissionMap.Def(), auth.PasswordLogin.Def()},
		Validate: validate, Test: h.test, Apply: h.apply}
}

// problems returns the field errors of an SSO configuration that is on.
func problems(d *settings.Draft) map[string]string {
	c := ConfigView.Get(d.Next)
	out := map[string]string{}
	if !c.OIDCEnabled() {
		return out
	}
	if c.OIDCClientID == "" {
		out[ClientID.Name()] = "required when SSO is enabled"
	}
	if c.OIDCClientSecret == "" {
		msg := "required when SSO is enabled"
		if why := d.Broken(ClientSecret.Name()); why != "" {
			msg += " (the stored value cannot be read: " + why + ")"
		}
		out[ClientSecret.Name()] = msg
	}

	if c.OIDCRedirectURL == "" {
		out[RedirectURL.Name()] = "required when SSO is enabled and the public base URL is empty"
	}
	return out
}

// mapProblem checks the permission map: every value names server permissions of the registry,
// and a map needs the claim. It returns "" when the map is fine.
func mapProblem(d *settings.Draft) string {
	m := PermissionMap.Get(d.Next)
	if len(m) == 0 {
		return ""
	}
	if PermissionClaim.Get(d.Next) == "" {
		return "set the permission claim to use the map"
	}
	for _, value := range slices.Sorted(maps.Keys(m)) {
		if _, err := perm.Current().Parse(perm.ScopeServer, m[value]); err != nil {
			return value + ": " + err.Error()
		}
	}
	return ""
}

// validate checks the SSO group. A change gets every problem as a field error. At startup a
// problem caused by the environment stops the server; a problem of the stored values (for
// example a client secret that does not decrypt after a change of the master key) starts the
// server without SSO and with password sign-in for everyone, so nobody is locked out.
func validate(d *settings.Draft, mode settings.Mode) error {
	bad := problems(d)
	if msg := mapProblem(d); msg != "" {
		bad[PermissionMap.Name()] = msg
	}
	pwOff := auth.PasswordLogin.Get(d.Next) == auth.PasswordLoginPublishers
	const needsSSO = "publishers needs single sign-on (set the OIDC issuer)"
	if mode == settings.ModeChange {
		for k, msg := range bad {
			d.Fail(k, msg)
		}
		if pwOff && Issuer.Get(d.Next) == "" {
			d.Fail(auth.PasswordLogin.Name(), needsSSO)
		}
		return nil
	}
	var why []string
	if len(bad) > 0 {
		if d.Source(Issuer.Name()) == settings.SourceEnv {
			for k, msg := range bad {
				d.Fail(k, msg)
			}
			return nil
		}
		d.Override(Issuer.Name(), "")
		for _, k := range slices.Sorted(maps.Keys(bad)) {
			why = append(why, k+": "+bad[k])
		}
	}
	if pwOff && Issuer.Get(d.Next) == "" {
		if d.Source(auth.PasswordLogin.Name()) == settings.SourceEnv {
			d.Fail(auth.PasswordLogin.Name(), needsSSO)
			return nil
		}
		d.Override(auth.PasswordLogin.Name(), auth.PasswordLoginAll)
		why = append(why, "password sign-in is publishers but single sign-on is off; everyone signs in with a password")
	}
	if len(why) > 0 {
		return fmt.Errorf("%w: SSO is off: %s", settings.ErrDegraded, strings.Join(why, "; "))
	}
	return nil
}

// discover fetches the discovery document of the issuer of c (10 s timeout).
func discover(ctx context.Context, c Config) error {
	if !c.OIDCEnabled() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	if _, err := gooidc.NewProvider(ctx, c.OIDCIssuer); err != nil {
		msg := "OIDC discovery failed: " + err.Error()
		return &settings.HookError{Status: http.StatusBadRequest, Message: msg, Fields: map[string]string{Issuer.Name(): msg}}
	}
	return nil
}

// test checks the discovery document of the issuer (POST /api/settings/sso/test).
func (h *Handler) test(ctx context.Context, d *settings.Draft) error {
	return discover(ctx, ConfigView.Get(d.Next))
}

// apply refuses a change of the connection settings whose issuer does not answer the discovery
// request.
func (h *Handler) apply(ctx context.Context, c *settings.ApplyCall) (*settings.Applied, error) {
	if !c.Changed(connectionKeys...) {
		return nil, nil
	}
	return nil, discover(ctx, ConfigView.Get(c.Next))
}
