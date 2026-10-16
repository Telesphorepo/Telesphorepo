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
	"runtime"
	"slices"
	"time"
)

// clientID is the publisher client of the helper on the server.
const clientID = "docker"

// deviceFile is ~/.telesphorepo/<dir>/docker/device.json, with the schema of the device file of
// the Gradle and Maven clients:
//
//	{"server": "https://repo.example.com", "device_id": 7, "client": "docker",
//	 "private_key": "<base64 PKCS#8>", "public_key": "<base64 raw 32 bytes>",
//	 "created_at": "2026-10-15T10:00:00Z"}
//
// Only its owner can read it: mode 600 in a 700 folder on Linux and macOS, an ACL with the owner
// as the only entry on Windows.
type deviceFile struct {
	Server     string `json:"server"`
	DeviceID   int64  `json:"device_id"`
	Client     string `json:"client"`
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
	CreatedAt  string `json:"created_at"`

	id  identity           // the server
	key ed25519.PrivateKey // the parsed private key
}

// hostsFile is hosts.json next to the device file: the Docker hosts of the server (GET
// /docker/hosts), e.g. {"hosts": ["repo.example.com", "push.repo.example.com"]}.
type hostsFile struct {
	Hosts []string `json:"hosts"`
}

var clientIDRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// rootDir is ~/.telesphorepo.
func rootDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot find the home folder: %w", err)
	}
	return filepath.Join(home, ".telesphorepo"), nil
}

// deviceDirOf is ~/.telesphorepo/<dir>/docker for the server id.
func deviceDirOf(id identity) (string, error) {
	root, err := rootDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, id.deviceDir(), "docker"), nil
}

// newDevice returns the device file of a key that the server approved as device deviceID.
func newDevice(id identity, deviceID int64, key ed25519.PrivateKey) (*deviceFile, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return &deviceFile{
		Server: id.base(), DeviceID: deviceID, Client: clientID,
		PrivateKey: base64.StdEncoding.EncodeToString(der),
		PublicKey:  base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)),
		CreatedAt:  time.Now().UTC().Truncate(time.Second).Format(time.RFC3339),
		id:         id, key: key,
	}, nil
}

// loadDevice reads the device file in dir. It returns nil and no error when there is none. A
// file that its group or other users can read is an error: the helper does not use it.
func loadDevice(dir string) (*deviceFile, error) {
	f := filepath.Join(dir, "device.json")
	st, err := os.Stat(f)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s must be readable by its owner only. Run: chmod 600 %s", f, f)
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

// check validates the fields and parses the server and the key.
func (d *deviceFile) check() error {
	id, err := parseIdentity(d.Server)
	if err != nil || id.base() != d.Server {
		return errors.New(`no valid "server"`)
	}
	if d.DeviceID <= 0 {
		return errors.New(`no valid "device_id"`)
	}
	if !clientIDRE.MatchString(d.Client) {
		return errors.New(`no valid "client"`)
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

// save writes the device file and hosts.json in dir at once (a temporary file, then a rename).
// Only the owner can read them and the folder.
func (d *deviceFile) save(dir string, hosts []string) (string, error) {
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
	h, err := json.MarshalIndent(hostsFile{Hosts: hosts}, "", "  ")
	if err != nil {
		return "", err
	}
	if err := writeOwnerOnly(filepath.Join(dir, "hosts.json"), append(h, '\n')); err != nil {
		return "", err
	}
	return f, nil
}

// hosts returns the Docker hosts of the device: those of hosts.json in dir, and the host of the
// server, each as "host:port" (registryKey).
func (d *deviceFile) hosts(dir string) []string {
	def := d.id.scheme
	out := []string{d.id.host + ":" + fmt.Sprint(d.id.port)}
	var h hostsFile
	if b, err := os.ReadFile(filepath.Join(dir, "hosts.json")); err == nil && json.Unmarshal(b, &h) == nil {
		for _, s := range h.Hosts {
			if k, err := registryKey(s, def); err == nil && !slices.Contains(out, k) {
				out = append(out, k)
			}
		}
	}
	return out
}

// dockerHosts returns the hosts of the device as Docker names them (host[:port]): the server's,
// then those of hosts.json.
func (d *deviceFile) dockerHosts(dir string) []string {
	out := []string{d.id.hostKey()}
	var h hostsFile
	if b, err := os.ReadFile(filepath.Join(dir, "hosts.json")); err == nil && json.Unmarshal(b, &h) == nil {
		for _, s := range h.Hosts {
			if s != "" && !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	return out
}

// enrolledDirs returns every folder ~/.telesphorepo/*/docker that holds a device file.
func enrolledDirs() ([]string, error) {
	root, err := rootDir()
	if err != nil {
		return nil, err
	}
	m, err := filepath.Glob(filepath.Join(root, "*", "docker", "device.json"))
	if err != nil {
		return nil, err
	}
	out := make([]string, len(m))
	for i, f := range m {
		out[i] = filepath.Dir(f)
	}
	return out, nil
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
