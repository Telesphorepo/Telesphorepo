package main

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A server folder ~/.telesphorepo/<server>/ holds the accounts of this computer on that server,
// each a device of one user, and the files of the server (hosts.json, changes.json):
//
//	~/.telesphorepo/<server>/accounts/<user>/device.json   the device of user
//	~/.telesphorepo/<server>/accounts/<user>/exchange.json the helper's nonces of that device
//	~/.telesphorepo/<server>/active                        the user name of the active account
//
// Every command uses the active account (deviceOf). A folder from an older CLI holds one device
// in <server>/device.json, with no accounts/: that is the one account, and the CLI moves it into
// accounts/ when a second account comes.

// deviceFile is the device.json of an account, the one secret file of the CLI:
//
//	{"server": "https://repo.example.com", "user": "alice", "device_id": 7,
//	 "private_key": "<base64 PKCS#8>", "public_key": "<base64 raw 32 bytes>",
//	 "created_at": "2026-10-15T10:00:00Z"}
//
// "user" is the owner of the device as the server named it at the approval; a file of an older
// CLI has none. Only its owner can read it: mode 600 in a 700 folder on Linux and macOS, an ACL
// with the owner as the only entry on Windows.
type deviceFile struct {
	Server     string `json:"server"`
	User       string `json:"user,omitempty"`
	DeviceID   int64  `json:"device_id"`
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
	CreatedAt  string `json:"created_at"`

	id  identity           // the server
	key ed25519.PrivateKey // the parsed private key
	dir string             // the folder of the file
}

// hostsFile is hosts.json in the server folder: the Docker hosts of the server (GET
// /docker/hosts), e.g. {"hosts": ["repo.example.com"]}.
type hostsFile struct {
	Hosts []string `json:"hosts"`
}

// rootDir is ~/.telesphorepo.
func rootDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot find the home folder: %w", err)
	}
	return filepath.Join(home, ".telesphorepo"), nil
}

// serverDir is ~/.telesphorepo/<server> for the server id.
func serverDir(id identity) (string, error) {
	root, err := rootDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, id.deviceDir()), nil
}

// newDevice returns the device file of a key that the server took as device deviceID.
func newDevice(id identity, deviceID int64, key ed25519.PrivateKey) (*deviceFile, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return &deviceFile{
		Server: id.base(), DeviceID: deviceID,
		PrivateKey: base64.StdEncoding.EncodeToString(der),
		PublicKey:  base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)),
		CreatedAt:  time.Now().UTC().Truncate(time.Second).Format(time.RFC3339),
		id:         id, key: key,
	}, nil
}

// loadDevice reads the device file in dir. It returns nil and no error when there is none. A
// file that its group or other users can read is an error: the CLI does not use it.
func loadDevice(dir string) (*deviceFile, error) {
	f := filepath.Join(dir, "device.json")
	_, err := os.Stat(f)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := checkOwnerOnly(f); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return nil, err
	}
	var d deviceFile
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("%s is not a valid device file: %w", f, err)
	}
	if err := d.check(); err != nil {
		return nil, fmt.Errorf("%s is not a valid device file: %w", f, err)
	}
	d.dir = dir
	return &d, nil
}

// deviceOf returns the device of the active account of server id and the server folder. The
// device is nil when this computer has none. Every command that signs with a device reads it
// here, so all of them follow the active account.
func deviceOf(id identity) (*deviceFile, string, error) {
	dir, err := serverDir(id)
	if err != nil {
		return nil, "", err
	}
	ds, err := accountsIn(dir)
	if err != nil {
		return nil, dir, err
	}
	d := activeOf(dir, ds)
	if d != nil && d.id.base() != id.base() {
		return nil, dir, fmt.Errorf("%s holds the device of %s, not of %s", filepath.Join(d.dir, "device.json"), d.Server, id.base())
	}
	return d, dir, nil
}

// legacyAccount is the name of an account whose user the CLI does not know: "device-7".
func (d *deviceFile) accountName() string {
	if validAccount(d.User) {
		return d.User
	}
	return "device-" + strconv.FormatInt(d.DeviceID, 10)
}

// label names the account to a person: its user, or "device 7" when the CLI does not know it.
func (d *deviceFile) label() string {
	if validAccount(d.User) {
		return d.User
	}
	return "device " + strconv.FormatInt(d.DeviceID, 10)
}

var accountRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// validAccount reports whether user can name a folder under accounts/.
func validAccount(user string) bool { return accountRe.MatchString(user) }

