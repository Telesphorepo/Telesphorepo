//go:build edge

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The edge zone: the real Worker of edge/ in wrangler dev in front of the real server (see
// edge_harness_test.go). The same request to the Worker and to the server gives the same answer;
// the header Telesphorepo-Edge says which answers the Worker gave itself.

// edgeUser is a user of the parity matrix: its password, its read token and what it reads.
type edgeUser struct {
	name, pass string
	token      edgeToken
	all        bool            // repositories.all
	grants     map[string]bool // the repos where it holds repo.download
}

// newUser creates a local user with the server permissions perms and a read token.
func (e *Edge) newUser(t testing.TB, prefix string, perms ...string) *edgeUser {
	t.Helper()
	u := &edgeUser{name: RandName(prefix), pass: "pass-" + randHex(8), grants: map[string]bool{}}
	e.S.CreateUser(t, u.name, u.pass, perms...)
	u.all = containsString(perms, "repositories.all")
	u.token = e.createToken(t, u.name)
	return u
}

// grant gives u repo.download on repo.
func (e *Edge) grant(t testing.TB, u *edgeUser, repo string) {
	t.Helper()
	e.S.SetPerms(t, repo, u.name, "repo.download")
	u.grants[repo] = true
}

// edgeRepo is the repo of a read as the access rules see it: the group for a group read.
type edgeRepo struct {
	name string
	anon bool
}

// edgeTarget is one read of the matrix.
type edgeTarget struct {
	what   string
	path   string
	repo   edgeRepo
	image  string    // the oci shape: <repo>/<image>, the scope of a registry token
	served bool      // the edge answers it to a credential that it checks and that reads the repo
	via    *edgeRepo // a group read of a member's file: the credential must read that member too
	once   bool      // the first read changes it (a proxy fetches the file): read with the first credential only
	bad    bool      // the read of the format that also gets a bad token (each counts as a failed attempt)
}

// edgeCred is one credential of the matrix.
type edgeCred struct {
	name  string
	user  *edgeUser // whose rows decide; nil: anonymous
	edge  bool      // the edge checks this credential itself
	shape string    // "oci" or "standard": only on that shape; "": both
	bad   bool      // a credential that fails: only on the targets with bad set
	auth  func(t testing.TB, tg edgeTarget) Auth
}

// reads reports whether the credential reads repo with repo.download.
func (c edgeCred) reads(r edgeRepo) bool {
	return r.anon || (c.user != nil && (c.user.all || c.user.grants[r.name]))
}

// TestEdgeParity is the parity matrix: each format, each credential, each repo setting (public,
// private with and without a grant, repositories.all, a read IP rule) and each kind (hosted,
// proxy, group with a private member, a guarded member and a proxy member). The Worker and the
// server answer the same status, bytes and headers, and the Worker answers itself exactly the
// reads that it can prove.
func TestEdgeParity(t *testing.T) {
	t.Parallel()
	e := startEdge(t, edgeOpts{})
	reader := e.newUser(t, "reader")
	outsider := e.newUser(t, "outsider")
	everyone := e.newUser(t, "all", "repositories.all")

	var targets []edgeTarget
	targets = append(targets, e.parityMaven(t, reader)...)
	targets = append(targets, e.parityRaw(t, reader)...)
	targets = append(targets, e.parityDebian(t, reader)...)
	targets = append(targets, e.parityDocker(t, reader)...)

	creds := e.parityCreds(t, reader, outsider, everyone)

	// Wait for the outbox: every read that the edge serves is an edge answer before the matrix.
	for _, tg := range targets {
		if !tg.served || tg.once {
			continue
		}
		var auth Auth
		if !tg.repo.anon || tg.via != nil && !tg.via.anon {
			auth = Basic{"ci", reader.token.Token}
		}
		e.waitHit(t, tg.path, auth)
	}

	for _, tg := range targets {
		for i, c := range creds {
			if (c.shape == "oci") != (tg.image != "") && c.shape != "" || (tg.once && i > 0) || (c.bad && !tg.bad) {
				continue
			}
			hit := c.edge && tg.served && c.reads(tg.repo) && (tg.via == nil || c.reads(*tg.via))
			e.compare(t, tg.what+" with "+c.name, http.MethodGet, tg.path, c.auth(t, tg), nil, hit)
		}
	}
}

// parityCreds returns the credentials of the matrix, anonymous first.
func (e *Edge) parityCreds(t *testing.T, reader, outsider, everyone *edgeUser) []edgeCred {
	t.Helper()
	s := e.S
	proof, _ := e.deviceProof(t, reader.name)
	session := s.SignIn(t, reader.name, reader.pass)
	filesRepo := RandName("pubsess")
	e.createRepo(t, filesRepo, "raw", false)
	publish := Basic{"ci", PublishToken(t, s, s.AdminUser, filesRepo)}
	badToken := "trp_" + randHex(6) + "_" + strings.Repeat("A", 43)

	fixed := func(a Auth) func(testing.TB, edgeTarget) Auth { return func(testing.TB, edgeTarget) Auth { return a } }
	tokens := map[string]edgeBearer{}
	bearer := func(name string, a Auth) func(testing.TB, edgeTarget) Auth {
		return func(t testing.TB, tg edgeTarget) Auth {
			k := name + " " + tg.image
			if tokens[k] == "" {
				tokens[k] = e.registryToken(t, a, tg.image)
			}
			return tokens[k]
		}
	}
	return []edgeCred{
		{name: "no credential", edge: true, auth: fixed(nil)},
		{name: "a read token as the Basic password", user: reader, edge: true, auth: fixed(Basic{"ci", reader.token.Token})},
		{name: "a read token as a Bearer", user: reader, edge: true, auth: fixed(edgeBearer(reader.token.Token))},
		{name: "a read token without a grant", user: outsider, edge: true, auth: fixed(Basic{"ci", outsider.token.Token})},
		{name: "a read token of repositories.all", user: everyone, edge: true, auth: fixed(Basic{"ci", everyone.token.Token})},
		{name: "a password", user: reader, auth: fixed(Basic{reader.name, reader.pass})},
		{name: "a UI session", user: reader, auth: fixed(session)},
		{name: "a publish session token", auth: fixed(publish)},
		{name: "a device proof", user: reader, auth: fixed(Basic{"telesphorepo-device", proof})},
		{name: "a bad read token", bad: true, auth: fixed(Basic{RandName("bad"), badToken})},
		{name: "an anonymous core bearer", shape: "oci", edge: true, auth: bearer("anonymous", nil)},
		{name: "a core bearer of a password", shape: "oci", user: reader, edge: true, auth: bearer("password", Basic{reader.name, reader.pass})},
		{name: "a core bearer of a read token", shape: "oci", user: reader, edge: true, auth: bearer("token", Basic{"ci", reader.token.Token})},
		{name: "a core bearer of a device", shape: "oci", user: reader, edge: true, auth: bearer("device", Basic{"telesphorepo-device", proof})},
		{name: "a core bearer of a publish session", shape: "oci", auth: bearer("publish", publish)},
		{name: "a core bearer on a standard route", shape: "standard", auth: func(t testing.TB, tg edgeTarget) Auth {
			if tokens["standard"] == "" {
				tokens["standard"] = e.registryToken(t, nil, RandName("none")+"/app")
			}
			return tokens["standard"]
		}},
	}
}

// parityMaven creates the Maven repos of the matrix and returns their reads.
func (e *Edge) parityMaven(t *testing.T, reader *edgeUser) []edgeTarget {
	t.Helper()
	s := e.S
	pub, priv, ruled, prx := RandName("mpub"), RandName("mpriv"), RandName("mruled"), RandName("mprx")
	gPriv, gRuled, gPrx := RandName("mgpriv"), RandName("mgruled"), RandName("mgprx")
	e.createRepo(t, pub, "maven", true)
	e.createRepo(t, priv, "maven", false)
	e.createRepo(t, ruled, "maven", true)
	e.guard(t, ruled)
	e.grant(t, reader, priv)
	const g = "com/e2e/edge"
	jar := func(a string) string { return g + "/" + a + "/1.0/" + a + "-1.0.jar" }
	pom := func(a string) string { return g + "/" + a + "/1.0/" + a + "-1.0.pom" }
	e.put(t, pub, "/maven/"+pub+"/"+jar("lib"), rawRandom(t, 20<<10))
	e.put(t, pub, "/maven/"+pub+"/"+pom("lib"), []byte(`<project><modelVersion>4.0.0</modelVersion><groupId>com.e2e.edge</groupId><artifactId>lib</artifactId><version>1.0</version></project>`))
	e.put(t, pub, "/maven/"+pub+"/"+g+"/lib/maven-metadata.xml", []byte(`<metadata><groupId>com.e2e.edge</groupId><artifactId>lib</artifactId><versioning><release>1.0</release><versions><version>1.0</version></versions></versioning></metadata>`))
	e.put(t, priv, "/maven/"+priv+"/"+jar("secret"), rawRandom(t, 8<<10))
	e.put(t, ruled, "/maven/"+ruled+"/"+jar("ruled"), rawRandom(t, 8<<10))
	e.createProxy(t, prx, "maven", s.InternalURL+"/maven/"+pub, nil)
	e.createGroup(t, gPriv, "maven", priv, pub)
	e.createGroup(t, gRuled, "maven", ruled, pub)
	e.createGroup(t, gPrx, "maven", prx, pub)
	// The proxy caches the jar; the pom stays at the remote until a read fetches it.
	if resp, b := e.send(t, s.URL, http.MethodGet, "/maven/"+prx+"/"+jar("lib"), nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("fetch the jar through the Maven proxy: status %d: %s", resp.StatusCode, b)
	}
	r := func(name string, anon bool) edgeRepo { return edgeRepo{name, anon} }
	return []edgeTarget{
		{what: "maven: a release jar", path: "/maven/" + pub + "/" + jar("lib"), repo: r(pub, true), served: true, bad: true},
		{what: "maven: a pom", path: "/maven/" + pub + "/" + pom("lib"), repo: r(pub, true), served: true},
		{what: "maven: maven-metadata.xml (home pointer)", path: "/maven/" + pub + "/" + g + "/lib/maven-metadata.xml", repo: r(pub, true), served: true},
		{what: "maven: a missing file", path: "/maven/" + pub + "/" + jar("missing"), repo: r(pub, true)},
		{what: "maven: a private jar", path: "/maven/" + priv + "/" + jar("secret"), repo: r(priv, false), served: true},
		{what: "maven: a jar of a repo with a read IP rule", path: "/maven/" + ruled + "/" + jar("ruled"), repo: r(ruled, true)},
		{what: "maven: a jar that the proxy cached", path: "/maven/" + prx + "/" + jar("lib"), repo: r(prx, true), served: true},
		{what: "maven: a pom that the proxy has not fetched", path: "/maven/" + prx + "/" + pom("lib"), repo: r(prx, true), once: true},
		{what: "maven: a jar of a group with a private member", path: "/maven/" + gPriv + "/" + jar("lib"), repo: r(gPriv, true), served: true},
		{what: "maven: the private member's jar through its group", path: "/maven/" + gPriv + "/" + jar("secret"), repo: r(gPriv, true),
			served: true, via: &edgeRepo{priv, false}},
		{what: "maven: maven-metadata.xml of a group", path: "/maven/" + gPriv + "/" + g + "/lib/maven-metadata.xml", repo: r(gPriv, true)},
		{what: "maven: a jar of a group with a guarded member", path: "/maven/" + gRuled + "/" + jar("lib"), repo: r(gRuled, true)},
		{what: "maven: a jar of a group whose proxy member cached it", path: "/maven/" + gPrx + "/" + jar("lib"), repo: r(gPrx, true), served: true},
	}
}

// parityRaw creates the raw repos of the matrix (raw has hosted repos only) and returns their
// reads. Every raw path is mutable: its pointer is in the home bucket only.
func (e *Edge) parityRaw(t *testing.T, reader *edgeUser) []edgeTarget {
	t.Helper()
	pub, priv, ruled := RandName("rpub"), RandName("rpriv"), RandName("rruled")
	e.createRepo(t, pub, "raw", true)
	e.createRepo(t, priv, "raw", false)
	e.createRepo(t, ruled, "raw", true)
	e.guard(t, ruled)
	e.grant(t, reader, priv)
	e.put(t, pub, "/raw/"+pub+"/dir/a.bin", rawRandom(t, 30<<10))
	e.put(t, pub, "/raw/"+pub+"/dir/notes.txt", []byte("plain text is shown inline\n"))
	e.put(t, priv, "/raw/"+priv+"/dir/b.bin", rawRandom(t, 4<<10))
	e.put(t, ruled, "/raw/"+ruled+"/dir/c.bin", rawRandom(t, 4<<10))
	return []edgeTarget{
		{what: "raw: a file", path: "/raw/" + pub + "/dir/a.bin", repo: edgeRepo{pub, true}, served: true, bad: true},
		{what: "raw: a text file", path: "/raw/" + pub + "/dir/notes.txt", repo: edgeRepo{pub, true}, served: true},
		{what: "raw: a missing file", path: "/raw/" + pub + "/dir/none.bin", repo: edgeRepo{pub, true}},
		{what: "raw: a private file", path: "/raw/" + priv + "/dir/b.bin", repo: edgeRepo{priv, false}, served: true},
		{what: "raw: a file of a repo with a read IP rule", path: "/raw/" + ruled + "/dir/c.bin", repo: edgeRepo{ruled, true}},
	}
}

// parityDebian creates the Debian repos of the matrix (hosted and proxy) and returns their reads.
func (e *Edge) parityDebian(t *testing.T, reader *edgeUser) []edgeTarget {
	t.Helper()
	s := e.S
	pub, priv, ruled, prx := RandName("dpub"), RandName("dpriv"), RandName("druled"), RandName("dprx")
	e.createRepo(t, pub, "debian", true)
	e.createRepo(t, priv, "debian", false)
	e.createRepo(t, ruled, "debian", true)
	e.guard(t, ruled)
	e.grant(t, reader, priv)
	pkg := "edgepkg"
	pool := "pool/main/e/" + pkg + "/" + pkg + "_1.0_all.deb"
	for _, repo := range []string{pub, priv, ruled} {
		e.uploadDeb(t, repo, edgeDeb(t, pkg, "1.0"))
	}
	e.createProxy(t, prx, "debian", s.InternalURL+"/debian/"+pub, map[string]any{"distribution": "trixie"})
	if resp, b := e.send(t, s.URL, http.MethodGet, "/debian/"+prx+"/"+pool, nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("fetch the package through the Debian proxy: status %d: %s", resp.StatusCode, b)
	}
	r := func(name string, anon bool) edgeRepo { return edgeRepo{name, anon} }
	return []edgeTarget{
		{what: "debian: a package in pool/", path: "/debian/" + pub + "/" + pool, repo: r(pub, true), served: true, bad: true},
		{what: "debian: Release (home pointer)", path: "/debian/" + pub + "/dists/trixie/Release", repo: r(pub, true), served: true},
		{what: "debian: InRelease (home pointer)", path: "/debian/" + pub + "/dists/trixie/InRelease", repo: r(pub, true), served: true},
		{what: "debian: key.asc", path: "/debian/" + pub + "/key.asc", repo: r(pub, true)},
		{what: "debian: a private package", path: "/debian/" + priv + "/" + pool, repo: r(priv, false), served: true},
		{what: "debian: a package of a repo with a read IP rule", path: "/debian/" + ruled + "/" + pool, repo: r(ruled, true)},
		{what: "debian: a package that the proxy cached", path: "/debian/" + prx + "/" + pool, repo: r(prx, true), served: true},
		{what: "debian: Release of the proxy (it changes)", path: "/debian/" + prx + "/dists/trixie/Release", repo: r(prx, true)},
	}
}

// parityDocker creates the Docker repos of the matrix and returns their reads.
func (e *Edge) parityDocker(t *testing.T, reader *edgeUser) []edgeTarget {
	t.Helper()
	s := e.S
	pub, priv, ruled, prx := RandName("opub"), RandName("opriv"), RandName("oruled"), RandName("oprx")
	gPriv, gRuled, gPrx := RandName("ogpriv"), RandName("ogruled"), RandName("ogprx")
	e.createRepo(t, pub, "docker", true)
	e.createRepo(t, priv, "docker", false)
	e.createRepo(t, ruled, "docker", true)
	e.guard(t, ruled)
	e.grant(t, reader, priv)
	img, imgPriv, imgRuled := newOCI(t, rawRandom(t, 40<<10)), newOCI(t, rawRandom(t, 4<<10)), newOCI(t, rawRandom(t, 4<<10))
	e.pushOCI(t, pub+"/app", "v1", img)
	e.pushOCI(t, priv+"/app", "v1", imgPriv)
	e.pushOCI(t, ruled+"/app", "v1", imgRuled)
	e.createProxy(t, prx, "docker", s.InternalURL+"/"+pub+"/", nil)
	e.createGroup(t, gPriv, "docker", priv, pub)
	e.createGroup(t, gRuled, "docker", ruled, pub)
	e.createGroup(t, gPrx, "docker", prx, pub)
	// The proxy caches the manifest and the layer.
	for _, p := range []string{"/v2/" + prx + "/app/manifests/v1", "/v2/" + prx + "/app/blobs/sha256:" + img.Layers[0]} {
		if resp, b := e.send(t, s.URL, http.MethodGet, p, nil, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("fetch %s through the Docker proxy: status %d: %s", p, resp.StatusCode, b)
		}
	}
	m := func(repo, ref string) string { return "/v2/" + repo + "/app/manifests/" + ref }
	b := func(repo, hex string) string { return "/v2/" + repo + "/app/blobs/sha256:" + hex }
	r := func(name string, anon bool) edgeRepo { return edgeRepo{name, anon} }
	return []edgeTarget{
		{what: "docker: a manifest by tag (home pointer)", path: m(pub, "v1"), repo: r(pub, true), image: pub + "/app", served: true},
		{what: "docker: a manifest by digest", path: m(pub, "sha256:"+img.Hex), repo: r(pub, true), image: pub + "/app", served: true, bad: true},
		{what: "docker: a layer", path: b(pub, img.Layers[0]), repo: r(pub, true), image: pub + "/app", served: true},
		{what: "docker: a missing manifest", path: m(pub, "sha256:"+strings.Repeat("0", 64)), repo: r(pub, true), image: pub + "/app"},
		{what: "docker: the tags list", path: "/v2/" + pub + "/app/tags/list", repo: r(pub, true), image: pub + "/app"},
		{what: "docker: a private manifest", path: m(priv, "sha256:"+imgPriv.Hex), repo: r(priv, false), image: priv + "/app", served: true},
		{what: "docker: a private layer", path: b(priv, imgPriv.Layers[0]), repo: r(priv, false), image: priv + "/app", served: true},
		{what: "docker: a manifest of a repo with a read IP rule", path: m(ruled, "sha256:"+imgRuled.Hex), repo: r(ruled, true), image: ruled + "/app"},
		{what: "docker: a manifest that the proxy cached", path: m(prx, "sha256:"+img.Hex), repo: r(prx, true), image: prx + "/app", served: true},
		{what: "docker: a layer that the proxy cached", path: b(prx, img.Layers[0]), repo: r(prx, true), image: prx + "/app", served: true},
		{what: "docker: a tag of the proxy (it changes)", path: m(prx, "v1"), repo: r(prx, true), image: prx + "/app"},
		{what: "docker: a manifest of a group with a private member", path: m(gPriv, "sha256:"+img.Hex), repo: r(gPriv, true), image: gPriv + "/app", served: true},
		{what: "docker: a tag of a group", path: m(gPriv, "v1"), repo: r(gPriv, true), image: gPriv + "/app"},
		{what: "docker: a manifest of a group with a guarded member", path: m(gRuled, "sha256:"+img.Hex), repo: r(gRuled, true), image: gRuled + "/app"},
		{what: "docker: a manifest of a group whose proxy member cached it", path: m(gPrx, "sha256:"+img.Hex), repo: r(gPrx, true), image: gPrx + "/app", served: true},
	}
}

// TestEdgeRedirect: with s3.redirect on, the server answers a GET with a redirect to the store
// and the edge streams the blob itself; the bytes after the redirect are the same.
func TestEdgeRedirect(t *testing.T) {
	t.Parallel()
	e := startEdge(t, edgeOpts{Redirect: true})
	repo := RandName("mvn")
	e.createRepo(t, repo, "maven", true)
	body := rawRandom(t, 50<<10)
	p := "/maven/" + repo + "/com/e2e/redirect/1.0/redirect-1.0.jar"
	e.put(t, repo, p, body)
	resp, eb := e.waitHit(t, p, nil)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(eb, body) {
		t.Fatalf("GET %s at the edge: status %d, %d bytes, want 200 with the %d bytes of the file", p, resp.StatusCode, len(eb), len(body))
	}
	or, _ := e.send(t, e.S.URL, http.MethodGet, p, nil, nil)
	if or.StatusCode != http.StatusFound {
		t.Fatalf("GET %s at the server with s3.redirect: status %d, want 302", p, or.StatusCode)
	}
	// The presigned URL names the server's view of the Worker (127.0.0.1:8787, see startForwarder);
	// the signature covers the Host, so the request keeps it.
	loc, err := url.Parse(or.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, strings.Replace(loc.String(), loc.Scheme+"://"+loc.Host, e.URL, 1), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = loc.Host
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("follow the redirect: %v", err)
	}
	defer r.Body.Close()
	ob, _ := io.ReadAll(r.Body)
	if r.StatusCode != http.StatusOK || !bytes.Equal(ob, eb) {
		t.Fatalf("the redirect of the server: status %d, %d bytes; the edge answered %d bytes", r.StatusCode, len(ob), len(eb))
	}
}

// TestEdgeClientIP: the Worker forwards the address that it got, never one from the client. A
// spoofed X-Real-IP or X-Forwarded-For does not reach the server's audit log. Locally a client
// may set CF-Connecting-IP itself (on Cloudflare the edge sets it), so it stands for the address
// that the Worker got.
func TestEdgeClientIP(t *testing.T) {
	t.Parallel()
	e := startEdge(t, edgeOpts{})
	signIn := func(hdr map[string]string) string {
		t.Helper()
		user := RandName("nobody")
		body, _ := json.Marshal(map[string]string{"username": user, "password": "wrong-password"})
		req, err := http.NewRequest(http.MethodPost, e.URL+"/api/auth/login", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, b := e.S.Do(t, req, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("a failed sign-in through the Worker: status %d, want 401: %s", resp.StatusCode, b)
		}
		var page struct {
			Items []struct {
				Actor string `json:"actor"`
				IP    string `json:"ip"`
			} `json:"items"`
		}
		e.S.MustAPI(t, http.MethodGet, "/api/audit?action=signin.failed&limit=200", nil, e.S.Admin(), &page)
		for _, it := range page.Items {
			if it.Actor == user {
				return it.IP
			}
		}
		t.Fatalf("no signin.failed event for %s", user)
		return ""
	}
	spoof := map[string]string{"X-Real-IP": "203.0.113.9", "X-Forwarded-For": "203.0.113.9"}
	if ip := signIn(spoof); ip == "" || ip == "203.0.113.9" {
		t.Fatalf("a spoofed X-Real-IP and X-Forwarded-For: the server recorded %q, want the address that the Worker got", ip)
	}
	spoof["CF-Connecting-IP"] = "198.51.100.7"
	if ip := signIn(spoof); ip != "198.51.100.7" {
		t.Fatalf("CF-Connecting-IP 198.51.100.7 with a spoofed X-Real-IP: the server recorded %q, want 198.51.100.7", ip)
	}
}

// TestEdgeRevocation: with edge.access_ttl 1s, a token delete, a user disable, a grant removal,
// a repo made private, a device revoke and a read IP rule each turn edge answers into server
// answers within 2 s.
func TestEdgeRevocation(t *testing.T) {
	t.Parallel()
	e := startEdge(t, edgeOpts{TTL: "1s"})
	s := e.S
	priv, pub, ruled, img := RandName("priv"), RandName("pub"), RandName("ruled"), RandName("img")
	e.createRepo(t, priv, "raw", false)
	e.createRepo(t, pub, "raw", true)
	e.createRepo(t, ruled, "raw", true)
	e.createRepo(t, img, "docker", false)
	for _, repo := range []string{priv, pub, ruled} {
		e.put(t, repo, "/raw/"+repo+"/f.bin", rawRandom(t, 4<<10))
	}
	image := newOCI(t, rawRandom(t, 4<<10))
	e.pushOCI(t, img+"/app", "v1", image)
	const within = 2 * time.Second
	reader := func() *edgeUser {
		u := e.newUser(t, "u")
		e.grant(t, u, priv)
		return u
	}
	privPath := "/raw/" + priv + "/f.bin"

	t.Run("token deleted", func(t *testing.T) {
		u := reader()
		auth := Basic{"ci", u.token.Token}
		e.waitHit(t, privPath, auth)
		s.MustAPI(t, http.MethodDelete, fmt.Sprintf("/api/tokens/%d", u.token.ID), nil, s.SignIn(t, u.name, u.pass), nil)
		resp, _ := e.waitMiss(t, "after the token delete", privPath, auth, within)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("the deleted token: status %d, want 401 from the server", resp.StatusCode)
		}
	})
	t.Run("user disabled", func(t *testing.T) {
		u := reader()
		auth := Basic{"ci", u.token.Token}
		e.waitHit(t, privPath, auth)
		s.MustAPI(t, http.MethodPatch, "/api/users", map[string]any{"usernames": []string{u.name}, "disabled": true}, s.Admin(), nil)
		e.waitMiss(t, "after the user disable", privPath, auth, within)
	})
	t.Run("grant removed", func(t *testing.T) {
		u := reader()
		auth := Basic{"ci", u.token.Token}
		e.waitHit(t, privPath, auth)
		s.SetPerms(t, priv, u.name)
		e.waitMiss(t, "after the grant removal", privPath, auth, within)
	})
	t.Run("repo made private", func(t *testing.T) {
		p := "/raw/" + pub + "/f.bin"
		e.waitHit(t, p, nil)
		s.MustAPI(t, http.MethodPatch, "/api/repos", map[string]any{"names": []string{pub}, "anonymous_read": false}, s.Admin(), nil)
		resp, _ := e.waitMiss(t, "after the repo became private", p, nil, within)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("anonymous read of the private repo: status %d, want 401 from the server", resp.StatusCode)
		}
	})
	t.Run("device revoked", func(t *testing.T) {
		u := e.newUser(t, "dev")
		e.grant(t, u, img)
		proof, d := e.deviceProof(t, u.name)
		tok := e.registryToken(t, Basic{"telesphorepo-device", proof}, img+"/app")
		p := "/v2/" + img + "/app/manifests/sha256:" + image.Hex
		e.waitHit(t, p, tok)
		s.MustAPI(t, http.MethodDelete, fmt.Sprintf("/api/devices/%d", d.ID), nil, s.SignIn(t, u.name, u.pass), nil)
		e.waitMiss(t, "after the device revoke", p, tok, within)
	})
	t.Run("read IP rule", func(t *testing.T) {
		p := "/raw/" + ruled + "/f.bin"
		e.waitHit(t, p, nil)
		e.guard(t, ruled)
		e.waitMiss(t, "after the read IP rule", p, nil, within)
	})
}

