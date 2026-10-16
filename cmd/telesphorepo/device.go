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
	"slices"
	"time"
)

// deviceFile is ~/.telesphorepo/<server>/device.json, the one secret file of the CLI:
//
//	{"server": "https://repo.example.com", "device_id": 7,
//	 "private_key": "<base64 PKCS#8>", "public_key": "<base64 raw 32 bytes>",
//	 "created_at": "2026-10-15T10:00:00Z"}
//
// Only its owner can read it: mode 600 in a 700 folder on Linux and macOS, an ACL with the owner
// as the only entry on Windows.
type deviceFile struct {
	Server     string `json:"server"`
	DeviceID   int64  `json:"device_id"`
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
	CreatedAt  string `json:"created_at"`

	id  identity           // the server
	key ed25519.PrivateKey // the parsed private key
}

// hostsFile is hosts.json next to the device file: the Docker hosts of the server (GET
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
	return &d, nil
}

// deviceOf returns the device of server id and its folder. The device is nil when this computer
// has none.
func deviceOf(id identity) (*deviceFile, string, error) {
	dir, err := serverDir(id)
	if err != nil {
		return nil, "", err
	}
	d, err := loadDevice(dir)
	if err != nil {
		return nil, dir, err
	}
	if d != nil && d.id.base() != id.base() {
		return nil, dir, fmt.Errorf("%s holds the device of %s, not of %s", filepath.Join(dir, "device.json"), d.Server, id.base())
	}
	return d, dir, nil
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

// save writes the device file in dir (a temporary file, then a rename). Only the owner can read
// it and the folder.
func (d *deviceFile) save(dir string) (string, error) {
	if err := ownerOnlyDir(dir); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return "", err
	}
	f := filepath.Join(dir, "device.json")
	if err := writeOwnerOnly(f, append(b, '\n')); err != nil {
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

// enrolledDirs returns every folder ~/.telesphorepo/* that holds a device file.
func enrolledDirs() ([]string, error) {
	root, err := rootDir()
	if err != nil {
		return nil, err
	}
	m, err := filepath.Glob(filepath.Join(root, "*", "device.json"))
	if err != nil {
		return nil, err
	}
	out := make([]string, len(m))
	for i, f := range m {
		out[i] = filepath.Dir(f)
	}
	return out, nil
}

// enrolledDevices returns the valid devices of this computer. A device file that is not valid
// is skipped; the reason goes to stderr.
func enrolledDevices() ([]*deviceFile, []string, error) {
	dirs, err := enrolledDirs()
	if err != nil {
		return nil, nil, err
	}
	var ds []*deviceFile
	var out []string
	for _, dir := range dirs {
		d, err := loadDevice(dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		if d != nil {
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
