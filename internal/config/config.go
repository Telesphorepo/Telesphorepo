// Package config holds the boot environment (TR_DATA_DIR, TR_LISTEN, TR_MASTER_KEY, spec §3) and
// the effective-configuration struct built by internal/settings from DB settings, TR_* env
// overrides and defaults. Services read it through a Provider so setting changes apply live.
package config

import (
	"errors"
	"log/slog"
	"os"
	"strings"
	"time"
)

// Boot is the bare-minimum boot environment. Everything else is an in-app setting.
type Boot struct {
	DataDir   string
	Listen    string
	MasterKey string // base64, optional (else <DataDir>/master.key)

	// Test/automation override: create this admin when the users table is empty.
	BootstrapAdminUser     string
	BootstrapAdminPassword string
}

// LoadBoot reads the boot environment. defaults replaces built-in defaults (keyed by env var
// name, e.g. "TR_DATA_DIR"); non-empty environment values still win.
func LoadBoot(defaults map[string]string) (*Boot, error) {
	get := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		if v, ok := defaults[k]; ok {
			return v
		}
		return def
	}
	b := &Boot{
		DataDir:                get("TR_DATA_DIR", DefaultDataDir),
		Listen:                 get("TR_LISTEN", ":8080"),
		MasterKey:              get("TR_MASTER_KEY", ""),
		BootstrapAdminUser:     get("TR_BOOTSTRAP_ADMIN_USER", ""),
		BootstrapAdminPassword: get("TR_BOOTSTRAP_ADMIN_PASSWORD", ""),
	}
	if (b.BootstrapAdminUser == "") != (b.BootstrapAdminPassword == "") {
		return nil, errors.New("config: TR_BOOTSTRAP_ADMIN_USER and TR_BOOTSTRAP_ADMIN_PASSWORD must be set together")
	}
	return b, nil
}

// DBFile is the SQLite database path.
func (b *Boot) DBFile() string { return joinOS(b.DataDir, "telesphorepo.db") }

// TmpDir is the upload spool directory (on the data volume, not tmpfs).
func (b *Boot) TmpDir() string { return joinOS(b.DataDir, "tmp") }

// Config is the effective configuration: boot values plus the current settings snapshot.
// A *Config is immutable once published; never modify one obtained from a Provider.
type Config struct {
	Listen  string
	DataDir string
	BaseURL string

	Storage string // "fs" or "s3"
	FSRoot  string

	S3Endpoint        string
	S3Region          string
	S3Bucket          string
	S3AccessKeyID     string
	S3SecretAccessKey string
	S3Prefix          string
	S3PathStyle       bool
	S3Redirect        bool
	PresignTTL        time.Duration

	MaxUploadBytes int64
	MaxJSONBytes   int64

	CookieSecure bool
	SessionTTL   time.Duration
	// TrustedProxies are CIDRs, IP addresses, "private" and IP lists as "list:<name>" (see
	// internal/netrules): the TCP peers whose X-Forwarded-For the server reads.
	TrustedProxies []string

	// OIDC admin sign-on (design doc §7.5). SSO is enabled iff OIDCIssuer is set.
	OIDCIssuer         string
	OIDCClientID       string
	OIDCClientSecret   string
	OIDCRedirectURL    string   // effective: oidc.redirect_url, else BaseURL + OIDCCallbackPath
	OIDCScopes         []string // split on whitespace
	OIDCAdminClaim     string   // dotted claim path, e.g. "groups" or "realm_access.roles"
	OIDCAdminValues    []string
	OIDCAdminEmails    []string
	OIDCSessionTTL     time.Duration
	AdminPasswordLogin bool // false: local admins cannot use UI password login

	BootstrapAdminUser     string
	BootstrapAdminPassword string

	LogLevel slog.Level
}

// OIDCCallbackPath is the OIDC redirect endpoint, appended to BaseURL when oidc.redirect_url is
// empty.
const OIDCCallbackPath = "/api/auth/oidc/callback"

// OIDCEnabled reports whether SSO is configured.
func (c *Config) OIDCEnabled() bool { return c.OIDCIssuer != "" }

// Provider returns the current effective configuration (an atomic snapshot; cheap to call on
// every request).
type Provider func() *Config

// Static returns a Provider that always returns c (offline CLI commands).
func Static(c *Config) Provider { return func() *Config { return c } }

// DBFile is the SQLite database path.
func (c *Config) DBFile() string { return joinOS(c.DataDir, "telesphorepo.db") }

// TmpDir is the upload spool directory (on the data volume, not tmpfs).
func (c *Config) TmpDir() string { return joinOS(c.DataDir, "tmp") }

func joinOS(dir, name string) string {
	if strings.HasSuffix(dir, "/") || strings.HasSuffix(dir, `\`) {
		return dir + name
	}
	return dir + string(os.PathSeparator) + name
}

// DefaultDataDir is the data directory when TR_DATA_DIR is unset (the image sets /data).
const DefaultDataDir = "./data"

// MasterKeyFileName holds the generated master key when TR_MASTER_KEY is unset.
const MasterKeyFileName = "master.key"

// DevDataDir is the data directory of "telesphorepo dev" when TR_DATA_DIR is unset.
const DevDataDir = "./.telesphorepo-dev"

// DevAdminPasswordFile is the file in the dev data dir holding the generated admin password.
const DevAdminPasswordFile = "admin-password.txt"

// CLIDataDir is the data dir for offline commands (user add/passwd): TR_DATA_DIR if set;
// otherwise ./data, unless ./data does not exist and the dev data dir does (so the commands
// work against "telesphorepo dev" without extra setup). fromDev reports the fallback.
func CLIDataDir() (dir string, fromDev bool) {
	if v := os.Getenv("TR_DATA_DIR"); v != "" {
		return v, false
	}
	if _, err := os.Stat(DefaultDataDir); err != nil {
		if st, err := os.Stat(DevDataDir); err == nil && st.IsDir() {
			return DevDataDir, true
		}
	}
	return DefaultDataDir, false
}

// JoinDataDir joins a file name onto a data dir using the OS separator.
func JoinDataDir(dir, name string) string { return joinOS(dir, name) }
