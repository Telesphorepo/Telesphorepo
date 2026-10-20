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

func TestDebian(t *testing.T) {
	t.Parallel()
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
		s.CreateUser(t, writer, writerPass)
		s.CreateUser(t, reader, readerPass)
		s.SetPerms(t, repo, writer, "repo.download", "repo.publish")
		s.SetPerms(t, repo, reader, "repo.download")
		// Each upload phase mints a new publish session, so no session outlives the default TTL
		// while apt runs.
		wauth := func() Auth { return Basic{User: "ci", Pass: PublishToken(t, s, writer, repo)} }
		rauth := Basic{User: reader, Pass: readerPass}

		// Upload: auth checks, created, idempotent re-upload, conflict.
		if st, b := debUpload(t, s, repo, pkgs.hello1, nil); st != http.StatusUnauthorized {
			t.Fatalf("anonymous upload: status %d, want 401: %s", st, b)
		}
		if st, b := debUpload(t, s, repo, pkgs.hello1, rauth); st != http.StatusUnauthorized {
			t.Fatalf("upload with a reader password: status %d, want 401: %s", st, b)
		}
		if st, b := debUpload(t, s, repo, pkgs.hello1, Basic{User: "ci", Pass: s.CreateToken(t, writer, writerPass, "deb-read")}); st != http.StatusUnauthorized {
			t.Fatalf("upload with a read token: status %d, want 401: %s", st, b)
		}
		w := wauth()
		debMustUpload(t, s, repo, pkgs.hello1, w, "e2e-hello", "1.0", "all")
		if st, b := debUpload(t, s, repo, pkgs.hello1, w); st != http.StatusOK {
			t.Fatalf("identical re-upload: status %d, want 200: %s", st, b)
		}
		if st, b := debUpload(t, s, repo, pkgs.hello1Conflict, w); st != http.StatusConflict {
			t.Fatalf("conflicting upload: status %d, want 409: %s", st, b)
		}
		// A repo of only "all" packages: its Release still lists the configured architectures, and
		// each binary-<arch>/Packages holds the "all" packages, so apt on any of them reads its own
		// index with no "doesn't support architecture" notice (debAptInstall fails on one).
		allRepo := RandName("deball")
		s.CreateRepo(t, allRepo, "debian", false, map[string]any{"architectures": archs})
		s.SetPerms(t, allRepo, writer, "repo.download", "repo.publish")
		s.SetPerms(t, allRepo, reader, "repo.download")
		debMustUpload(t, s, allRepo, pkgs.hello1, Basic{User: "ci", Pass: PublishToken(t, s, writer, allRepo)}, "e2e-hello", "1.0", "all")
		s.WaitDebianIndexed(t, allRepo)
		if out := debAptInstallOf(t, s, allRepo, reader, readerPass, s.DebianKey(t, allRepo, rauth), "e2e-hello"); !strings.Contains(out, "RESULT: hello 1.0\n") {
			t.Fatalf("apt install from a repo of only \"all\" packages:\n%s", out)
		}

		debMustUpload(t, s, repo, pkgs.native, w, "e2e-native", "1.0", pkgs.arch)

		poolPath := "pool/main/e/e2e-hello/e2e-hello_1.0_all.deb"
		if st, b := debGet(t, s, opts.S3, "/debian/"+repo+"/"+poolPath, rauth); st != http.StatusOK || !bytes.Equal(b, pkgs.hello1) {
			t.Fatalf("GET pool file: status %d, %d bytes (want %d identical bytes)", st, len(b), len(pkgs.hello1))
		}
		if opts.S3 != nil {
			want := blobKey(sha256Hex(pkgs.hello1))
			if !slices.ContainsFunc(opts.S3.ListKeys(t), func(k string) bool { return strings.HasSuffix(k, want) }) {
				t.Fatalf("S3 bucket has no key ending in %s", want)
			}
		}

		s.WaitDebianIndexed(t, repo)
		key := s.DebianKey(t, repo, rauth)

		out := debAptInstall(t, s, repo, reader, readerPass, key)
		if !strings.Contains(out, "RESULT: hello 1.0\n") || !strings.Contains(out, "NATIVE: native "+pkgs.arch) {
			t.Fatalf("apt install of 1.0 did not produce expected output:\n%s", out)
		}

		// Second version → incremental rebuild picked up by a fresh client.
		debMustUpload(t, s, repo, pkgs.hello2, wauth(), "e2e-hello", "2.0", "all")
		s.WaitDebianIndexed(t, repo)
		out = debAptInstall(t, s, repo, reader, readerPass, key)
		if !strings.Contains(out, "RESULT: hello 2.0\n") {
			t.Fatalf("apt install after 2.0 upload did not produce hello 2.0:\n%s", out)
		}

		debCheckIndexes(t, s, opts.S3, repo, rauth, key)
		debCheckSecurity(t, s, opts.S3, repo, wauth(), rauth)
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

// TestDebianOversizeUpload checks TR_MAX_UPLOAD_SIZE on a dedicated server.
func TestDebianOversizeUpload(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{Env: map[string]string{"TR_MAX_UPLOAD_SIZE": "4KiB"}})
	repo := RandName("deb")
	s.CreateRepo(t, repo, "debian", false, nil)
	body := bytes.Repeat([]byte("x"), 256<<10)
	if st, b := debUpload(t, s, repo, body, s.PublishAuth(t, "admin", repo)); st != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize upload: status %d, want 413: %s", st, b)
	}
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
	return goDeb(t,
		debMember{"./zeros", make([]byte, 17<<20)},
		debMember{"./control", []byte("Package: e2e-bomb\nVersion: 1.0\nArchitecture: all\nMaintainer: E2E <e2e@telesphorepo.test>\nDescription: bomb\n")},
	)
}

// debMember is a file of a .deb or of one of its tar members.
type debMember = struct {
	name string
	data []byte
}

// goDeb returns a .deb with the files control of control.tar.gz and an empty data.tar.gz.
func goDeb(t *testing.T, control ...debMember) []byte {
	t.Helper()
	targz := func(files ...debMember) []byte {
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
	var ar bytes.Buffer
	ar.WriteString("!<arch>\n")
	for _, m := range []debMember{{"debian-binary", []byte("2.0\n")}, {"control.tar.gz", targz(control...)}, {"data.tar.gz", targz()}} {
		fmt.Fprintf(&ar, "%-16s%-12d%-6d%-6d%-8s%-10d`\n", m.name, 0, 0, 0, "100644", len(m.data))
		ar.Write(m.data)
		if len(m.data)%2 == 1 {
			ar.WriteByte('\n')
		}
	}
	return ar.Bytes()
}
