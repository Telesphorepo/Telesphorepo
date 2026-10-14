// Package config loads server configuration from TR_* environment variables (spec §3).
package config

import (
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

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

	CookieSecure   bool
	SessionTTL     time.Duration
	TrustedProxies []netip.Prefix

	GPGKeyFile        string
	GPGPassphraseFile string
	DebDebounce       time.Duration
	DebMaxDelay       time.Duration

	BootstrapAdminUser     string
	BootstrapAdminPassword string

	LogLevel slog.Level
}

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

// SigningKeyFileName is the auto-generated signing key's name inside the data dir.
const SigningKeyFileName = "signing.asc"

// DefaultSigningKeyFile is where the signing key lives when TR_GPG_KEY_FILE is unset.
func (c *Config) DefaultSigningKeyFile() string { return joinOS(c.DataDir, SigningKeyFileName) }

// DataDirFromEnv returns TR_DATA_DIR or DefaultDataDir, without loading the rest of the config.
func DataDirFromEnv() string { return env("TR_DATA_DIR", DefaultDataDir) }

// Load reads the configuration from TR_* environment variables.
func Load() (*Config, error) { return LoadWithDefaults(nil) }

// LoadWithDefaults is Load with overridden built-in defaults (keyed by env var name, e.g.
// "TR_DATA_DIR"). Values set in the environment still win. Used by "telesphorepo dev".
func LoadWithDefaults(defaults map[string]string) (*Config, error) {
	overrides = defaults
	defer func() { overrides = nil }()
	var errs []string
	c := &Config{
		Listen:            env("TR_LISTEN", ":8080"),
		DataDir:           env("TR_DATA_DIR", DefaultDataDir),
		BaseURL:           env("TR_BASE_URL", ""),
		Storage:           strings.ToLower(env("TR_STORAGE", "fs")),
		S3Endpoint:        env("TR_S3_ENDPOINT", ""),
		S3Region:          env("TR_S3_REGION", "auto"),
		S3Bucket:          env("TR_S3_BUCKET", ""),
		S3AccessKeyID:     env("TR_S3_ACCESS_KEY_ID", ""),
		S3SecretAccessKey: env("TR_S3_SECRET_ACCESS_KEY", ""),
		S3Prefix:          strings.Trim(env("TR_S3_PREFIX", ""), "/"),
		GPGKeyFile:        env("TR_GPG_KEY_FILE", ""),
		GPGPassphraseFile: env("TR_GPG_PASSPHRASE_FILE", ""),

		BootstrapAdminUser:     env("TR_BOOTSTRAP_ADMIN_USER", ""),
		BootstrapAdminPassword: env("TR_BOOTSTRAP_ADMIN_PASSWORD", ""),
	}
	if (c.BootstrapAdminUser == "") != (c.BootstrapAdminPassword == "") {
		errs = append(errs, "TR_BOOTSTRAP_ADMIN_USER and TR_BOOTSTRAP_ADMIN_PASSWORD must be set together")
	}
	if c.Storage == "r2" {
		c.Storage = "s3"
	}
	c.FSRoot = env("TR_FS_ROOT", joinOS(c.DataDir, "blobs"))
	if c.S3Prefix != "" {
		c.S3Prefix = path.Clean(c.S3Prefix)
	}

	c.S3PathStyle = boolEnv("TR_S3_PATH_STYLE", false, &errs)
	c.S3Redirect = boolEnv("TR_S3_REDIRECT", true, &errs)
	c.CookieSecure = boolEnv("TR_COOKIE_SECURE", true, &errs)
	c.PresignTTL = durEnv("TR_PRESIGN_TTL", 5*time.Minute, &errs)
	c.SessionTTL = durEnv("TR_SESSION_TTL", 168*time.Hour, &errs)
	c.DebDebounce = durEnv("TR_DEB_DEBOUNCE", 2*time.Second, &errs)
	c.DebMaxDelay = durEnv("TR_DEB_MAX_DELAY", 30*time.Second, &errs)
	c.MaxUploadBytes = intEnv("TR_MAX_UPLOAD_BYTES", 2<<30, &errs)
	c.MaxJSONBytes = intEnv("TR_MAX_JSON_BYTES", 1<<20, &errs)

	for _, s := range strings.Split(env("TR_TRUSTED_PROXIES", ""), ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			errs = append(errs, "TR_TRUSTED_PROXIES: "+err.Error())
			continue
		}
		c.TrustedProxies = append(c.TrustedProxies, p)
	}

	switch strings.ToLower(env("TR_LOG_LEVEL", "info")) {
	case "debug":
		c.LogLevel = slog.LevelDebug
	case "info":
		c.LogLevel = slog.LevelInfo
	case "warn":
		c.LogLevel = slog.LevelWarn
	case "error":
		c.LogLevel = slog.LevelError
	default:
		errs = append(errs, "TR_LOG_LEVEL: must be debug|info|warn|error")
	}

	switch c.Storage {
	case "fs":
	case "s3":
		for k, v := range map[string]string{
			"TR_S3_ENDPOINT": c.S3Endpoint, "TR_S3_BUCKET": c.S3Bucket,
			"TR_S3_ACCESS_KEY_ID": c.S3AccessKeyID, "TR_S3_SECRET_ACCESS_KEY": c.S3SecretAccessKey,
		} {
			if v == "" {
				errs = append(errs, k+" is required when TR_STORAGE=s3")
			}
		}
	default:
		errs = append(errs, "TR_STORAGE: must be fs or s3")
	}

	if len(errs) > 0 {
		return nil, fmt.Errorf("config: %s", strings.Join(errs, "; "))
	}
	return c, nil
}

// overrides holds LoadWithDefaults' replacement defaults for the duration of a load.
var overrides map[string]string

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	if v, ok := overrides[k]; ok {
		return v
	}
	return def
}

func boolEnv(k string, def bool, errs *[]string) bool {
	v := env(k, "")
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		*errs = append(*errs, k+": "+err.Error())
		return def
	}
	return b
}

func durEnv(k string, def time.Duration, errs *[]string) time.Duration {
	v := env(k, "")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		*errs = append(*errs, k+": invalid duration")
		return def
	}
	return d
}

func intEnv(k string, def int64, errs *[]string) int64 {
	v := env(k, "")
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		*errs = append(*errs, k+": invalid positive integer")
		return def
	}
	return n
}
