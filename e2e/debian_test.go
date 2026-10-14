//go:build e2e

package e2e

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
)

const debImage = "debian:trixie-20261005"

// debBuildScript builds the test packages with dpkg-deb into /out.
const debBuildScript = `#!/bin/sh
set -e
mkdir -p /out
mk() { # package version arch message compression outname
	d=/build/$6
	mkdir -p "$d/DEBIAN" "$d/usr/bin"
	printf 'Package: %s\nVersion: %s\nArchitecture: %s\nMaintainer: E2E <e2e@telesphorepo.test>\nDescription: telesphorepo e2e test package\n' "$1" "$2" "$3" > "$d/DEBIAN/control"
	printf '#!/bin/sh\necho "%s"\n' "$4" > "$d/usr/bin/$1"
	chmod 0755 "$d/usr/bin/$1"
	dpkg-deb --root-owner-group -Z"$5" --build "$d" "/out/$6.deb"
}
arch=$(dpkg --print-architecture)
printf '%s' "$arch" > /out/arch
mk e2e-hello 1.0 all "hello 1.0" xz hello-1.0
mk e2e-hello 2.0 all "hello 2.0" xz hello-2.0
mk e2e-hello 1.0 all "hello 1.0 conflicting" gzip hello-1.0-conflict
mk e2e-native 1.0 "$arch" "native $arch" gzip native
ls -l /out
`

type debPackages struct {
	arch                                   string
	hello1, hello2, hello1Conflict, native []byte
}

func buildDebPackages(t *testing.T) *debPackages {
	t.Helper()
	r := RunClientResult(t, debImage, ClientOpts{
		Cmd:   []string{"sh", "/build.sh"},
		Files: []ContainerFile{{Content: debBuildScript, Path: "/build.sh", Mode: 0o755}},
	})
	if r.ExitCode != 0 {
		t.Fatalf("building .debs failed (exit %d):\n%s", r.ExitCode, r.Output)
	}
	return &debPackages{
		arch:           strings.TrimSpace(string(r.ReadFile(t, "/out/arch"))),
		hello1:         r.ReadFile(t, "/out/hello-1.0.deb"),
		hello2:         r.ReadFile(t, "/out/hello-2.0.deb"),
		hello1Conflict: r.ReadFile(t, "/out/hello-1.0-conflict.deb"),
		native:         r.ReadFile(t, "/out/native.deb"),
	}
}

