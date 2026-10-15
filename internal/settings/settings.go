// Package settings implements the in-app settings store (spec §3): typed settings in the
// settings table (secrets AES-256-GCM encrypted), forced by TR_<KEY> env overrides, falling
// back to defaults. The effective values live in an immutable atomic snapshot that is rebuilt on
// every write; request paths read the snapshot, never the table.
package settings

import (
	"context"
	"crypto/cipher"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"

	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/storage"
)

// Source says where a setting's effective value comes from.
type Source string

const (
	SourceDB      Source = "db"
	SourceEnv     Source = "env"
	SourceDefault Source = "default"
)

// ValidationError reports invalid settings, keyed by setting key ("" for general errors).
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string {
	keys := slices.Sorted(maps.Keys(e.Fields))
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		if k == "" {
			parts = append(parts, e.Fields[k])
		} else {
			parts = append(parts, k+": "+e.Fields[k])
		}
	}
	return "invalid settings: " + strings.Join(parts, "; ")
}

func (e *ValidationError) add(key, msg string) {
	if _, ok := e.Fields[key]; !ok {
		e.Fields[key] = msg
	}
}

func (e *ValidationError) orNil() error {
	if len(e.Fields) == 0 {
		return nil
	}
	return e
}

func newVE() *ValidationError { return &ValidationError{Fields: map[string]string{}} }

// Snapshot is an immutable view of the effective settings.
type Snapshot struct {
	Config *config.Config

	values  map[string]any
	sources map[string]Source
	db      map[string]any // decoded rows of the settings table
}

// Value returns the effective value of key (see Type for representations).
func (s *Snapshot) Value(key string) any { return s.values[key] }

// Source returns where key's effective value comes from.
func (s *Snapshot) Source(key string) Source { return s.sources[key] }

// Typed reads of a setting's effective value (the zero value for an unknown key or another
// type). Format plugins read their own settings with these.

func (s *Snapshot) String(key string) string {
	v, _ := s.values[key].(string)
	return v
}

func (s *Snapshot) Bool(key string) bool {
	v, _ := s.values[key].(bool)
	return v
}

// Size returns a size setting in bytes.
func (s *Snapshot) Size(key string) int64 {
	v, _ := s.values[key].(int64)
	return v
}

func (s *Snapshot) List(key string) []string {
	v, _ := s.values[key].([]string)
	return v
}

func (s *Snapshot) Duration(key string) time.Duration {
	v, ok := s.values[key].(string)
	if !ok {
		return 0
	}
	return durationOf(v)
}

// Options configures Load.
type Options struct {
	// Dev makes the Dev value of every Def replace its Default. Used by "telesphorepo dev";
	// env still wins and the UI shows these as defaults, not as locked.
	Dev bool
	Log *slog.Logger
}

// Service owns the settings table and the effective snapshot.
type Service struct {
	db   *db.DB
	boot *config.Boot
	aead cipher.AEAD
	log  *slog.Logger
	dev  bool
	env  map[string]string // non-empty TR_* overrides, captured at Load

	mu   sync.Mutex // serializes writers
	snap atomic.Pointer[Snapshot]

	subsMu sync.Mutex
	subs   []func(old, new *Snapshot)
}

// Load reads the master key, the settings table and the env overrides and builds the first
// snapshot. Invalid env values and invalid combinations are errors; invalid or undecryptable
// stored values are logged and ignored (so the server can start and they can be fixed in the UI).
func Load(ctx context.Context, d *db.DB, boot *config.Boot, opts Options) (*Service, error) {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	master, err := loadMasterKey(boot, log)
	if err != nil {
		return nil, err
	}
	aead, err := newAEAD(master)
	if err != nil {
		return nil, err
	}
	s := &Service{db: d, boot: boot, aead: aead, log: log, dev: opts.Dev, env: map[string]string{}}
	for _, def := range registry {
		if v := os.Getenv(def.Env()); v != "" {
			s.env[def.Key] = v
		}
	}
	warnUnknownEnv(log)
	rows, err := s.readDB(ctx)
	if err != nil {
		return nil, err
	}
	snap, err := s.build(rows, true)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	s.snap.Store(snap)
	return s, nil
}

