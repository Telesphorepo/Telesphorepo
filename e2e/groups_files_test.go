//go:build e2e

package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"testing"
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