func TestDebian(t *testing.T) {
	pkgs := buildDebPackages(t)
	archs := []string{"amd64", "arm64"}
	if !slices.Contains(archs, pkgs.arch) {
		archs = append(archs, pkgs.arch)
	}

	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo := RandName("deb")
		s.CreateRepo(t, repo, "debian", false, map[string]any{"architectures": archs})
		writer, writerPass := RandName("writer"), randHex(12)
		reader, readerPass := RandName("reader"), randHex(12)
		s.CreateUser(t, writer, writerPass, false)
		s.CreateUser(t, reader, readerPass, false)
		s.SetPermission(t, repo, writer, "write")
		s.SetPermission(t, repo, reader, "read")
		wauth := Basic{User: "ci", Pass: s.CreateToken(t, writer, writerPass, "deb-upload")}
		rauth := Basic{User: reader, Pass: readerPass}

		// Upload: auth checks, created, idempotent re-upload, conflict.
		if st, b := debUpload(t, s, repo, pkgs.hello1, nil); st != http.StatusUnauthorized {
			t.Fatalf("anonymous upload: status %d, want 401: %s", st, b)
		}
		if st, b := debUpload(t, s, repo, pkgs.hello1, rauth); st != http.StatusForbidden {
			t.Fatalf("reader upload: status %d, want 403: %s", st, b)
		}
		debMustUpload(t, s, repo, pkgs.hello1, wauth, "e2e-hello", "1.0", "all")
		if st, b := debUpload(t, s, repo, pkgs.hello1, wauth); st != http.StatusOK {
			t.Fatalf("identical re-upload: status %d, want 200: %s", st, b)
		}
		if st, b := debUpload(t, s, repo, pkgs.hello1Conflict, wauth); st != http.StatusConflict {
			t.Fatalf("conflicting upload: status %d, want 409: %s", st, b)
		}
		debMustUpload(t, s, repo, pkgs.native, wauth, "e2e-native", "1.0", pkgs.arch)

		poolPath := "pool/main/e/e2e-hello/e2e-hello_1.0_all.deb"
		if st, b := debGet(t, s, opts.S3, "/debian/"+repo+"/"+poolPath, rauth); st != http.StatusOK || !bytes.Equal(b, pkgs.hello1) {
			t.Fatalf("GET pool file: status %d, %d bytes (want %d identical bytes)", st, len(b), len(pkgs.hello1))
		}
		if opts.S3 != nil {
			want := "repos/" + repo + "/" + poolPath
			if !slices.ContainsFunc(opts.S3.ListKeys(t), func(k string) bool { return strings.HasSuffix(k, want) }) {
				t.Fatalf("S3 bucket has no key ending in %s", want)
			}
		}

		s.WaitDebianIndexed(t, repo)
		st, keyBody := s.API(t, http.MethodGet, "/keys/debian.asc", nil, nil)
		if st != http.StatusOK || !strings.Contains(string(keyBody), "BEGIN PGP PUBLIC KEY BLOCK") {
			t.Fatalf("GET /keys/debian.asc: status %d: %s", st, keyBody)
		}
		key := string(keyBody)

		out := debAptInstall(t, s, repo, reader, readerPass, key)
		if !strings.Contains(out, "RESULT: hello 1.0\n") || !strings.Contains(out, "NATIVE: native "+pkgs.arch) {
			t.Fatalf("apt install of 1.0 did not produce expected output:\n%s", out)
		}

		// Second version → incremental rebuild picked up by a fresh client.
		debMustUpload(t, s, repo, pkgs.hello2, wauth, "e2e-hello", "2.0", "all")
		s.WaitDebianIndexed(t, repo)
		out = debAptInstall(t, s, repo, reader, readerPass, key)
		if !strings.Contains(out, "RESULT: hello 2.0\n") {
			t.Fatalf("apt install after 2.0 upload did not produce hello 2.0:\n%s", out)
		}

		debCheckIndexes(t, s, opts.S3, repo, rauth, key)
		debCheckSecurity(t, s, opts.S3, repo, wauth, rauth)
		debCheckArchRemoval(t, s, opts.S3, repo, archs, pkgs.arch, rauth)
	})
}

// debCheckArchRemoval removes a non-native architecture from the repo config and checks that
// its index files are no longer served or listed in Release.
func debCheckArchRemoval(t *testing.T, s *Server, tg *S3Target, repo string, archs []string, native string, auth Auth) {
	t.Helper()
	if st, b := s.API(t, http.MethodPatch, "/api/repos/"+repo, map[string]any{"config": map[string]any{"architectures": []string{"amd64", "all"}}}, s.Admin()); st != http.StatusBadRequest {
		t.Fatalf("PATCH architectures with \"all\": status %d, want 400: %s", st, b)
	}
	i := slices.IndexFunc(archs, func(a string) bool { return a != native })
	removed := archs[i]
	keep := slices.Delete(slices.Clone(archs), i, i+1)
	base := "/debian/" + repo + "/dists/trixie/"
	if st, _ := debGet(t, s, tg, base+"main/binary-"+removed+"/Packages", auth); st != http.StatusOK {
		t.Fatalf("GET binary-%s/Packages before removal: status %d, want 200", removed, st)
	}

	s.MustAPI(t, http.MethodPatch, "/api/repos/"+repo, map[string]any{"config": map[string]any{"architectures": keep}}, s.Admin(), nil)
	s.WaitDebianIndexed(t, repo)

	for _, f := range []string{"Packages", "Packages.gz"} {
		if st, _ := debGet(t, s, tg, base+"main/binary-"+removed+"/"+f, auth); st != http.StatusNotFound {
			t.Fatalf("GET binary-%s/%s after removal: status %d, want 404", removed, f, st)
		}
	}
	st, release := debGet(t, s, tg, base+"Release", auth)
	if st != http.StatusOK {
		t.Fatalf("GET Release: status %d: %s", st, release)
	}
	if bytes.Contains(release, []byte("binary-"+removed+"/")) {
		t.Fatalf("Release still lists binary-%s after removal:\n%s", removed, release)
	}
	if !bytes.Contains(release, []byte("\nArchitectures: "+strings.Join(keep, " ")+"\n")) {
		t.Fatalf("Release Architectures is not %q:\n%s", strings.Join(keep, " "), release)
	}
	if st, _ := debGet(t, s, tg, base+"main/binary-"+native+"/Packages", auth); st != http.StatusOK {
		t.Fatalf("GET binary-%s/Packages after removal: status %d, want 200", native, st)
	}
}

