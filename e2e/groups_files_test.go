//go:build e2e

package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
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

// walkTree reads every page of folder dir of repo, limit entries at a time, and fails on a page
// longer than limit, a path out of order or a path twice.
func walkTree(t *testing.T, s *Server, repo, dir string, limit int, auth Auth) []treeNode {
	t.Helper()
	var out []treeNode
	after := ""
	for pages := 0; ; pages++ {
		if pages > 1000 {
			t.Fatalf("tree of %s/%s: more than 1000 pages", repo, dir)
		}
		q := url.Values{"path": {dir}, "limit": {strconv.Itoa(limit)}}
		if after != "" {
			q.Set("after", after)
		}
		st, b := s.API(t, http.MethodGet, "/api/repos/"+repo+"/tree?"+q.Encode(), nil, auth)
		if st != http.StatusOK {
			t.Fatalf("tree of %s/%s after %q: status %d: %s", repo, dir, after, st, b)
		}
		var page struct {
			Items []treeNode `json:"items"`
			Next  *string    `json:"next"`
		}
		if err := json.Unmarshal(b, &page); err != nil {
			t.Fatalf("tree of %s/%s: %v: %s", repo, dir, err, b)
		}
		if len(page.Items) > limit {
			t.Fatalf("tree of %s/%s: %d entries, limit %d", repo, dir, len(page.Items), limit)
		}
		for _, n := range page.Items {
			if len(out) > 0 && out[len(out)-1].Path >= n.Path {
				t.Fatalf("tree of %s/%s: %q after %q: out of order or twice", repo, dir, n.Path, out[len(out)-1].Path)
			}
			out = append(out, n)
		}
		if page.Next == nil || *page.Next == "" {
			return out
		}
		after = *page.Next
	}
}

// unionTree is what a group lists at dir: the entries of each member in read order, a path once,
// a file from the first member that holds it (named in Repo), a folder without a member.
func unionTree(t *testing.T, s *Server, members []string, dir string, auth Auth) []treeNode {
	t.Helper()
	seen := map[string]bool{}
	var out []treeNode
	for _, m := range members {
		for _, n := range walkTree(t, s, m, dir, 200, auth) {
			if seen[n.Path] {
				continue
			}
			seen[n.Path] = true
			n.Repo = ""
			if n.Type == "file" {
				n.Repo = m
			}
			out = append(out, n)
		}
	}
	slices.SortFunc(out, func(a, b treeNode) int {
		switch {
		case a.Path < b.Path:
			return -1
		case a.Path > b.Path:
			return 1
		}
		return 0
	})
	return out
}

func sameTree(t *testing.T, what string, got, want []treeNode) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d entries, want %d:\n got %+v\nwant %+v", what, len(got), len(want), got, want)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Path != w.Path || g.Type != w.Type || g.Repo != w.Repo || g.SHA256 != w.SHA256 {
			t.Fatalf("%s: entry %d is %+v, want %+v", what, i, g, w)
		}
	}
}