// bootEnv are TR_* variables read outside the settings registry (boot config, tooling).
var bootEnv = map[string]bool{
	"TR_DATA_DIR": true, "TR_LISTEN": true, "TR_MASTER_KEY": true,
	"TR_BOOTSTRAP_ADMIN_USER": true, "TR_BOOTSTRAP_ADMIN_PASSWORD": true,
	"TR_ENGINE": true, // container engine selection (dev tooling, e2e)
}

// warnUnknownEnv logs TR_* variables that match no setting or boot variable (typos, removed
// or not-yet-supported settings), naming only the variable, never its value.
func warnUnknownEnv(log *slog.Logger) {
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(name, "TR_") || bootEnv[name] || strings.HasPrefix(name, "TR_E2E_") {
			continue
		}
		known := false
		for _, def := range registry {
			if def.Env() == name {
				known = true
				break
			}
		}
		if !known {
			log.Warn("ignoring unknown environment variable; it matches no setting (see Admin > Settings)", "var", name)
		}
	}
}

// Current returns the current snapshot.
func (s *Service) Current() *Snapshot { return s.snap.Load() }

// Config returns the current effective configuration (usable as a config.Provider).
func (s *Service) Config() *config.Config { return s.snap.Load().Config }

// Subscribe registers fn to run after every applied change, with the previous and the new
// snapshot. fn runs synchronously in the writer; keep it short.
func (s *Service) Subscribe(fn func(old, new *Snapshot)) {
	s.subsMu.Lock()
	s.subs = append(s.subs, fn)
	s.subsMu.Unlock()
}

// readDB returns the decoded settings rows, skipping (and logging) unknown, undecryptable or
// invalid ones.
func (s *Service) readDB(ctx context.Context) (map[string]any, error) {
	rows, err := dbq.New(s.db.R).ListSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	out := map[string]any{}
	for _, row := range rows {
		key := row.Key
		def := Lookup(key)
		if def == nil {
			s.log.Warn("ignoring unknown stored setting", "key", key)
			continue
		}
		raw := []byte(row.Value)
		if row.Secret {
			if raw, err = open(s.aead, key, row.Value); err != nil {
				s.log.Warn("ignoring stored secret setting: "+err.Error()+"; set it again", "key", key)
				continue
			}
		}
		v, err := def.decodeJSON(raw)
		if err != nil {
			s.log.Warn("ignoring invalid stored setting", "key", key, "error", err.Error())
			continue
		}
		out[key] = v
	}
	return out, nil
}

func (s *Service) defaultText(def *Def) string {
	if s.dev && def.Dev != "" {
		return def.Dev
	}
	if def.Key == KeyFSRoot {
		return config.JoinDataDir(s.boot.DataDir, "blobs")
	}
	return def.Default
}