// TestDebianOversizeUpload checks TR_MAX_UPLOAD_BYTES on a dedicated server.
func TestDebianOversizeUpload(t *testing.T) {
	s := StartServer(t, ServerOpts{Env: map[string]string{"TR_MAX_UPLOAD_BYTES": "4096"}})
	repo := RandName("deb")
	s.CreateRepo(t, repo, "debian", false, nil)
	body := bytes.Repeat([]byte("x"), 256<<10)
	if st, b := debUpload(t, s, repo, body, s.Admin()); st != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize upload: status %d, want 413: %s", st, b)
	}
}

func debUpload(t *testing.T, s *Server, repo string, deb []byte, auth Auth) (int, []byte) {
	t.Helper()
	req := s.NewRequest(t, http.MethodPut, "/debian/"+repo+"/upload/trixie/main", bytes.NewReader(deb))
	req.Header.Set("Content-Type", "application/vnd.debian.binary-package")
	resp, b := s.Do(t, req, auth)
	return resp.StatusCode, b
}

func debMustUpload(t *testing.T, s *Server, repo string, deb []byte, auth Auth, pkg, ver, arch string) {
	t.Helper()
	st, b := debUpload(t, s, repo, deb, auth)
	if st != http.StatusCreated {
		t.Fatalf("upload %s %s %s: status %d, want 201: %s", pkg, ver, arch, st, b)
	}
	var res struct{ Package, Version, Architecture, Filename, SHA256 string }
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatalf("upload response %s: %v", b, err)
	}
	sum := sha256.Sum256(deb)
	wantFile := fmt.Sprintf("pool/main/e/%s/%s_%s_%s.deb", pkg, pkg, ver, arch)
	if res.Package != pkg || res.Version != ver || res.Architecture != arch || res.SHA256 != hex.EncodeToString(sum[:]) || res.Filename != wantFile {
		t.Fatalf("upload response %+v does not match %s %s %s (filename %s)", res, pkg, ver, arch, wantFile)
	}
}

