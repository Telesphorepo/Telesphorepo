//go:build e2e || import || clients

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

// The dnf client of every zone: Fedora (dnf5, rpm with Sequoia).
var fedoraImage = floatingImage("registry.fedoraproject.org/fedora:latest")

// nfpmRPMScript builds the RPM test packages with nfpm into /out, as Anchor's release does:
// e2e-anchor 1.0.0-1 and 1.1.0-1 for x86_64, a 1.0.0-1 with other bytes, and e2e-data 1.0.0-1
// for noarch.
const nfpmRPMScript = `set -e
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
	nfpm package -p rpm -f /w/$5/nfpm.yaml -t /w/$5/
	mv /w/$5/*.rpm /out/$5
}
mk e2e-anchor amd64 1.0.0 "anchor 1.0.0" anchor10
mk e2e-anchor amd64 1.1.0 "anchor 1.1.0" anchor11
mk e2e-anchor amd64 1.0.0 "anchor 1.0.0 conflicting" anchor10c
mk e2e-data all 1.0.0 "data 1.0.0" data
ls -l /out
`

type rpmPackages struct {
	anchor10, anchor11, anchor10Conflict, data []byte
}

// buildRPMPackages builds the test packages with nfpm.
func buildRPMPackages(t *testing.T) *rpmPackages {
	t.Helper()
	r := RunClientResult(t, nfpmImage, ClientOpts{Cmd: []string{nfpmRPMScript}})
	if r.ExitCode != 0 {
		t.Fatalf("building RPM packages with nfpm failed (exit %d):\n%s", r.ExitCode, r.Output)
	}
	return &rpmPackages{
		anchor10:         r.ReadFile(t, "/out/anchor10"),
		anchor11:         r.ReadFile(t, "/out/anchor11"),
		anchor10Conflict: r.ReadFile(t, "/out/anchor10c"),
		data:             r.ReadFile(t, "/out/data"),
	}
}

// goRPM is a package written here for the server's checks (dnf does not install it): the lead,
// a signature header with the header's SHA-256, a header with the identity, one file and the
// payload digest, and a payload of random bytes.
type goRPM struct {
	name, version, release, arch string
	epoch                        int // -1: none
	source                       bool
	noPayloadDigest              bool
	badPayload                   bool // the payload does not match its digest
	description                  int  // bytes of the description
	payload                      int  // bytes of the payload; 0 for 64
}

type rpmEntry struct {
	tag, typ, count uint32
	data            []byte
}

func rpmStr(tag uint32, s string) rpmEntry { return rpmEntry{tag, 6, 1, append([]byte(s), 0)} }
func rpmStrs(tag uint32, ss ...string) rpmEntry {
	var d []byte
	for _, s := range ss {
		d = append(append(d, s...), 0)
	}
	return rpmEntry{tag, 8, uint32(len(ss)), d}
}
func rpmInt32(tag uint32, ns ...uint32) rpmEntry {
	var d []byte
	for _, n := range ns {
		d = binary.BigEndian.AppendUint32(d, n)
	}
	return rpmEntry{tag, 4, uint32(len(ns)), d}
}

// rpmHeader encodes a header with the region tag region, its entries in tag order.
func rpmHeader(region uint32, es []rpmEntry) []byte {
	slices.SortFunc(es, func(a, b rpmEntry) int { return int(a.tag) - int(b.tag) })
	align := map[uint32]int{3: 2, 4: 4, 5: 8}
	var data, index []byte
	for _, e := range es {
		if a := align[e.typ]; a > 0 {
			for len(data)%a != 0 {
				data = append(data, 0)
			}
		}
		index = binary.BigEndian.AppendUint32(index, e.tag)
		index = binary.BigEndian.AppendUint32(index, e.typ)
		index = binary.BigEndian.AppendUint32(index, uint32(len(data)))
		index = binary.BigEndian.AppendUint32(index, e.count)
		data = append(data, e.data...)
	}
	il := uint32(len(es) + 1)
	trailer := uint32(len(data))
	for _, n := range []uint32{region, 7, uint32(-int32(il * 16)), 16} {
		data = binary.BigEndian.AppendUint32(data, n)
	}
	out := []byte{0x8e, 0xad, 0xe8, 0x01, 0, 0, 0, 0}
	for _, n := range []uint32{il, uint32(len(data)), region, 7, trailer, 16} {
		out = binary.BigEndian.AppendUint32(out, n)
	}
	return append(append(out, index...), data...)
}