// build computes the effective snapshot from stored rows, env overrides and defaults, and
// validates the whole configuration. lenient (startup) downgrades failures caused by stored
// SSO settings to warnings.
func (s *Service) build(rows map[string]any, lenient bool) (*Snapshot, error) {
	ve := newVE()
	snap := &Snapshot{values: map[string]any{}, sources: map[string]Source{}, db: rows}
	for _, def := range registry {
		if text, ok := s.env[def.Key]; ok {
			v, err := def.parseText(text)
			if err != nil {
				ve.add(def.Key, def.Env()+": "+err.Error())
				continue
			}
			snap.values[def.Key], snap.sources[def.Key] = v, SourceEnv
			continue
		}
		if v, ok := rows[def.Key]; ok {
			snap.values[def.Key], snap.sources[def.Key] = v, SourceDB
			continue
		}
		v, err := def.parseText(s.defaultText(def))
		if err != nil {
			ve.add(def.Key, "invalid default: "+err.Error())
			continue
		}
		snap.values[def.Key], snap.sources[def.Key] = v, SourceDefault
	}
	if err := ve.orNil(); err != nil {
		return nil, err
	}
	str := func(k string) string { return snap.values[k].(string) }
	b := func(k string) bool { return snap.values[k].(bool) }
	n := func(k string) int64 { return snap.values[k].(int64) }

	c := &config.Config{
		Listen:                 s.boot.Listen,
		DataDir:                s.boot.DataDir,
		BootstrapAdminUser:     s.boot.BootstrapAdminUser,
		BootstrapAdminPassword: s.boot.BootstrapAdminPassword,
		BaseURL:                str(KeyBaseURL),
		Storage:                str(KeyStorage),
		FSRoot:                 str(KeyFSRoot),
		S3Endpoint:             str(KeyS3Endpoint),
		S3Region:               str(KeyS3Region),
		S3Bucket:               str(KeyS3Bucket),
		S3AccessKeyID:          str(KeyS3AccessKeyID),
		S3SecretAccessKey:      str(KeyS3SecretAccessKey),
		S3Prefix:               str(KeyS3Prefix),
		S3PathStyle:            b(KeyS3PathStyle),
		S3Redirect:             b(KeyS3Redirect),
		PresignTTL:             durationOf(snap.values[KeyPresignTTL]),
		MaxUploadBytes:         n(KeyMaxUploadSize),
		MaxJSONBytes:           n(KeyMaxJSONSize),
		CookieSecure:           b(KeyCookieSecure),
		SessionTTL:             durationOf(snap.values[KeySessionTTL]),
	}
	if c.FSRoot == "" {
		c.FSRoot = config.JoinDataDir(s.boot.DataDir, "blobs")
	}
	c.TrustedProxies = snap.values[KeyTrustedProxies].([]string) // validated
	switch str(KeyLogLevel) {
	case "debug":
		c.LogLevel = slog.LevelDebug
	case "warn":
		c.LogLevel = slog.LevelWarn
	case "error":
		c.LogLevel = slog.LevelError
	default:
		c.LogLevel = slog.LevelInfo
	}
	if c.Storage == "s3" {
		for _, k := range []string{KeyS3Endpoint, KeyS3Bucket, KeyS3AccessKeyID, KeyS3SecretAccessKey} {
			if str(k) == "" {
				ve.add(k, "required when storage is s3")
			}
		}
	}
	snap.Config = c
	strict := func(k string) bool { return !lenient || snap.sources[k] == SourceEnv }

	// SSO (design doc §7.5).
	c.OIDCIssuer = str(KeyOIDCIssuer)
	c.OIDCClientID = str(KeyOIDCClientID)
	c.OIDCClientSecret = str(KeyOIDCClientSecret)
	c.OIDCRedirectURL = str(KeyOIDCRedirectURL)
	if c.OIDCRedirectURL == "" && c.BaseURL != "" {
		c.OIDCRedirectURL = c.BaseURL + config.OIDCCallbackPath
	}
	c.OIDCScopes = strings.Fields(str(KeyOIDCScopes))
	if !slices.Contains(c.OIDCScopes, "openid") {
		c.OIDCScopes = append([]string{"openid"}, c.OIDCScopes...)
	}
	c.OIDCAdminClaim = str(KeyOIDCAdminClaim)
	c.OIDCAdminValues = snap.values[KeyOIDCAdminValues].([]string)
	c.OIDCAdminEmails = snap.values[KeyOIDCAdminEmails].([]string)
	c.OIDCSessionTTL = durationOf(snap.values[KeyOIDCSessionTTL])
	c.AdminPasswordLogin = b(KeyAdminPasswordLogin)
	if c.OIDCIssuer != "" {
		sso := newVE()
		if c.OIDCClientID == "" {
			sso.add(KeyOIDCClientID, "required when SSO is enabled")
		}
		if c.OIDCClientSecret == "" {
			sso.add(KeyOIDCClientSecret, "required when SSO is enabled")
		}
		if len(c.OIDCAdminValues) == 0 && len(c.OIDCAdminEmails) == 0 {
			sso.add(KeyOIDCAdminValues, "set admin claim values or admin emails (at least one) when SSO is enabled")
		}
		if c.OIDCRedirectURL == "" {
			sso.add(KeyOIDCRedirectURL, "required when SSO is enabled and the public base URL is empty")
		}
		if len(sso.Fields) > 0 {
			if strict(KeyOIDCIssuer) {
				for k, m := range sso.Fields {
					ve.add(k, m)
				}
			} else {
				// e.g. the client secret could not be decrypted: start without SSO.
				s.log.Error("SSO settings incomplete; SSO is disabled until they are fixed", "error", sso.Error())
				c.OIDCIssuer = ""
			}
		}
	}
	if !c.AdminPasswordLogin && c.OIDCIssuer == "" {
		if strict(KeyAdminPasswordLogin) {
			ve.add(KeyAdminPasswordLogin, "can be turned off only while SSO is enabled (set the OIDC issuer)")
		} else {
			s.log.Error("admin password sign-in is off but SSO is not enabled; allowing admin password sign-in")
			c.AdminPasswordLogin = true
		}
	}

	if err := ve.orNil(); err != nil {
		return nil, err
	}
	return snap, nil
}