// TestEdgeFreshness: a file delete and a Maven republish show at the edge at once, a Docker tag
// move serves the new digest, and a release replaced in the first member of a group never serves
// the second member's copy.
func TestEdgeFreshness(t *testing.T) {
	t.Parallel()
	e := startEdge(t, edgeOpts{})

	t.Run("file delete", func(t *testing.T) {
		repo := RandName("raw")
		e.createRepo(t, repo, "raw", true)
		p := "/raw/" + repo + "/gone.bin"
		e.put(t, repo, p, rawRandom(t, 4<<10))
		e.waitHit(t, p, nil)
		e.deleteFile(t, repo, "gone.bin")
		e.compare(t, "the deleted file", http.MethodGet, p, nil, nil, false)
		if resp, _ := e.get(t, p, nil); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET of the deleted file at the edge: status %d, want 404", resp.StatusCode)
		}
	})

	t.Run("maven republish", func(t *testing.T) {
		repo := RandName("mvn")
		e.createRepo(t, repo, "maven", true)
		body := func(p, which string) []byte {
			if strings.HasSuffix(p, ".xml") {
				return []byte(`<metadata><groupId>com.e2e</groupId><artifactId>fresh</artifactId><versioning><versions><version>1.0-SNAPSHOT</version></versions></versioning><!-- ` + which + " " + randHex(8) + ` --></metadata>`)
			}
			return []byte(which + " " + randHex(8))
		}
		for _, p := range []string{"com/e2e/fresh/1.0-SNAPSHOT/fresh-1.0-SNAPSHOT.jar", "com/e2e/fresh/maven-metadata.xml"} {
			full := "/maven/" + repo + "/" + p
			e.put(t, repo, full, body(p, "first"))
			e.waitHit(t, full, nil)
			second := body(p, "second")
			e.put(t, repo, full, second)
			if _, b := e.get(t, full, nil); !bytes.Equal(b, second) {
				t.Fatalf("GET %s at the edge right after the republish: %q, want %q", p, b, second)
			}
		}
	})

	t.Run("docker tag move", func(t *testing.T) {
		repo := RandName("img")
		e.createRepo(t, repo, "docker", true)
		a, b := newOCI(t, rawRandom(t, 2048)), newOCI(t, rawRandom(t, 2048))
		e.pushOCI(t, repo+"/app", "v1", a)
		p := "/v2/" + repo + "/app/manifests/v1"
		if resp, _ := e.waitHit(t, p, nil); resp.Header.Get("Docker-Content-Digest") != "sha256:"+a.Hex {
			t.Fatalf("v1 at the edge: %s, want sha256:%s", resp.Header.Get("Docker-Content-Digest"), a.Hex)
		}
		e.pushOCI(t, repo+"/app", "v1", b)
		if resp, _ := e.get(t, p, nil); resp.Header.Get("Docker-Content-Digest") != "sha256:"+b.Hex {
			t.Fatalf("v1 at the edge right after the tag moved: %s, want sha256:%s", resp.Header.Get("Docker-Content-Digest"), b.Hex)
		}
	})

	t.Run("group member replaced", func(t *testing.T) {
		first, second, group := RandName("m1"), RandName("m2"), RandName("grp")
		e.createRepo(t, first, "maven", true)
		e.createRepo(t, second, "maven", true)
		e.createGroup(t, group, "maven", first, second)
		rel := "com/e2e/replaced/1.0/replaced-1.0.jar"
		c1, c2, c3 := []byte("first member "+randHex(8)), []byte("second member "+randHex(8)), []byte("first member again "+randHex(8))
		e.put(t, second, "/maven/"+second+"/"+rel, c2)
		e.put(t, first, "/maven/"+first+"/"+rel, c1)
		p := "/maven/" + group + "/" + rel
		if _, b := e.waitHit(t, p, nil); !bytes.Equal(b, c1) {
			t.Fatalf("the group at the edge: %q, want the first member's %q", b, c1)
		}
		// Without the first member's file, both answer the second member's copy.
		e.deleteFile(t, first, rel)
		e.compare(t, "the group after the delete in the first member", http.MethodGet, p, nil, nil, true)
		// The release is back in the first member: from the commit on, no answer is the second
		// member's copy, until and after the edge answers it again.
		e.put(t, first, "/maven/"+first+"/"+rel, c3)
		deadline := time.Now().Add(20 * time.Second)
		for {
			resp, b := e.get(t, p, nil)
			if !bytes.Equal(b, c3) {
				t.Fatalf("the group after the release is back in the first member: %q (%s), want %q", b, hitOf(resp), c3)
			}
			if hitOf(resp) != "" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the group: no edge answer 20s after the release is back")
			}
			time.Sleep(100 * time.Millisecond)
		}
		for range 10 {
			if _, b := e.get(t, p, nil); !bytes.Equal(b, c3) {
				t.Fatalf("the group: %q, want %q", b, c3)
			}
		}
	})
}

