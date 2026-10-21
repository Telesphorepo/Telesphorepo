//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestDockerGroupBearer reads a private docker group with the bearer tokens of the token endpoint:
// a user who holds download on the group and on one member pulls through the group with the
// Docker CLI after docker login, with crane, and with a bearer token over HTTP. The group skips the
// member that the user may not read, the token reads no member directly, and every read answer
// carries Cache-Control: private.
func TestDockerGroupBearer(t *testing.T) {
	t.Parallel()
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		host := registryHost(s)
		arch := hostArch()
		mem, hidden, grp := RandName("gmem"), RandName("ghid"), RandName("grp")
		s.CreateRepo(t, mem, "docker", false, nil)
		s.CreateRepo(t, hidden, "docker", false, nil)

		// v1 is in both members; the hidden member is first in read order, so a reader of both gets
		// the hidden image and a reader of mem only gets the image of mem.
		mine, secret := tarImage(t, arch, rawRandom(t, 4096)), tarImage(t, arch, rawRandom(t, 4096))
		memClient := registryClient{base: s.URL, auth: s.PublishAuth(t, s.AdminUser, mem).apply}
		hidClient := registryClient{base: s.URL, auth: s.PublishAuth(t, s.AdminUser, hidden).apply}
		memClient.pushImage(t, mem+"/app", "v1", mine)
		hidClient.pushImage(t, hidden+"/app", "v1", secret)
		hidClient.pushImage(t, hidden+"/app", "only-hidden", secret)
		s.CreateGroup(t, grp, "docker", false, []string{hidden, mem})

		const user, pass = "greader", "greader-pass-1234"
		s.CreateUser(t, user, pass)
		s.SetPerms(t, mem, user, "repo.download")
		s.SetPerms(t, grp, user, "repo.download")
		ref := host + "/" + grp + "/app"

		t.Run("docker CLI", func(t *testing.T) {
			d := StartDind(t, host)
			d.MustExec(t, "pull through the group", fmt.Sprintf(`set -e
printf '%%s' '%[1]s' | docker login %[2]s -u %[3]s --password-stdin
docker pull %[4]s:v1
`, pass, host, user, ref))
			out := d.MustExec(t, "the digest of the pulled image", fmt.Sprintf("docker image inspect --format '{{json .RepoDigests}}' %s:v1\n", ref))
			if !strings.Contains(out, "sha256:"+mine.Hex) {
				t.Fatalf("docker pull through the group: repo digests %s, want the image of %s (sha256:%s)", out, mem, mine.Hex)
			}
			if code, out := d.Exec(t, fmt.Sprintf("docker pull %s:only-hidden 2>&1", ref)); code == 0 {
				t.Fatalf("docker pull of a tag of the hidden member through the group: exit 0, want a failure:\n%s", out)
			}
		})

		t.Run("crane", func(t *testing.T) {
			code, out := RunClient(t, craneImage, ClientOpts{Cmd: []string{fmt.Sprintf(
				`set -e; crane auth login %[1]s -u %[2]s -p '%[3]s'; crane pull --insecure %[4]s:v1 /tmp/img.tar; echo "DIGEST $(crane digest --insecure %[4]s:v1)"; echo "TAGS $(crane ls --insecure %[4]s | tr '\n' ' ')"`,
				host, user, pass, ref)}})
			if code != 0 || !strings.Contains(out, "DIGEST sha256:"+mine.Hex) {
				t.Fatalf("crane through the group: exit %d, want the digest sha256:%s:\n%s", code, mine.Hex, out)
			}
			if !strings.Contains(out, "TAGS v1") || strings.Contains(out, "only-hidden") {
				t.Fatalf("crane ls through the group: want v1 and no tag of the hidden member:\n%s", out)
			}
		})

		accept := map[string]string{"Accept": "application/vnd.oci.image.manifest.v1+json"}
		tags := func(t *testing.T, auth Auth) []string {
			t.Helper()
			resp, body := ociDo(t, s, http.MethodGet, "/v2/"+grp+"/app/tags/list", nil, auth, nil)
			wantOCI(t, "tags list of the group", resp, body, http.StatusOK, "")
			var tl struct {
				Tags []string `json:"tags"`
			}
			if err := json.Unmarshal(body, &tl); err != nil {
				t.Fatal(err)
			}
			return tl.Tags
		}

		t.Run("bearer token", func(t *testing.T) {
			st, bearer, b := registryToken(t, s, Basic{user, pass}, "repository:"+grp+"/app:pull")
			wantAPIStatus(t, "token for a pull of the group", st, b, http.StatusOK)

			resp, body := ociDo(t, s, http.MethodGet, "/v2/"+grp+"/app/manifests/v1", nil, bearer, accept)
			wantOCI(t, "GET the manifest through the group", resp, body, http.StatusOK, "")
			if got := resp.Header.Get("Docker-Content-Digest"); got != "sha256:"+mine.Hex {
				t.Fatalf("the group serves %s for v1 to %s, want the image of %s (sha256:%s)", got, user, mem, mine.Hex)
			}
			if cc := resp.Header.Get("Cache-Control"); cc != "private" {
				t.Fatalf("GET a manifest of a private group: Cache-Control %q, want private", cc)
			}
			resp, body = ociDo(t, s, http.MethodHead, "/v2/"+grp+"/app/manifests/v1", nil, bearer, accept)
			wantOCI(t, "HEAD the manifest through the group", resp, body, http.StatusOK, "")
			if cc := resp.Header.Get("Cache-Control"); cc != "private" {
				t.Fatalf("HEAD a manifest of a private group: Cache-Control %q, want private", cc)
			}

			resp, body = ociDo(t, s, http.MethodGet, "/v2/"+grp+"/app/blobs/sha256:"+mine.Layers[0], nil, bearer, nil)
			switch resp.StatusCode {
			case http.StatusOK:
				if cc := resp.Header.Get("Cache-Control"); cc != "private" {
					t.Fatalf("GET a blob of a private group: Cache-Control %q, want private", cc)
				}
			case http.StatusFound, http.StatusTemporaryRedirect:
				if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
					t.Fatalf("a redirect of a blob of a private group: Cache-Control %q, want no-store", cc)
				}
			default:
				t.Fatalf("GET a blob through the group: status %d: %s", resp.StatusCode, body)
			}

			resp, body = ociDo(t, s, http.MethodGet, "/v2/"+grp+"/app/manifests/only-hidden", nil, bearer, accept)
			wantOCI(t, "a tag of the hidden member through the group", resp, body, http.StatusNotFound, "MANIFEST_UNKNOWN")
			if got := tags(t, bearer); !slices.Contains(got, "v1") || slices.Contains(got, "only-hidden") {
				t.Fatalf("tags list of the group for %s: %v, want v1 and no tag of the hidden member", user, got)
			}

			// The token reads the group only: a member is not in its scope.
			resp, body = ociDo(t, s, http.MethodGet, "/v2/"+mem+"/app/manifests/v1", nil, bearer, accept)
			wantOCI(t, "the group's token on a member", resp, body, http.StatusUnauthorized, "UNAUTHORIZED")
			if ch := resp.Header.Get("WWW-Authenticate"); !strings.Contains(ch, `error="insufficient_scope"`) {
				t.Fatalf("the group's token on a member: challenge %q, want insufficient_scope", ch)
			}
		})

		t.Run("admin and anonymous", func(t *testing.T) {
			st, bearer, b := registryToken(t, s, s.AdminBasic(), "repository:"+grp+"/app:pull")
			wantAPIStatus(t, "admin token for a pull of the group", st, b, http.StatusOK)
			if got := headDigest(t, s, bearer, grp+"/app", "v1"); got != "sha256:"+secret.Hex {
				t.Fatalf("the group serves %s for v1 to the admin, want the hidden member's sha256:%s", got, secret.Hex)
			}
			if got := tags(t, bearer); !slices.Contains(got, "only-hidden") {
				t.Fatalf("tags list of the group for the admin: %v, want only-hidden", got)
			}

			st, anon, b := registryToken(t, s, nil, "repository:"+grp+"/app:pull")
			wantAPIStatus(t, "anonymous token for the private group", st, b, http.StatusOK)
			resp, body := ociDo(t, s, http.MethodGet, "/v2/"+grp+"/app/manifests/v1", nil, anon, accept)
			wantOCI(t, "anonymous bearer on the private group", resp, body, http.StatusUnauthorized, "UNAUTHORIZED")

			// An anonymous bearer reads a public group's public member. (The Docker CLI with the
			// containerd image store sends no token when the anonymous GET succeeds, so only a
			// client that presents the token reaches this path.)
			pubm, pubg := RandName("gpub"), RandName("gpubg")
			s.CreateRepo(t, pubm, "docker", true, nil)
			registryClient{base: s.URL, auth: s.PublishAuth(t, s.AdminUser, pubm).apply}.pushImage(t, pubm+"/app", "v1", mine)
			s.CreateGroup(t, pubg, "docker", true, []string{pubm})
			st, anon, b = registryToken(t, s, nil, "repository:"+pubg+"/app:pull")
			wantAPIStatus(t, "anonymous token for the public group", st, b, http.StatusOK)
			if got := headDigest(t, s, anon, pubg+"/app", "v1"); got != "sha256:"+mine.Hex {
				t.Fatalf("an anonymous bearer through the public group: digest %s, want sha256:%s", got, mine.Hex)
			}
		})
	})
}

