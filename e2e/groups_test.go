//go:build e2e

package e2e

import (
	"crypto/sha1"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
)

// GroupRepo is the part of the repo JSON that the group tests read.
type GroupRepo struct {
	Kind        string `json:"kind"`
	MemberCount *int   `json:"member_count"`
	Members     []struct {
		Name   string `json:"name"`
		Format string `json:"format"`
		Kind   string `json:"kind"`
		Public bool   `json:"public"`
	} `json:"members"`
	Groups []string `json:"groups"`
}

// CreateGroup creates a group repo of format with members, in read order.
func (s *Server) CreateGroup(t testing.TB, name, format string, anon bool, members []string) {
	t.Helper()
	s.MustAPI(t, http.MethodPost, "/api/repos", map[string]any{
		"name": name, "format": format, "anonymous_read": anon, "kind": "group", "members": members}, s.Admin(), nil)
}

// GetGroupRepo reads GET /api/repos/{name} as the admin.
func (s *Server) GetGroupRepo(t testing.TB, name string) GroupRepo {
	t.Helper()
	var g GroupRepo
	s.MustAPI(t, http.MethodGet, "/api/repos/"+name, nil, s.Admin(), &g)
	return g
}

func memberNames(g GroupRepo) []string {
	var out []string
	for _, m := range g.Members {
		out = append(out, m.Name)
	}
	return out
}

// mavenPut PUTs body at path of a Maven repo with a publish session of user.
func mavenPut(t *testing.T, s *Server, user, repo, path string, body []byte) {
	t.Helper()
	resp, b := rawPut(t, s, "/maven/"+repo+"/"+path, body, s.PublishAuth(t, user, repo))
	if resp.StatusCode/100 != 2 {
		t.Fatalf("PUT /maven/%s/%s: status %d: %s", repo, path, resp.StatusCode, b)
	}
}