// TestEdgeCopies: a new blob reaches both regions, and the Worker reads the home blob before
// that; a regional copy that is deleted by hand comes back after a gap check; a blob GC removes
// the regional copies.
func TestEdgeCopies(t *testing.T) {
	t.Parallel()
	e := startEdge(t, edgeOpts{})
	repo := RandName("raw")
	e.createRepo(t, repo, "raw", true)
	body := rawRandom(t, 64<<10)
	sum := hexSHA256(body)
	p := "/raw/" + repo + "/copy.bin"
	e.put(t, repo, p, body)
	regionOf := func() string {
		t.Helper()
		resp, b := e.waitHit(t, p, nil)
		if !bytes.Equal(b, body) {
			t.Fatalf("GET %s at the edge: %d bytes, want the %d bytes of the file", p, len(b), len(body))
		}
		return strings.TrimPrefix(hitOf(resp), "hit; region=")
	}
	if got := regionOf(); got != edgeHomeHint {
		t.Fatalf("before the copy the edge reads region %q, want the home bucket (%s)", got, edgeHomeHint)
	}
	e.waitBlobs(t, "the copy of a new blob", sum, true, "weur", "apac")
	if got := regionOf(); got != "weur" {
		t.Fatalf("after the copy the edge reads region %q, want weur (the client is in London)", got)
	}

	// A regional copy deleted by hand: the edge reads the home blob, and the gap check copies it
	// again.
	if err := e.stores["weur"].Delete(context.Background(), edgeBlobKey(sum)); err != nil {
		t.Fatal(err)
	}
	if got := regionOf(); got != edgeHomeHint {
		t.Fatalf("without the regional copy the edge reads region %q, want the home bucket", got)
	}
	e.waitBlobs(t, "the copy deleted by hand", sum, true, "weur")
	if got := regionOf(); got != "weur" {
		t.Fatalf("after the gap check the edge reads region %q, want weur", got)
	}

	// The last file of the blob goes: the GC deletes the home blob, and the gap check the copies.
	e.deleteFile(t, repo, "copy.bin")
	deadline := time.Now().Add(60 * time.Second)
	for e.has(t, "home", edgeBlobKey(sum)) {
		if time.Now().After(deadline) {
			t.Fatal("the blob GC did not delete the home blob within 60s")
		}
		time.Sleep(time.Second)
	}
	e.waitBlobs(t, "the copies of a collected blob", sum, false, "weur", "apac")
}

