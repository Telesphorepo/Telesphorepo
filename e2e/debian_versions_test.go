//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// Versions, builds and artifact snippets of a Debian package: a
// ~SNAPSHOT version groups its timestamped builds, like a Maven snapshot.

// debVersionsScript builds the urn packages with dpkg-deb into /out/<n>.deb, n counting the
// lines of debVersionsBuilds from 1. The names are plain because Podman cannot copy a path
// with "~" out of a container.
const debVersionsScript = `#!/bin/sh
set -e
mkdir -p /out
n=0
while read -r version arch; do
	n=$((n+1))
	d=/build/$version-$arch
	mkdir -p "$d/DEBIAN" "$d/usr/share/urn"
	printf 'Package: urn\nVersion: %s\nArchitecture: %s\nMaintainer: E2E <e2e@telesphorepo.test>\nDescription: telesphorepo e2e versions package\n' "$version" "$arch" > "$d/DEBIAN/control"
	printf '%s %s\n' "$version" "$arch" > "$d/usr/share/urn/version"
	dpkg-deb --root-owner-group -Zgzip --build "$d" "/out/$n.deb"
done < /builds
`

// The versions of urn: two snapshot versions, one of them with a build for two architectures,
// and a release.
var debVersionsBuilds = [][2]string{
	{"0.1~SNAPSHOT.20261003234857", "all"},
	{"3.0~SNAPSHOT.20261009160251", "all"},
	{"3.0~SNAPSHOT.20261014211855", "amd64"},
	{"3.0~SNAPSHOT.20261014211855", "arm64"},
	{"3.0", "all"},
}

