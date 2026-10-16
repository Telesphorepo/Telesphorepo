// Package config holds the boot environment (TR_DATA_DIR, TR_LISTEN, TR_MASTER_KEY and the
// bootstrap admin). Everything else is a setting (internal/settings).
package config

import (
	"errors"
	"os"
	"strings"
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