// TestEdgeRegions: adding a region fills it and marks it ready; removing one stops the writes to
// its bucket.
func TestEdgeRegions(t *testing.T) {
	t.Parallel()
	e := startEdge(t, edgeOpts{Regions: []string{"weur=edge-weur"}})
	repo := RandName("mvn")
	e.createRepo(t, repo, "maven", true)
	jar := func(v string) string { return "/maven/" + repo + "/com/e2e/regions/" + v + "/regions-" + v + ".jar" }
	e.put(t, repo, jar("1.0"), rawRandom(t, 16<<10))
	e.waitHit(t, jar("1.0"), nil)
	if n := e.count(t, "apac", "edge/v1/p/"); n != 0 {
		t.Fatalf("the bucket of a region that is not chosen holds %d pointers", n)
	}

	// Add apac: it fills (its pointers, then its blobs through the gap check) and becomes ready.
	e.S.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"edge.regions": edgeBothRegions}, e.S.Admin(), nil)
	st := e.waitStatus(t, "apac listed", 30*time.Second, func(st edgeStatus) bool { return st.state("apac") != "" })
	if s := st.state("apac"); s != "filling" {
		t.Fatalf("apac right after it was added: state %q, want filling", s)
	}
	deadline := time.Now().Add(5 * time.Minute)
	for e.status(t).state("apac") != "ready" {
		if time.Now().After(deadline) {
			b, _ := json.Marshal(e.status(t))
			t.Fatalf("apac is not ready 5 minutes after it was added: %s", b)
		}
		e.requestGap(t)
		e.gap(t)
		time.Sleep(5 * time.Second)
	}
	if n := e.count(t, "apac", "edge/v1/p/"); n == 0 {
		t.Fatal("apac is ready without the pointer of the release")
	}
	if n := e.count(t, "apac", "blobs/"); n == 0 {
		t.Fatal("apac is ready without the blob of the release")
	}

	// Remove apac: the server writes no pointer there any more.
	e.S.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"edge.regions": []string{"weur=edge-weur"}}, e.S.Admin(), nil)
	e.waitStatus(t, "apac removed", time.Minute, func(st edgeStatus) bool { return st.state("apac") == "" })
	apac, weur := e.count(t, "apac", "edge/v1/p/"), e.count(t, "weur", "edge/v1/p/")
	e.put(t, repo, jar("2.0"), rawRandom(t, 16<<10))
	e.waitHit(t, jar("2.0"), nil)
	time.Sleep(2 * time.Second)
	if n := e.count(t, "weur", "edge/v1/p/"); n <= weur {
		t.Fatalf("weur holds %d pointers after a new release, want more than %d", n, weur)
	}
	if n := e.count(t, "apac", "edge/v1/p/"); n != apac {
		t.Fatalf("the removed region apac holds %d pointers after a new release, want still %d", n, apac)
	}
}