// accountsIn returns the valid devices of the server folder dir, sorted by account name: the
// device of each accounts/<user>/, or the one device of a folder of an older CLI. A device file
// that is not valid is skipped; the reason goes to stderr. A device.json of an older CLI next to
// accounts/ (an older CLI ran after this one) moves into accounts/ first.
func accountsIn(dir string) ([]*deviceFile, error) {
	legacy, err := loadDevice(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	m, err := filepath.Glob(filepath.Join(dir, "accounts", "*", "device.json"))
	if err != nil {
		return nil, err
	}
	var ds []*deviceFile
	for _, f := range m {
		d, err := loadDevice(filepath.Dir(f))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		if d != nil {
			ds = append(ds, d)
		}
	}
	if legacy != nil {
		if len(m) == 0 {
			return []*deviceFile{legacy}, nil
		}
		if err := moveLegacy(dir, legacy); err != nil {
			return nil, err
		}
		ds = append(ds, legacy)
	}
	slices.SortFunc(ds, func(a, b *deviceFile) int { return strings.Compare(a.accountName(), b.accountName()) })
	return ds, nil
}

// activeOf returns the device of the active account among ds, the accounts of the server folder
// dir: the one that the file active names, else the first. It is nil when ds is empty.
func activeOf(dir string, ds []*deviceFile) *deviceFile {
	if len(ds) == 0 {
		return nil
	}
	b, _ := os.ReadFile(filepath.Join(dir, "active"))
	name := strings.TrimSpace(string(b))
	for _, d := range ds {
		if d.dir != dir && d.accountName() == name {
			return d
		}
	}
	return ds[0]
}

// setActive makes the account of d the active one of the server folder dir.
func setActive(dir string, d *deviceFile) error {
	if d.dir == dir {
		// The one device of an older CLI is active without the file.
		return nil
	}
	return writeOwnerOnly(filepath.Join(dir, "active"), []byte(d.accountName()+"\n"))
}

// moveLegacy moves the device file of an older CLI in the server folder dir, and its
// exchange.json, into accounts/<name>/. A rename is atomic, so a crash leaves the key in one of
// the two places, never in none.
func moveLegacy(dir string, d *deviceFile) error {
	to := filepath.Join(dir, "accounts", d.accountName())
	if _, err := os.Stat(filepath.Join(to, "device.json")); err == nil {
		to += "-" + strconv.FormatInt(time.Now().Unix(), 10)
	}
	if err := ownerOnlyDir(filepath.Join(dir, "accounts")); err != nil {
		return err
	}
	if err := ownerOnlyDir(to); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(dir, "device.json"), filepath.Join(to, "device.json")); err != nil {
		return fmt.Errorf("cannot move the device file into %s: %w", to, err)
	}
	_ = os.Rename(filepath.Join(dir, "exchange.json"), filepath.Join(to, "exchange.json"))
	d.dir = to
	return nil
}