// TestGroupFiles lists the files of a Maven group of a repository, a proxy of a second server
// and a private repository: the union of the members the reader reads, folders merged, a path
// held by two members from the first, paged without gaps or duplicates, and no writes.
func TestGroupFiles(t *testing.T) {
	t.Parallel()
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		remote := StartServer(t, ServerOpts{})
		s := StartServer(t, opts)

		const base = "com/e2e/merge/"
		shared := base + "shared/1.0/shared-1.0.txt"
		many := base + "many/1.0/"
		put := func(srv *Server, auth Auth, repo, path, body string) {
			t.Helper()
			resp, b := rawPut(t, srv, "/maven/"+repo+"/"+path, []byte(body), auth)
			if resp.StatusCode/100 != 2 {
				t.Fatalf("PUT /maven/%s/%s: status %d: %s", repo, path, resp.StatusCode, b)
			}
		}

		// The remote of the proxy: a second server.
		up := RandName("upstream")
		remote.CreateRepo(t, up, "maven", true, nil)
		upAuth := remote.PublishAuth(t, remote.AdminUser, up)
		remoteFiles := []string{shared, base + "proxied/1.0/proxied-1.0.txt", "org/e2e/remote/1.0/remote-1.0.txt"}
		for i := range 6 {
			remoteFiles = append(remoteFiles, fmt.Sprintf("%smany-1.0-r%02d.txt", many, i))
		}
		for i := range 4 {
			remoteFiles = append(remoteFiles, fmt.Sprintf("%smany-1.0-s%02d.txt", many, i)) // the repository holds these too
		}
		for _, p := range remoteFiles {
			put(remote, upAuth, up, p, "from the remote: "+p)
		}

		hosted := RandName("hosted")
		s.CreateRepo(t, hosted, "maven", true, nil)
		hostedAuth := s.PublishAuth(t, s.AdminUser, hosted)
		put(s, hostedAuth, hosted, shared, "from the repository")
		put(s, hostedAuth, hosted, base+"hosted/1.0/hosted-1.0.txt", "hosted")
		for i := range 6 {
			put(s, hostedAuth, hosted, fmt.Sprintf("%smany-1.0-h%02d.txt", many, i), "hosted")
		}
		for i := range 4 {
			put(s, hostedAuth, hosted, fmt.Sprintf("%smany-1.0-s%02d.txt", many, i), "from the repository")
		}

		// The proxy keeps the files that a read asked for.
		central := RandName("central")
		s.CreateProxy(t, central, "maven", true, map[string]any{"remote_url": remote.InternalURL + "/maven/" + up}, false)
		for _, p := range remoteFiles {
			rawWantGet(t, s, opts.S3, "/maven/"+central+"/"+p, nil, []byte("from the remote: "+p))
		}

		// A private member: only people who can read it see its files through the group.
		secret := RandName("secret")
		s.CreateRepo(t, secret, "maven", false, nil)
		secretAuth := s.PublishAuth(t, s.AdminUser, secret)
		put(s, secretAuth, secret, base+"secret/1.0/secret-1.0.txt", "secret")
		put(s, secretAuth, secret, many+"many-1.0-z00.txt", "secret")

		grp := RandName("merged")
		members := []string{hosted, central, secret}
		s.CreateGroup(t, grp, "maven", true, members)

		admin := s.AdminBasic()
		for _, dir := range []string{"", base, many} {
			want := unionTree(t, s, members, dir, admin)
			for _, limit := range []int{1, 3, 200} {
				sameTree(t, fmt.Sprintf("group tree of %q, limit %d, as the administrator", dir, limit), walkTree(t, s, grp, dir, limit, admin), want)
			}
		}

		// The top folder: com/ from the repository and the proxy is one folder, org/ comes from the proxy only.
		top := walkTree(t, s, grp, "", 200, admin)
		if len(top) != 2 || top[0].Path != "com/" || top[1].Path != "org/" || top[0].Type != "dir" || top[0].Repo != "" {
			t.Fatalf("group top folder: %+v, want the folders com/ and org/ without a member", top)
		}
		// The path in both the repository and the proxy comes from the repository, the first member.
		sum := sha256.Sum256([]byte("from the repository"))
		got := walkTree(t, s, grp, base+"shared/1.0/", 200, admin)
		if len(got) != 1 || got[0].Repo != hosted || got[0].SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatalf("group tree of the shared folder: %+v, want one file of %s with the sha256 of its bytes", got, hosted)
		}
		var f struct {
			Repo   string `json:"repo"`
			SHA256 string `json:"sha256"`
		}
		s.MustAPI(t, http.MethodGet, "/api/repos/"+grp+"/file?path="+url.QueryEscape(shared), nil, admin, &f)
		if f.Repo != hosted || f.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatalf("group file details of %s: %+v, want the file of %s", shared, f, hosted)
		}
		s.MustAPI(t, http.MethodGet, "/api/repos/"+grp+"/file?path="+url.QueryEscape(base+"proxied/1.0/proxied-1.0.txt"), nil, admin, &f)
		if f.Repo != central {
			t.Fatalf("group file details of the proxied file: %+v, want the file of %s", f, central)
		}

		// Anonymous: the private member is left out.
		for _, dir := range []string{base, many} {
			want := unionTree(t, s, []string{hosted, central}, dir, nil)
			for _, n := range want {
				if n.Repo == secret {
					t.Fatalf("anonymous union holds %s of the private member", n.Path)
				}
			}
			sameTree(t, fmt.Sprintf("group tree of %q, limit 2, anonymous", dir), walkTree(t, s, grp, dir, 2, nil), want)
		}
		if st, b := s.API(t, http.MethodGet, "/api/repos/"+grp+"/file?path="+url.QueryEscape(base+"secret/1.0/secret-1.0.txt"), nil, nil); st != http.StatusNotFound {
			t.Fatalf("anonymous file details of the private member's file through the group: status %d, want 404: %s", st, b)
		}

		// The counters: each path once, from the first member that the reader reads.
		wantUnionCounts(t, s, grp, members, admin)
		wantUnionCounts(t, s, grp, []string{hosted, central}, nil)
		// A publish to a member and a delete keep them: the shared path stays one file, now of the
		// proxy; a new path counts once.
		put(s, hostedAuth, hosted, base+"late/1.0/late-1.0.txt", "late")
		s.MustAPI(t, http.MethodPost, "/api/repos/"+hosted+"/files/delete", map[string][]string{"paths": {shared}}, s.Admin(), nil)
		wantUnionCounts(t, s, grp, members, admin)
		wantUnionCounts(t, s, grp, []string{hosted, central}, nil)
		put(s, hostedAuth, hosted, shared, "from the repository")

		// A bad cursor is refused as in any repository.
		if st, b := s.API(t, http.MethodGet, "/api/repos/"+grp+"/tree?after=..%2Fx", nil, admin); st != http.StatusBadRequest {
			t.Fatalf("group tree with a bad cursor: status %d, want 400: %s", st, b)
		}

		// A group takes no writes: no delete, no publish token, no PUT.
		if st, b := s.API(t, http.MethodPost, "/api/repos/"+grp+"/files/delete", map[string]any{"paths": []string{shared}}, s.Admin()); st != http.StatusMethodNotAllowed {
			t.Fatalf("delete files of a group: status %d, want 405: %s", st, b)
		}
		if st, b := s.ManualSession(t, s.Admin(), s.AdminUser, grp); st != http.StatusConflict {
			t.Fatalf("manual publish session for a group: status %d, want 409: %s", st, b)
		}
		if resp, b := rawPut(t, s, "/maven/"+grp+"/"+shared, []byte("x"), admin); resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("PUT to the group: status %d, want 405: %s", resp.StatusCode, b)
		}
		// The repository's file is still the one the group serves.
		rawWantGet(t, s, opts.S3, "/maven/"+grp+"/"+shared, nil, []byte("from the repository"))
	})
}