// TestEdgeRanges: Range, HEAD, 304, 416 and If-Range answer the same at the edge and at the
// server, on a mutable path (raw, home pointer) and an immutable one (a Maven release).
func TestEdgeRanges(t *testing.T) {
	t.Parallel()
	e := startEdge(t, edgeOpts{})
	raw, mvn := RandName("raw"), RandName("mvn")
	e.createRepo(t, raw, "raw", true)
	e.createRepo(t, mvn, "maven", true)
	body := rawRandom(t, 1000)
	paths := []string{"/raw/" + raw + "/r.bin", "/maven/" + mvn + "/com/e2e/ranges/1.0/ranges-1.0.jar"}
	e.put(t, raw, paths[0], body)
	e.put(t, mvn, paths[1], body)
	empty := "/raw/" + raw + "/empty.bin"
	e.put(t, raw, empty, []byte{})
	etag := `"` + hexSHA256(body) + `"`
	for _, p := range append(paths, empty) {
		e.waitHit(t, p, nil)
	}
	cases := []struct {
		what   string
		method string
		hdr    map[string]string
		hit    bool
	}{
		{"the whole file", http.MethodGet, nil, true},
		{"HEAD", http.MethodHead, nil, true},
		{"bytes=0-9", http.MethodGet, map[string]string{"Range": "bytes=0-9"}, true},
		{"bytes=-5", http.MethodGet, map[string]string{"Range": "bytes=-5"}, true},
		{"bytes=990-", http.MethodGet, map[string]string{"Range": "bytes=990-"}, true},
		{"bytes=900-5000 (the end is cut)", http.MethodGet, map[string]string{"Range": "bytes=900-5000"}, true},
		{"several ranges (the whole file)", http.MethodGet, map[string]string{"Range": "bytes=0-1,5-6"}, true},
		{"a Range that does not parse (the whole file)", http.MethodGet, map[string]string{"Range": "bytes=abc"}, true},
		{"If-Range with the ETag", http.MethodGet, map[string]string{"Range": "bytes=10-19", "If-Range": etag}, true},
		{"If-Range with another ETag (the whole file)", http.MethodGet, map[string]string{"Range": "bytes=10-19", "If-Range": `"other"`}, true},
		{"If-None-Match (304)", http.MethodGet, map[string]string{"If-None-Match": etag}, true},
		{"If-None-Match on HEAD (304)", http.MethodHead, map[string]string{"If-None-Match": etag}, true},
		{"If-None-Match of another version", http.MethodGet, map[string]string{"If-None-Match": `"other"`}, true},
		{"bytes=-0 (416 at the server)", http.MethodGet, map[string]string{"Range": "bytes=-0"}, false},
		{"a range outside the file (416 at the server)", http.MethodGet, map[string]string{"Range": "bytes=5000-"}, false},
	}
	for _, p := range paths {
		for _, c := range cases {
			e.compare(t, p+": "+c.what, c.method, p, nil, c.hdr, c.hit)
		}
	}
	e.compare(t, "a range of an empty file (416 at the server)", http.MethodGet, empty, nil, map[string]string{"Range": "bytes=0-0"}, false)
	e.compare(t, "an empty file", http.MethodGet, empty, nil, nil, true)
}