func (p goRPM) bytes(t testing.TB) []byte {
	t.Helper()
	n := p.payload
	if n == 0 {
		n = 64
	}
	payload := make([]byte, n)
	rand.Read(payload)
	digest := sha256.Sum256(payload)
	if p.badPayload {
		payload[0] ^= 1
	}
	es := []rpmEntry{rpmStr(1000, p.name), rpmStr(1001, p.version), rpmStr(1002, p.release), rpmStr(1022, p.arch),
		rpmStr(1021, "linux"), rpmStr(1004, "telesphorepo e2e package"), rpmStr(1014, "MIT"),
		rpmStr(1005, "e2e"+strings.Repeat(" ", p.description)),
		rpmInt32(1006, uint32(time.Now().Unix())), rpmInt32(1009, 1),
		rpmStrs(1117, p.name), rpmStrs(1118, "/usr/share/"+p.name+"/"), rpmInt32(1116, 0),
		{1030, 3, 1, []byte{0x81, 0xa4}}, rpmInt32(1037, 0),
		rpmStr(1124, "cpio"), rpmStr(1125, "gzip")}
	if p.epoch >= 0 {
		es = append(es, rpmInt32(1003, uint32(p.epoch)))
	}
	if p.source {
		es = append(es, rpmInt32(1106, 1))
	}
	if !p.noPayloadDigest {
		es = append(es, rpmStrs(5092, hex.EncodeToString(digest[:])), rpmInt32(5093, 8))
	}
	hdr := rpmHeader(63, es)
	hsum := sha256.Sum256(hdr)
	sig := rpmHeader(62, []rpmEntry{rpmStr(273, hex.EncodeToString(hsum[:])), rpmInt32(1000, uint32(len(hdr)+len(payload)))})
	lead := make([]byte, 96)
	copy(lead, []byte{0xed, 0xab, 0xee, 0xdb, 3, 0})
	lead[79] = 5
	out := append(append(lead, sig...), make([]byte, (8-len(sig)%8)%8)...)
	return append(append(out, hdr...), payload...)
}

// rpmUpload PUTs a package to /rpm/{repo}/upload.
func rpmUpload(t testing.TB, s *Server, repo string, pkg []byte, auth Auth) (int, []byte) {
	t.Helper()
	resp, b := s.Do(t, s.NewRequest(t, http.MethodPut, "/rpm/"+repo+"/upload", bytes.NewReader(pkg)), auth)
	return resp.StatusCode, b
}

// rpmUploadResult is the answer of an upload.
type rpmUploadResult struct{ Package, Version, Architecture, Filename, SHA256 string }

// rpmMustUpload uploads a new package and checks the answer: the identity, and a stored file
// that is not the uploaded one (the server signs it).
func rpmMustUpload(t testing.TB, s *Server, repo string, pkg []byte, auth Auth, name, version, arch string) rpmUploadResult {
	t.Helper()
	st, b := rpmUpload(t, s, repo, pkg, auth)
	if st != http.StatusCreated {
		t.Fatalf("upload %s %s %s: status %d, want 201: %s", name, version, arch, st, b)
	}
	var res rpmUploadResult
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatalf("upload response %s: %v", b, err)
	}
	_, ver, _ := strings.Cut(version, ":")
	if !strings.Contains(version, ":") {
		ver = version
	}
	want := fmt.Sprintf("Packages/%s/%s-%s.%s.rpm", name, name, ver, arch)
	if res.Package != name || res.Version != version || res.Architecture != arch || res.Filename != want || res.SHA256 == rpmSum(pkg) {
		t.Fatalf("upload response %+v does not match %s %s %s (filename %s, a signed file)", res, name, version, arch, want)
	}
	return res
}