// groupVersion is one item of GET /api/repos/{name}/versions.
type groupVersion struct {
	Version     string `json:"version"`
	Repo        string `json:"repo"`
	BuildsCount int    `json:"builds_count"`
}

// walkVersions reads every page of the versions of artifact folder dir of repo, limit at a time,
// and fails on a page longer than limit or a version twice.
func walkVersions(t *testing.T, s *Server, repo, dir string, limit int, auth Auth) []groupVersion {
	t.Helper()
	var out []groupVersion
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
			Items []groupVersion `json:"items"`
			Next  *string        `json:"next"`
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

// TestGroupVersions browses the versions of a Maven artifact in a group of two repositories and
// a proxy as a hosted repository shows them: the versions of every member in the order of the
// format, each from the first member that has it, paged without gaps or duplicates; a version
// page and the file details naming the member; the proxy's cached versions only; only the members
// that the reader reads; and no writes.
func TestGroupVersions(t *testing.T) {
	t.Parallel()
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		remote := StartServer(t, ServerOpts{})
		s := StartServer(t, opts)

		const art = "com/e2e/gv/lib/"
		put := func(srv *Server, auth Auth, repo, v string) {
			t.Helper()
			for _, ext := range []string{"pom", "jar"} {
				p := art + v + "/lib-" + v + "." + ext
				resp, b := rawPut(t, srv, "/maven/"+repo+"/"+p, []byte(repo+" "+p), auth)
				if resp.StatusCode/100 != 2 {
					t.Fatalf("PUT /maven/%s/%s: status %d: %s", repo, p, resp.StatusCode, b)
				}
			}
		}
		jar := func(v string) string { return art + v + "/lib-" + v + ".jar" }
		sumOf := func(repo, v string) string {
			sum := sha256.Sum256([]byte(repo + " " + jar(v)))
			return hex.EncodeToString(sum[:])
		}

		// The remote of the proxy holds 3.0 and 4.0; a build asks the proxy for 3.0 only.
		up := RandName("upstream")
		remote.CreateRepo(t, up, "maven", true, nil)
		upAuth := remote.PublishAuth(t, remote.AdminUser, up)
		put(remote, upAuth, up, "3.0")
		put(remote, upAuth, up, "4.0")
		central := RandName("central")
		s.CreateProxy(t, central, "maven", false, map[string]any{"remote_url": remote.InternalURL + "/maven/" + up}, false)
		for _, ext := range []string{"pom", "jar"} {
			p := art + "3.0/lib-3.0." + ext
			rawWantGet(t, s, opts.S3, "/maven/"+central+"/"+p, s.AdminBasic(), []byte(up+" "+p))
		}

		first, second := RandName("first"), RandName("second")
		for _, r := range []string{first, second} {
			s.CreateRepo(t, r, "maven", false, nil)
		}
		firstAuth, secondAuth := s.PublishAuth(t, s.AdminUser, first), s.PublishAuth(t, s.AdminUser, second)
		for _, v := range []string{"1.0", "2.0"} {
			put(s, firstAuth, first, v)
		}
		for _, v := range []string{"1.5", "2.0", "2.5"} { // 2.0 is in both repositories
			put(s, secondAuth, second, v)
		}
		// A hosted repository with every version: the order of the format.
		ref := RandName("ref")
		s.CreateRepo(t, ref, "maven", false, nil)
		refAuth := s.PublishAuth(t, s.AdminUser, ref)
		for _, v := range []string{"1.0", "1.5", "2.0", "2.5", "3.0"} {
			put(s, refAuth, ref, v)
		}

		grp := RandName("versions")
		s.CreateGroup(t, grp, "maven", false, []string{first, second, central})
		admin := s.AdminBasic()

		want := map[string]string{"1.0": first, "1.5": second, "2.0": first, "2.5": second, "3.0": central}
		var order []string
		for _, v := range walkVersions(t, s, ref, art, 200, admin) {
			order = append(order, v.Version)
		}
		for _, limit := range []int{1, 2, 3, 200} {
			got := walkVersions(t, s, grp, art, limit, admin)
			if len(got) != len(order) {
				t.Fatalf("group versions, limit %d: %+v, want the versions %v", limit, got, order)
			}
			for i, v := range got {
				if v.Version != order[i] || v.Repo != want[v.Version] {
					t.Fatalf("group versions, limit %d: item %d is %+v, want %s of %s (order %v)", limit, i, v, order[i], want[order[i]], order)
				}
			}
		}

		// A version page: the builds of the member that serves the version.
		builds := func(v string, auth Auth) (string, []string) {
			t.Helper()
			var b struct {
				Version *struct {
					Repo string `json:"repo"`
				} `json:"version"`
				Items []struct {
					Files []struct {
						Path   string `json:"path"`
						SHA256 string `json:"sha256"`
					} `json:"files"`
				} `json:"items"`
			}
			s.MustAPI(t, http.MethodGet, "/api/repos/"+grp+"/builds?"+url.Values{"path": {art}, "version": {v}}.Encode(), nil, auth, &b)
			if b.Version == nil || len(b.Items) != 1 {
				t.Fatalf("group builds of %s: %+v, want one build", v, b)
			}
			var sums []string
			for _, f := range b.Items[0].Files {
				if f.Path == jar(v) {
					sums = append(sums, f.SHA256)
				}
			}
			return b.Version.Repo, sums
		}
		for v, m := range want {
			repo, sums := builds(v, admin)
			writer := m // the proxy keeps the bytes of its remote
			if m == central {
				writer = up
			}
			if repo != m || len(sums) != 1 || sums[0] != sumOf(writer, v) {
				t.Fatalf("group builds of %s: member %s, jar sha256 %v, want the jar of %s", v, repo, sums, m)
			}
		}
		// The file details name the member, the first one that has the path.
		var f struct {
			Repo   string `json:"repo"`
			SHA256 string `json:"sha256"`
		}
		s.MustAPI(t, http.MethodGet, "/api/repos/"+grp+"/file?path="+url.QueryEscape(jar("2.0")), nil, admin, &f)
		if f.Repo != first || f.SHA256 != sumOf(first, "2.0") {
			t.Fatalf("group file details of %s: %+v, want the file of %s", jar("2.0"), f, first)
		}
		s.MustAPI(t, http.MethodGet, "/api/repos/"+grp+"/file?path="+url.QueryEscape(jar("2.5")), nil, admin, &f)
		if f.Repo != second {
			t.Fatalf("group file details of %s: %+v, want the file of %s", jar("2.5"), f, second)
		}
		// The files of a version folder, as the details read the checksum files of a file.
		var as struct {
			Items []struct {
				Path string `json:"path"`
				Repo string `json:"repo"`
			} `json:"items"`
		}
		s.MustAPI(t, http.MethodGet, "/api/repos/"+grp+"/assets?prefix="+url.QueryEscape(art+"2.0/"), nil, admin, &as)
		if len(as.Items) != 2 || as.Items[0].Repo != first || as.Items[1].Repo != first {
			t.Fatalf("group assets of %s2.0/: %+v, want the two files of %s", art, as.Items, first)
		}
		// The snippets of the artifact name the group.
		var sn struct {
			Artifact *struct{} `json:"artifact"`
			Version  string    `json:"version"`
			Snippets []struct {
				Code string `json:"code"`
			} `json:"snippets"`
		}
		s.MustAPI(t, http.MethodGet, "/api/repos/"+grp+"/snippets?"+url.Values{"path": {art}, "version": {"2.5"}}.Encode(), nil, admin, &sn)
		joined := ""
		for _, x := range sn.Snippets {
			joined += x.Code
		}
		if sn.Artifact == nil || sn.Version != "2.5" || !strings.Contains(joined, "/maven/"+grp) || strings.Contains(joined, "/maven/"+second) {
			t.Fatalf("group snippets of %s 2.5: %+v, want the snippets of the group", art, sn)
		}
		// The counters count each path once, from the first member that has it.
		wantUnionCounts(t, s, grp, []string{first, second, central}, admin)

		// A reader of the group and the second repository only sees the second repository's versions.
		reader := RandName("reader")
		s.CreateUser(t, reader, "reader-password-1")
		s.SetPerms(t, grp, reader, "repo.download")
		s.SetPerms(t, second, reader, "repo.download")
		readerAuth := Basic{reader, "reader-password-1"}
		got := walkVersions(t, s, grp, art, 1, readerAuth)
		var names []string
		for _, v := range got {
			if v.Repo != second {
				t.Fatalf("group versions for a reader of %s: %+v from another member", second, v)
			}
			names = append(names, v.Version)
		}
		var wantNames []string
		for _, v := range order {
			if slices.Contains([]string{"1.5", "2.0", "2.5"}, v) {
				wantNames = append(wantNames, v)
			}
		}
		if !slices.Equal(names, wantNames) {
			t.Fatalf("group versions for a reader of %s: %v, want the versions of %s", second, names, second)
		}
		if repo, _ := builds("2.0", readerAuth); repo != second {
			t.Fatalf("group builds of 2.0 for a reader of %s: member %s", second, repo)
		}
		if st, b := s.API(t, http.MethodGet, "/api/repos/"+grp+"/builds?"+url.Values{"path": {art}, "version": {"1.0"}}.Encode(), nil, readerAuth); st != http.StatusOK || !strings.Contains(string(b), `"version":null`) {
			t.Fatalf("group builds of 1.0 for a reader of %s: status %d: %s, want no version", second, st, b)
		}

		// A bad cursor is refused.
		if st, b := s.API(t, http.MethodGet, "/api/repos/"+grp+"/versions?path="+url.QueryEscape(art)+"&after=x!", nil, admin); st != http.StatusBadRequest {
			t.Fatalf("group versions with a bad cursor: status %d, want 400: %s", st, b)
		}
		// Read-only: no delete, no publish session.
		if st, b := s.API(t, http.MethodPost, "/api/repos/"+grp+"/files/delete", map[string]any{"prefixes": []string{art + "2.0/"}}, s.Admin()); st != http.StatusMethodNotAllowed {
			t.Fatalf("delete a version of a group: status %d, want 405: %s", st, b)
		}
		if st, b := s.ManualSession(t, s.Admin(), s.AdminUser, grp); st != http.StatusConflict {
			t.Fatalf("manual publish session for a group: status %d, want 409: %s", st, b)
		}
	})
}

