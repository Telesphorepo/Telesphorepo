//go:build e2e

package e2e

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestRPM: dnf on Fedora (dnf5) and Rocky 9 (dnf4) installs packages that nfpm built from a
// private RPM repo with gpgcheck and repo_gpgcheck, so every package and the repodata are signed
// by the server; upgrades to a newer version, finds a file through the filelists, downgrades to
// the older version (every version is in the repodata), stops seeing the newer version after its
// delete, and keeps working through the old name after a rename. On both stores.
func TestRPM(t *testing.T) {
	t.Parallel()
	pkgs := buildRPMPackages(t)
	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo := RandName("rpm")
		s.CreateRepo(t, repo, "rpm", false, nil)
		writer, writerPass := RandName("writer"), randHex(12)
		reader, readerPass := RandName("reader"), randHex(12)
		s.CreateUser(t, writer, writerPass)
		s.CreateUser(t, reader, readerPass)
		s.SetPerms(t, repo, writer, "repo.download", "repo.publish")
		s.SetPerms(t, repo, reader, "repo.download")
		wauth := func() Auth { return Basic{User: "ci", Pass: PublishToken(t, s, writer, repo)} }
		rauth := Basic{User: reader, Pass: readerPass}

		// Upload: auth checks, created and signed, idempotent re-upload, conflict.
		if st, b := rpmUpload(t, s, repo, pkgs.anchor10, nil); st != http.StatusUnauthorized {
			t.Fatalf("anonymous upload: status %d, want 401: %s", st, b)
		}
		if st, b := rpmUpload(t, s, repo, pkgs.anchor10, rauth); st != http.StatusUnauthorized {
			t.Fatalf("upload with a reader password: status %d, want 401: %s", st, b)
		}
		w := wauth()
		up := rpmMustUpload(t, s, repo, pkgs.anchor10, w, "e2e-anchor", "1.0.0-1", "x86_64")
		if st, b := rpmUpload(t, s, repo, pkgs.anchor10, w); st != http.StatusOK || !strings.Contains(string(b), up.SHA256) {
			t.Fatalf("identical re-upload: status %d, want 200 with the stored file's SHA-256 %s: %s", st, up.SHA256, b)
		}
		if st, b := rpmUpload(t, s, repo, pkgs.anchor10Conflict, w); st != http.StatusConflict {
			t.Fatalf("conflicting upload: status %d, want 409: %s", st, b)
		}
		rpmMustUpload(t, s, repo, pkgs.data, w, "e2e-data", "1.0.0-1", "noarch")
		if st, b := debGet(t, s, opts.S3, "/rpm/"+repo+"/"+up.Filename, rauth); st != http.StatusOK || archSum(b) != up.SHA256 {
			t.Fatalf("GET the stored package: status %d, SHA-256 %s, want %s", st, archSum(b), up.SHA256)
		}
		s.WaitDebianIndexed(t, repo)
		if st, _ := debGet(t, s, opts.S3, "/rpm/"+repo+"/repodata/repomd.xml", nil); st != http.StatusUnauthorized {
			t.Fatalf("anonymous GET of the repodata of a private repo: status %d, want 401", st)
		}

		key := s.RPMKey(t, repo, rauth)
		clients := map[string]*Dnf{"fedora": StartDnf(t, fedoraImage), "rocky9": StartDnf(t, rocky9Image)}
		for name, d := range clients {
			d.Trust(t, repo, key)
			d.AddRepo(t, repo, rpmBaseURL(s, repo), reader, readerPass)
			out := d.MustExec(t, name+": dnf install", "dnf -y install e2e-anchor e2e-data && e2e-anchor && e2e-data")
			archWant(t, name+": dnf install", out, "anchor 1.0.0\n", "data 1.0.0\n")
		}

		// A newer version: dnf upgrade --refresh takes it; the filelists find its file; dnf
		// downgrade goes back, since the repodata lists every version.
		rpmMustUpload(t, s, repo, pkgs.anchor11, wauth(), "e2e-anchor", "1.1.0-1", "x86_64")
		s.WaitDebianIndexed(t, repo)
		for name, d := range clients {
			out := d.MustExec(t, name+": dnf upgrade", "dnf -y upgrade --refresh && e2e-anchor")
			archWant(t, name+": dnf upgrade", out, "anchor 1.1.0\n")
			out = d.MustExec(t, name+": dnf provides", "dnf -q provides /usr/bin/e2e-anchor")
			archWant(t, name+": dnf provides", out, "e2e-anchor-1.1.0-1")
			out = d.MustExec(t, name+": dnf downgrade", "dnf -y downgrade e2e-anchor && e2e-anchor")
			archWant(t, name+": dnf downgrade", out, "anchor 1.0.0\n")
		}

		// A delete of the newest version: dnf no longer offers it.
		mustDelete(t, s, repo, "path", "Packages/e2e-anchor/e2e-anchor-1.1.0-1.x86_64.rpm", s.Admin(), 1)
		s.WaitDebianIndexed(t, repo)
		out := clients["fedora"].MustExec(t, "repoquery after a delete", "dnf -q repoquery --refresh e2e-anchor")
		if !strings.Contains(out, "1.0.0-1") || strings.Contains(out, "1.1.0-1") {
			t.Fatalf("the repodata after deleting 1.1.0-1 lists:\n%s", out)
		}

		// A rename: the .repo file keeps the old baseurl, the old name is a redirect.
		renamed := RandName("rpm-renamed")
		s.MustAPI(t, http.MethodPost, "/api/repos/"+repo+"/rename", map[string]any{"name": renamed}, s.Admin(), nil)
		out = clients["fedora"].MustExec(t, "dnf through the old name", "dnf -q repoquery --refresh e2e-data")
		archWant(t, "dnf through the old name", out, "e2e-data")
		if st, _ := debGet(t, s, opts.S3, "/rpm/"+renamed+"/repodata/repomd.xml", rauth); st != http.StatusOK {
			t.Fatalf("GET the repodata by the new name: status %d, want 200", st)
		}
	})
}