// debGet GETs path and follows storage redirects. Presigned SeaweedFS URLs point at the
// network-internal host, so the request is sent to the host endpoint with the original Host header.
func debGet(t *testing.T, s *Server, tg *S3Target, path string, auth Auth) (int, []byte) {
	t.Helper()
	resp, body := s.Do(t, s.NewRequest(t, http.MethodGet, path, nil), auth)
	for i := 0; i < 5 && resp.StatusCode >= 300 && resp.StatusCode < 400; i++ {
		loc, err := resp.Location()
		if err != nil {
			t.Fatalf("GET %s: redirect without location: %v", path, err)
		}
		req, err := http.NewRequest(http.MethodGet, loc.String(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if tg != nil {
			if ep, err := url.Parse(tg.Endpoint); err == nil && ep.Host == loc.Host {
				he, err := url.Parse(tg.HostEndpoint)
				if err != nil {
					t.Fatal(err)
				}
				req.URL.Scheme, req.URL.Host, req.Host = he.Scheme, he.Host, loc.Host
			}
		}
		resp, body = s.Do(t, req, nil)
	}
	return resp.StatusCode, body
}

const debAptScript = `#!/bin/sh
set -e
rm -f /etc/apt/sources.list.d/debian.sources /etc/apt/sources.list
if ! apt-get update >/tmp/update.log 2>&1; then cat /tmp/update.log; exit 1; fi
cat /tmp/update.log
echo '=== end of apt-get update ==='
apt-get install -y --no-install-recommends e2e-hello e2e-native
echo "RESULT: $(e2e-hello)"
echo "NATIVE: $(e2e-native)"
`

// debAptInstall runs apt-get update/install in a fresh container and returns its output.
func debAptInstall(t *testing.T, s *Server, repo, user, pass, key string) string {
	t.Helper()
	sources := fmt.Sprintf("Types: deb\nURIs: %s/debian/%s\nSuites: trixie\nComponents: main\nSigned-By: /etc/apt/keyrings/tr.asc\n", s.InternalURL, repo)
	// apt only applies auth.conf entries to plain http when the protocol is spelled out.
	auth := fmt.Sprintf("machine http://%s:8080/debian/%s login %s password %s\n", s.Alias, repo, user, pass)
	code, out := RunClient(t, debImage, ClientOpts{
		Cmd: []string{"sh", "/apt.sh"},
		Files: []ContainerFile{
			{Content: debAptScript, Path: "/apt.sh", Mode: 0o755},
			{Content: key, Path: "/etc/apt/keyrings/tr.asc"},
			{Content: sources, Path: "/etc/apt/sources.list.d/tr.sources"},
			{Content: auth, Path: "/etc/apt/auth.conf.d/tr.conf", Mode: 0o644},
		},
	})
	if code != 0 {
		t.Fatalf("apt client exited %d:\n%s", code, out)
	}
	update, _, ok := strings.Cut(out, "=== end of apt-get update ===")
	if !ok {
		t.Fatalf("apt output missing update marker:\n%s", out)
	}
	for _, bad := range []string{"NO_PUBKEY", "not signed", "insecure", "Err:"} {
		if strings.Contains(update, bad) {
			t.Fatalf("apt-get update output contains %q:\n%s", bad, update)
		}
	}
	return out
}

// debCheckIndexes verifies InRelease / Release.gpg signatures and a by-hash reference.
func debCheckIndexes(t *testing.T, s *Server, tg *S3Target, repo string, auth Auth, key string) {
	t.Helper()
	keyring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(key))
	if err != nil {
		t.Fatalf("parse served public key: %v", err)
	}
	base := "/debian/" + repo + "/dists/trixie/"

	st, inRelease := debGet(t, s, tg, base+"InRelease", auth)
	if st != http.StatusOK {
		t.Fatalf("GET InRelease: status %d: %s", st, inRelease)
	}
	for _, want := range []string{"BEGIN PGP SIGNED MESSAGE", "Acquire-By-Hash: yes"} {
		if !bytes.Contains(inRelease, []byte(want)) {
			t.Fatalf("InRelease lacks %q:\n%s", want, inRelease)
		}
	}
	block, _ := clearsign.Decode(inRelease)
	if block == nil {
		t.Fatal("InRelease is not a clearsigned message")
	}
	if _, err := block.VerifySignature(keyring, nil); err != nil {
		t.Fatalf("InRelease signature: %v", err)
	}

	st, release := debGet(t, s, tg, base+"Release", auth)
	if st != http.StatusOK {
		t.Fatalf("GET Release: status %d: %s", st, release)
	}
	st, sig := debGet(t, s, tg, base+"Release.gpg", auth)
	if st != http.StatusOK || len(sig) == 0 {
		t.Fatalf("GET Release.gpg: status %d, %d bytes", st, len(sig))
	}
	if _, err := openpgp.CheckArmoredDetachedSignature(keyring, bytes.NewReader(release), bytes.NewReader(sig), nil); err != nil {
		t.Fatalf("Release.gpg signature: %v", err)
	}

	// Find SHA256 entries and resolve one through by-hash.
	type entry struct{ sum, path string }
	var entries []entry
	inSHA := false
	sc := bufio.NewScanner(bytes.NewReader(release))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, " ") {
			inSHA = line == "SHA256:"
			continue
		}
		if f := strings.Fields(line); inSHA && len(f) == 3 {
			entries = append(entries, entry{f[0], f[2]})
		}
	}
	i := slices.IndexFunc(entries, func(e entry) bool { return strings.HasSuffix(e.path, "/Packages.gz") })
	if i < 0 {
		t.Fatalf("Release has no SHA256 entry for Packages.gz:\n%s", release)
	}
	e := entries[i]
	byHash := base + path.Dir(e.path) + "/by-hash/SHA256/" + e.sum
	st, b := debGet(t, s, tg, byHash, auth)
	if sum := sha256.Sum256(b); st != http.StatusOK || hex.EncodeToString(sum[:]) != e.sum {
		t.Fatalf("GET %s: status %d, sha256 %x (want %s)", byHash, st, sum, e.sum)
	}
	st, b = debGet(t, s, tg, base+e.path, auth)
	if sum := sha256.Sum256(b); st != http.StatusOK || hex.EncodeToString(sum[:]) != e.sum {
		t.Fatalf("GET %s: status %d, sha256 %x (want %s)", e.path, st, sum, e.sum)
	}
}