// dgVersion is one item of GET /api/repos/{name}/versions of a docker repo.
type dgVersion struct {
	Version     string    `json:"version"`
	Repo        string    `json:"repo"`
	LatestBuild string    `json:"latest_build"`
	PublishedAt time.Time `json:"published_at"`
}

// dgWalkVersions reads every page of the versions of image folder dir of repo, limit at a time,
// and fails on a page longer than limit or a version twice.
func dgWalkVersions(t *testing.T, s *Server, repo, dir string, limit int, auth Auth) []dgVersion {
	t.Helper()
	var out []dgVersion
	seen := map[string]bool{}
	after := ""
	for pages := 0; ; pages++ {
		if pages > 1000 {
			t.Fatalf("versions of %s/%s: more than 1000 pages", repo, dir)
		}
		q := url.Values{"path": {dir}, "limit": {strconv.Itoa(limit)}}
		if after != "" {
			q.Set("after", after)
		}
		var page struct {
			Artifact *struct {
				Path string `json:"path"`
			} `json:"artifact"`
			Items []dgVersion `json:"items"`
			Next  *string     `json:"next"`
		}
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/versions?"+q.Encode(), nil, auth, &page)
		if page.Artifact == nil {
			t.Fatalf("versions of %s/%s after %q: no artifact", repo, dir, after)
		}
		if len(page.Items) > limit {
			t.Fatalf("versions of %s/%s: %d items, limit %d", repo, dir, len(page.Items), limit)
		}
		for _, v := range page.Items {
			if seen[v.Version] {
				t.Fatalf("versions of %s/%s, limit %d: %s twice", repo, dir, limit, v.Version)
			}
			seen[v.Version] = true
			out = append(out, v)
		}
		if page.Next == nil || *page.Next == "" {
			return out
		}
		after = *page.Next
	}
}