// TestRPMRules: the checks of an upload, the repodata of a new repo before its first upload, a
// file name that a package of another epoch holds, and rpm's version order.
func TestRPMRules(t *testing.T) {
	t.Parallel()
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo := RandName("rpm")
		s.CreateRepo(t, repo, "rpm", true, nil)
		s.WaitDebianIndexed(t, repo)
		d := StartDnf(t, fedoraImage)
		d.Trust(t, repo, s.RPMKey(t, repo, nil))
		d.AddRepo(t, repo, rpmBaseURL(s, repo), "", "")
		d.MustExec(t, "dnf makecache of a repo without packages", "dnf makecache")

		auth := s.PublishAuth(t, s.AdminUser, repo)
		for what, pkg := range map[string]goRPM{
			"a source package":                         {name: "e2e-src", version: "1.0", release: "1", arch: "x86_64", epoch: -1, source: true},
			"a package without a payload digest":       {name: "e2e-old", version: "1.0", release: "1", arch: "x86_64", epoch: -1, noPayloadDigest: true},
			"a payload that does not match its digest": {name: "e2e-bad", version: "1.0", release: "1", arch: "x86_64", epoch: -1, badPayload: true},
			"a header past its limit":                  {name: "e2e-big", version: "1.0", release: "1", arch: "x86_64", epoch: -1, description: 33 << 20},
		} {
			if st, b := rpmUpload(t, s, repo, pkg.bytes(t), auth); st != http.StatusBadRequest {
				t.Fatalf("upload %s: status %d, want 400: %s", what, st, b)
			}
		}
		if st, b := rpmUpload(t, s, repo, []byte("not a package"), auth); st != http.StatusBadRequest {
			t.Fatalf("upload a file that is not a package: status %d, want 400: %s", st, b)
		}
		rpmMustUpload(t, s, repo, goRPM{name: "e2e-epoch", version: "1.0", release: "1", arch: "noarch", epoch: -1}.bytes(t), auth, "e2e-epoch", "1.0-1", "noarch")
		if st, b := rpmUpload(t, s, repo, goRPM{name: "e2e-epoch", version: "1.0", release: "1", arch: "noarch", epoch: 3}.bytes(t), auth); st != http.StatusConflict {
			t.Fatalf("upload a package whose file name a package of another epoch holds: status %d, want 409: %s", st, b)
		}

		// The order of versions is rpm's: the Versions list agrees with rpm.vercmp on every pair.
		versions := []string{"1.0-1", "1.0-2", "1.0-1.1", "1.0a-1", "1.0.1-1", "1.0~rc1-1", "1.0^git1-1", "1.10-1", "1.9-1",
			"2.0-1", "1:0.5-1", "1.0_beta-1", "01.1-3", "1.0~~a-1", "1.0~a-1"}
		for _, v := range versions {
			e, rest, hasEpoch := strings.Cut(v, ":")
			epoch := -1
			if hasEpoch {
				epoch, _ = strconv.Atoi(e)
			} else {
				rest = v
			}
			ver, rel, _ := strings.Cut(rest, "-")
			rpmMustUpload(t, s, repo, goRPM{name: "e2e-order", version: ver, release: rel, arch: "noarch", epoch: epoch}.bytes(t), auth, "e2e-order", v, "noarch")
		}
		var page struct {
			Items []struct {
				Version string `json:"version"`
			} `json:"items"`
		}
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/versions?limit=100&path=Packages/e2e-order/", nil, s.Admin(), &page)
		var order []string
		for _, it := range page.Items {
			order = append(order, it.Version)
		}
		if len(order) != len(versions) {
			t.Fatalf("versions of e2e-order: %v, want %d", order, len(versions))
		}
		var lua bytes.Buffer
		for i := range order {
			for j := i + 1; j < len(order); j++ {
				fmt.Fprintf(&lua, "print(%q, %q, rpm.vercmp(%q, %q))\n", order[i], order[j], evr0(order[i]), evr0(order[j]))
			}
		}
		out := d.MustExec(t, "rpm.vercmp", "cat > /tmp/v.lua <<'EOF'\n"+lua.String()+"EOF\nrpm --eval \"%{lua: dofile('/tmp/v.lua')}\"")
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			f := strings.Fields(line)
			if n, err := strconv.Atoi(f[len(f)-1]); err != nil || n < 0 {
				t.Fatalf("the server lists %s before %s, rpm.vercmp says %s; the order is %v", f[0], f[1], f[len(f)-1], order)
			}
		}
	})
}