// TestEdgeFailOpen: a malformed pointer, edge.enabled off and a Worker of another version each
// give server answers, not errors.
func TestEdgeFailOpen(t *testing.T) {
	t.Parallel()
	t.Run("malformed pointer and edge off", func(t *testing.T) {
		t.Parallel()
		e := startEdge(t, edgeOpts{})
		repo := RandName("raw")
		e.createRepo(t, repo, "raw", true)
		body := rawRandom(t, 4<<10)
		bad, good := "/raw/"+repo+"/bad.bin", "/raw/"+repo+"/good.bin"
		e.put(t, repo, bad, body)
		e.put(t, repo, good, body)
		e.waitHit(t, bad, nil)
		e.waitHit(t, good, nil)
		key := e.pointerKey(t, "bad.bin")
		garbage := []byte(`{"repo": "not a number", "path"`)
		if err := e.stores["home"].Put(context.Background(), key, bytes.NewReader(garbage), int64(len(garbage)), "application/json"); err != nil {
			t.Fatal(err)
		}
		resp, b := e.compare(t, "a malformed pointer", http.MethodGet, bad, nil, nil, false)
		if resp.StatusCode != http.StatusOK || !bytes.Equal(b, body) {
			t.Fatalf("a malformed pointer: status %d, %d bytes, want the file from the server", resp.StatusCode, len(b))
		}

		e.S.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"edge.enabled": false}, e.S.Admin(), nil)
		e.waitMiss(t, "after edge.enabled off", good, nil, 10*time.Second)
		resp, b = e.compare(t, "edge delivery off", http.MethodGet, good, nil, nil, false)
		if resp.StatusCode != http.StatusOK || !bytes.Equal(b, body) {
			t.Fatalf("edge delivery off: status %d, %d bytes, want the file from the server", resp.StatusCode, len(b))
		}
	})
	t.Run("a Worker of another version", func(t *testing.T) {
		t.Parallel()
		e := startEdge(t, edgeOpts{Worker: "e2e-other"})
		repo := RandName("raw")
		e.createRepo(t, repo, "raw", true)
		body := rawRandom(t, 4<<10)
		p := "/raw/" + repo + "/f.bin"
		e.put(t, repo, p, body)
		// The server writes the pointer: only the version keeps the Worker away.
		deadline := time.Now().Add(20 * time.Second)
		for e.count(t, "home", "edge/v1/p/") == 0 {
			if time.Now().After(deadline) {
				t.Fatal("no pointer in the home bucket")
			}
			time.Sleep(200 * time.Millisecond)
		}
		for range 3 {
			resp, b := e.compare(t, "a Worker of another version", http.MethodGet, p, nil, nil, false)
			if resp.StatusCode != http.StatusOK || !bytes.Equal(b, body) {
				t.Fatalf("a Worker of another version: status %d, %d bytes, want the file from the server", resp.StatusCode, len(b))
			}
		}
	})
}

