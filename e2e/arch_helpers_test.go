//go:build e2e || clients

package e2e

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/klauspost/compress/zstd"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

// The Arch Linux client and the packager of Arch packages that Anchor's release uses.
var (
	archImage = floatingImage("docker.io/library/archlinux:latest")
	nfpmImage = floatingImage("docker.io/goreleaser/nfpm:latest")
)

// nfpmBuildScript builds the test packages with nfpm into /out: e2e-anchor 1.0.0-1 and 1.1.0-1
// for x86_64, a 1.0.0-1 with other bytes, and e2e-data 1.0.0-1 for any.
const nfpmBuildScript = `set -e
mkdir -p /out
mk() { # name arch version message out
	mkdir -p /w/$5
	printf '#!/bin/sh\necho "%s"\n' "$4" > /w/$5/run.sh
	cat > /w/$5/nfpm.yaml <<EOF
name: $1
arch: $2
platform: linux
version: $3
release: 1
maintainer: E2E <e2e@telesphorepo.test>
description: telesphorepo e2e package
license: MIT
contents:
  - src: /w/$5/run.sh
    dst: /usr/bin/$1
    file_info:
      mode: 0755
EOF
	nfpm package -p archlinux -f /w/$5/nfpm.yaml -t /w/$5/
	mv /w/$5/*.pkg.tar.zst /out/$5
}
mk e2e-anchor amd64 1.0.0 "anchor 1.0.0" anchor10
mk e2e-anchor amd64 1.1.0 "anchor 1.1.0" anchor11
mk e2e-anchor amd64 1.0.0 "anchor 1.0.0 conflicting" anchor10c
mk e2e-data all 1.0.0 "data 1.0.0" data
ls -l /out
`

type archPackages struct {
	anchor10, anchor11, anchor10Conflict, data []byte
}

// buildArchPackages builds the test packages with nfpm.
func buildArchPackages(t *testing.T) *archPackages {
	t.Helper()
	r := RunClientResult(t, nfpmImage, ClientOpts{Cmd: []string{nfpmBuildScript}})
	if r.ExitCode != 0 {
		t.Fatalf("building Arch packages with nfpm failed (exit %d):\n%s", r.ExitCode, r.Output)
	}
	return &archPackages{
		anchor10:         r.ReadFile(t, "/out/anchor10"),
		anchor11:         r.ReadFile(t, "/out/anchor11"),
		anchor10Conflict: r.ReadFile(t, "/out/anchor10c"),
		data:             r.ReadFile(t, "/out/data"),
	}
}

// goArchPkg returns a package written here: .PKGINFO of name, version and arch, then the files of
// files (path, size of zero bytes), as a zstd-compressed tar.
func goArchPkg(t testing.TB, name, version, arch string, files map[string]int64) []byte {
	t.Helper()
	var out bytes.Buffer
	zw, err := zstd.NewWriter(&out)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(zw)
	info := fmt.Sprintf("pkgname = %s\npkgbase = %s\npkgver = %s\npkgdesc = telesphorepo e2e package\nbuilddate = %d\npackager = E2E <e2e@telesphorepo.test>\nsize = 0\narch = %s\nlicense = MIT\n",
		name, name, version, time.Now().Unix(), arch)
	if err := tw.WriteHeader(&tar.Header{Name: ".PKGINFO", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(info)), ModTime: time.Now()}); err != nil {
		t.Fatal(err)
	}
	io.WriteString(tw, info)
	for p, size := range files {
		if err := tw.WriteHeader(&tar.Header{Name: p, Typeflag: tar.TypeReg, Mode: 0o644, Size: size, ModTime: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.CopyN(tw, zeroReader{}, size); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// archUpload PUTs a package to /arch/{repo}/upload.
func archUpload(t testing.TB, s *Server, repo string, pkg []byte, auth Auth) (int, []byte) {
	t.Helper()
	resp, b := s.Do(t, s.NewRequest(t, http.MethodPut, "/arch/"+repo+"/upload", bytes.NewReader(pkg)), auth)
	return resp.StatusCode, b
}

// archMustUpload uploads a new package and checks the answer.
func archMustUpload(t testing.TB, s *Server, repo string, pkg []byte, auth Auth, name, version, arch string) {
	t.Helper()
	st, b := archUpload(t, s, repo, pkg, auth)
	if st != http.StatusCreated {
		t.Fatalf("upload %s %s %s: status %d, want 201: %s", name, version, arch, st, b)
	}
	var res struct{ Package, Version, Architecture, Filename, SHA256 string }
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatalf("upload response %s: %v", b, err)
	}
	want := fmt.Sprintf("pool/%s/%s-%s-%s.pkg.tar.zst", name, name, version, arch)
	if res.Package != name || res.Version != version || res.Architecture != arch || res.Filename != want || res.SHA256 != archSum(pkg) {
		t.Fatalf("upload response %+v does not match %s %s %s (filename %s)", res, name, version, arch, want)
	}
}

// ArchKey returns the armored public signing key of Arch repo at /arch/{repo}/key.asc, read with
// auth (nil for an anonymous-read repo).
func (s *Server) ArchKey(t testing.TB, repo string, auth Auth) string {
	t.Helper()
	resp, b := s.Do(t, s.NewRequest(t, http.MethodGet, "/arch/"+repo+"/key.asc", nil), auth)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), "BEGIN PGP PUBLIC KEY BLOCK") {
		t.Fatalf("GET /arch/%s/key.asc: status %d: %s", repo, resp.StatusCode, b)
	}
	return string(b)
}