func TestGroups(t *testing.T) {
	t.Parallel()
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		rel, snap := RandName("releases"), RandName("snapshots")
		s.CreateRepo(t, rel, "maven", true, nil)
		s.CreateRepo(t, snap, "maven", true, nil)
		writer, writerPass := RandName("writer"), randHex(12)
		s.CreateUser(t, writer, writerPass)
		s.SetPerms(t, rel, writer, "repo.download", "repo.publish")
		s.SetPerms(t, snap, writer, "repo.download", "repo.publish")
		token := s.CreateToken(t, writer, writerPass, "groups-e2e")

		grp := RandName("plex")
		s.CreateGroup(t, grp, "maven", true, []string{rel, snap})
		if g := s.GetGroupRepo(t, grp); g.Kind != "group" || g.MemberCount == nil || *g.MemberCount != 2 ||
			strings.Join(memberNames(g), ",") != rel+","+snap {
			t.Fatalf("GET group: %+v, want kind group with members %s, %s", g, rel, snap)
		}
		if g := s.GetGroupRepo(t, rel); g.Kind != "hosted" || len(g.Groups) != 1 || g.Groups[0] != grp {
			t.Fatalf("GET member %s: %+v, want groups [%s]", rel, g, grp)
		}

		// A release goes to one member, a snapshot to the other, in the layout mvn deploy writes; both
		// read through the group. How mvn and Gradle resolve is TestMaven's and TestGradlePlugin's.
		group := "com.example.group" + randHex(4)
		groupPath := strings.ReplaceAll(group, ".", "/")
		art := groupPath + "/hello/"
		pom := func(v string) []byte {
			return []byte("<project><groupId>" + group + "</groupId><artifactId>hello</artifactId><version>" + v + "</version></project>\n")
		}
		snapFile := "hello-1.1.0-20260101.000000-1"
		files := map[string]map[string][]byte{
			rel: {
				art + "1.0.0/hello-1.0.0.jar": rawRandom(t, 2048),
				art + "1.0.0/hello-1.0.0.pom": pom("1.0.0"),
				art + "maven-metadata.xml":    []byte("<metadata><groupId>" + group + "</groupId><artifactId>hello</artifactId><versioning><latest>1.0.0</latest><release>1.0.0</release><versions><version>1.0.0</version></versions><lastUpdated>20260101000000</lastUpdated></versioning></metadata>\n"),
			},
			snap: {
				art + "1.1.0-SNAPSHOT/" + snapFile + ".jar": rawRandom(t, 2048),
				art + "1.1.0-SNAPSHOT/" + snapFile + ".pom": pom("1.1.0-SNAPSHOT"),
				art + "1.1.0-SNAPSHOT/maven-metadata.xml":   []byte("<metadata modelVersion=\"1.1.0\"><groupId>" + group + "</groupId><artifactId>hello</artifactId><version>1.1.0-SNAPSHOT</version><versioning><snapshot><timestamp>20260101.000000</timestamp><buildNumber>1</buildNumber></snapshot><lastUpdated>20260101000000</lastUpdated></versioning></metadata>\n"),
				art + "maven-metadata.xml":                  []byte("<metadata><groupId>" + group + "</groupId><artifactId>hello</artifactId><versioning><latest>1.1.0-SNAPSHOT</latest><versions><version>1.1.0-SNAPSHOT</version></versions><lastUpdated>20260101000000</lastUpdated></versioning></metadata>\n"),
			},
		}
		for _, repo := range []string{rel, snap} {
			for p, body := range files[repo] {
				if strings.HasSuffix(p, "maven-metadata.xml") {
					continue
				}
				mavenPut(t, s, writer, repo, p, body)
			}
			for p, body := range files[repo] {
				if strings.HasSuffix(p, "maven-metadata.xml") {
					mavenPut(t, s, writer, repo, p, body)
				}
			}
		}
		readAuth := Basic{User: writer, Pass: token}
		for _, repo := range []string{rel, snap} {
			for p, body := range files[repo] {
				if !strings.HasSuffix(p, "maven-metadata.xml") {
					rawWantGet(t, s, opts.S3, "/maven/"+grp+"/"+p, readAuth, body)
				}
			}
		}

		// The artifact's maven-metadata.xml merges both members; its .sha1 is of the merged bytes.
		mdPath := "/maven/" + grp + "/" + groupPath + "/hello/maven-metadata.xml"
		st, md := rawGet(t, s, opts.S3, mdPath, nil)
		if st != http.StatusOK {
			t.Fatalf("GET %s: status %d: %s", mdPath, st, md)
		}
		for _, v := range []string{"<version>1.0.0</version>", "<version>1.1.0-SNAPSHOT</version>", "<release>1.0.0</release>", "<latest>1.1.0-SNAPSHOT</latest>"} {
			if !strings.Contains(string(md), v) {
				t.Fatalf("merged metadata misses %s:\n%s", v, md)
			}
		}
		st, sum := rawGet(t, s, opts.S3, mdPath+".sha1", nil)
		want := sha1.Sum(md)
		if st != http.StatusOK || strings.TrimSpace(string(sum)) != hex.EncodeToString(want[:]) {
			t.Fatalf("GET %s.sha1: status %d, %q, want %x", mdPath, st, sum, want)
		}

		// The member order decides when two members hold the same path.
		a, b := RandName("order-a"), RandName("order-b")
		for _, r := range []string{a, b} {
			s.CreateRepo(t, r, "maven", true, nil)
			s.SetPerms(t, r, writer, "repo.download", "repo.publish")
		}
		file := "com/e2e/order/1.0/order-1.0.txt"
		mavenPut(t, s, writer, a, file, []byte("from a"))
		mavenPut(t, s, writer, b, file, []byte("from b"))
		ord := RandName("order")
		s.CreateGroup(t, ord, "maven", true, []string{a, b})
		rawWantGet(t, s, opts.S3, "/maven/"+ord+"/"+file, nil, []byte("from a"))
		s.MustAPI(t, http.MethodPut, "/api/repos/"+ord+"/members", map[string]any{"members": []string{b, a}}, s.Admin(), nil)
		rawWantGet(t, s, opts.S3, "/maven/"+ord+"/"+file, nil, []byte("from b"))

		// The merge reads a member's maven-metadata.xml in the charset it declares (the Gradle
		// Plugin Portal writes US-ASCII).
		orderMD := "com/e2e/order/maven-metadata.xml"
		mavenPut(t, s, writer, a, orderMD, []byte("<?xml version='1.0' encoding='US-ASCII'?>\n<metadata><groupId>com.e2e</groupId><artifactId>order</artifactId><versioning><versions><version>2.0</version></versions></versioning></metadata>\n"))
		mavenPut(t, s, writer, b, orderMD, []byte(`<?xml version="1.0" encoding="UTF-8"?>`+"\n<metadata><groupId>com.e2e</groupId><artifactId>order</artifactId><versioning><versions><version>1.0</version></versions></versioning></metadata>\n"))
		st, md = rawGet(t, s, opts.S3, "/maven/"+ord+"/"+orderMD, nil)
		if st != http.StatusOK || !strings.Contains(string(md), "<version>1.0</version>") || !strings.Contains(string(md), "<release>2.0</release>") {
			t.Fatalf("GET the merged %s: status %d, want both members' versions and release 2.0:\n%s", orderMD, st, md)
		}

		// A public group with a private member: the member's files only for people who can read it.
		pub, priv := RandName("pub"), RandName("priv")
		s.CreateRepo(t, pub, "maven", true, nil)
		s.CreateRepo(t, priv, "maven", false, nil)
		for _, r := range []string{pub, priv} {
			s.SetPerms(t, r, writer, "repo.download", "repo.publish")
		}
		mavenPut(t, s, writer, pub, "com/e2e/pub/1.0/pub-1.0.txt", []byte("public file"))
		mavenPut(t, s, writer, priv, "com/e2e/priv/1.0/priv-1.0.txt", []byte("private file"))
		mixed := RandName("mixed")
		s.CreateGroup(t, mixed, "maven", true, []string{pub, priv})
		rawWantGet(t, s, opts.S3, "/maven/"+mixed+"/com/e2e/pub/1.0/pub-1.0.txt", nil, []byte("public file"))
		if st, b := rawGet(t, s, opts.S3, "/maven/"+mixed+"/com/e2e/priv/1.0/priv-1.0.txt", nil); st != http.StatusNotFound {
			t.Fatalf("anonymous GET of a private member's file through a public group: status %d, want 404: %s", st, b)
		}
		reader, readerPass := RandName("reader"), randHex(12)
		s.CreateUser(t, reader, readerPass)
		s.SetPerms(t, priv, reader, "repo.download")
		readerAuth := Basic{reader, s.CreateToken(t, reader, readerPass, "groups-reader")}
		rawWantGet(t, s, opts.S3, "/maven/"+mixed+"/com/e2e/priv/1.0/priv-1.0.txt", readerAuth, []byte("private file"))
		rawWantGet(t, s, opts.S3, "/maven/"+mixed+"/com/e2e/pub/1.0/pub-1.0.txt", readerAuth, []byte("public file"))

		// A group is read-only.
		for _, auth := range []Auth{nil, s.AdminBasic()} {
			resp, b := rawPut(t, s, "/maven/"+grp+"/com/e2e/x/1.0/x-1.0.jar", []byte("x"), auth)
			if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "GET, HEAD" || RefusalCode(b) != "read_only_kind" {
				t.Fatalf("PUT to the group: status %d, Allow %q: %s, want 405, Allow GET, HEAD and code read_only_kind", resp.StatusCode, resp.Header.Get("Allow"), b)
			}
		}
		if st, b := s.ManualSession(t, s.Admin(), s.AdminUser, grp); st != http.StatusConflict {
			t.Fatalf("manual publish session for a group: status %d, want 409: %s", st, b)
		}

		// Invalid members answer 400: another format, a nested group, an unknown repository.
		deb := RandName("deb")
		s.CreateRepo(t, deb, "debian", true, nil)
		for name, members := range map[string][]string{
			"another format":  {rel, deb},
			"a nested group":  {rel, ord},
			"an unknown repo": {rel, RandName("missing")},
			"no member":       {},
		} {
			st, b := s.API(t, http.MethodPost, "/api/repos", map[string]any{"name": RandName("bad"), "format": "maven", "kind": "group", "members": members}, s.Admin())
			if st != http.StatusBadRequest {
				t.Fatalf("create a group with %s: status %d, want 400: %s", name, st, b)
			}
			if st, b := s.API(t, http.MethodPut, "/api/repos/"+grp+"/members", map[string]any{"members": members}, s.Admin()); st != http.StatusBadRequest {
				t.Fatalf("set the members to %s: status %d, want 400: %s", name, st, b)
			}
		}
		if st, b := s.API(t, http.MethodPost, "/api/repos", map[string]any{"name": RandName("bad"), "format": "debian", "kind": "group", "members": []string{deb}}, s.Admin()); st != http.StatusBadRequest {
			t.Fatalf("create a Debian group: status %d, want 400: %s", st, b)
		}

		// Deleting a member removes it from the group.
		deleteRepos(t, s, snap)
		if g := s.GetGroupRepo(t, grp); *g.MemberCount != 1 || strings.Join(memberNames(g), ",") != rel {
			t.Fatalf("group after a member delete: %+v, want only %s", g, rel)
		}
		if st, _ := rawGet(t, s, opts.S3, "/maven/"+grp+"/"+groupPath+"/hello/1.1.0-SNAPSHOT/maven-metadata.xml", nil); st != http.StatusNotFound {
			t.Fatalf("snapshot metadata after its member was deleted: status %d, want 404", st)
		}
	})
}
