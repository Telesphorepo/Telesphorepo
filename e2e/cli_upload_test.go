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
// through the resumable upload routes, in more than one chunk) and .deb packages to the
// distribution and component of a Debian repo (a large package through the resumable upload
// routes as well), which apt then installs.
func TestCLIUpload(t *testing.T) {
	t.Parallel()
	c := newCLI(t)
	bigSize := cliBigUpload(t)
	debs := buildDebPackages(t)
	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		raw, deb := RandName("files"), RandName("apt")
		s.CreateRepo(t, raw, "raw", false, nil)
		s.CreateRepo(t, deb, "debian", true, nil)
		dev := RandName("dev")
		s.CreateUser(t, dev, "dev-pass-1234")
		for _, r := range []string{raw, deb} {
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
`, ClientOpts{
			Files: []ContainerFile{{Path: "/work/pkgs/hello-1.0.deb", Content: string(debs.hello1)}, {Path: "/work/pkgs/native.deb", Content: string(debs.native)}},
			Env: map[string]string{"RAW_LINK": s.NewSetupCode(t, dev, raw).Link, "DEB_LINK": s.NewSetupCode(t, dev, deb).Link,
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
	})
}
