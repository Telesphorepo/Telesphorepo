//go:build e2e

package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// TestArch: pacman installs packages that nfpm built from a private Arch repo with every package
// and index signed and SigLevel Required, upgrades to a newer version, finds a file with -F,
// installs an older version by its URL, falls back to the older version when the newer one is
// deleted, and keeps working through the old section name after a rename. On both stores.
func TestArch(t *testing.T) {
	t.Parallel()
	pkgs := buildArchPackages(t)
	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo := RandName("arch")
		s.CreateRepo(t, repo, "arch", false, nil)
		writer, writerPass := RandName("writer"), randHex(12)
		reader, readerPass := RandName("reader"), randHex(12)
		s.CreateUser(t, writer, writerPass)
		s.CreateUser(t, reader, readerPass)
		s.SetPerms(t, repo, writer, "repo.download", "repo.publish")
		s.SetPerms(t, repo, reader, "repo.download")
		wauth := func() Auth { return Basic{User: "ci", Pass: PublishToken(t, s, writer, repo)} }
		rauth := Basic{User: reader, Pass: readerPass}

		// Upload: auth checks, created, idempotent re-upload, conflict.
		if st, b := archUpload(t, s, repo, pkgs.anchor10, nil); st != http.StatusUnauthorized {
			t.Fatalf("anonymous upload: status %d, want 401: %s", st, b)
		}
		if st, b := archUpload(t, s, repo, pkgs.anchor10, rauth); st != http.StatusUnauthorized {
			t.Fatalf("upload with a reader password: status %d, want 401: %s", st, b)
		}
		w := wauth()
		archMustUpload(t, s, repo, pkgs.anchor10, w, "e2e-anchor", "1.0.0-1", "x86_64")
		if st, b := archUpload(t, s, repo, pkgs.anchor10, w); st != http.StatusOK {
			t.Fatalf("identical re-upload: status %d, want 200: %s", st, b)
		}
		if st, b := archUpload(t, s, repo, pkgs.anchor10Conflict, w); st != http.StatusConflict {
			t.Fatalf("conflicting upload: status %d, want 409: %s", st, b)
		}
		archMustUpload(t, s, repo, pkgs.data, w, "e2e-data", "1.0.0-1", "any")
		pool := "pool/e2e-anchor/e2e-anchor-1.0.0-1-x86_64.pkg.tar.zst"
		if st, b := debGet(t, s, opts.S3, "/arch/"+repo+"/"+pool, rauth); st != http.StatusOK || !bytes.Equal(b, pkgs.anchor10) {
			t.Fatalf("GET pool file: status %d, %d bytes (want %d identical bytes)", st, len(b), len(pkgs.anchor10))
		}
		if st, b := debGet(t, s, opts.S3, "/arch/"+repo+"/x86_64/e2e-anchor-1.0.0-1-x86_64.pkg.tar.zst", rauth); st != http.StatusOK || !bytes.Equal(b, pkgs.anchor10) {
			t.Fatalf("GET the package under x86_64/: status %d, %d bytes (want %d identical bytes)", st, len(b), len(pkgs.anchor10))
		}
		if st, _ := debGet(t, s, opts.S3, "/arch/"+repo+"/aarch64/e2e-anchor-1.0.0-1-x86_64.pkg.tar.zst", rauth); st != http.StatusNotFound {
			t.Fatalf("GET an x86_64 package under aarch64/: status %d, want 404", st)
		}
		s.WaitDebianIndexed(t, repo)
		if st, _ := debGet(t, s, opts.S3, "/arch/"+repo+"/x86_64/"+repo+".db", nil); st != http.StatusUnauthorized {
			t.Fatalf("anonymous GET of the index of a private repo: status %d, want 401", st)
		}

		key := s.ArchKey(t, repo, rauth)
		p := StartPacman(t)
		p.Trust(t, key)
		p.AddRepo(t, repo, archServer(s, reader, readerPass))
		out := p.MustExec(t, "pacman install", "pacman -Sy --noconfirm e2e-anchor e2e-data && e2e-anchor && e2e-data")
		archWant(t, "pacman install", out, "anchor 1.0.0\n", "data 1.0.0\n")

		// A newer version: pacman -Syu upgrades to it.
		archMustUpload(t, s, repo, pkgs.anchor11, wauth(), "e2e-anchor", "1.1.0-1", "x86_64")
		s.WaitDebianIndexed(t, repo)
		out = p.MustExec(t, "pacman upgrade", "pacman -Syu --noconfirm && e2e-anchor")
		archWant(t, "pacman upgrade", out, "anchor 1.1.0\n")

		// The files index, and an older version by its URL with its signature beside it.
		out = p.MustExec(t, "pacman -F", "pacman -Fy >/dev/null && pacman -F usr/bin/e2e-anchor")
		archWant(t, "pacman -F", out, repo+"/e2e-anchor 1.1.0-1")
		url := strings.Replace(archServer(s, reader, readerPass), "$repo/$arch", repo+"/"+pool, 1)
		out = p.MustExec(t, "pacman -U of an older version", "pacman -U --noconfirm '"+url+"' && e2e-anchor")
		archWant(t, "pacman -U of an older version", out, "anchor 1.0.0\n")

		// A signature goes only with its package; deleting the newest version gives its entry
		// back to the older one.
		if st, _ := deleteFiles(t, s, repo, "path", pool+".sig", s.Admin()); st != http.StatusBadRequest {
			t.Fatalf("delete the signature of a package alone: status %d, want 400", st)
		}
		mustDelete(t, s, repo, "path", "pool/e2e-anchor/e2e-anchor-1.1.0-1-x86_64.pkg.tar.zst", s.Admin(), 2)
		s.WaitDebianIndexed(t, repo)
		out = p.MustExec(t, "pacman after a delete", "pacman -Sy >/dev/null && pacman -Si "+repo+"/e2e-anchor")
		if !regexp.MustCompile(`Version\s*:\s*1\.0\.0-1`).MatchString(out) {
			t.Fatalf("the index after deleting 1.1.0-1 does not offer 1.0.0-1:\n%s", out)
		}

		// A rename: pacman.conf keeps the old section name and URL, the old name is a redirect.
		renamed := RandName("arch-renamed")
		s.MustAPI(t, http.MethodPost, "/api/repos/"+repo+"/rename", map[string]any{"name": renamed}, s.Admin(), nil)
		out = p.MustExec(t, "pacman through the old name", "pacman -Syy >/dev/null && pacman -Si "+repo+"/e2e-data")
		archWant(t, "pacman through the old name", out, "e2e-data")
		if st, _ := debGet(t, s, opts.S3, "/arch/"+renamed+"/x86_64/"+renamed+".db", rauth); st != http.StatusOK {
			t.Fatalf("GET the index by the new name: status %d, want 200", st)
		}
	})
}

