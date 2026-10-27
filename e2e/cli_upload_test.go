//go:build clients

package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"testing"
)

// cliBigUpload is a size that the CLI uploads through the resumable upload routes, in more than
// one chunk: one byte more than the threshold and one chunk of cmd/telesphorepo/upload.go.
func cliBigUpload(t testing.TB) int64 {
	t.Helper()
	return cliConst(t, "upload.go", "resumableFrom") + cliConst(t, "upload.go", "chunkSize") + 1
}

// TestCLIUpload: publish FILE uploads files into the folder "path" of a raw repo (a large file
// through the resumable upload routes, in more than one chunk; --into, --if-new and --if-match,
// whose false precondition exits 3) and .deb packages to the
// distribution and component of a Debian repo, and .pkg.tar.* packages to an Arch repo (a large
// package through the resumable upload routes as well), which apt and pacman then install.
func TestCLIUpload(t *testing.T) {
	t.Parallel()
	c := newCLI(t)
	bigSize := cliBigUpload(t)
	debs := buildDebPackages(t)
	archPkgs := buildArchPackages(t)
	rpmPkgs := buildRPMPackages(t)
	bigRPM := goRPM{name: "e2e-bigrpm", version: "1.0", release: "1", arch: "noarch", epoch: -1, payload: int(bigSize)}.bytes(t)
	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		raw, deb, arch, rpmRepo := RandName("files"), RandName("apt"), RandName("pacman"), RandName("dnf")
		s.CreateRepo(t, raw, "raw", false, map[string]any{"overwrite": "allow"})
		s.CreateRepo(t, deb, "debian", true, nil)
		s.CreateRepo(t, arch, "arch", true, nil)
		s.CreateRepo(t, rpmRepo, "rpm", true, nil)
		dev := RandName("dev")
		s.CreateUser(t, dev, "dev-pass-1234")
		for _, r := range []string{raw, deb, arch, rpmRepo} {
			s.SetPerms(t, r, dev, "repo.download", "repo.publish")
		}
		out := c.mustRun(t, debImage, "publish files", `set -e
mkdir -p /work/files
cd /work/files
echo '=== raw setup ==='
telesphorepo setup "$RAW_LINK"
printf 'path = "dist"\n' >> telesphorepo.toml
printf 'small file\n' > small.txt
head -c $BIG_SIZE /dev/urandom > big.bin
echo "big sha256: $(sha256sum big.bin | cut -d ' ' -f 1)"
echo '=== raw publish ==='
telesphorepo publish small.txt big.bin
rm big.bin
echo '=== conditional publish ==='
printf '{"build":1}\n' > latest.json
telesphorepo publish --if-new --into v1 latest.json
set +e
telesphorepo publish --if-new --into v1 latest.json
echo "if-new again: exit $?"
set -e
first=$(sha256sum latest.json | cut -d ' ' -f 1)
printf '{"build":2}\n' > latest.json
echo "second sha256: $(sha256sum latest.json | cut -d ' ' -f 1)"
telesphorepo publish --into=v1 --if-match "$first" latest.json
set +e
telesphorepo publish --into v1 --if-match "\"$first\"" latest.json
echo "stale if-match: exit $?"
telesphorepo publish --if-match "$first" latest.json small.txt
echo "if-match with two files: exit $?"
set -e
mkdir -p /work/deb
cd /work/deb
echo '=== deb setup ==='
telesphorepo setup "$DEB_LINK"
echo '=== deb project ==='
cat telesphorepo.toml
sed -i 's/^dist = .*/dist = "trixie"/' telesphorepo.toml
echo '=== deb project changed ==='
cat telesphorepo.toml
echo '=== deb publish ==='
telesphorepo publish /work/pkgs/hello-1.0.deb /work/pkgs/native.deb
mkdir -p /tmp/big/DEBIAN /tmp/big/usr/share/e2e-big
printf 'Package: e2e-big\nVersion: 1.0\nArchitecture: all\nMaintainer: E2E <e2e@telesphorepo.test>\nDescription: big\n' > /tmp/big/DEBIAN/control
head -c $BIG_SIZE /dev/urandom > /tmp/big/usr/share/e2e-big/blob
dpkg-deb --root-owner-group -Zgzip --build /tmp/big /tmp/e2e-big.deb >/dev/null
rm -rf /tmp/big
echo '=== big deb publish ==='
telesphorepo publish /tmp/e2e-big.deb
rm /tmp/e2e-big.deb
mkdir -p /work/arch
cd /work/arch
echo '=== arch setup ==='
telesphorepo setup "$ARCH_LINK"
echo '=== arch publish ==='
telesphorepo publish /work/pkgs/e2e-anchor-1.0.0-1-x86_64.pkg.tar.zst
mkdir -p /tmp/bigpkg/usr/share/e2e-bigpkg
printf 'pkgname = e2e-bigpkg\npkgver = 1.0-1\npkgdesc = big\narch = any\nsize = %s\n' $BIG_SIZE > /tmp/bigpkg/.PKGINFO
head -c $BIG_SIZE /dev/urandom > /tmp/bigpkg/usr/share/e2e-bigpkg/blob
tar -C /tmp/bigpkg -czf /tmp/e2e-bigpkg-1.0-1-any.pkg.tar.gz .PKGINFO usr
echo '=== big arch publish ==='
telesphorepo publish /tmp/e2e-bigpkg-1.0-1-any.pkg.tar.gz
mkdir -p /work/rpm
cd /work/rpm
echo '=== rpm setup ==='
telesphorepo setup "$RPM_LINK"
echo '=== rpm publish ==='
telesphorepo publish /work/pkgs/e2e-anchor-1.0.0-1.x86_64.rpm
echo '=== big rpm publish ==='
telesphorepo publish /work/pkgs/e2e-bigrpm-1.0-1.noarch.rpm
`, ClientOpts{
			Files: []ContainerFile{{Path: "/work/pkgs/hello-1.0.deb", Content: string(debs.hello1)}, {Path: "/work/pkgs/native.deb", Content: string(debs.native)},
				{Path: "/work/pkgs/e2e-anchor-1.0.0-1-x86_64.pkg.tar.zst", Content: string(archPkgs.anchor10)},
				{Path: "/work/pkgs/e2e-anchor-1.0.0-1.x86_64.rpm", Content: string(rpmPkgs.anchor10)},
				{Path: "/work/pkgs/e2e-bigrpm-1.0-1.noarch.rpm", Content: string(bigRPM)}},
			Env: map[string]string{"RAW_LINK": s.NewSetupCode(t, dev, raw).Link, "DEB_LINK": s.NewSetupCode(t, dev, deb).Link,
				"ARCH_LINK": s.NewSetupCode(t, dev, arch).Link, "RPM_LINK": s.NewSetupCode(t, dev, rpmRepo).Link,
				"BIG_SIZE": strconv.FormatInt(bigSize, 10)},
		})

		base := s.InternalURL + "/raw/" + raw + "/dist/"
		wantText(t, "publish raw files", section(out, "raw publish"), "Uploaded small.txt to "+base+"small.txt",
			"Uploading big.bin ("+strconv.FormatInt(bigSize, 10), "in chunks of", "Uploaded big.bin to "+base+"big.bin")
		m := regexp.MustCompile(`big sha256: ([0-9a-f]{64})`).FindStringSubmatch(out)
		if m == nil {
			t.Fatalf("no SHA-256 of big.bin in the output:\n%s", out)
		}
		if b := getFollowS3(t, s, opts.S3, "/raw/"+raw+"/dist/small.txt"); string(b) != "small file\n" {
			t.Fatalf("GET small.txt: %q", b)
		}
		b := getFollowS3(t, s, opts.S3, "/raw/"+raw+"/dist/big.bin")
		if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != m[1] {
			t.Fatalf("GET big.bin: %d bytes, want the file of SHA-256 %s", len(b), m[1])
		}

		// --into writes to its folder; --if-new and a stale --if-match exit 3 with the ETag of the
		// file now, and --if-match takes one FILE.
		cond := section(out, "conditional publish")
		second := regexp.MustCompile(`second sha256: ([0-9a-f]{64})`).FindStringSubmatch(cond)
		if second == nil {
			t.Fatalf("no SHA-256 of the second latest.json in the output:\n%s", cond)
		}
		first := sha256.Sum256([]byte("{\"build\":1}\n"))
		wantText(t, "conditional publish", cond, "Uploaded latest.json to "+s.InternalURL+"/raw/"+raw+"/v1/latest.json",
			`v1/latest.json changed on the server (now "`+hex.EncodeToString(first[:])+`"); read it again and publish again.`, "if-new again: exit 3",
			`v1/latest.json changed on the server (now "`+second[1]+`")`, "stale if-match: exit 3",
			"--if-match names the file on the server that one FILE replaces", "if-match with two files: exit 1")
		if b := getFollowS3(t, s, opts.S3, "/raw/"+raw+"/v1/latest.json"); string(b) != "{\"build\":2}\n" {
			t.Fatalf("GET v1/latest.json: %q, want the second build", b)
		}

		// Setup names the distribution and the component that it wrote into the project file.
		project := section(out, "deb project")
		dist := regexp.MustCompile(`(?m)^dist = "([^"]+)"$`).FindStringSubmatch(project)
		comp := regexp.MustCompile(`(?m)^component = "([^"]+)"$`).FindStringSubmatch(project)
		if dist == nil || comp == nil {
			t.Fatalf("telesphorepo.toml of the Debian repo has no dist and component:\n%s", project)
		}
		wantText(t, "set up a Debian repo", section(out, "deb setup"), dist[1], comp[1])
		wantText(t, "change the distribution", section(out, "deb project changed"), `dist = "trixie"`)
		to := "trixie/" + comp[1] + " of " + deb
		wantText(t, "publish .deb packages", section(out, "deb publish"), "Uploaded e2e-hello 1.0 (all) to "+to,
			"Uploaded e2e-native 1.0 ("+debs.arch+") to "+to)
		wantText(t, "publish a large .deb package", section(out, "big deb publish"), "Uploading /tmp/e2e-big.deb (", "in chunks of",
			"Uploaded e2e-big 1.0 (all) to "+to)
		s.WaitDebianIndexed(t, deb)
		apt := debAptInstall(t, s, deb, s.AdminUser, s.AdminPass, s.DebianKey(t, deb, nil))
		wantText(t, "apt install", apt, "RESULT: hello 1.0", "NATIVE: native "+debs.arch)

		wantText(t, "publish an Arch package", section(out, "arch publish"), "Uploaded e2e-anchor 1.0.0-1 (x86_64) to "+arch)
		wantText(t, "publish a large Arch package", section(out, "big arch publish"), "Uploading /tmp/e2e-bigpkg-1.0-1-any.pkg.tar.gz (", "in chunks of",
			"Uploaded e2e-bigpkg 1.0-1 (any) to "+arch)
		s.WaitDebianIndexed(t, arch)
		p := StartPacman(t)
		p.Trust(t, s.ArchKey(t, arch, nil))
		p.AddRepo(t, arch, archServer(s, "", ""))
		wantText(t, "pacman install", p.MustExec(t, "pacman install", "pacman -Sy --noconfirm e2e-anchor e2e-bigpkg && e2e-anchor"), "anchor 1.0.0")

		wantText(t, "publish an RPM package", section(out, "rpm publish"), "Uploaded e2e-anchor 1.0.0-1 (x86_64) to "+rpmRepo)
		wantText(t, "publish a large RPM package", section(out, "big rpm publish"), "Uploading /work/pkgs/e2e-bigrpm-1.0-1.noarch.rpm (", "in chunks of",
			"Uploaded e2e-bigrpm 1.0-1 (noarch) to "+rpmRepo)
		s.WaitDebianIndexed(t, rpmRepo)
		d := StartDnf(t, fedoraImage)
		d.Trust(t, rpmRepo, s.RPMKey(t, rpmRepo, nil))
		d.AddRepo(t, rpmRepo, rpmBaseURL(s, rpmRepo), "", "")
		wantText(t, "dnf install", d.MustExec(t, "dnf install", "dnf -y install e2e-anchor && e2e-anchor && dnf -q repoquery e2e-bigrpm"), "anchor 1.0.0", "e2e-bigrpm-0:1.0-1.noarch")
	})
}