// Item is one setting as shown by the API (GET /api/settings).
type Item struct {
	Key     string   `json:"key"`
	Env     string   `json:"env"`
	Group   string   `json:"group"`
	Label   string   `json:"label"`
	Help    string   `json:"help,omitempty"`
	Type    Type     `json:"type"`
	Options []string `json:"options,omitempty"`
	Secret  bool     `json:"secret"`
	// Value is the effective value; for secrets it is SecretValue (never the secret itself).
	Value   any `json:"value"`
	Default any `json:"default"` // nil for secrets
	// Display and DefaultDisplay put a size or a duration in words ("512 MiB", "90 days").
	Display        string `json:"display,omitempty"`
	DefaultDisplay string `json:"default_display,omitempty"`
	Source         Source `json:"source"`
	// Locked: forced by the environment variable Env, read-only in the UI.
	Locked bool `json:"locked"`
}

// SecretValue is what List reports for a secret setting.
type SecretValue struct {
	Set bool `json:"set"`
}

// List returns every setting in registry order (grouped by Group, see Groups).
func (s *Service) List(ctx context.Context) []Item {
	snap := s.Current()
	out := make([]Item, 0, len(registry))
	for _, def := range registry {
		it := Item{
			Key: def.Key, Env: def.Env(), Group: def.Group, Label: def.Label, Help: def.Help,
			Type: def.Type, Options: def.Options, Secret: def.Secret,
			Source: snap.sources[def.Key], Locked: snap.sources[def.Key] == SourceEnv,
		}
		if def.Key == KeyStorage {
			it.Options = []string{"fs", "s3"}
		}
		if def.Secret {
			it.Value = SecretValue{Set: snap.values[def.Key].(string) != ""}
		} else {
			it.Value = snap.values[def.Key]
			if dv, err := def.parseText(s.defaultText(def)); err == nil {
				it.Default = dv
			}
		}
		it.Display, it.DefaultDisplay = display(def.Type, it.Value), display(def.Type, it.Default)
		if l, ok := it.Value.([]string); ok && l == nil {
			it.Value = []string{}
		}
		if l, ok := it.Default.([]string); ok && l == nil {
			it.Default = []string{}
		}
		out = append(out, it)
	}
	return out
}

// display puts a size or a duration value in words; other types give "".
func display(t Type, v any) string {
	switch t {
	case TypeSize:
		if n, ok := v.(int64); ok {
			return formatSize(n)
		}
	case TypeDuration:
		if s, ok := v.(string); ok {
			return formatDuration(durationOf(s))
		}
	}
	return ""
}

// candidate applies patch (JSON values; null resets to the default) to the stored rows of snap.
// It returns the new rows and the keys whose stored row changes.
func (s *Service) candidate(snap *Snapshot, patch map[string]json.RawMessage) (map[string]any, []string, *ValidationError) {
	ve := newVE()
	rows := maps.Clone(snap.db)
	var changed []string
	for key, raw := range patch {
		def := Lookup(key)
		if def == nil {
			ve.add(key, "unknown setting")
			continue
		}
		if string(raw) == "null" || len(raw) == 0 {
			if _, ok := rows[key]; ok {
				delete(rows, key)
				changed = append(changed, key)
			}
			continue
		}
		v, err := def.decodeJSON(raw)
		if err != nil {
			ve.add(key, err.Error())
			continue
		}
		if def.Secret && v == "" { // clearing a secret = reset
			if _, ok := rows[key]; ok {
				delete(rows, key)
				changed = append(changed, key)
			}
			continue
		}
		if snap.sources[key] == SourceEnv {
			if !def.Secret && reflect.DeepEqual(v, snap.values[key]) {
				continue // the UI echoing a locked value back is fine
			}
			ve.add(key, "set by environment variable "+def.Env()+"; change it there")
			continue
		}
		if old, ok := rows[key]; ok && reflect.DeepEqual(old, v) {
			continue
		}
		rows[key] = v
		changed = append(changed, key)
	}
	sort.Strings(changed)
	return rows, changed, ve
}

