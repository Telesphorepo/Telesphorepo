package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Type is a setting's value type. Value representations (in Snapshot.Value, List and JSON):
// string/duration -> string (durations keep the text form, e.g. "5m"), bool -> bool,
// size -> int64 (bytes), list -> []string. Input of a size or a duration accepts friendly forms
// ("512MiB", "1.5 GiB", "90 days").
type Type string

const (
	TypeString   Type = "string"
	TypeBool     Type = "bool"
	TypeSize     Type = "size"
	TypeDuration Type = "duration"
	TypeList     Type = "list"
)

// Core groups. Format plugins add one group each (see Register).
const (
	GroupGeneral    = "general"
	GroupStorage    = "storage"
	GroupSecurity   = "security"
	GroupPublishing = "publishing"
	GroupSSO        = "sso"
)

// Groups lists the groups in display order (UI sections): the core groups, then the plugin
// groups in registration order.
var Groups = []string{GroupGeneral, GroupStorage, GroupSecurity, GroupPublishing, GroupSSO}

// GroupLabels are the display names of the groups.
var GroupLabels = map[string]string{
	GroupGeneral:    "General",
	GroupStorage:    "Storage",
	GroupSecurity:   "Security",
	GroupPublishing: "Publishing",
	GroupSSO:        "Single sign-on",
}

// Def describes one setting. Its env override is TR_<KEY upper-cased, dots -> '_'>.
type Def struct {
	Key     string
	Type    Type
	Default string // text form, parsed like an env value ("" = empty/none)
	Dev     string // default of "telesphorepo dev" instead of Default ("" = same as Default)
	Secret  bool
	Group   string
	Label   string
	Help    string
	Options []string // allowed values (lower-case) for enumerated strings
	// Validate checks a decoded, normalized value (optional).
	Validate func(v any) error
	// normalize canonicalises a decoded value (optional).
	normalize func(v any) any
	// storage marks settings that select or configure the blob store.
	storage bool
}

// IsStorageKey reports whether key selects or configures the blob store.
func IsStorageKey(key string) bool {
	d := byKey[key]
	return d != nil && d.storage
}

// Env is the environment variable that overrides this setting.
func (d *Def) Env() string {
	return "TR_" + strings.ToUpper(strings.ReplaceAll(d.Key, ".", "_"))
}

// Setting keys used by the code (the registry below is authoritative).
const (
	KeyBaseURL           = "base_url"
	KeyLogLevel          = "log.level"
	KeyMaxUploadSize     = "max_upload_size"
	KeyMaxJSONSize       = "max_json_size"
	KeyStorage           = "storage"
	KeyFSRoot            = "fs.root"
	KeyS3Endpoint        = "s3.endpoint"
	KeyS3Region          = "s3.region"
	KeyS3Bucket          = "s3.bucket"
	KeyS3AccessKeyID     = "s3.access_key_id"
	KeyS3SecretAccessKey = "s3.secret_access_key"
	KeyS3Prefix          = "s3.prefix"
	KeyS3PathStyle       = "s3.path_style"
	KeyS3Redirect        = "s3.redirect"
	KeyPresignTTL        = "presign_ttl"
	KeyCookieSecure      = "cookie.secure"
	KeySessionTTL        = "session_ttl"
	KeyTrustedProxies    = "trusted_proxies"

	KeyOIDCIssuer         = "oidc.issuer"
	KeyOIDCClientID       = "oidc.client_id"
	KeyOIDCClientSecret   = "oidc.client_secret"
	KeyOIDCRedirectURL    = "oidc.redirect_url"
	KeyOIDCScopes         = "oidc.scopes"
	KeyOIDCAdminClaim     = "oidc.admin_claim"
	KeyOIDCAdminValues    = "oidc.admin_values"
	KeyOIDCAdminEmails    = "oidc.admin_emails"
	KeyOIDCSessionTTL     = "oidc.session_ttl"
	KeyAdminPasswordLogin = "admin_password_login"

	KeyNexusSyncInterval = "nexus.sync_interval"

	KeyPublishSessionTTL = "publish.session_ttl"
	KeyPublishDeviceTTL  = "publish.device_ttl"

	KeyAuditRetention = "audit.retention"
)