// debCheckSecurity covers the decompression bomb and path traversal attempts.
func debCheckSecurity(t *testing.T, s *Server, tg *S3Target, repo string, wauth, rauth Auth) {
	t.Helper()
	if st, b := debUpload(t, s, repo, debBomb(t), wauth); st != http.StatusBadRequest {
		t.Fatalf("decompression bomb upload: status %d, want 400: %s", st, b)
	}

	for _, p := range []string{
		"/debian/" + repo + "/../x",
		"/debian/" + repo + "/pool/%2e%2e/x",
		"/debian/" + repo + "/pool%2fx",
		"/debian/" + repo + "/pool/%2e%2e/dists/trixie/Release",
		"/debian/" + repo + "/pool/..%2fdists/trixie/Release",
		"/debian/" + repo + "/dists/trixie/%2e%2e/trixie/Release",
		"/debian/" + repo + "/pool/main/e/e2e-hello/%2e%2e%2fe2e-hello/e2e-hello_1.0_all.deb",
	} {
		req := s.NewRequest(t, http.MethodGet, p, nil)
		resp, b := s.Do(t, req, rauth)
		if resp.StatusCode < 300 || resp.StatusCode == http.StatusFound {
			t.Errorf("GET %s: status %d (must not serve content)", p, resp.StatusCode)
		}
		if bytes.Contains(b, []byte("Codename:")) || bytes.Contains(b, []byte("!<arch>")) {
			t.Errorf("GET %s: response leaks file content", p)
		}
	}
}

// debBomb returns a .deb whose control.tar.gz decompresses to well over 16 MiB.
func debBomb(t *testing.T) []byte {
	t.Helper()
	targz := func(files ...struct {
		name string
		data []byte
	}) []byte {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		for _, f := range files {
			if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.data)), Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write(f.data); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	type file = struct {
		name string
		data []byte
	}
	control := targz(
		file{"./zeros", make([]byte, 17<<20)},
		file{"./control", []byte("Package: e2e-bomb\nVersion: 1.0\nArchitecture: all\nMaintainer: E2E <e2e@telesphorepo.test>\nDescription: bomb\n")},
	)
	data := targz()

	var ar bytes.Buffer
	ar.WriteString("!<arch>\n")
	for _, m := range []file{{"debian-binary", []byte("2.0\n")}, {"control.tar.gz", control}, {"data.tar.gz", data}} {
		fmt.Fprintf(&ar, "%-16s%-12d%-6d%-6d%-8s%-10d`\n", m.name, 0, 0, 0, "100644", len(m.data))
		ar.Write(m.data)
		if len(m.data)%2 == 1 {
			ar.WriteByte('\n')
		}
	}
	return ar.Bytes()
}