// TestEdgeLastUsed: a read token that the edge checked gets its last_used_at from the Worker's
// seen/ files (the job edge.seen).
func TestEdgeLastUsed(t *testing.T) {
	t.Parallel()
	e := startEdge(t, edgeOpts{})
	repo := RandName("raw")
	e.createRepo(t, repo, "raw", true)
	p := "/raw/" + repo + "/f.bin"
	e.put(t, repo, p, rawRandom(t, 4<<10))
	e.waitHit(t, p, nil)
	users := e.count(t, "home", "edge/v1/a/user/")
	u := e.newUser(t, "seen")
	lastUsed := func() *time.Time {
		t.Helper()
		var out []struct {
			ID         int64      `json:"id"`
			LastUsedAt *time.Time `json:"last_used_at"`
		}
		_, b := e.S.API(t, http.MethodGet, "/api/tokens", nil, e.S.SignIn(t, u.name, u.pass))
		var page struct {
			Items json.RawMessage `json:"items"`
		}
		if json.Unmarshal(b, &page) == nil && len(page.Items) > 0 {
			b = page.Items
		}
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("GET /api/tokens: %s: %v", b, err)
		}
		for _, tk := range out {
			if tk.ID == u.token.ID {
				return tk.LastUsedAt
			}
		}
		t.Fatalf("GET /api/tokens: no token %d", u.token.ID)
		return nil
	}
	// The token and user objects reach the bucket before the token is used, so the server never
	// checks the token.
	deadline := time.Now().Add(20 * time.Second)
	for !e.has(t, "home", "edge/v1/a/token/"+u.token.publicID()) || e.count(t, "home", "edge/v1/a/user/") <= users {
		if time.Now().After(deadline) {
			t.Fatal("no access objects of the new user and its read token after 20s")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if at := lastUsed(); at != nil {
		t.Fatalf("a new read token: last_used_at %s, want none", at)
	}
	resp, _ := e.get(t, p, Basic{"ci", u.token.Token})
	if hitOf(resp) == "" || resp.StatusCode != http.StatusOK {
		t.Fatalf("a download with the read token: status %d, edge %q, want an edge answer", resp.StatusCode, hitOf(resp))
	}
	deadline = time.Now().Add(3 * time.Minute)
	for lastUsed() == nil {
		if time.Now().After(deadline) {
			t.Fatal("last_used_at of a read token used at the edge is still empty after 3 minutes")
		}
		time.Sleep(2 * time.Second)
	}
}

// TestEdgeClients: mvn, Gradle, apt-get and docker resolve and pull through the Worker, from a
// public and a private repo.
func TestEdgeClients(t *testing.T) {
	t.Parallel()
	e := startEdge(t, edgeOpts{})
	s := e.S
	reader := e.newUser(t, "reader")
	pkgs := buildDebPackages(t)
	archs := []string{"amd64", "arm64"}
	if !containsString(archs, pkgs.arch) {
		archs = append(archs, pkgs.arch)
	}
	d := StartDind(t, e.Host)
	for _, private := range []bool{false, true} {
		name := map[bool]string{false: "public", true: "private"}[private]
		t.Run(name, func(t *testing.T) {
			// Maven: deploy through the Worker (the write goes to the server), then resolve and run.
			mvn := RandName("mvn")
			e.createRepo(t, mvn, "maven", !private)
			if private {
				e.grant(t, reader, mvn)
			}
			group := "com.e2eedge.g" + randHex(4)
			msg := "hello through the edge " + randHex(4)
			repoURL := e.Internal + "/maven/" + mvn
			code, out := RunClient(t, mavenImage, ClientOpts{
				Cmd: []string{"cd /work/lib && mvn -B -ntp -q -s /work/deploy.xml deploy"},
				Files: append(mavenLibProject(group, "1.0.0", repoURL, msg),
					ContainerFile{Path: "/work/deploy.xml", Content: mavenSettingsXML("ci", PublishToken(t, s, s.AdminUser, mvn))}),
				Mounts:  map[string]string{M2Volume: "/root/.m2/repository"},
				Timeout: 15 * time.Minute,
			})
			if code != 0 {
				t.Fatalf("mvn deploy through the Worker: exit %d\n%s", code, out)
			}
			gp := strings.ReplaceAll(group, ".", "/")
			jar := "/maven/" + mvn + "/" + gp + "/hello/1.0.0/hello-1.0.0.jar"
			var read Auth
			if private {
				read = Basic{"ci", reader.token.Token}
			}
			e.waitHit(t, jar, read)
			code, out = RunClient(t, mavenImage, ClientOpts{
				Cmd: []string{fmt.Sprintf("set -e\ncd /work/consumer\nrm -rf /root/.m2/repository/%s\nmvn -B -ntp -q -s /work/settings.xml -Ddep.version=1.0.0 -Dmdep.outputFile=/tmp/cp.txt org.apache.maven.plugins:maven-dependency-plugin:3.8.1:build-classpath\njava -cp \"$(cat /tmp/cp.txt)\" %s.Hello\n", gp, group)},
				Files: []ContainerFile{{Path: "/work/settings.xml", Content: mavenSettingsXML("ci", reader.token.Token)},
					mavenConsumerProject(group, repoURL)},
				Mounts:  map[string]string{M2Volume: "/root/.m2/repository"},
				Timeout: 15 * time.Minute,
			})
			if code != 0 || !strings.Contains(out, msg) {
				t.Fatalf("mvn resolve through the Worker: exit %d, want %q in the output\n%s", code, msg, out)
			}

			// Gradle resolves the same jar.
			code, out = RunClient(t, gradleImage, ClientOpts{
				Cmd:     []string{"cd /work/g && gradle --no-daemon --console=plain --no-configuration-cache -q resolve"},
				Files:   edgeGradleProject(group, repoURL, read),
				Env:     map[string]string{"GRADLE_USER_HOME": "/home/gradle/.gradle"},
				Mounts:  map[string]string{GradleVolume: "/home/gradle/.gradle"},
				User:    "root",
				Timeout: 15 * time.Minute,
			})
			if code != 0 || !strings.Contains(out, "RESOLVED hello-1.0.0.jar") {
				t.Fatalf("gradle through the Worker: exit %d\n%s", code, out)
			}

			// apt-get installs a package through the Worker.
			deb := RandName("deb")
			s.CreateRepo(t, deb, "debian", !private, map[string]any{"architectures": archs})
			if private {
				e.grant(t, reader, deb)
			}
			e.uploadDeb(t, deb, pkgs.hello1)
			e.uploadDeb(t, deb, pkgs.native)
			key := s.DebianKey(t, deb, s.AdminBasic())
			e.waitHit(t, "/debian/"+deb+"/pool/main/e/e2e-hello/e2e-hello_1.0_all.deb", read)
			sources := fmt.Sprintf("Types: deb\nURIs: %s/debian/%s\nSuites: trixie\nComponents: main\nSigned-By: /etc/apt/keyrings/tr.asc\n", e.Internal, deb)
			files := []ContainerFile{
				{Content: debAptScript, Path: "/apt.sh", Mode: 0o755},
				{Content: key, Path: "/etc/apt/keyrings/tr.asc"},
				{Content: sources, Path: "/etc/apt/sources.list.d/tr.sources"},
			}
			if private {
				files = append(files, ContainerFile{Path: "/etc/apt/auth.conf.d/tr.conf", Mode: 0o644,
					Content: fmt.Sprintf("machine http://%s/debian/%s login ci password %s\n", e.Host, deb, reader.token.Token)})
			}
			code, out = RunClient(t, debImage, ClientOpts{Cmd: []string{"sh", "/apt.sh"}, Files: files})
			if code != 0 || !strings.Contains(out, "RESULT: hello 1.0\n") {
				t.Fatalf("apt-get install through the Worker: exit %d\n%s", code, out)
			}

			// docker pull through the Worker; the registry host is the Worker's.
			img := RandName("img")
			e.createRepo(t, img, "docker", !private)
			if private {
				e.grant(t, reader, img)
			}
			image := newOCI(t, rawRandom(t, 256<<10))
			e.pushOCI(t, img+"/app", "v1", image)
			e.waitHit(t, "/v2/"+img+"/app/blobs/sha256:"+image.Layers[0], read)
			script := "set -e\n"
			if private {
				script += fmt.Sprintf("echo %s | docker login %s -u ci --password-stdin\n", reader.token.Token, e.Host)
			}
			script += fmt.Sprintf("docker pull %s/%s/app:v1\ndocker image inspect --format '{{.Id}}' %s/%s/app:v1\n", e.Host, img, e.Host, img)
			out = d.MustExec(t, "docker pull through the Worker", script)
			// With the containerd image store the image ID is the manifest digest.
			if want := hexSHA256(image.Manifest); !strings.Contains(out, want) {
				t.Fatalf("docker pull through the Worker: the image ID is not the manifest sha256:%s\n%s", want, out)
			}
		})
	}
}

// edgeGradleProject is a Kotlin DSL build that resolves group:hello:1.0.0 from repoURL, with the
// read token of auth when it is Basic, and prints the name of each file of the runtime classpath.
func edgeGradleProject(group, repoURL string, auth Auth) []ContainerFile {
	creds := ""
	if b, ok := auth.(Basic); ok {
		creds = fmt.Sprintf("\n        credentials {\n            username = %q\n            password = %q\n        }", b.User, b.Pass)
	}
	build := fmt.Sprintf(`plugins {
    java
}

repositories {
    maven {
        url = uri(%q)
        isAllowInsecureProtocol = true%s
    }
}

dependencies {
    implementation("%s:hello:1.0.0")
}

tasks.register("resolve") {
    val cp = configurations.runtimeClasspath
    doLast {
        cp.get().files.forEach { println("RESOLVED " + it.name) }
    }
}
`, repoURL, creds, group)
	return []ContainerFile{
		{Path: "/work/g/settings.gradle.kts", Content: `rootProject.name = "resolve"` + "\n"},
		{Path: "/work/g/build.gradle.kts", Content: build},
	}
}