func TestDebianVersions(t *testing.T) {
	var list strings.Builder
	for _, b := range debVersionsBuilds {
		list.WriteString(b[0] + " " + b[1] + "\n")
	}
	r := RunClientResult(t, debImage, ClientOpts{
		Cmd: []string{"sh", "/build.sh"},
		Files: []ContainerFile{
			{Content: debVersionsScript, Path: "/build.sh", Mode: 0o755},
			{Content: list.String(), Path: "/builds"},
		},
	})
	if r.ExitCode != 0 {
		t.Fatalf("building the urn packages failed (exit %d):\n%s", r.ExitCode, r.Output)
	}
	debs := make([][]byte, len(debVersionsBuilds))
	for i := range debVersionsBuilds {
		debs[i] = r.ReadFile(t, fmt.Sprintf("/out/%d.deb", i+1))
	}

	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo := RandName("debv")
		s.CreateRepo(t, repo, "debian", false, map[string]any{"architectures": []string{"amd64", "arm64"}})
		pub := s.PublishAuth(t, s.AdminUser, repo)
		for i, deb := range debs {
			if st, b := debUpload(t, s, repo, deb, pub); st != http.StatusCreated {
				t.Fatalf("upload urn %s %s: status %d, want 201: %s", debVersionsBuilds[i][0], debVersionsBuilds[i][1], st, b)
			}
		}
		read := s.AdminBasic()
		const dir = "pool/main/u/urn/"

		t.Run("versions", func(t *testing.T) {
			var v versionsJSON
			s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/versions?path="+dir, nil, read, &v)
			if v.Artifact == nil || v.Artifact.Path != dir || v.Artifact.Title != "urn" || v.Artifact.Kind != "package" {
				t.Fatalf("artifact = %+v", v.Artifact)
			}
			var got []string
			for _, it := range v.Items {
				got = append(got, it.Version+"/"+it.Kind)
			}
			if !slices.Equal(got, []string{"3.0/release", "3.0~SNAPSHOT/snapshot", "0.1~SNAPSHOT/snapshot"}) || v.Next != nil {
				t.Fatalf("versions = %v (next %v)", got, v.Next)
			}
			if rel := v.Items[0]; rel.BuildsCount != 1 || rel.LatestBuild != "" || rel.PublishedAt == "" {
				t.Fatalf("release = %+v", rel)
			}
			if snap := v.Items[1]; snap.BuildsCount != 2 || snap.LatestBuild != "3.0~SNAPSHOT.20261014211855" {
				t.Fatalf("3.0~SNAPSHOT = %+v", snap)
			}
			if snap := v.Items[2]; snap.BuildsCount != 1 || snap.LatestBuild != "0.1~SNAPSHOT.20261003234857" {
				t.Fatalf("0.1~SNAPSHOT = %+v", snap)
			}

			var page versionsJSON
			s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/versions?path="+dir+"&limit=2", nil, read, &page)
			if len(page.Items) != 2 || page.Next == nil || *page.Next != "3.0~SNAPSHOT" {
				t.Fatalf("first page = %+v", page)
			}
			s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/versions?path="+dir+"&limit=2&after="+url.QueryEscape(*page.Next), nil, read, &page)
			if len(page.Items) != 1 || page.Items[0].Version != "0.1~SNAPSHOT" || page.Next != nil {
				t.Fatalf("second page = %+v", page)
			}
		})

		t.Run("builds", func(t *testing.T) {
			var b buildsJSON
			s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/builds?path="+dir+"&version="+url.QueryEscape("3.0~SNAPSHOT"), nil, read, &b)
			if b.Artifact == nil || b.Artifact.Title != "urn" || b.Version == nil || b.Version.Version != "3.0~SNAPSHOT" || b.Version.BuildsCount != 2 {
				t.Fatalf("builds: %+v", b)
			}
			var ids []string
			for _, it := range b.Items {
				ids = append(ids, it.Build)
			}
			if !slices.Equal(ids, []string{"3.0~SNAPSHOT.20261014211855", "3.0~SNAPSHOT.20261009160251"}) {
				t.Fatalf("builds = %v", ids)
			}
			var files []string
			for _, f := range b.Items[0].Files {
				files = append(files, f.Path+" "+f.Classifier+" "+f.Extension)
			}
			want := []string{
				dir + "urn_3.0~SNAPSHOT.20261014211855_amd64.deb amd64 deb",
				dir + "urn_3.0~SNAPSHOT.20261014211855_arm64.deb arm64 deb",
			}
			if !slices.Equal(files, want) {
				t.Fatalf("files of the newest build = %v, want %v", files, want)
			}
			if len(b.Items[1].Files) != 1 || b.Items[1].Files[0].Classifier != "all" {
				t.Fatalf("files of the older build = %+v", b.Items[1].Files)
			}

			var page buildsJSON
			s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/builds?path="+dir+"&version="+url.QueryEscape("3.0~SNAPSHOT")+"&limit=1", nil, read, &page)
			if len(page.Items) != 1 || page.Next == nil || *page.Next != "3.0~SNAPSHOT.20261014211855" {
				t.Fatalf("first page = %+v", page)
			}
			s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/builds?path="+dir+"&version="+url.QueryEscape("3.0~SNAPSHOT")+"&limit=1&after="+url.QueryEscape(*page.Next), nil, read, &page)
			if len(page.Items) != 1 || page.Items[0].Build != "3.0~SNAPSHOT.20261009160251" || page.Next != nil {
				t.Fatalf("second page = %+v", page)
			}

			var rel buildsJSON
			s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/builds?path="+dir+"&version=3.0", nil, read, &rel)
			if rel.Version == nil || rel.Version.Kind != "release" || len(rel.Items) != 1 || rel.Items[0].Build != "3.0" {
				t.Fatalf("release builds = %+v", rel)
			}
		})

		t.Run("not a package", func(t *testing.T) {
			for _, p := range []string{"pool/main/u/", "pool/main/x/urn/", "pool/contrib/u/urn/", "dists/"} {
				var v versionsJSON
				s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/versions?path="+p, nil, read, &v)
				if v.Artifact != nil || len(v.Items) != 0 {
					t.Fatalf("%s: %+v", p, v)
				}
			}
			var b buildsJSON
			s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/builds?path="+dir+"&version=9.9", nil, read, &b)
			if b.Artifact != nil || b.Version != nil {
				t.Fatalf("a version that does not exist: %+v", b)
			}
		})

		t.Run("snippets", func(t *testing.T) {
			snippets := func(t *testing.T, path, version, mode string) artifactSnippetsJSON {
				t.Helper()
				var out artifactSnippetsJSON
				q := "/api/repos/" + repo + "/snippets?path=" + url.QueryEscape(path) + "&version=" + url.QueryEscape(version) + "&version_mode=" + mode
				s.MustAPI(t, http.MethodGet, q, nil, read, &out)
				return out
			}
			install := func(u artifactSnippetsJSON) string {
				for _, sn := range u.Snippets {
					if sn.Context != "artifact" || sn.Tool != "apt" {
						t.Fatalf("snippet %+v", sn)
					}
				}
				code := codeOf(u.Snippets, "apt")
				if !strings.Contains(code, "/debian/"+repo+" trixie main") {
					t.Fatalf("the source names no trixie main:\n%s", code)
				}
				_, line, _ := strings.Cut(code, "apt-get install ")
				line, _, _ = strings.Cut(line, "\n")
				return line
			}
			build := snippets(t, dir+"urn_3.0~SNAPSHOT.20261014211855_amd64.deb", "", "pinned")
			if build.Artifact == nil || build.Artifact.Title != "urn" || install(build) != "urn=3.0~SNAPSHOT.20261014211855" {
				t.Fatalf("a build: %+v", build)
			}
			snap := snippets(t, dir, "3.0~SNAPSHOT", "pinned")
			if snap.Version != "3.0~SNAPSHOT" || install(snap) != "urn" {
				t.Fatalf("a snapshot version: %+v", snap)
			}
			rel := snippets(t, dir, "", "pinned")
			if rel.Version != "3.0" || install(rel) != "urn=3.0" {
				t.Fatalf("the artifact: %+v", rel)
			}
			if latest := snippets(t, dir, "3.0", "latest"); install(latest) != "urn" {
				t.Fatalf("latest mode: %+v", latest)
			}
		})
	})
}