// archWant fails the test when out lacks one of subs.
func archWant(t *testing.T, what, out string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(out, s) {
			t.Fatalf("%s: the output lacks %q:\n%s", what, s, out)
		}
	}
}

// TestArchRules: the checks of an upload, the architectures of the config (an any package in each
// architecture's index, the index of a removed architecture gone), and pacman's version order.
func TestArchRules(t *testing.T) {
	t.Parallel()
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo := RandName("arch")
		s.CreateRepo(t, repo, "arch", true, nil)
		auth := s.PublishAuth(t, s.AdminUser, repo)

		if st, b := s.API(t, http.MethodPatch, "/api/repos/"+repo, map[string]any{"config": map[string]any{"architectures": []string{"x86_64", "any"}}}, s.Admin()); st != http.StatusBadRequest {
			t.Fatalf("PATCH architectures with any: status %d, want 400: %s", st, b)
		}
		for what, pkg := range map[string][]byte{
			"a package of an architecture the repo does not list": goArchPkg(t, "e2e-riscv", "1.0-1", "riscv64", nil),
			"a version without pkgrel":                             goArchPkg(t, "e2e-norel", "1.0", "any", nil),
			"a file that is not a package":                         []byte("not a package"),
			"a package that decompresses past its limit":           goArchPkg(t, "e2e-bomb", "1.0-1", "any", map[string]int64{"usr/share/zeros": 1<<30 + 1<<20}),
		} {
			if st, b := archUpload(t, s, repo, pkg, auth); st != http.StatusBadRequest {
				t.Fatalf("upload %s: status %d, want 400: %s", what, st, b)
			}
		}

		// An any package is in the index of an architecture added later; a removed
		// architecture's index goes.
		archMustUploadGo(t, s, repo, auth, "e2e-any", "1.0-1", "any")
		archMustUploadGo(t, s, repo, auth, "e2e-x86", "1.0-1", "x86_64")
		s.MustAPI(t, http.MethodPatch, "/api/repos/"+repo, map[string]any{"config": map[string]any{"architectures": []string{"x86_64", "aarch64", "riscv64"}}}, s.Admin(), nil)
		s.WaitDebianIndexed(t, repo)
		if got := archIndexEntries(t, s, repo, "riscv64"); !slices.Equal(got, []string{"e2e-any-1.0-1/"}) {
			t.Fatalf("the riscv64 index lists %v, want the any package only", got)
		}
		if got := archIndexEntries(t, s, repo, "x86_64"); !slices.Equal(got, []string{"e2e-any-1.0-1/", "e2e-x86-1.0-1/"}) {
			t.Fatalf("the x86_64 index lists %v", got)
		}
		s.MustAPI(t, http.MethodPatch, "/api/repos/"+repo, map[string]any{"config": map[string]any{"architectures": []string{"x86_64"}}}, s.Admin(), nil)
		s.WaitDebianIndexed(t, repo)
		for _, a := range []string{"riscv64", "aarch64"} {
			if st, _ := rawGet(t, s, nil, "/arch/"+repo+"/"+a+"/"+repo+".db", nil); st != http.StatusNotFound {
				t.Fatalf("GET the index of the removed architecture %s: status %d, want 404", a, st)
			}
		}

		// The order of versions is pacman's: the Versions list agrees with vercmp on every pair.
		versions := []string{"1.0-1", "1.0-2", "1.0-1.1", "1.0a-1", "1.0.1-1", "1.0rc1-1", "1.0+git1-1", "1.10-1", "1.9-1",
			"2.0-1", "1:0.5-1", "1.0_beta-1", "01.0-3"}
		for _, v := range versions {
			archMustUploadGo(t, s, repo, auth, "e2e-order", v, "any")
		}
		var page struct {
			Items []struct {
				Version string `json:"version"`
			} `json:"items"`
		}
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/versions?limit=100&path=pool/e2e-order/", nil, s.Admin(), &page)
		var order []string
		for _, it := range page.Items {
			order = append(order, it.Version)
		}
		if len(order) != len(versions) {
			t.Fatalf("versions of e2e-order: %v, want %d", order, len(versions))
		}
		var script strings.Builder
		for i := range order {
			for j := i + 1; j < len(order); j++ {
				fmt.Fprintf(&script, "echo \"%s %s $(vercmp '%s' '%s')\"\n", order[i], order[j], order[i], order[j])
			}
		}
		out := StartPacman(t).MustExec(t, "vercmp", script.String())
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			f := strings.Fields(line)
			if n, err := strconv.Atoi(f[len(f)-1]); err != nil || n < 0 {
				t.Fatalf("the server lists %s before %s, vercmp says %s; the order is %v", f[0], f[1], f[len(f)-1], order)
			}
		}
	})
}