// gfAsset is one item of GET /api/repos/{name}/assets.
type gfAsset struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// memberAssets reads every page of the files of repo.
func memberAssets(t *testing.T, s *Server, repo string, auth Auth) []gfAsset {
	t.Helper()
	var out []gfAsset
	after := ""
	for pages := 0; pages < 1000; pages++ {
		q := url.Values{"limit": {"200"}}
		if after != "" {
			q.Set("after", after)
		}
		var page struct {
			Items []gfAsset `json:"items"`
			Next  *string   `json:"next"`
		}
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/assets?"+q.Encode(), nil, auth, &page)
		out = append(out, page.Items...)
		if page.Next == nil || *page.Next == "" {
			return out
		}
		after = *page.Next
	}
	t.Fatalf("files of %s: more than 1000 pages", repo)
	return nil
}

// groupCounters reads the counters of group repo for auth, once the server has counted them.
func groupCounters(t *testing.T, s *Server, repo string, auth Auth) (files, size int64) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		var r struct {
			FileCount     int64 `json:"file_count"`
			TotalSize     int64 `json:"total_size"`
			CountsPending bool  `json:"counts_pending"`
		}
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo, nil, auth, &r)
		if !r.CountsPending {
			// The list of repositories answers the same counters.
			var all struct {
				Items []struct {
					Name          string `json:"name"`
					FileCount     int64  `json:"file_count"`
					TotalSize     int64  `json:"total_size"`
					CountsPending bool   `json:"counts_pending"`
				} `json:"items"`
			}
			s.MustAPI(t, http.MethodGet, "/api/repos?name="+repo, nil, auth, &all)
			for _, x := range all.Items {
				if x.Name == repo && (x.CountsPending || x.FileCount != r.FileCount || x.TotalSize != r.TotalSize) {
					t.Fatalf("the list of repositories has %+v for %s, GET of the repo %+v", x, repo, r)
				}
			}
			return r.FileCount, r.TotalSize
		}
		if time.Now().After(deadline) {
			t.Fatalf("the counters of %s are still pending", repo)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// wantUnionCounts checks the counters of group grp for auth: each path of the members (in read
// order, those auth reads) once, with the size of the first member's copy.
func wantUnionCounts(t *testing.T, s *Server, grp string, members []string, auth Auth) {
	t.Helper()
	seen := map[string]bool{}
	var files, size int64
	for _, m := range members {
		for _, a := range memberAssets(t, s, m, auth) {
			if !seen[a.Path] {
				seen[a.Path] = true
				files++
				size += a.Size
			}
		}
	}
	if gotFiles, gotSize := groupCounters(t, s, grp, auth); gotFiles != files || gotSize != size {
		t.Fatalf("counters of %s: %d files, %d bytes; want %d files, %d bytes: each path of %v once", grp, gotFiles, gotSize, files, size, members)
	}
}

// gfBuild is one build of GET /api/repos/{name}/builds.
type gfBuild struct {
	ID    string `json:"id"`
	Files []struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
		Repo   string `json:"repo"`
		Remote bool   `json:"remote"`
	} `json:"files"`
}