// rpmUpdatedAt returns the updated_at of the file p of repo.
func rpmUpdatedAt(t *testing.T, s *Server, repo, p string) time.Time {
	t.Helper()
	var page struct {
		Items []struct {
			Path      string    `json:"path"`
			UpdatedAt time.Time `json:"updated_at"`
		} `json:"items"`
	}
	s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/assets?limit=10&prefix="+p, nil, s.Admin(), &page)
	for _, it := range page.Items {
		if strings.TrimPrefix(it.Path, "/") == p {
			return it.UpdatedAt
		}
	}
	t.Fatalf("%s has no %s: %+v", repo, p, page.Items)
	return time.Time{}
}

// evr0 is the version v with its epoch, 0 when it has none, as rpm.vercmp compares full EVRs.
func evr0(v string) string {
	if strings.Contains(v, ":") {
		return v
	}
	return "0:" + v
}

// TestRPMSigning: after the repo's key changes to an imported RSA key, every package and the
// repodata are signed again, each package keeping the time of its upload: a client that trusts only the old key refuses the repo, a fresh one
// that trusts the new key installs from it, and so does Rocky 8, whose rpm checks only RSA keys.
func TestRPMSigning(t *testing.T) {
	t.Parallel()
	pkgs := buildRPMPackages(t)
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo := RandName("rpm")
		s.CreateRepo(t, repo, "rpm", true, nil)
		before := rpmMustUpload(t, s, repo, pkgs.anchor10, s.PublishAuth(t, s.AdminUser, repo), "e2e-anchor", "1.0.0-1", "x86_64")
		s.WaitDebianIndexed(t, repo)
		oldKey := s.RPMKey(t, repo, nil)
		updated := rpmUpdatedAt(t, s, repo, before.Filename)

		d := StartDnf(t, fedoraImage)
		d.Trust(t, repo, oldKey)
		d.AddRepo(t, repo, rpmBaseURL(s, repo), "", "")
		d.MustExec(t, "dnf install with the key trusted", "dnf -y install e2e-anchor && e2e-anchor")

		rsa := rsaSigningKey(t)
		name := RandName("rsa")
		s.MustAPI(t, http.MethodPost, "/api/signing-keys", map[string]any{"name": name, "armored": rsa}, s.Admin(), nil)
		s.MustAPI(t, http.MethodPatch, "/api/repos/"+repo, map[string]any{"fields": map[string]any{"signing_key": name}}, s.Admin(), nil)
		s.WaitDebianIndexed(t, repo)
		newKey := s.RPMKey(t, repo, nil)
		if archFingerprint(t, newKey) == archFingerprint(t, oldKey) {
			t.Fatal("key.asc did not change with the repo's key")
		}
		if st, b := debGet(t, s, opts.S3, "/rpm/"+repo+"/"+before.Filename, nil); st != http.StatusOK || archSum(b) == before.SHA256 {
			t.Fatalf("GET the package after the key change: status %d, the same file as before: %v", st, archSum(b) == before.SHA256)
		}
		if got := rpmUpdatedAt(t, s, repo, before.Filename); !got.Equal(updated) {
			t.Fatalf("the package signed again shows the time %s, want its upload's %s", got, updated)
		}
		if code, out := d.Exec(t, "dnf -y reinstall --refresh e2e-anchor"); code == 0 {
			t.Fatalf("dnf took repodata signed with a key it does not trust:\n%s", out)
		}
		for _, img := range []string{fedoraImage, rocky8Image} {
			fresh := StartDnf(t, img)
			fresh.Trust(t, repo, newKey)
			fresh.AddRepo(t, repo, rpmBaseURL(s, repo), "", "")
			out := fresh.MustExec(t, "dnf install after the key change on "+img, "dnf -y install e2e-anchor && e2e-anchor")
			archWant(t, "dnf install after the key change on "+img, out, "anchor 1.0.0\n")
		}
	})
}
