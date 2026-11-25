//go:build clients

package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
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
// whose false precondition exits 3; --tree, whose files keep their paths) and .deb packages to the
// distribution and component of a Debian repo, .pkg.tar.* packages to an Arch repo and .rpm
// packages to an RPM repo (a large package of each through the resumable upload routes as well);
// the index of each repo names each package, whose file the server serves.
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
echo '=== tree publish ==='
mkdir -p out/Plex/versions/2.0.1/builds out/Plex/channels/dev/latest/minecraft
printf '{"build":617}\n' > out/Plex/versions/2.0.1/builds/617.json
printf '{"latest":617}\n' > out/Plex/channels/dev/latest.json
printf '{"minecraft":"26.2"}\n' > out/Plex/channels/dev/latest/minecraft/26.2.json
telesphorepo publish --tree out --into api/v1
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
echo "big deb sha256: $(sha256sum /tmp/e2e-big.deb | cut -d ' ' -f 1)"
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
echo "big arch sha256: $(sha256sum /tmp/e2e-bigpkg-1.0-1-any.pkg.tar.gz | cut -d ' ' -f 1)"
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

		// --tree publishes each file of the folder at its path inside it, under --into, with one
		// session.
		tree := section(out, "tree publish")
		if n := strings.Count(tree, "Publishing to"); n != 1 {
			t.Fatalf("tree publish: %d sessions, want 1:\n%s", n, tree)
		}
		for rel, body := range map[string]string{
			"Plex/versions/2.0.1/builds/617.json":          "{\"build\":617}\n",
			"Plex/channels/dev/latest.json":                "{\"latest\":617}\n",
			"Plex/channels/dev/latest/minecraft/26.2.json": "{\"minecraft\":\"26.2\"}\n",
		} {
			wantText(t, "tree publish", tree, "Uploaded out/"+rel+" to "+s.InternalURL+"/raw/"+raw+"/api/v1/"+rel)
			if b := getFollowS3(t, s, opts.S3, "/raw/"+raw+"/api/v1/"+rel); string(b) != body {
				t.Fatalf("GET api/v1/%s: %q, want %q", rel, b, body)
			}
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
		pkgs := debPackagesIndex(getFollowS3(t, s, opts.S3, "/debian/"+deb+"/dists/trixie/"+comp[1]+"/binary-"+debs.arch+"/Packages"))
		wantIndexed(t, s, opts.S3, "/debian/"+deb+"/", pkgs, map[string]string{"e2e-hello": archSum(debs.hello1), "e2e-native": archSum(debs.native), "e2e-big": shaOf(t, out, "big deb")})

		wantText(t, "publish an Arch package", section(out, "arch publish"), "Uploaded e2e-anchor 1.0.0-1 (x86_64) to "+arch)
		wantText(t, "publish a large Arch package", section(out, "big arch publish"), "Uploading /tmp/e2e-bigpkg-1.0-1-any.pkg.tar.gz (", "in chunks of",
			"Uploaded e2e-bigpkg 1.0-1 (any) to "+arch)
		s.WaitDebianIndexed(t, arch)
		pkgs = archDBIndex(t, getFollowS3(t, s, opts.S3, "/arch/"+arch+"/x86_64/"+arch+".db"))
		wantIndexed(t, s, opts.S3, "/arch/"+arch+"/x86_64/", pkgs, map[string]string{"e2e-anchor": archSum(archPkgs.anchor10), "e2e-bigpkg": shaOf(t, out, "big arch")})

		wantText(t, "publish an RPM package", section(out, "rpm publish"), "Uploaded e2e-anchor 1.0.0-1 (x86_64) to "+rpmRepo)
		wantText(t, "publish a large RPM package", section(out, "big rpm publish"), "Uploading /work/pkgs/e2e-bigrpm-1.0-1.noarch.rpm (", "in chunks of",
			"Uploaded e2e-bigrpm 1.0-1 (noarch) to "+rpmRepo)
		s.WaitDebianIndexed(t, rpmRepo)
		// The server signs each RPM package, so the file it serves is not the file of the upload.
		pkgs = rpmPrimaryIndex(t, s, opts.S3, rpmRepo)
		wantIndexed(t, s, opts.S3, "/rpm/"+rpmRepo+"/", pkgs, map[string]string{
			"Packages/e2e-anchor/e2e-anchor-1.0.0-1.x86_64.rpm": "", "Packages/e2e-bigrpm/e2e-bigrpm-1.0-1.noarch.rpm": ""})
	})
}

// wantIndexed checks that index names each package (or path) of want and that GET base+path answers bytes
// of the SHA-256 the index gives; a non-empty want value is the SHA-256 of the uploaded file,
// which the index must give too.
func wantIndexed(t *testing.T, s *Server, tg *S3Target, base string, index map[string]indexedFile, want map[string]string) {
	t.Helper()
	for name, upload := range want {
		f, ok := index[name]
		if !ok {
			t.Fatalf("the index of %s does not name %s: %v", base, name, index)
		}
		if upload != "" && f.sum != upload {
			t.Fatalf("the index of %s gives %s the SHA-256 %s, want the upload's %s", base, name, f.sum, upload)
		}
		if got := archSum(getFollowS3(t, s, tg, base+f.path)); got != f.sum {
			t.Fatalf("GET %s%s: SHA-256 %s, want the index's %s", base, f.path, got, f.sum)
		}
	}
}

// shaOf is the SHA-256 that the script printed as "<what> sha256: <hex>".
func shaOf(t *testing.T, out, what string) string {
	t.Helper()
	m := regexp.MustCompile(regexp.QuoteMeta(what) + ` sha256: ([0-9a-f]{64})`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no SHA-256 of %s in the output:\n%s", what, out)
	}
	return m[1]
}

// debPackagesIndex reads a Debian Packages file: the Filename and SHA256 of each package.
func debPackagesIndex(b []byte) map[string]indexedFile {
	index := map[string]indexedFile{}
	for _, stanza := range strings.Split(string(b), "\n\n") {
		fields := map[string]string{}
		for _, line := range strings.Split(stanza, "\n") {
			if k, v, ok := strings.Cut(line, ": "); ok {
				fields[k] = v
			}
		}
		if fields["Package"] != "" {
			index[fields["Package"]] = indexedFile{fields["Filename"], fields["SHA256"]}
		}
	}
	return index
}

// archDBIndex reads a pacman database: the %FILENAME% and %SHA256SUM% of each package's desc.
func archDBIndex(t *testing.T, b []byte) map[string]indexedFile {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("the pacman database is not gzip: %v", err)
	}
	tr := tar.NewReader(zr)
	index := map[string]indexedFile{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return index
		}
		if err != nil {
			t.Fatalf("read the pacman database: %v", err)
		}
		if path.Base(h.Name) != "desc" {
			continue
		}
		desc, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read %s of the pacman database: %v", h.Name, err)
		}
		fields := map[string]string{}
		for _, part := range strings.Split(string(desc), "\n\n") {
			if k, v, ok := strings.Cut(strings.TrimSpace(part), "\n"); ok {
				fields[strings.Trim(k, "%")] = v
			}
		}
		index[fields["NAME"]] = indexedFile{fields["FILENAME"], fields["SHA256SUM"]}
	}
}