// SSOKeys are the settings whose change re-validates SSO by fetching the issuer's discovery
// document (see Service.TestOIDC).
var SSOKeys = []string{KeyOIDCIssuer, KeyOIDCClientID, KeyOIDCClientSecret, KeyOIDCRedirectURL, KeyOIDCScopes}

func trim(v any) any { return strings.TrimSpace(v.(string)) }

func httpURL(v any) error {
	s := v.(string)
	if s == "" {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("must be an http:// or https:// URL")
	}
	return nil
}

func lower(v any) any { return strings.ToLower(strings.TrimSpace(v.(string))) }

var registry = []*Def{
	// General
	{Key: KeyBaseURL, Type: TypeString, Group: GroupGeneral, Label: "Public base URL", Dev: "http://localhost:8080",
		Help:      "Clients reach this server at this URL, for example https://repo.example.com. The UI uses it in client snippets.",
		normalize: func(v any) any { return strings.TrimRight(strings.TrimSpace(v.(string)), "/") },
		Validate: func(v any) error {
			s := v.(string)
			if s == "" {
				return nil
			}
			u, err := url.Parse(s)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return errors.New("must be an http:// or https:// URL")
			}
			return nil
		}},
	{Key: KeyLogLevel, Type: TypeString, Default: "info", Group: GroupGeneral, Label: "Log level",
		Help: "The server log keeps messages of this level and more important ones.", Options: []string{"debug", "info", "warn", "error"}, normalize: lower},
	{Key: KeyMaxUploadSize, Type: TypeSize, Default: "2GiB", Group: GroupGeneral, Label: "Max upload size",
		Help: "The server refuses a bigger artifact or package upload, for example above 2GiB."},
	{Key: KeyMaxJSONSize, Type: TypeSize, Default: "1MiB", Group: GroupGeneral, Label: "Max JSON body size",
		Help: "The server refuses a bigger JSON API request body, for example above 1MiB."},
	{Key: KeyNexusSyncInterval, Type: TypeDuration, Default: "15m", Group: GroupGeneral, Label: "Nexus sync interval",
		Help: "The server syncs each mirroring Nexus source this often, for example every 15m. The minimum is 1m.",
		Validate: func(v any) error {
			if durationOf(v) < time.Minute {
				return errors.New("must be at least 1m")
			}
			return nil
		}},

	// Storage
	{Key: KeyStorage, Type: TypeString, Default: "fs", Group: GroupStorage, Label: "Storage backend",
		Help:    "With fs, the server stores blobs on the data volume. With s3, it uses any S3-compatible store (Cloudflare R2, SeaweedFS, AWS). When the location changes, the server moves the existing blobs to the new store in the background.",
		Options: []string{"fs", "s3", "r2"}, storage: true,
		normalize: func(v any) any {
			if s := lower(v).(string); s != "r2" {
				return s
			}
			return "s3"
		}},
	{Key: KeyFSRoot, Type: TypeString, Group: GroupStorage, Label: "Filesystem root",
		Help: "The server keeps blobs in this directory when storage is fs. The default is blobs/ in the data dir.", storage: true,
		normalize: func(v any) any { return strings.TrimSpace(v.(string)) }},
	{Key: KeyS3Endpoint, Type: TypeString, Group: GroupStorage, Label: "S3 endpoint",
		Help: "The URL of the S3 API, for example https://<account>.r2.cloudflarestorage.com or http://seaweedfs:8333. Leave it empty for AWS.", storage: true,
		normalize: func(v any) any { return strings.TrimSpace(v.(string)) },
		Validate: func(v any) error {
			s := v.(string)
			if s == "" {
				return nil
			}
			u, err := url.Parse(s)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return errors.New("must be an http:// or https:// URL")
			}
			return nil
		}},
	{Key: KeyS3Region, Type: TypeString, Default: "auto", Group: GroupStorage, Label: "S3 region",
		Help: "The region of the bucket. R2 uses auto.", storage: true,
		normalize: func(v any) any { return strings.TrimSpace(v.(string)) }},
	{Key: KeyS3Bucket, Type: TypeString, Group: GroupStorage, Label: "S3 bucket", storage: true,
		normalize: func(v any) any { return strings.TrimSpace(v.(string)) }},
	{Key: KeyS3AccessKeyID, Type: TypeString, Group: GroupStorage, Label: "S3 access key ID", storage: true,
		normalize: func(v any) any { return strings.TrimSpace(v.(string)) }},
	{Key: KeyS3SecretAccessKey, Type: TypeString, Secret: true, Group: GroupStorage, Label: "S3 secret access key", storage: true},
	{Key: KeyS3Prefix, Type: TypeString, Group: GroupStorage, Label: "S3 key prefix",
		Help: "Every object key starts with this prefix, for example telesphorepo/. Leave it empty for no prefix.", storage: true,
		normalize: func(v any) any {
			s := strings.Trim(strings.TrimSpace(v.(string)), "/")
			if s != "" {
				s = path.Clean(s)
			}
			return s
		},
		Validate: func(v any) error {
			for _, seg := range strings.Split(v.(string), "/") {
				if seg == ".." {
					return errors.New("must not contain ..")
				}
			}
			return nil
		}},
	{Key: KeyS3PathStyle, Type: TypeBool, Default: "false", Group: GroupStorage, Label: "S3 path-style addressing",
		Help: "SeaweedFS and MinIO need path-style addresses. R2 and AWS do not.", storage: true},
	{Key: KeyS3Redirect, Type: TypeBool, Default: "true", Group: GroupStorage, Label: "Redirect downloads to S3",
		Help: "When this is on, a GET download answers 302 with a presigned URL. When it is off, the server streams the file.", storage: true},
	{Key: KeyPresignTTL, Type: TypeDuration, Default: "5m", Group: GroupStorage, Label: "Presigned URL lifetime",
		Help: "A presigned download URL works for this time."},

	// Security
	{Key: KeyCookieSecure, Type: TypeBool, Default: "true", Dev: "false", Group: GroupSecurity, Label: "Secure session cookie",
		Help: "The browser sends the session cookie only over HTTPS. Turn this off only for a plain-HTTP setup."},
	{Key: KeySessionTTL, Type: TypeDuration, Default: "168h", Group: GroupSecurity, Label: "Session lifetime",
		Help: "After this time, a person signed in to the UI must sign in again."},
	{Key: KeyTrustedProxies, Type: TypeList, Group: GroupSecurity, Label: "Trusted proxies",
		Help: "The server reads the client address from X-Forwarded-For only when the connection comes from one of these proxies. An entry is a CIDR, an address (for example 10.89.0.5), \"private\" (loopback, RFC 1918, ULA and link-local) or an IP list as \"list:<name>\" (for example list:cloudflare, the ranges that Cloudflare publishes, refreshed daily). With an empty list (the default), the server trusts no proxy and uses the TCP peer address.",
		normalize: func(v any) any {
			l := v.([]string)
			out := make([]string, 0, len(l))
			for _, s := range l {
				if k := strings.ToLower(s); k == "private" || strings.HasPrefix(k, TrustedProxyList) {
					s = k
				}
				out = append(out, s)
			}
			if len(out) == 0 {
				return []string(nil)
			}
			return out
		},
		Validate: func(v any) error {
			for _, s := range v.([]string) {
				if s == "private" {
					continue
				}
				if name, ok := strings.CutPrefix(s, TrustedProxyList); ok {
					if !listNameRE.MatchString(name) {
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
				return fmt.Errorf("%q is not a CIDR, an address, \"private\" or \"list:<name>\"", s)
			}
			return nil
		}},
	{Key: KeyAuditRetention, Type: TypeDuration, Default: "8760h", Group: GroupSecurity, Label: "Audit log retention",
		Help: "The audit log keeps an event for this time, for example 8760h (365 days). A job deletes older events every hour."},

	// Publishing (publish sessions and enrolled devices)
	{Key: KeyPublishSessionTTL, Type: TypeDuration, Default: "15m", Group: GroupPublishing, Label: "Publish session lifetime",
		Help: "A publish session, the credential that every upload needs, lasts at most this time, for example 15m. A client ends its session when the publish is done. The maximum is 1h.",
		Validate: func(v any) error {
			if durationOf(v) > time.Hour {
				return errors.New("must be at most 1h")
			}
			return nil
		}},
	{Key: KeyPublishDeviceTTL, Type: TypeDuration, Default: "2160h", Group: GroupPublishing, Label: "Device lifetime",
		Help: "An enrolled device key (Gradle plugin, Maven extension) can start publish sessions for this time, for example 2160h (90 days). After this time, the developer must enroll the device again."},

	// Single sign-on (OIDC, admins only)
	{Key: KeyOIDCIssuer, Type: TypeString, Group: GroupSSO, Label: "OIDC issuer URL",
		Help:      "The issuer URL of your identity provider, for example https://kc.example.com/realms/ops. When it is set, administrators can sign in with SSO. A save checks the discovery document of the issuer.",
		normalize: trim, Validate: httpURL},
	{Key: KeyOIDCClientID, Type: TypeString, Group: GroupSSO, Label: "Client ID",
		Help: "The identity provider gives this ID to the confidential client that you register there.", normalize: trim},
	{Key: KeyOIDCClientSecret, Type: TypeString, Secret: true, Group: GroupSSO, Label: "Client secret",
		Help: "The identity provider gives this secret with the client ID."},
	{Key: KeyOIDCRedirectURL, Type: TypeString, Group: GroupSSO, Label: "Redirect URL",
		Help:      "Register this callback URL at the identity provider. The default is <public base URL>/api/auth/oidc/callback.",
		normalize: trim, Validate: httpURL},
	{Key: KeyOIDCScopes, Type: TypeString, Default: "openid profile email", Group: GroupSSO, Label: "Scopes",
		Help:      "The server asks for these space-separated scopes at sign-in. It always adds openid.",
		normalize: func(v any) any { return strings.Join(strings.Fields(v.(string)), " ") }},
	{Key: KeyOIDCAdminClaim, Type: TypeString, Default: "groups", Group: GroupSSO, Label: "Admin claim",
		Help:      "The server checks this ID token claim for admin rights. A dotted path is allowed, for example realm_access.roles for Keycloak realm roles.",
		normalize: trim,
		Validate: func(v any) error {
			s := v.(string)
			if s == "" {
				return errors.New("must not be empty")
			}
			for _, seg := range strings.Split(s, ".") {
				if seg == "" {
					return errors.New("must be a claim name or a dotted path such as realm_access.roles")
				}
			}
			return nil
		}},
	{Key: KeyOIDCAdminValues, Type: TypeList, Group: GroupSSO, Label: "Admin claim values",
		Help: "The admin claim (a string or a list) must contain one of these values, for example repo-admins."},
	{Key: KeyOIDCAdminEmails, Type: TypeList, Group: GroupSSO, Label: "Admin emails",
		Help: "A person with one of these email addresses is an admin too. The server matches an address only when the provider marks it verified.",
		normalize: func(v any) any {
			l := v.([]string)
			out := make([]string, 0, len(l))
			for _, e := range l {
				out = append(out, strings.ToLower(e))
			}
			if len(out) == 0 {
				return []string(nil)
			}
			return out
		}},
	{Key: KeyOIDCSessionTTL, Type: TypeDuration, Default: "12h", Group: GroupSSO, Label: "SSO session lifetime",
		Help: "After this time, an admin signed in through SSO must sign in again."},
	{Key: KeyAdminPasswordLogin, Type: TypeBool, Default: "true", Group: GroupSSO, Label: "Password sign-in for admins",
		Help: "When this is off, local admins cannot sign in to the UI with a password, but publishers still can. It needs SSO. To turn it on again without SSO, set TR_ADMIN_PASSWORD_LOGIN=true and restart."},
}

// TrustedProxyList is the prefix of a trusted_proxies entry that names an IP list, e.g.
// "list:cloudflare".
const TrustedProxyList = "list:"

// listNameRE is the form of an IP list name (the repo name form).
var listNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

var byKey = func() map[string]*Def {
	m := make(map[string]*Def, len(registry))
	for _, d := range registry {
		m[d.Key] = d
	}
	return m
}()

// Register adds a settings group contributed by a format plugin: group is the format name and
// label its title. Every Def gets Group = group. It must run before Load, once per process;
// a key or group that already exists is an error.
func Register(group, label string, defs []Def) error {
	if _, ok := GroupLabels[group]; ok {
		return fmt.Errorf("settings: group %q already registered", group)
	}
	seen := map[string]bool{}
	for _, d := range defs {
		if byKey[d.Key] != nil || seen[d.Key] {
			return fmt.Errorf("settings: key %q already registered", d.Key)
		}
		seen[d.Key] = true
	}
	for _, d := range defs {
		d.Group = group
		registry = append(registry, &d)
		byKey[d.Key] = &d
	}
	Groups = append(Groups, group)
	GroupLabels[group] = label
	return nil
}

// Definitions returns the setting registry in display order.
func Definitions() []*Def { return registry }

// Lookup returns the definition of key, or nil.
func Lookup(key string) *Def { return byKey[key] }

// parseText parses an env or default value.
func (d *Def) parseText(s string) (any, error) {
	var v any
	switch d.Type {
	case TypeString:
		v = s
	case TypeBool:
		b, err := strconv.ParseBool(strings.TrimSpace(s))
		if err != nil {
			return nil, errors.New("must be true or false")
		}
		v = b
	case TypeSize:
		n, err := parseSize(s)
		if err != nil {
			return nil, err
		}
		v = n
	case TypeDuration:
		v = strings.TrimSpace(s)
	case TypeList:
		var out []string
		for _, p := range strings.Split(s, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		v = out
	}
	return d.check(v)
}

// decodeJSON parses a value from the API or the database.
func (d *Def) decodeJSON(raw json.RawMessage) (any, error) {
	var v any
	switch d.Type {
	case TypeString, TypeDuration:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, errors.New("must be a string")
		}
		v = s
	case TypeBool:
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			var s string
			if json.Unmarshal(raw, &s) != nil {
				return nil, errors.New("must be true or false")
			}
			return d.parseText(s)
		}
		v = b
	case TypeSize:
		var n int64
		if err := json.Unmarshal(raw, &n); err != nil {
			var s string
			if json.Unmarshal(raw, &s) != nil {
				return nil, errors.New(sizeHint)
			}
			return d.parseText(s)
		}
		v = n
	case TypeList:
		var l []string
		if err := json.Unmarshal(raw, &l); err != nil {
			var s string
			if json.Unmarshal(raw, &s) != nil {
				return nil, errors.New("must be a list of strings")
			}
			return d.parseText(s)
		}
		var out []string
		for _, p := range l {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		v = out
	}
	return d.check(v)
}

// check normalizes and validates a decoded value.
func (d *Def) check(v any) (any, error) {
	if d.normalize != nil {
		v = d.normalize(v)
	}
	switch d.Type {
	case TypeSize:
		if v.(int64) <= 0 {
			return nil, errors.New("must be a positive size")
		}
	case TypeDuration:
		dur, err := parseDuration(v.(string))
		if err != nil || dur <= 0 {
			return nil, errors.New(`must be a positive duration such as "15m", "12h", "90 days" or "1h30m"`)
		}
		v = canonicalDuration(dur)
	}
	if len(d.Options) > 0 {
		s, _ := v.(string)
		if !slices.Contains(d.Options, s) {
			return nil, fmt.Errorf("must be one of %s", strings.Join(d.Options, ", "))
		}
	}
	if d.Validate != nil {
		if err := d.Validate(v); err != nil {
			return nil, err
		}
	}
	return v, nil
}

func durationOf(v any) time.Duration {
	d, _ := time.ParseDuration(v.(string))
	return d
}