// archMustUploadGo uploads a package written here.
func archMustUploadGo(t *testing.T, s *Server, repo string, auth Auth, name, version, arch string) {
	t.Helper()
	pkg := goArchPkg(t, name, version, arch, map[string]int64{"usr/share/" + name + "/" + version: 1})
	st, b := archUpload(t, s, repo, pkg, auth)
	if st != http.StatusCreated {
		t.Fatalf("upload %s %s %s: status %d, want 201: %s", name, version, arch, st, b)
	}
}

// archIndexEntries returns the folders of the index of arch of an anonymous-read repo.
func archIndexEntries(t *testing.T, s *Server, repo, arch string) []string {
	t.Helper()
	st, b := rawGet(t, s, nil, "/arch/"+repo+"/"+arch+"/"+repo+".db", nil)
	if st != http.StatusOK {
		t.Fatalf("GET the %s index: status %d", arch, st)
	}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("the %s index is not gzip: %v", arch, err)
	}
	tr := tar.NewReader(zr)
	var out []string
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("read the %s index: %v", arch, err)
		}
		if h.Typeflag == tar.TypeDir {
			out = append(out, h.Name)
		}
	}
}

// TestArchSigning: pacman refuses a repo whose key it does not trust; after the repo's key changes
// to an imported RSA key, every package and index is signed again: a client that trusts only the
// old key refuses the repo, and one that trusts the new key installs from it.
func TestArchSigning(t *testing.T) {
	t.Parallel()
	pkgs := buildArchPackages(t)
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo := RandName("arch")
		s.CreateRepo(t, repo, "arch", true, nil)
		archMustUpload(t, s, repo, pkgs.anchor10, s.PublishAuth(t, s.AdminUser, repo), "e2e-anchor", "1.0.0-1", "x86_64")
		s.WaitDebianIndexed(t, repo)
		oldKey := s.ArchKey(t, repo, nil)

		p := StartPacman(t)
		p.AddRepo(t, repo, archServer(s, "", ""))
		if code, out := p.Exec(t, "pacman -Sy --noconfirm e2e-anchor"); code == 0 {
			t.Fatalf("pacman installed from a repo whose key it does not trust:\n%s", out)
		}
		p.Trust(t, oldKey)
		p.MustExec(t, "pacman install with the key trusted", "pacman -Sy --noconfirm e2e-anchor && e2e-anchor")

		rsa := rsaSigningKey(t)
		name := RandName("rsa")
		s.MustAPI(t, http.MethodPost, "/api/signing-keys", map[string]any{"name": name, "armored": rsa}, s.Admin(), nil)
		s.MustAPI(t, http.MethodPatch, "/api/repos/"+repo, map[string]any{"fields": map[string]any{"signing_key": name}}, s.Admin(), nil)
		s.WaitDebianIndexed(t, repo)
		newKey := s.ArchKey(t, repo, nil)
		if archFingerprint(t, newKey) == archFingerprint(t, oldKey) {
			t.Fatal("key.asc did not change with the repo's key")
		}
		if code, out := p.Exec(t, "pacman -Syy"); code == 0 {
			t.Fatalf("pacman took an index signed with a key it does not trust:\n%s", out)
		}
		fresh := StartPacman(t)
		fresh.Trust(t, newKey)
		fresh.AddRepo(t, repo, archServer(s, "", ""))
		out := fresh.MustExec(t, "pacman install after the key change", "pacman -Sy --noconfirm e2e-anchor && e2e-anchor")
		archWant(t, "pacman install after the key change", out, "anchor 1.0.0\n")
	})
}

// rsaSigningKey returns an armored RSA private key without a passphrase.
func rsaSigningKey(t *testing.T) string {
	t.Helper()
	cfg := &packet.Config{Algorithm: packet.PubKeyAlgoRSA, RSABits: 3072}
	ent, err := openpgp.NewEntity("Telesphorepo E2E RSA", "", "rsa@telesphorepo.test", cfg)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, openpgp.PrivateKeyType, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ent.SerializePrivate(w, cfg); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}