// gfVersionOf is the version part of GET /api/repos/{name}/builds.
type gfVersionOf struct {
	Version     string `json:"version"`
	Repo        string `json:"repo"`
	BuildsCount int    `json:"builds_count"`
	LatestBuild string `json:"latest_build"`
	Remote      bool   `json:"remote"`
}

// walkBuilds reads every page of the builds of version v of artifact folder dir of repo.
func walkBuilds(t *testing.T, s *Server, repo, dir, v string, limit int, auth Auth) (*gfVersionOf, []gfBuild) {
	t.Helper()
	var ver *gfVersionOf
	var out []gfBuild
	after := ""
	for pages := 0; pages < 1000; pages++ {
		q := url.Values{"path": {dir}, "version": {v}, "limit": {strconv.Itoa(limit)}}
		if after != "" {
			q.Set("after", after)
		}
		var page struct {
			Version *gfVersionOf `json:"version"`
			Items   []gfBuild    `json:"items"`
			Next    *string      `json:"next"`
		}
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/builds?"+q.Encode(), nil, auth, &page)
		if page.Version == nil {
			return nil, nil
		}
		if len(page.Items) > limit {
			t.Fatalf("builds of %s %s: %d items, limit %d", repo, v, len(page.Items), limit)
		}
		ver = page.Version
		out = append(out, page.Items...)
		if page.Next == nil || *page.Next == "" {
			return ver, out
		}
		after = *page.Next
	}
	t.Fatalf("builds of %s %s: more than 1000 pages", repo, v)
	return nil, nil
}