// removeAccount deletes the files of the account of d. The server folder stays.
func removeAccount(dir string, d *deviceFile) error {
	if d.dir == dir {
		for _, f := range []string{"device.json", "exchange.json"} {
			if err := os.Remove(filepath.Join(dir, f)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		return nil
	}
	return os.RemoveAll(d.dir)
}

// check validates the fields and parses the server and the key.
func (d *deviceFile) check() error {
	id, err := parseIdentity(d.Server)
	if err != nil || id.base() != d.Server {
		return errors.New(`no valid "server"`)
	}
	if d.DeviceID <= 0 {
		return errors.New(`no valid "device_id"`)
	}
	der, err := base64.StdEncoding.DecodeString(d.PrivateKey)
	if err != nil {
		return errors.New(`no valid "private_key"`)
	}
	k, err := x509.ParsePKCS8PrivateKey(der)
	key, ok := k.(ed25519.PrivateKey)
	if err != nil || !ok {
		return errors.New(`no valid "private_key"`)
	}
	pub, err := base64.StdEncoding.DecodeString(d.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New(`no valid "public_key"`)
	}
	if _, err := time.Parse(time.RFC3339, d.CreatedAt); err != nil {
		return errors.New(`no valid "created_at"`)
	}
	d.id, d.key = id, key
	return nil
}

// save writes the device file as an account of the server folder dir and makes it the active
// one: in accounts/<user>/, over the device that account had. A device whose user the CLI does
// not know (a server that does not name it) on a computer with no other account goes to
// <server>/device.json, as an older CLI keeps it. A second account first moves that one into
// accounts/. Only the owner can read the files and the folders.
func (d *deviceFile) save(dir string) (string, error) {
	if err := ownerOnlyDir(dir); err != nil {
		return "", err
	}
	ds, err := accountsIn(dir)
	if err != nil {
		return "", err
	}
	to := dir
	if validAccount(d.User) || len(ds) > 1 || (len(ds) == 1 && ds[0].dir != dir) {
		for _, o := range ds {
			if o.dir == dir {
				if err := moveLegacy(dir, o); err != nil {
					return "", err
				}
			}
		}
		if err := ownerOnlyDir(filepath.Join(dir, "accounts")); err != nil {
			return "", err
		}
		to = filepath.Join(dir, "accounts", d.accountName())
	}
	if err := ownerOnlyDir(to); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return "", err
	}
	f := filepath.Join(to, "device.json")
	if err := writeOwnerOnly(f, append(b, '\n')); err != nil {
		return "", err
	}
	// The nonces of the device that the account had before.
	_ = os.Remove(filepath.Join(to, "exchange.json"))
	d.dir = to
	if err := setActive(dir, d); err != nil {
		return "", err
	}
	return f, nil
}

// saveHosts writes hosts.json in dir.
func saveHosts(dir string, hosts []string) error {
	if err := ownerOnlyDir(dir); err != nil {
		return err
	}
	h, err := json.MarshalIndent(hostsFile{Hosts: hosts}, "", "  ")
	if err != nil {
		return err
	}
	return writeOwnerOnly(filepath.Join(dir, "hosts.json"), append(h, '\n'))
}

// loadHosts reads the hosts of hosts.json in dir. A file that is missing or not valid has none.
func loadHosts(dir string) []string {
	var h hostsFile
	if b, err := os.ReadFile(filepath.Join(dir, "hosts.json")); err == nil && json.Unmarshal(b, &h) == nil {
		return h.Hosts
	}
	return nil
}

// hosts returns the Docker hosts of the device: those of hosts.json in dir, and the host of the
// server, each as "host:port" (registryKey).
func (d *deviceFile) hosts(dir string) []string {
	out := []string{d.id.host + ":" + fmt.Sprint(d.id.port)}
	for _, s := range loadHosts(dir) {
		if k, err := registryKey(s, d.id.scheme); err == nil && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// dockerHosts returns the Docker hosts of server id as Docker names them (host[:port]): the
// server's, then those of hosts.json in dir.
func dockerHosts(id identity, dir string) []string {
	out := []string{id.hostKey()}
	for _, s := range loadHosts(dir) {
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// enrolledDirs returns every server folder ~/.telesphorepo/* that holds a device file.
func enrolledDirs() ([]string, error) {
	root, err := rootDir()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, pat := range []string{filepath.Join(root, "*", "device.json"), filepath.Join(root, "*", "accounts", "*", "device.json")} {
		m, err := filepath.Glob(pat)
		if err != nil {
			return nil, err
		}
		for _, f := range m {
			dir := filepath.Dir(f)
			if filepath.Base(filepath.Dir(dir)) == "accounts" {
				dir = filepath.Dir(filepath.Dir(dir))
			}
			if !slices.Contains(out, dir) {
				out = append(out, dir)
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

// enrolledDevices returns the device of the active account of each server of this computer, and
// the server folders. A device file that is not valid is skipped; the reason goes to stderr.
func enrolledDevices() ([]*deviceFile, []string, error) {
	dirs, err := enrolledDirs()
	if err != nil {
		return nil, nil, err
	}
	var ds []*deviceFile
	var out []string
	for _, dir := range dirs {
		as, err := accountsIn(dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		if d := activeOf(dir, as); d != nil {
			ds = append(ds, d)
			out = append(out, dir)
		}
	}
	return ds, out, nil
}

// ownerOnlyDir creates dir (and its parents) so that only the owner can open dir.
func ownerOnlyDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return restrictDir(dir)
}

// writeOwnerOnly writes b to f through a temporary file that only the owner can read, then
// renames it over f.
func writeOwnerOnly(f string, b []byte) error {
	tmp := f + ".tmp"
	_ = os.Remove(tmp)
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("cannot write %s: %w", f, err)
	}
	if err := restrictFile(tmp); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if _, err := out.Write(b); err != nil {
		out.Close()
		os.Remove(tmp)
		return fmt.Errorf("cannot write %s: %w", f, err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("cannot write %s: %w", f, err)
	}
	if err := os.Rename(tmp, f); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("cannot write %s: %w", f, err)
	}
	return nil
}