// dgMerge is what a group lists from the versions of its members (in read order): each tag from
// the first member that has it, newest first, by tag for the same time.
func dgMerge(members []string, versions [][]dgVersion) []dgVersion {
	byTag := map[string]bool{}
	var out []dgVersion
	for i, m := range members {
		for _, v := range versions[i] {
			if byTag[v.Version] {
				continue
			}
			byTag[v.Version] = true
			v.Repo = m
			out = append(out, v)
		}
	}
	slices.SortStableFunc(out, func(a, b dgVersion) int {
		if c := b.PublishedAt.Compare(a.PublishedAt); c != 0 {
			return c
		}
		return strings.Compare(b.Version, a.Version)
	})
	return out
}

// dgSameVersions fails unless got is want, item by item.
func dgSameVersions(t *testing.T, what string, got, want []dgVersion) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d versions, want %d:\n got %+v\nwant %+v", what, len(got), len(want), got, want)
	}
	for i, w := range want {
		g := got[i]
		if g.Version != w.Version || g.Repo != w.Repo || g.LatestBuild != w.LatestBuild || !g.PublishedAt.Equal(w.PublishedAt) {
			t.Fatalf("%s: item %d is %+v, want %+v", what, i, g, w)
		}
	}
}

// dgFile is one file of a build of a docker version.
type dgFile struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	Extension string `json:"extension"`
	Repo      string `json:"repo"`
}

