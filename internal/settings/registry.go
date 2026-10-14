package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Type is a setting's value type. Value representations (in Snapshot.Value, List and JSON):
// string/duration -> string (durations keep the text form, e.g. "5m"), bool -> bool,
// int -> int64, list -> []string.
type Type string

const (
	TypeString   Type = "string"
	TypeBool     Type = "bool"
	TypeInt      Type = "int"
	TypeDuration Type = "duration"
	TypeList     Type = "list"
)

// Core groups. Format plugins add one group each (see Register).
const (
	GroupGeneral  = "general"
	GroupStorage  = "storage"
	GroupSecurity = "security"
	GroupSSO      = "sso"
	GroupSigning  = "signing"
)

// Groups lists the groups in display order (UI sections): the core groups, then the plugin
// groups in registration order.
var Groups = []string{GroupGeneral, GroupStorage, GroupSecurity, GroupSSO, GroupSigning}

// GroupLabels are the display names of the groups.
var GroupLabels = map[string]string{
	GroupGeneral:  "General",
	GroupStorage:  "Storage",
	GroupSecurity: "Security",
	GroupSSO:      "Single sign-on",
	GroupSigning:  "Signing",
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
	// apiReadOnly settings cannot be changed through Update (they have a dedicated endpoint).
	apiReadOnly bool
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
	KeyMaxUploadBytes    = "max_upload_bytes"
	KeyMaxJSONBytes      = "max_json_bytes"
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
	KeyGPGKeyFile        = "gpg.key_file"
	KeyGPGPassphraseFile = "gpg.passphrase_file"
	KeySigningKey        = "signing.key"

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
	{Key: KeyBaseURL, Type: TypeString, Group: GroupGeneral, Label: "Public base URL",
		Help:      "URL clients use to reach this server, e.g. https://repo.example.com (used in UI hints).",
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
		Help: "Minimum level of the server log.", Options: []string{"debug", "info", "warn", "error"}, normalize: lower},
	{Key: KeyMaxUploadBytes, Type: TypeInt, Default: "2147483648", Group: GroupGeneral, Label: "Max upload size (bytes)",
		Help: "Largest artifact or package upload accepted."},
	{Key: KeyMaxJSONBytes, Type: TypeInt, Default: "1048576", Group: GroupGeneral, Label: "Max JSON body (bytes)",
		Help: "Largest JSON API request body accepted."},
	{Key: KeyNexusSyncInterval, Type: TypeDuration, Default: "15m", Group: GroupGeneral, Label: "Nexus sync interval",
		Help: "Time between two syncs of a mirroring Nexus source, e.g. 15m. The minimum is 1m.",
		Validate: func(v any) error {
			if durationOf(v) < time.Minute {
				return errors.New("must be at least 1m")
			}
			return nil
		}},

	// Storage
	{Key: KeyStorage, Type: TypeString, Default: "fs", Group: GroupStorage, Label: "Storage backend",
		Help:    "fs stores blobs on the data volume; s3 uses any S3-compatible store (Cloudflare R2, SeaweedFS, AWS). A change to another location moves the existing blobs to the new store in the background.",
		Options: []string{"fs", "s3", "r2"}, storage: true,
		normalize: func(v any) any {
			if s := lower(v).(string); s != "r2" {
				return s
			}
			return "s3"
		}},
	{Key: KeyFSRoot, Type: TypeString, Group: GroupStorage, Label: "Filesystem root",
		Help: "Directory for blobs when storage is fs (default: blobs/ in the data dir).", storage: true,
		normalize: func(v any) any { return strings.TrimSpace(v.(string)) }},
	{Key: KeyS3Endpoint, Type: TypeString, Group: GroupStorage, Label: "S3 endpoint",
		Help: "e.g. https://<account>.r2.cloudflarestorage.com or http://seaweedfs:8333.", storage: true,
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
		Help: "Region name; R2 uses auto.", storage: true,
		normalize: func(v any) any { return strings.TrimSpace(v.(string)) }},
	{Key: KeyS3Bucket, Type: TypeString, Group: GroupStorage, Label: "S3 bucket", storage: true,
		normalize: func(v any) any { return strings.TrimSpace(v.(string)) }},
	{Key: KeyS3AccessKeyID, Type: TypeString, Group: GroupStorage, Label: "S3 access key ID", storage: true,
		normalize: func(v any) any { return strings.TrimSpace(v.(string)) }},
	{Key: KeyS3SecretAccessKey, Type: TypeString, Secret: true, Group: GroupStorage, Label: "S3 secret access key", storage: true},
	{Key: KeyS3Prefix, Type: TypeString, Group: GroupStorage, Label: "S3 key prefix",
		Help: "Optional prefix for every object key, e.g. telesphorepo/.", storage: true,
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
		Help: "Needed by SeaweedFS and MinIO; not by R2 or AWS.", storage: true},
	{Key: KeyS3Redirect, Type: TypeBool, Default: "true", Group: GroupStorage, Label: "Redirect downloads to S3",
		Help: "GET downloads answer 302 with a presigned URL; off streams through the server.", storage: true},
	{Key: KeyPresignTTL, Type: TypeDuration, Default: "5m", Group: GroupStorage, Label: "Presigned URL lifetime"},

	// Security
	{Key: KeyCookieSecure, Type: TypeBool, Default: "true", Dev: "false", Group: GroupSecurity, Label: "Secure session cookie",
		Help: "Send the session cookie only over HTTPS. Turn off only for plain-HTTP setups."},
	{Key: KeySessionTTL, Type: TypeDuration, Default: "168h", Group: GroupSecurity, Label: "Session lifetime",
		Help: "Absolute lifetime of a UI sign-in."},
	{Key: KeyTrustedProxies, Type: TypeList, Group: GroupSecurity, Label: "Trusted proxies",
		Help: "CIDRs whose X-Forwarded-For header is trusted for the client IP, e.g. 10.0.0.0/8.",
		Validate: func(v any) error {
			for _, s := range v.([]string) {
				if _, err := netip.ParsePrefix(s); err != nil {
					return fmt.Errorf("%q is not a CIDR (e.g. 10.0.0.0/8)", s)
				}
			}
			return nil
		}},

	// Single sign-on (OIDC, admins only)
	{Key: KeyOIDCIssuer, Type: TypeString, Group: GroupSSO, Label: "OIDC issuer URL",
		Help:      "Issuer of your identity provider, e.g. https://kc.example.com/realms/ops. Setting it enables \"Sign in with SSO\" for administrators; saving checks the issuer's discovery document.",
		normalize: trim, Validate: httpURL},
	{Key: KeyOIDCClientID, Type: TypeString, Group: GroupSSO, Label: "Client ID",
		Help: "Client ID of the confidential client registered at the identity provider.", normalize: trim},
	{Key: KeyOIDCClientSecret, Type: TypeString, Secret: true, Group: GroupSSO, Label: "Client secret"},
	{Key: KeyOIDCRedirectURL, Type: TypeString, Group: GroupSSO, Label: "Redirect URL",
		Help:      "Callback URL registered at the identity provider (default: <public base URL>/api/auth/oidc/callback).",
		normalize: trim, Validate: httpURL},
	{Key: KeyOIDCScopes, Type: TypeString, Default: "openid profile email", Group: GroupSSO, Label: "Scopes",
		Help:      "Space-separated scopes requested at sign-in; openid is always included.",
		normalize: func(v any) any { return strings.Join(strings.Fields(v.(string)), " ") }},
	{Key: KeyOIDCAdminClaim, Type: TypeString, Default: "groups", Group: GroupSSO, Label: "Admin claim",
		Help:      "ID token claim checked for admin rights; dotted paths allowed (realm_access.roles for Keycloak realm roles).",
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
		Help: "The admin claim (a string or a list) must contain one of these, e.g. repo-admins."},
	{Key: KeyOIDCAdminEmails, Type: TypeList, Group: GroupSSO, Label: "Admin emails",
		Help: "Additional allowlist of admin email addresses; matched only when the provider marks the email verified.",
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
		Help: "Absolute lifetime of a UI sign-in through SSO."},
	{Key: KeyAdminPasswordLogin, Type: TypeBool, Default: "true", Group: GroupSSO, Label: "Password sign-in for admins",
		Help: "Off: local admins can no longer sign in to the UI with a password (publishers still can). Requires SSO. Break-glass: set TR_ADMIN_PASSWORD_LOGIN=true and restart."},

	// Signing
	{Key: KeySigningKey, Type: TypeString, Secret: true, Group: GroupSigning, Label: "Signing key",
		Help:        "Armored OpenPGP private key that signs repository indexes. Generated on first start; upload or regenerate it here.",
		apiReadOnly: true},
	{Key: KeyGPGKeyFile, Type: TypeString, Group: GroupSigning, Label: "Signing key file",
		Help:      "Path of a mounted armored private key; when set it replaces the stored signing key.",
		normalize: func(v any) any { return strings.TrimSpace(v.(string)) }},
	{Key: KeyGPGPassphraseFile, Type: TypeString, Group: GroupSigning, Label: "Signing key passphrase file",
		Help:      "Path of a file holding the signing key's passphrase (only for encrypted keys).",
		normalize: func(v any) any { return strings.TrimSpace(v.(string)) }},
}

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
	case TypeInt:
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return nil, errors.New("must be an integer")
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
	case TypeInt:
		var n int64
		if err := json.Unmarshal(raw, &n); err != nil {
			var s string
			if json.Unmarshal(raw, &s) != nil {
				return nil, errors.New("must be an integer")
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
	case TypeInt:
		if v.(int64) <= 0 {
			return nil, errors.New("must be a positive integer")
		}
	case TypeDuration:
		dur, err := time.ParseDuration(v.(string))
		if err != nil || dur <= 0 {
			return nil, errors.New(`must be a positive duration such as "300ms", "5m" or "168h"`)
		}
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