// Pacman is a running Arch Linux container with an initialized keyring and a pacman.conf that
// lists no repository, on TestNetwork. Each test adds its repositories.
type Pacman struct{ c testcontainers.Container }

// pacmanConf is the pacman.conf the container starts with: pacman's default levels, no
// repository (the official ones need the internet).
const pacmanConf = `[options]
Architecture = auto
SigLevel = Required DatabaseOptional
LocalFileSigLevel = Optional
`

// StartPacman starts the Arch Linux container and initializes its keyring.
func StartPacman(t testing.TB) *Pacman {
	t.Helper()
	c, err := testcontainers.Run(context.Background(), archImage,
		testcontainers.WithLabels(e2eLabels),
		testcontainers.WithEntrypoint("sleep", "infinity"),
		network.WithNetwork(nil, TestNetwork),
		testcontainers.WithWaitStrategyAndDeadline(2*time.Minute, wait.ForExec([]string{"true"})),
	)
	registerCleanup(t, c, "pacman")
	if err != nil {
		t.Fatalf("start the Arch Linux container: %v", err)
	}
	p := &Pacman{c: c}
	p.MustExec(t, "init the pacman keyring", "pacman-key --init >/dev/null 2>&1 && cat > /etc/pacman.conf <<'EOF'\n"+pacmanConf+"EOF\n")
	return p
}

// Exec runs script with bash and returns the exit code and the combined output.
func (p *Pacman) Exec(t testing.TB, script string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	code, rd, err := p.c.Exec(ctx, []string{"bash", "-c", "exec 2>&1; " + script}, tcexec.Multiplexed())
	if err != nil {
		t.Fatalf("pacman container exec: %v", err)
	}
	out, _ := io.ReadAll(rd)
	return code, string(out)
}

// MustExec is Exec that fails the test on a non-zero exit.
func (p *Pacman) MustExec(t testing.TB, what, script string) string {
	t.Helper()
	code, out := p.Exec(t, script)
	if code != 0 {
		t.Fatalf("%s: exit %d:\n%s", what, code, out)
	}
	return out
}

// Trust adds the armored key to pacman's keyring and signs it locally, so pacman trusts what it
// signs.
func (p *Pacman) Trust(t testing.TB, key string) {
	t.Helper()
	f := "/tmp/key-" + randHex(4) + ".asc"
	p.MustExec(t, "trust a key", fmt.Sprintf("cat > %s <<'EOF'\n%sEOF\npacman-key --add %s && pacman-key --lsign-key %s", f, key, f, archFingerprint(t, key)))
}

// AddRepo appends the section [section] with SigLevel Required and server, the Server line as
// pacman reads it ($repo and $arch unexpanded).
func (p *Pacman) AddRepo(t testing.TB, section, server string) {
	t.Helper()
	p.MustExec(t, "add a repository", fmt.Sprintf("cat >> /etc/pacman.conf <<'EOF'\n\n[%s]\nSigLevel = Required\nServer = %s\nEOF\n", section, server))
}

// archSum is the hex SHA-256 of b.
func archSum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// archFingerprint returns the primary key fingerprint (upper-case hex) of an armored key.
func archFingerprint(t testing.TB, armored string) string {
	t.Helper()
	ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(armored))
	if err != nil || len(ring) == 0 {
		t.Fatalf("parse armored key: %v (entities: %d)", err, len(ring))
	}
	return fmt.Sprintf("%X", ring[0].PrimaryKey.Fingerprint)
}

// archServer is the Server line of Arch repos on s, with the credentials user and pass ("" for
// none).
func archServer(s *Server, user, pass string) string {
	cred := ""
	if user != "" {
		cred = user + ":" + pass + "@"
	}
	return "http://" + cred + s.Alias + ":8080/arch/$repo/$arch"
}