// dgBuilds reads GET /api/repos/{repo}/builds of tag of image folder dir: the version (nil for
// none) and the files of its one build.
func dgBuilds(t *testing.T, s *Server, repo, dir, tag string, auth Auth) (*dgVersion, []dgFile) {
	t.Helper()
	var b struct {
		Version *dgVersion `json:"version"`
		Items   []struct {
			Files []dgFile `json:"files"`
		} `json:"items"`
	}
	s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/builds?"+url.Values{"path": {dir}, "version": {tag}}.Encode(), nil, auth, &b)
	if b.Version == nil {
		return nil, nil
	}
	if len(b.Items) != 1 || len(b.Items[0].Files) == 0 {
		t.Fatalf("builds of %s %s%s: %+v, want one build with files", repo, dir, tag, b)
	}
	return b.Version, b.Items[0].Files
}

// dgAssetPaths reads every page of GET /api/repos/{repo}/assets, limit at a time: the set of
// its file paths.
func dgAssetPaths(t *testing.T, s *Server, repo string, limit int, auth Auth) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	after := ""
	for pages := 0; ; pages++ {
		if pages > 1000 {
			t.Fatalf("assets of %s: more than 1000 pages", repo)
		}
		q := url.Values{"limit": {strconv.Itoa(limit)}}
		if after != "" {
			q.Set("after", after)
		}
		var page struct {
			Items []struct {
				Path string `json:"path"`
			} `json:"items"`
			Next *string `json:"next"`
		}
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/assets?"+q.Encode(), nil, auth, &page)
		for _, x := range page.Items {
			if out[x.Path] {
				t.Fatalf("assets of %s: %s twice", repo, x.Path)
			}
			out[x.Path] = true
		}
		if page.Next == nil || *page.Next == "" {
			return out
		}
		after = *page.Next
	}
}