// rpmSum is the hex SHA-256 of b.
func rpmSum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// RPMKey returns the armored public signing key of RPM repo at /rpm/{repo}/key.asc.
func (s *Server) RPMKey(t testing.TB, repo string, auth Auth) string {
	t.Helper()
	resp, b := s.Do(t, s.NewRequest(t, http.MethodGet, "/rpm/"+repo+"/key.asc", nil), auth)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), "BEGIN PGP PUBLIC KEY BLOCK") {
		t.Fatalf("GET /rpm/%s/key.asc: status %d: %s", repo, resp.StatusCode, b)
	}
	return string(b)
}

// Dnf is a running Fedora or RHEL-family container on TestNetwork whose own repositories are
// removed (they need the internet). Each test adds its repositories.
type Dnf struct{ c testcontainers.Container }

// StartDnf starts a container of image.
func StartDnf(t testing.TB, image string) *Dnf {
	t.Helper()
	c, err := testcontainers.Run(context.Background(), image,
		e2eContainer,
		testcontainers.WithEntrypoint("sleep", "infinity"),
		network.WithNetwork(nil, TestNetwork),
		testcontainers.WithWaitStrategyAndDeadline(2*time.Minute, wait.ForExec([]string{"true"})),
	)
	registerCleanup(t, c, "dnf")
	if err != nil {
		t.Fatalf("start the dnf container %s: %v", image, err)
	}
	d := &Dnf{c: c}
	d.MustExec(t, "remove the distribution's repositories", "rm -f /etc/yum.repos.d/*.repo")
	return d
}

// Exec runs script with bash and returns the exit code and the combined output.
func (d *Dnf) Exec(t testing.TB, script string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	code, rd, err := d.c.Exec(ctx, []string{"bash", "-c", "exec 2>&1; " + script}, tcexec.Multiplexed())
	if err != nil {
		t.Fatalf("dnf container exec: %v", err)
	}
	out, _ := io.ReadAll(rd)
	return code, string(out)
}

// MustExec is Exec that fails the test on a non-zero exit.
func (d *Dnf) MustExec(t testing.TB, what, script string) string {
	t.Helper()
	code, out := d.Exec(t, script)
	if code != 0 {
		t.Fatalf("%s: exit %d:\n%s", what, code, out)
	}
	return out
}

// Trust writes the armored key to /etc/pki/rpm-gpg/RPM-GPG-KEY-<id> and imports it into rpm, as
// the snippet of a private repo does; the .repo file of AddRepo names that file.
func (d *Dnf) Trust(t testing.TB, id, key string) {
	t.Helper()
	f := "/etc/pki/rpm-gpg/RPM-GPG-KEY-" + id
	d.MustExec(t, "trust a key", fmt.Sprintf("mkdir -p /etc/pki/rpm-gpg && cat > %s <<'EOF'\n%s\nEOF\nrpm --import %s", f, key, f))
}

// AddRepo writes /etc/yum.repos.d/<id>.repo with gpgcheck and repo_gpgcheck against the key that
// Trust wrote, and the credentials user and pass ("" for none).
func (d *Dnf) AddRepo(t testing.TB, id, baseurl, user, pass string) {
	t.Helper()
	cred := ""
	if user != "" {
		cred = "username=" + user + "\npassword=" + pass + "\n"
	}
	d.MustExec(t, "add a repository", fmt.Sprintf("cat > /etc/yum.repos.d/%s.repo <<'EOF'\n[%s]\nname=%s\nbaseurl=%s\ngpgcheck=1\nrepo_gpgcheck=1\ngpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-%s\nmetadata_expire=1h\n%sEOF\n",
		id, id, id, baseurl, id, cred))
}

// rpmBaseURL is the baseurl of RPM repo repo on s, as a container on TestNetwork reaches it.
func rpmBaseURL(s *Server, repo string) string {
	return "http://" + s.Alias + ":8080/rpm/" + repo + "/"
}