// Update applies a partial update (setting key -> JSON value; null resets to the default),
// validating the whole resulting configuration first. Invalid input returns a
// *ValidationError. Storage changes must be checked with TestStorage first by the caller; Update
// only verifies the new store can be constructed. userID 0 records no author.
func (s *Service) Update(ctx context.Context, patch map[string]json.RawMessage, userID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.Current()
	rows, changed, ve := s.candidate(cur, patch)
	if err := ve.orNil(); err != nil {
		return err
	}
	if len(changed) == 0 {
		return nil
	}
	next, err := s.build(rows, false)
	if err != nil {
		return err
	}
	if !storage.SameBackend(cur.Config, next.Config) {
		if _, err := storage.Open(ctx, next.Config); err != nil {
			return &ValidationError{Fields: map[string]string{KeyStorage: err.Error()}}
		}
	}
	return s.write(ctx, rows, changed, userID)
}

// write persists the changed keys of rows, reloads the snapshot and notifies subscribers.
// Caller holds s.mu.
func (s *Service) write(ctx context.Context, rows map[string]any, changed []string, userID int64) error {
	var by sql.NullInt64
	if userID > 0 {
		by = sql.NullInt64{Int64: userID, Valid: true}
	}
	now := time.Now().Unix()
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(s.db.R).WithTx(tx)
		for _, key := range changed {
			v, ok := rows[key]
			if !ok {
				if err := q.DeleteSetting(ctx, key); err != nil {
					return err
				}
				continue
			}
			def := Lookup(key)
			raw, err := json.Marshal(v)
			if err != nil {
				return err
			}
			value := string(raw)
			if def.Secret {
				value = seal(s.aead, key, raw)
			}
			if err := q.UpsertSetting(ctx, dbq.UpsertSettingParams{
				Key: key, Value: value, Secret: def.Secret, UpdatedAt: now, UpdatedBy: by,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	return s.reload(ctx, changed)
}

func (s *Service) reload(ctx context.Context, changed []string) error {
	rows, err := s.readDB(ctx)
	if err != nil {
		return err
	}
	next, err := s.build(rows, true)
	if err != nil {
		return err
	}
	old := s.snap.Swap(next)
	s.log.Info("settings changed", "keys", strings.Join(changed, ","))
	s.subsMu.Lock()
	subs := slices.Clone(s.subs)
	s.subsMu.Unlock()
	for _, fn := range subs {
		fn(old, next)
	}
	return nil
}

// TestStorage checks the storage configuration that patch would produce (current settings when
// patch is empty) by writing, reading and deleting a probe object. An invalid patch returns a
// *ValidationError; a failed probe returns an error whose message is meant for the
// administrator. Nothing is saved.
func (s *Service) TestStorage(ctx context.Context, patch map[string]json.RawMessage) error {
	cur := s.Current()
	rows, _, ve := s.candidate(cur, patch)
	if err := ve.orNil(); err != nil {
		return err
	}
	next, err := s.build(rows, true)
	if err != nil {
		return err
	}
	st, err := storage.Open(ctx, next.Config)
	if err != nil {
		return err
	}
	return storage.Probe(ctx, st)
}

// Preview returns the effective configuration that patch would produce, without saving it.
// An invalid patch returns a *ValidationError.
func (s *Service) Preview(patch map[string]json.RawMessage) (*config.Config, error) {
	rows, _, ve := s.candidate(s.Current(), patch)
	if err := ve.orNil(); err != nil {
		return nil, err
	}
	next, err := s.build(rows, false)
	if err != nil {
		return nil, err
	}
	return next.Config, nil
}

// TestOIDC checks the SSO configuration that patch would produce: the whole configuration must
// validate, and when SSO is enabled the issuer's discovery document must be fetchable (10 s
// timeout). Failures are *ValidationError; discovery errors are reported under oidc.issuer.
// Nothing is saved.
func (s *Service) TestOIDC(ctx context.Context, patch map[string]json.RawMessage) error {
	cur := s.Current()
	rows, _, ve := s.candidate(cur, patch)
	if err := ve.orNil(); err != nil {
		return err
	}
	next, err := s.build(rows, false)
	if err != nil {
		return err
	}
	if next.Config.OIDCIssuer == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := gooidc.NewProvider(ctx, next.Config.OIDCIssuer); err != nil {
		return &ValidationError{Fields: map[string]string{KeyOIDCIssuer: "OIDC discovery failed: " + err.Error()}}
	}
	return nil
}