// TestDockerGroupFiles browses the Files tab of a docker group of two hosted repos and a proxy
// as a hosted repo shows it: the tags of every member newest first, each from the first member
// that has it, paged without gaps or duplicates; the builds of a tag naming the member of the
// version and of each file; a pull of a shared tag serving the first member's image; only the
// members the reader reads; and the file count of the distinct paths of the members.
func TestDockerGroupFiles(t *testing.T) {
	t.Parallel()
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		rm := telesphorepoRemote(t)
		s := StartServer(t, opts)
		arch := hostArch()
		const image = "app"
		dir := image + "/"

		a, b, p, g := RandName("dga"), RandName("dgb"), RandName("dgp"), RandName("dgg")
		s.CreateRepo(t, a, "docker", true, nil)
		s.CreateRepo(t, b, "docker", false, nil) // the member the reader cannot read
		s.CreateProxy(t, p, "docker", true, map[string]any{"remote_url": rm.url, "username": rm.user, "password": rm.pass}, false)
		clients := map[string]registryClient{
			a: {base: s.URL, auth: s.PublishAuth(t, s.AdminUser, a).apply},
			b: {base: s.URL, auth: s.PublishAuth(t, s.AdminUser, b).apply},
		}

		// Each push gets its own second, so the tags have distinct times.
		imgs := map[string]ociImageBlobs{}
		push := func(repo, tag string, img ociImageBlobs) {
			t.Helper()
			clients[repo].pushImage(t, repo+"/"+image, tag, img)
			imgs[repo+":"+tag] = img
			time.Sleep(1100 * time.Millisecond)
		}
		aLayer := rawRandom(t, 4096)
		push(a, "a1", tarImage(t, arch, aLayer))
		push(b, "b1", tarImage(t, arch, aLayer, rawRandom(t, 2048))) // the layer path of a1 in B too
		push(a, "shared", tarImage(t, arch, rawRandom(t, 4096)))
		push(b, "shared", tarImage(t, arch, rawRandom(t, 4096))) // newer in B, another manifest

		// The proxy caches ptag by a pull: the manifest by tag, then its blobs.
		pimg := tarImage(t, arch, rawRandom(t, 4096))
		rm.client.pushImage(t, rm.prefix+image, "ptag", pimg)
		accept := map[string]string{"Accept": "application/vnd.oci.image.manifest.v1+json"}
		resp, body := ociDo(t, s, http.MethodGet, "/v2/"+p+"/"+image+"/manifests/ptag", nil, nil, accept)
		wantOCI(t, "pull ptag through the proxy", resp, body, http.StatusOK, "")
		if got := resp.Header.Get("Docker-Content-Digest"); got != "sha256:"+pimg.Hex {
			t.Fatalf("the proxy serves %s for ptag, want sha256:%s", got, pimg.Hex)
		}
		for _, h := range pimg.Order {
			resp, body := ociDo(t, s, http.MethodGet, "/v2/"+p+"/"+image+"/blobs/sha256:"+h, nil, nil, nil)
			wantOCI(t, "pull a blob of ptag through the proxy", resp, body, http.StatusOK, "")
			if sha256Hex(body) != h {
				t.Fatalf("blob %s through the proxy: other bytes", h)
			}
		}
		imgs[p+":ptag"] = pimg
		admin := s.AdminBasic()
		// The proxy stores a fetched blob while it streams it: wait until it holds every file of ptag.
		for deadline := time.Now().Add(10 * time.Second); ; {
			missing := "the manifest"
			var pb struct {
				Items []struct {
					Files []dgFile `json:"files"`
				} `json:"items"`
			}
			st, body := s.API(t, http.MethodGet, "/api/repos/"+p+"/builds?"+url.Values{"path": {dir}, "version": {"ptag"}}.Encode(), nil, admin)
			if st == http.StatusOK && json.Unmarshal(body, &pb) == nil && len(pb.Items) == 1 && len(pb.Items[0].Files) == len(pimg.Order)+1 {
				missing = ""
				have := dgAssetPaths(t, s, p, 200, admin)
				for _, f := range pb.Items[0].Files {
					if !have[f.Path] {
						missing = f.Path
					}
				}
			}
			if missing == "" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the proxy holds no %s 10 s after the pull", missing)
			}
			time.Sleep(200 * time.Millisecond)
		}
		time.Sleep(1100 * time.Millisecond)
		push(a, "a2", tarImage(t, arch, rawRandom(t, 4096)))
		push(b, "b2", tarImage(t, arch, rawRandom(t, 4096)))

		members := []string{a, b, p}
		s.CreateGroup(t, g, "docker", false, members)

		// Versions: the merge of the members' own versions, newest first, each tag once.
		per := make([][]dgVersion, len(members))
		for i, m := range members {
			per[i] = dgWalkVersions(t, s, m, dir, 200, admin)
		}
		want := dgMerge(members, per)
		var order []string
		for _, v := range want {
			order = append(order, v.Version)
		}
		// Push order, newest first; shared at the time of A's push, from A.
		if wantOrder := []string{"b2", "a2", "ptag", "shared", "b1", "a1"}; !slices.Equal(order, wantOrder) {
			t.Fatalf("the members' versions merge to %v, want %v", order, wantOrder)
		}
		for _, limit := range []int{1, 2, 200} {
			dgSameVersions(t, fmt.Sprintf("group versions, limit %d, as the administrator", limit), dgWalkVersions(t, s, g, dir, limit, admin), want)
		}
		shared := want[slices.Index(order, "shared")]
		aHex, bHex := imgs[a+":shared"].Hex, imgs[b+":shared"].Hex
		if shared.Repo != a || !strings.HasPrefix(aHex, shared.LatestBuild) || strings.HasPrefix(bHex, shared.LatestBuild) {
			t.Fatalf("group version shared: %+v, want the short digest of %s (sha256:%s)", shared, a, aHex)
		}

		// Builds: the version and each file name the member that serves it, the first member in
		// read order that stores the path.
		stored := make([]map[string]bool, len(members))
		for i, m := range members {
			stored[i] = dgAssetPaths(t, s, m, 7, admin)
		}
		holder := func(path string) string {
			for i, m := range members {
				if stored[i][path] {
					return m
				}
			}
			return ""
		}
		for _, v := range want {
			ver, files := dgBuilds(t, s, g, dir, v.Version, admin)
			if ver == nil || ver.Repo != v.Repo || ver.LatestBuild != v.LatestBuild {
				t.Fatalf("group builds of %s: version %+v, want the version of %s", v.Version, ver, v.Repo)
			}
			img := imgs[v.Repo+":"+v.Version]
			sums := map[string]bool{}
			for _, f := range files {
				sums[f.SHA256] = true
				if h := holder(f.Path); h == "" || f.Repo != h {
					t.Fatalf("group builds of %s: file %+v, want the member %q that stores it first", v.Version, f, h)
				}
				switch {
				case v.Version == "shared" && f.Repo != a:
					t.Fatalf("group builds of shared: file %+v, want %s", f, a)
				case v.Version == "ptag" && f.Repo != p:
					t.Fatalf("group builds of ptag: file %+v, want %s", f, p)
				case v.Version == "b1" && f.SHA256 == img.Layers[0] && f.Repo != a:
					t.Fatalf("group builds of b1: the layer that %s stores too is %+v, want %s", a, f, a)
				case v.Version == "b1" && f.SHA256 != img.Layers[0] && f.Repo != b:
					t.Fatalf("group builds of b1: file %+v, want %s", f, b)
				}
			}
			for _, h := range append([]string{img.Hex}, img.Order...) {
				if !sums[h] {
					t.Fatalf("group builds of %s: no file with sha256 %s (files %+v)", v.Version, h, files)
				}
			}
		}

		// A pull of shared through the group gets A's image.
		st, bearer, tb := registryToken(t, s, admin, "repository:"+g+"/"+image+":pull")
		wantAPIStatus(t, "token for a pull of the group", st, tb, http.StatusOK)
		resp, body = ociDo(t, s, http.MethodGet, "/v2/"+g+"/"+image+"/manifests/shared", nil, bearer, accept)
		wantOCI(t, "GET shared through the group", resp, body, http.StatusOK, "")
		if got := resp.Header.Get("Docker-Content-Digest"); got != "sha256:"+aHex || sha256Hex(body) != aHex {
			t.Fatalf("the group serves %s for shared, want the image of %s (sha256:%s)", got, a, aHex)
		}
		for _, h := range imgs[a+":shared"].Order {
			resp, body := ociDo(t, s, http.MethodGet, "/v2/"+g+"/"+image+"/blobs/sha256:"+h, nil, bearer, nil)
			wantOCI(t, "GET a blob of shared through the group", resp, body, http.StatusOK, "")
			if sha256Hex(body) != h {
				t.Fatalf("blob %s through the group: other bytes", h)
			}
		}

		// A reader of the group who cannot read B sees the versions of A and the proxy only.
		reader, pass := RandName("dgr"), "dg-reader-pass-1234"
		s.CreateUser(t, reader, pass)
		s.SetPerms(t, g, reader, "repo.download")
		readerAuth := Basic{reader, pass}
		readable := dgMerge([]string{a, p}, [][]dgVersion{per[0], per[2]})
		for _, limit := range []int{1, 200} {
			got := dgWalkVersions(t, s, g, dir, limit, readerAuth)
			dgSameVersions(t, fmt.Sprintf("group versions, limit %d, for a reader without %s", limit, b), got, readable)
			for _, v := range got {
				if v.Repo == b || v.Version == "b1" || v.Version == "b2" {
					t.Fatalf("group versions for a reader without %s: %+v", b, v)
				}
			}
		}
		for _, tag := range []string{"b1", "b2"} {
			st, body := s.API(t, http.MethodGet, "/api/repos/"+g+"/builds?"+url.Values{"path": {dir}, "version": {tag}}.Encode(), nil, readerAuth)
			if st != http.StatusOK || !strings.Contains(string(body), `"version":null`) {
				t.Fatalf("group builds of %s for a reader without %s: status %d: %s, want no version", tag, b, st, body)
			}
		}
		if ver, _ := dgBuilds(t, s, g, dir, "shared", readerAuth); ver == nil || ver.Repo != a {
			t.Fatalf("group builds of shared for a reader without %s: %+v, want the version of %s", b, ver, a)
		}

		// The file count of the group: the distinct paths of its members, each once.
		union := map[string]bool{}
		sum := 0
		for _, set := range stored {
			sum += len(set)
			for path := range set {
				union[path] = true
			}
		}
		if len(union) == sum {
			t.Fatalf("the members hold no path in common (%d files), want the tag shared in %s and %s", sum, a, b)
		}
		var repo struct {
			FileCount     int64 `json:"file_count"`
			CountsPending bool  `json:"counts_pending"`
		}
		for deadline := time.Now().Add(10 * time.Second); ; {
			s.MustAPI(t, http.MethodGet, "/api/repos/"+g, nil, admin, &repo)
			if !repo.CountsPending {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the group's counts are still pending 10 s after the last push")
			}
			time.Sleep(200 * time.Millisecond)
			repo.CountsPending = false
		}
		if repo.FileCount != int64(len(union)) {
			t.Fatalf("group file_count %d, want %d, the distinct paths of its members (%d files in all)", repo.FileCount, len(union), sum)
		}
	})
}