// TestGroupSnapshotBuilds browses a Maven snapshot version that two members of a group hold: the
// group lists the union of their builds, a build id once with the files of both, a path from the
// first member that has it (named in repo), in the order of a hosted repository that holds the
// same files; the version counts the union; the counters count each path once.
func TestGroupSnapshotBuilds(t *testing.T) {
	t.Parallel()
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		const art = "com/e2e/snap/lib/"
		const v = "1.0-SNAPSHOT"
		dir := art + v + "/"
		file := func(build, tail string) string { return dir + "lib-1.0-" + build + tail }
		const b1, b2, b3 = "20261001.101010-1", "20261002.101010-2", "20261003.101010-3"

		first, second, ref := RandName("first"), RandName("second"), RandName("ref")
		for _, r := range []string{first, second, ref} {
			s.CreateRepo(t, r, "maven", false, nil)
		}
		auths := map[string]Basic{}
		for _, r := range []string{first, second, ref} {
			auths[r] = s.PublishAuth(t, s.AdminUser, r)
		}
		put := func(repo, p, body string) {
			t.Helper()
			resp, b := rawPut(t, s, "/maven/"+repo+"/"+p, []byte(body), auths[repo])
			if resp.StatusCode/100 != 2 {
				t.Fatalf("PUT /maven/%s/%s: status %d: %s", repo, p, resp.StatusCode, b)
			}
		}
		// The files of each member; the shared jar of build 2 has other bytes in the second.
		held := map[string][]string{
			first:  {file(b1, ".pom"), file(b1, ".jar"), file(b2, ".pom"), file(b2, ".jar")},
			second: {file(b2, ".jar"), file(b2, "-sources.jar"), file(b3, ".pom"), file(b3, ".jar")},
		}
		serves := map[string]string{} // path -> the member a group read serves it from
		for _, m := range []string{first, second} {
			for _, p := range held[m] {
				put(m, p, m+" "+p)
				if serves[p] == "" {
					serves[p] = m
				}
			}
		}
		// The reference holds what the group serves: each path with the bytes of its member.
		for p, m := range serves {
			put(ref, p, m+" "+p)
		}
		grp := RandName("snapshots")
		s.CreateGroup(t, grp, "maven", false, []string{first, second})
		admin := s.AdminBasic()

		refVer, refBuilds := walkBuilds(t, s, ref, art, v, 200, admin)
		if refVer == nil || len(refBuilds) != 3 {
			t.Fatalf("builds of the reference: %+v %+v, want three builds", refVer, refBuilds)
		}
		for _, limit := range []int{1, 2, 200} {
			ver, builds := walkBuilds(t, s, grp, art, v, limit, admin)
			if ver == nil || ver.Repo != first || ver.BuildsCount != refVer.BuildsCount || ver.LatestBuild != refVer.LatestBuild {
				t.Fatalf("group version %s, limit %d: %+v, want %d builds, the latest %s, of %s", v, limit, ver, refVer.BuildsCount, refVer.LatestBuild, first)
			}
			if len(builds) != len(refBuilds) {
				t.Fatalf("group builds of %s, limit %d: %+v, want the builds of the reference %+v", v, limit, builds, refBuilds)
			}
			for i, b := range builds {
				want := refBuilds[i]
				if b.ID != want.ID || len(b.Files) != len(want.Files) {
					t.Fatalf("group build %d of %s, limit %d: %+v, want %+v", i, v, limit, b, want)
				}
				sums := map[string]string{}
				for _, f := range want.Files {
					sums[f.Path] = f.SHA256
				}
				for _, f := range b.Files {
					if sums[f.Path] == "" || f.SHA256 != sums[f.Path] || f.Repo != serves[f.Path] {
						t.Fatalf("group build %s, file %s: sha256 %s of %s, want %s of %s", b.ID, f.Path, f.SHA256, f.Repo, sums[f.Path], serves[f.Path])
					}
				}
			}
		}
		// One build of the union by its id.
		var one struct {
			Items []gfBuild `json:"items"`
		}
		s.MustAPI(t, http.MethodGet, "/api/repos/"+grp+"/builds?"+url.Values{"path": {art}, "version": {v}, "build": {b2}}.Encode(), nil, admin, &one)
		if len(one.Items) != 1 || one.Items[0].ID != b2 || len(one.Items[0].Files) != 3 {
			t.Fatalf("group build %s: %+v, want the pom and jar of %s and the sources of %s", b2, one.Items, first, second)
		}
		// The versions list counts the builds of the union.
		for _, x := range walkVersions(t, s, grp, art, 200, admin) {
			if x.Version == v && (x.Repo != first || x.BuildsCount != refVer.BuildsCount) {
				t.Fatalf("group versions: %+v, want %d builds of %s", x, refVer.BuildsCount, first)
			}
		}
		// A reader of the second member only sees its builds.
		reader := RandName("reader")
		s.CreateUser(t, reader, "reader-password-1")
		s.SetPerms(t, grp, reader, "repo.download")
		s.SetPerms(t, second, reader, "repo.download")
		readerAuth := Basic{reader, "reader-password-1"}
		ver, builds := walkBuilds(t, s, grp, art, v, 200, readerAuth)
		if ver == nil || ver.Repo != second || len(builds) != 2 {
			t.Fatalf("group builds for a reader of %s: %+v %+v, want its two builds", second, ver, builds)
		}
		for _, b := range builds {
			for _, f := range b.Files {
				if f.Repo != second {
					t.Fatalf("group builds for a reader of %s: %s of %s", second, f.Path, f.Repo)
				}
			}
		}
		wantUnionCounts(t, s, grp, []string{first, second}, admin)
		wantUnionCounts(t, s, grp, []string{second}, readerAuth)
	})
}

// gfNPMTarball is the tarball of package name at version, as npm pack writes it.
func gfNPMTarball(t *testing.T, name, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	pj, _ := json.Marshal(map[string]string{"name": name, "version": version, "main": "index.js", "license": "MIT"})
	for _, f := range []struct{ name, body string }{
		{"package/package.json", string(pj)},
		{"package/index.js", fmt.Sprintf("module.exports = %q;\n", name+" "+version)},
	} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.body)), ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(f.body))
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	gz.Close()
	return buf.Bytes()
}

// gfNPMPublish publishes version of package name to repo as npm publish sends it, and returns the
// tarball.
func gfNPMPublish(t *testing.T, s *Server, repo, name, version string) []byte {
	t.Helper()
	tgz := gfNPMTarball(t, name, version)
	file := name + "-" + version + ".tgz"
	s1, s5 := sha1.Sum(tgz), sha512.Sum512(tgz)
	body, _ := json.Marshal(map[string]any{
		"_id": name, "name": name, "dist-tags": map[string]string{"latest": version},
		"versions": map[string]any{version: map[string]any{"name": name, "version": version, "main": "index.js", "license": "MIT",
			"dist": map[string]string{"shasum": hex.EncodeToString(s1[:]), "integrity": "sha512-" + base64.StdEncoding.EncodeToString(s5[:]),
				"tarball": s.InternalURL + "/npm/" + repo + "/" + name + "/-/" + file}}},
		"_attachments": map[string]any{file: map[string]any{"content_type": "application/octet-stream",
			"data": base64.StdEncoding.EncodeToString(tgz), "length": len(tgz)}},
	})
	req := s.NewRequest(t, http.MethodPut, "/npm/"+repo+"/"+name, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, b := s.Do(t, req, s.PublishAuth(t, s.AdminUser, repo))
	if resp.StatusCode/100 != 2 {
		t.Fatalf("publish %s@%s to %s: status %d: %s", name, version, repo, resp.StatusCode, b)
	}
	return tgz
}

// TestGroupNPMProxyOwner browses an npm package in a group whose first member is a proxy and
// whose second, a hosted repository, has its own versions of the name: the group lists the
// versions of the proxy's remote, as a download takes them from the proxy, marked while the proxy
// has not cached their tarball; a download through the group serves the remote's tarball; the
// hosted versions do not show; the counters count the files of the package from the proxy only.
func TestGroupNPMProxyOwner(t *testing.T) {
	t.Parallel()
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		remote := StartServer(t, ServerOpts{})
		s := StartServer(t, opts)
		pkg, other := RandName("shared"), RandName("other")

		up := RandName("upstream")
		remote.CreateRepo(t, up, "npm", true, nil)
		tarballs := map[string][]byte{}
		for _, v := range []string{"1.0.0", "2.0.0"} {
			tarballs[v] = gfNPMPublish(t, remote, up, pkg, v)
		}
		npmjs := RandName("npmjs")
		s.CreateProxy(t, npmjs, "npm", true, map[string]any{"remote_url": remote.InternalURL + "/npm/" + up + "/"}, false)
		hosted := RandName("hosted")
		s.CreateRepo(t, hosted, "npm", true, nil)
		gfNPMPublish(t, s, hosted, pkg, "3.0.0")
		gfNPMPublish(t, s, hosted, other, "1.0.0")
		grp := RandName("npmgroup")
		s.CreateGroup(t, grp, "npm", true, []string{npmjs, hosted})
		admin := s.AdminBasic()
		folder := pkg + "/"

		// The order of the versions is the remote's own.
		var order []string
		for _, x := range walkVersions(t, remote, up, folder, 200, remote.AdminBasic()) {
			order = append(order, x.Version)
		}
		type item struct {
			Version string `json:"version"`
			Repo    string `json:"repo"`
			Remote  bool   `json:"remote"`
		}
		versions := func(limit int) []item {
			t.Helper()
			var out []item
			after := ""
			for {
				q := url.Values{"path": {folder}, "limit": {strconv.Itoa(limit)}}
				if after != "" {
					q.Set("after", after)
				}
				var page struct {
					Items []item  `json:"items"`
					Next  *string `json:"next"`
				}
				s.MustAPI(t, http.MethodGet, "/api/repos/"+grp+"/versions?"+q.Encode(), nil, admin, &page)
				out = append(out, page.Items...)
				if page.Next == nil || *page.Next == "" {
					return out
				}
				after = *page.Next
			}
		}
		for _, limit := range []int{1, 200} {
			got := versions(limit)
			if len(got) != len(order) {
				t.Fatalf("group versions of %s, limit %d: %+v, want the remote's %v", pkg, limit, got, order)
			}
			for i, x := range got {
				if x.Version != order[i] || x.Repo != npmjs || !x.Remote {
					t.Fatalf("group versions of %s, limit %d: item %d is %+v, want %s of %s, not cached", pkg, limit, i, x, order[i], npmjs)
				}
			}
		}
		ver, builds := walkBuilds(t, s, grp, folder, "2.0.0", 200, admin)
		if ver == nil || ver.Repo != npmjs || len(builds) != 1 || len(builds[0].Files) != 1 || !builds[0].Files[0].Remote || builds[0].Files[0].Repo != npmjs {
			t.Fatalf("group builds of %s 2.0.0: %+v %+v, want the tarball of %s, not cached", pkg, ver, builds, npmjs)
		}
		if ver, _ := walkBuilds(t, s, grp, folder, "3.0.0", 200, admin); ver != nil {
			t.Fatalf("group builds of %s 3.0.0: %+v, want none: the proxy owns the name", pkg, ver)
		}

		// A download of a listed version through the group serves the remote's tarball.
		tgzPath := "/npm/" + grp + "/" + pkg + "/-/" + pkg + "-1.0.0.tgz"
		if resp, b := s.Do(t, s.NewRequest(t, http.MethodGet, tgzPath, nil), admin); resp.StatusCode != http.StatusOK || !bytes.Equal(b, tarballs["1.0.0"]) {
			t.Fatalf("GET %s: status %d, %d bytes, want the remote's tarball", tgzPath, resp.StatusCode, len(b))
		}
		for _, x := range versions(200) {
			if x.Remote != (x.Version != "1.0.0") {
				t.Fatalf("group versions after the download: %+v, want 1.0.0 cached only", x)
			}
		}
		sum := sha256.Sum256(tarballs["1.0.0"])
		ver, builds = walkBuilds(t, s, grp, folder, "1.0.0", 200, admin)
		if ver == nil || len(builds) != 1 || len(builds[0].Files) != 1 || builds[0].Files[0].Remote ||
			builds[0].Files[0].SHA256 != hex.EncodeToString(sum[:]) || builds[0].Files[0].Repo != npmjs {
			t.Fatalf("group builds of %s 1.0.0: %+v %+v, want the cached tarball of %s", pkg, ver, builds, npmjs)
		}

		// The counters: every file of the proxy, and of the hosted repository the files of the
		// other package only (the group serves the name from the proxy).
		var files, size int64
		for _, a := range memberAssets(t, s, npmjs, admin) {
			files++
			size += a.Size
		}
		for _, a := range memberAssets(t, s, hosted, admin) {
			if strings.HasPrefix(a.Path, other+"/") {
				files++
				size += a.Size
			}
		}
		if gotFiles, gotSize := groupCounters(t, s, grp, admin); gotFiles != files || gotSize != size {
			t.Fatalf("counters of %s: %d files, %d bytes; want %d files, %d bytes", grp, gotFiles, gotSize, files, size)
		}
	})
}
