//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// pyUpload posts one file through the legacy upload API of repo the way twine does, with the
// extra form fields.
func pyUpload(t *testing.T, s *Server, repo, filename string, data []byte, auth Auth, fields map[string]string) (int, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField(":action", "file_upload")
	mw.WriteField("protocol_version", "1")
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	w, err := mw.CreateFormFile("content", filename)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(data)
	mw.Close()
	req := s.NewRequest(t, http.MethodPost, "/pypi/"+repo+"/legacy/", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, b := s.Do(t, req, auth)
	return resp.StatusCode, string(b)
}

// pyPage is the JSON form of a project page.
type pyPage struct {
	Meta struct {
		APIVersion string `json:"api-version"`
	} `json:"meta"`
	Name     string   `json:"name"`
	Versions []string `json:"versions"`
	Files    []struct {
		Filename       string            `json:"filename"`
		URL            string            `json:"url"`
		Hashes         map[string]string `json:"hashes"`
		RequiresPython string            `json:"requires-python"`
		Yanked         any               `json:"yanked"`
		Size           *int64            `json:"size"`
		UploadTime     string            `json:"upload-time"`
		CoreMetadata   any               `json:"core-metadata"`
		DistInfo       any               `json:"dist-info-metadata"`
	} `json:"files"`
}

// pyGetPage reads the JSON page of project n of repo.
func pyGetPage(t *testing.T, s *Server, repo, n string, auth Auth) (int, pyPage) {
	t.Helper()
	req := s.NewRequest(t, http.MethodGet, "/pypi/"+repo+"/simple/"+n+"/", nil)
	req.Header.Set("Accept", "application/vnd.pypi.simple.v1+json")
	resp, b := s.Do(t, req, auth)
	var p pyPage
	if resp.StatusCode == http.StatusOK {
		if ct := resp.Header.Get("Content-Type"); ct != "application/vnd.pypi.simple.v1+json" {
			t.Fatalf("page of %s: Content-Type %q", n, ct)
		}
		if err := json.Unmarshal(b, &p); err != nil {
			t.Fatalf("page of %s: %v\n%s", n, err, b)
		}
	}
	return resp.StatusCode, p
}

// poetryApp is a Poetry project at /work/app that installs dependencies only (no package of its own).
var poetryApp = ContainerFile{Path: "/work/app/pyproject.toml", Content: `[project]
name = "app"
version = "0.1.0"
requires-python = ">=3.9"
dependencies = []

[tool.poetry]
package-mode = false
`}

// TestPyPI: twine uploads the wheel and sdist that python -m build makes to a private hosted repo;
// the page lists both with hashes, sizes, upload times and the wheel's core metadata, whose bytes
// the .metadata file serves; an upload of other bytes under a file name that exists fails with
// 409; pip installs the package with --require-hashes.
func TestPyPI(t *testing.T) {
	t.Parallel()
	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo := RandName("py")
		s.CreateRepo(t, repo, "pypi", false, nil)
		writer, writerPass := RandName("writer"), randHex(12)
		s.CreateUser(t, writer, writerPass)
		s.SetPerms(t, repo, writer, "repo.download", "repo.publish")
		token := s.CreateToken(t, writer, writerPass, "pypi-e2e")
		name, mod := pyNames()
		msg := "hello from " + name
		upload := s.InternalURL + "/pypi/" + repo + "/"

		code, out := RunClient(t, pythonImage, ClientOpts{
			Cmd: []string{`set -e
pip install -q --root-user-action=ignore build twine
cd /work/p
python -m build
twine upload --non-interactive --repository-url "$URL" dist/*
sed -i 's/hello from/changed hello from/' ` + mod + `/__init__.py
rm -rf dist build *.egg-info
python -m build
if twine upload --non-interactive --repository-url "$URL" dist/*; then echo "UPLOAD OF OTHER BYTES PASSED"; exit 1; fi
`},
			Files: pySetuptoolsProject("/work/p", name, mod, "1.0.0", msg),
			Env:   map[string]string{"URL": upload, "TWINE_USERNAME": "__token__", "TWINE_PASSWORD": PublishToken(t, s, writer, repo)},
		})
		if code != 0 {
			t.Fatalf("build and twine upload: exit %d\n%s", code, out)
		}
		if !strings.Contains(out, "409 Conflict") {
			t.Fatalf("twine output has no 409 Conflict:\n%s", out)
		}

		read := Basic{writer, token}
		st, page := pyGetPage(t, s, repo, name, read)
		if st != http.StatusOK || page.Meta.APIVersion != "1.1" || page.Name != name || len(page.Files) != 2 ||
			len(page.Versions) != 1 || page.Versions[0] != "1.0.0" {
			t.Fatalf("page of %s: status %d: %+v", name, st, page)
		}
		var wheelURL, wheelSHA, sdistSHA, metaSHA string
		for _, f := range page.Files {
			if f.Hashes["sha256"] == "" || f.Size == nil || *f.Size <= 0 || f.UploadTime == "" || f.RequiresPython != ">=3.8" || f.Yanked != false {
				t.Fatalf("file of the page: %+v", f)
			}
			if strings.HasSuffix(f.Filename, ".whl") {
				cm, _ := f.CoreMetadata.(map[string]any)
				di, _ := f.DistInfo.(map[string]any)
				if cm == nil || di == nil || cm["sha256"] == "" || cm["sha256"] != di["sha256"] {
					t.Fatalf("wheel without core-metadata: %+v", f)
				}
				wheelURL, wheelSHA, metaSHA = f.URL, f.Hashes["sha256"], cm["sha256"].(string)
			} else {
				sdistSHA = f.Hashes["sha256"]
			}
		}
		base, _ := url.Parse(s.URL + "/pypi/" + repo + "/simple/" + name + "/")
		ref, _ := url.Parse(wheelURL)
		wheelPath := base.ResolveReference(ref).Path
		if meta := getFollowS3(t, s, opts.S3, wheelPath+".metadata"); !bytes.HasPrefix(meta, []byte("Metadata-Version:")) || sha256Hex(meta) != metaSHA {
			t.Fatalf("GET %s.metadata: sha256 %s, want %s:\n%s", wheelPath, sha256Hex(meta), metaSHA, meta)
		}
		if whl := getFollowS3(t, s, opts.S3, wheelPath); sha256Hex(whl) != wheelSHA {
			t.Fatalf("GET %s: sha256 %s, want %s", wheelPath, sha256Hex(whl), wheelSHA)
		}

		code, out = RunClient(t, pythonImage, ClientOpts{
			Cmd: []string{`set -e
pip install --root-user-action=ignore --no-cache-dir --index-url "$INDEX" --require-hashes -r /work/req.txt
python -c "import ` + mod + `; print(` + mod + `.MSG)"
`},
			Files: []ContainerFile{{Path: "/work/req.txt", Content: name + "==1.0.0 --hash=sha256:" + wheelSHA + " --hash=sha256:" + sdistSHA + "\n"}},
			Env:   map[string]string{"INDEX": pyIndex(s, repo, writer, token), "PIP_TRUSTED_HOST": s.Alias},
		})
		if code != 0 || !strings.Contains(out, msg) || strings.Contains(out, "changed "+msg) {
			t.Fatalf("pip install --require-hashes: exit %d, want %q:\n%s", code, msg, out)
		}
	})
}

// TestPyPIProtocol: the simple API answers JSON, HTML and text/html by Accept (406 for none of
// them) with Vary and a strong ETag (304); a name that is not normalised, or that lacks the slash,
// is sent to the normalised page; the project list; the legacy upload refuses a password, a form
// name or a digest that disagrees, and other bytes under a file name that exists (409 "File
// already exists"), and keeps the same bytes (200); the version browser; a delete of the wheel
// drops it from the page, and a delete of the last file drops the project.
func TestPyPIProtocol(t *testing.T) {
	t.Parallel()
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo := RandName("py")
		s.CreateRepo(t, repo, "pypi", false, nil)
		writer, writerPass := RandName("writer"), randHex(12)
		s.CreateUser(t, writer, writerPass)
		s.SetPerms(t, repo, writer, "repo.download", "repo.publish")
		pub := s.PublishAuth(t, writer, repo)
		read := Basic{writer, writerPass}
		name, mod := pyNames()
		file, whl := pyWheel(name, mod, "2.0", "v2")

		if st, b := pyUpload(t, s, repo, file, whl, read, nil); st != http.StatusUnauthorized || !strings.Contains(b, "publish session") {
			t.Fatalf("upload with a password: status %d, want 401 naming the publish session: %s", st, b)
		}
		if st, b := pyUpload(t, s, repo, file, whl, pub, map[string]string{"name": "other-name"}); st != http.StatusBadRequest {
			t.Fatalf("upload with another form name: status %d, want 400: %s", st, b)
		}
		if st, b := pyUpload(t, s, repo, file, whl, pub, map[string]string{"sha256_digest": strings.Repeat("0", 64)}); st != http.StatusBadRequest {
			t.Fatalf("upload with a wrong digest: status %d, want 400: %s", st, b)
		}
		if st, b := pyUpload(t, s, repo, file, whl, pub, map[string]string{"name": name, "version": "2.0", "sha256_digest": sha256Hex(whl)}); st != http.StatusCreated {
			t.Fatalf("upload: status %d, want 201: %s", st, b)
		}
		if st, b := pyUpload(t, s, repo, file, whl, pub, nil); st != http.StatusOK {
			t.Fatalf("upload of the same bytes: status %d, want 200: %s", st, b)
		}
		_, other := pyWheel(name, mod, "2.0", "other bytes")
		if st, b := pyUpload(t, s, repo, file, other, pub, nil); st != http.StatusConflict || !strings.Contains(b, "File already exists") {
			t.Fatalf("upload of other bytes: status %d, want 409 File already exists: %s", st, b)
		}
		_, v1 := pyWheel(name, mod, "1.0", "v1")
		file1 := strings.Replace(file, "-2.0-", "-1.0-", 1)
		if st, b := pyUpload(t, s, repo, file1, v1, pub, nil); st != http.StatusCreated {
			t.Fatalf("upload of 1.0: status %d: %s", st, b)
		}

		get := func(path, accept string, auth Auth) (*http.Response, []byte) {
			req := s.NewRequest(t, http.MethodGet, path, nil)
			if accept != "" {
				req.Header.Set("Accept", accept)
			}
			return s.Do(t, req, auth)
		}
		pagePath := "/pypi/" + repo + "/simple/" + name + "/"
		if resp, _ := get(pagePath, "", nil); resp.StatusCode != http.StatusUnauthorized || !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Basic") {
			t.Fatalf("anonymous read of a private repo: status %d, WWW-Authenticate %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
		}
		for _, c := range []struct{ accept, ct string }{
			{"application/vnd.pypi.simple.v1+json", "application/vnd.pypi.simple.v1+json"},
			{"application/vnd.pypi.simple.latest+json", "application/vnd.pypi.simple.v1+json"},
			{"application/vnd.pypi.simple.v1+json, application/vnd.pypi.simple.v1+html;q=0.1, text/html;q=0.01", "application/vnd.pypi.simple.v1+json"},
			{"application/vnd.pypi.simple.v1+html", "application/vnd.pypi.simple.v1+html"},
			{"text/html", "text/html; charset=utf-8"},
			{"", "text/html; charset=utf-8"},
		} {
			resp, b := get(pagePath, c.accept, read)
			if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != c.ct || !strings.Contains(resp.Header.Get("Vary"), "Accept") {
				t.Fatalf("Accept %q: status %d, Content-Type %q, Vary %q", c.accept, resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Vary"))
			}
			if strings.Contains(c.ct, "html") {
				for _, want := range []string{`<meta name="pypi:repository-version" content="1.1">`, `data-requires-python="&gt;=3.8"`,
					`data-core-metadata="sha256=`, `data-dist-info-metadata="sha256=`, "#sha256=" + sha256Hex(whl), ">" + file + "</a>"} {
					if !strings.Contains(string(b), want) {
						t.Fatalf("HTML page has no %q:\n%s", want, b)
					}
				}
			}
			etag := resp.Header.Get("ETag")
			req := s.NewRequest(t, http.MethodGet, pagePath, nil)
			req.Header.Set("Accept", c.accept)
			req.Header.Set("If-None-Match", etag)
			if resp, _ := s.Do(t, req, read); etag == "" || resp.StatusCode != http.StatusNotModified {
				t.Fatalf("If-None-Match %s: status %d, want 304", etag, resp.StatusCode)
			}
		}
		if resp, _ := get(pagePath, "application/xml", read); resp.StatusCode != http.StatusNotAcceptable {
			t.Fatalf("Accept application/xml: status %d, want 406", resp.StatusCode)
		}
		st, page := pyGetPage(t, s, repo, name, read)
		if st != http.StatusOK || len(page.Files) != 2 || strings.Join(page.Versions, ",") != "1.0,2.0" {
			t.Fatalf("page: status %d: %+v", st, page)
		}

		for _, p := range []string{"/pypi/" + repo + "/simple/" + strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "/",
			"/pypi/" + repo + "/simple/" + name} {
			resp, _ := get(p, "", read)
			if resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Location") != pagePath {
				t.Fatalf("GET %s: status %d, Location %q, want 301 to %s", p, resp.StatusCode, resp.Header.Get("Location"), pagePath)
			}
		}
		resp, b := get("/pypi/"+repo+"/simple/", "application/vnd.pypi.simple.v1+json", read)
		var index struct {
			Projects []struct {
				Name string `json:"name"`
			} `json:"projects"`
		}
		if resp.StatusCode != http.StatusOK || json.Unmarshal(b, &index) != nil || len(index.Projects) != 1 || index.Projects[0].Name != name {
			t.Fatalf("project list: status %d: %s", resp.StatusCode, b)
		}
		if resp, b := get("/pypi/"+repo+"/simple/", "text/html", read); resp.StatusCode != http.StatusOK || !strings.Contains(string(b), `href="`+name+`/"`) {
			t.Fatalf("HTML project list: status %d:\n%s", resp.StatusCode, b)
		}
		if resp, _ := get("/pypi/"+repo+"/simple/no-such-project/", "", read); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("missing project: status %d, want 404", resp.StatusCode)
		}

		var versions struct {
			Artifact *struct {
				Title string `json:"title"`
			} `json:"artifact"`
			Items []struct {
				Version string `json:"version"`
			} `json:"items"`
			Next *string `json:"next"`
		}
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/versions?path=packages/"+name+"/&limit=1", nil, read, &versions)
		if versions.Artifact == nil || len(versions.Items) != 1 || versions.Items[0].Version != "2.0" || versions.Next == nil {
			t.Fatalf("versions, page 1: %+v", versions)
		}
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/versions?path=packages/"+name+"/&limit=1&after="+url.QueryEscape(*versions.Next), nil, read, &versions)
		if len(versions.Items) != 1 || versions.Items[0].Version != "1.0" || versions.Next != nil {
			t.Fatalf("versions, page 2: %+v", versions)
		}

		s.MustAPI(t, http.MethodPost, "/api/repos/"+repo+"/files/delete", map[string][]string{"paths": {"packages/" + name + "/" + file}}, s.Admin(), nil)
		if st, page := pyGetPage(t, s, repo, name, read); st != http.StatusOK || len(page.Files) != 1 || page.Files[0].Filename != file1 {
			t.Fatalf("page after the delete of %s: status %d: %+v", file, st, page)
		}
		if paths := mavenAssetPaths(t, s, repo, "packages/", s.Admin()); len(paths) != 2 {
			t.Fatalf("files after the delete of %s (its .metadata goes too): %v", file, paths)
		}
		s.MustAPI(t, http.MethodPost, "/api/repos/"+repo+"/files/delete", map[string][]string{"paths": {"packages/" + name + "/" + file1}}, s.Admin(), nil)
		if st, _ := pyGetPage(t, s, repo, name, read); st != http.StatusNotFound {
			t.Fatalf("page after the delete of every file: status %d, want 404", st)
		}
		if resp, b := get("/pypi/"+repo+"/simple/", "application/vnd.pypi.simple.v1+json", read); !strings.Contains(string(b), `"projects":[]`) {
			t.Fatalf("project list after the delete: status %d: %s", resp.StatusCode, b)
		}
	})
}

// TestPyPIClients: uv publish and poetry publish upload to a hosted repo; uv resolves from the
// .metadata file without the wheel, and uv and poetry install the packages.
func TestPyPIClients(t *testing.T) {
	t.Parallel()
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo := RandName("py")
		s.CreateRepo(t, repo, "pypi", false, nil)
		writer, writerPass := RandName("writer"), randHex(12)
		s.CreateUser(t, writer, writerPass)
		s.SetPerms(t, repo, writer, "repo.download", "repo.publish")
		token := s.CreateToken(t, writer, writerPass, "pypi-e2e")
		uvName, uvMod := pyNames()
		poName, poMod := pyNames()
		upload := s.InternalURL + "/pypi/" + repo + "/legacy/"

		code, out := RunClient(t, uvImage, ClientOpts{
			Cmd: []string{`set -e
cd /work/p
uv build
uv publish --publish-url "$URL" dist/*
`},
			Files: pySetuptoolsProject("/work/p", uvName, uvMod, "1.0.0", "uv "+uvName),
			Env:   map[string]string{"URL": upload, "UV_PUBLISH_USERNAME": "__token__", "UV_PUBLISH_PASSWORD": PublishToken(t, s, writer, repo)},
		})
		if code != 0 {
			t.Fatalf("uv build and uv publish: exit %d\n%s", code, out)
		}
		code, out = RunClient(t, pythonImage, ClientOpts{
			Cmd: []string{`set -e
pip install -q --root-user-action=ignore poetry
cd /work/p
poetry config repositories.tr "$URL"
poetry publish --build -r tr -u __token__ -p "$TOKEN"
`},
			Files: pyPoetryProject("/work/p", poName, poMod, "1.0.0", "poetry "+poName),
			Env:   map[string]string{"URL": upload, "TOKEN": PublishToken(t, s, writer, repo)},
		})
		if code != 0 {
			t.Fatalf("poetry publish: exit %d\n%s", code, out)
		}
		have := strings.Join(mavenAssetPaths(t, s, repo, "packages/", s.Admin()), "\n") + "\n"
		for _, want := range []string{"packages/" + uvName + "/" + uvMod + "-1.0.0-py3-none-any.whl", "packages/" + uvName + "/" + uvMod + "-1.0.0-py3-none-any.whl.metadata",
			"packages/" + poName + "/" + poMod + "-1.0.0-py3-none-any.whl"} {
			if !strings.Contains(have, want+"\n") {
				t.Fatalf("asset %s not published; have:\n%s", want, have)
			}
		}

		// A resolve reads the .metadata file and not the wheel.
		wheel := "/pypi/" + repo + "/packages/" + uvName + "/" + uvMod + "-1.0.0-py3-none-any.whl"
		code, out = RunClient(t, uvImage, ClientOpts{
			Cmd: []string{`set -e
uv pip compile --no-cache --index-url "$INDEX" /work/req.in
`},
			Files: []ContainerFile{{Path: "/work/req.in", Content: uvName + "\n"}},
			Env:   map[string]string{"INDEX": pyIndex(s, repo, writer, token)},
		})
		if code != 0 || !strings.Contains(out, uvName+"==1.0.0") {
			t.Fatalf("uv pip compile: exit %d\n%s", code, out)
		}
		logs := s.LogsThrough(t)
		if !strings.Contains(logs, wheel+".metadata") || strings.Contains(logs, wheel+" ") || strings.Contains(logs, wheel+"\"") {
			t.Fatalf("uv resolved without the .metadata file, or downloaded the wheel %s:\n%s", wheel, logs)
		}

		code, out = RunClient(t, uvImage, ClientOpts{
			Cmd: []string{`set -e
uv venv -q /tmp/venv
uv pip install --no-cache --python /tmp/venv/bin/python --index-url "$INDEX" ` + uvName + ` ` + poName + `
/tmp/venv/bin/python -c "import ` + uvMod + `, ` + poMod + `; print(` + uvMod + `.MSG); print(` + poMod + `.MSG)"
`},
			Env: map[string]string{"INDEX": pyIndex(s, repo, writer, token)},
		})
		if code != 0 || !strings.Contains(out, "uv "+uvName) || !strings.Contains(out, "poetry "+poName) {
			t.Fatalf("uv pip install: exit %d\n%s", code, out)
		}
		code, out = RunClient(t, pythonImage, ClientOpts{
			Cmd: []string{`set -e
pip install -q --root-user-action=ignore poetry
cd /work/app
poetry source add --priority=primary tr "$INDEX"
poetry config http-basic.tr "$USER" "$TOKEN"
poetry add ` + uvName + `
poetry run python -c "import ` + uvMod + `; print(` + uvMod + `.MSG)"
`},
			Files:   []ContainerFile{poetryApp},
			Env:     map[string]string{"INDEX": pyIndex(s, repo, "", ""), "USER": writer, "TOKEN": token},
			Timeout: 15 * time.Minute,
		})
		if code != 0 || !strings.Contains(out, "uv "+uvName) {
			t.Fatalf("poetry add: exit %d\n%s", code, out)
		}
	})
}

// TestPyPIProxyGroup: a proxy of pypi.org serves pip, uv and poetry; its files come from the
// files host into files/; a group of a hosted repo and the proxy serves a project of the hosted
// repo only from it, though pypi.org has a higher version of that name.
func TestPyPIProxyGroup(t *testing.T) {
	t.Parallel()
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		proxy, hosted, group := RandName("pypi-org"), RandName("py"), RandName("py-all")
		s.CreateProxy(t, proxy, "pypi", true, map[string]any{"remote_url": "https://pypi.org/"}, false)
		s.CreateRepo(t, hosted, "pypi", true, nil)
		s.CreateGroup(t, group, "pypi", true, []string{hosted, proxy})
		writer := RandName("writer")
		s.CreateUser(t, writer, randHex(12))
		s.SetPerms(t, hosted, writer, "repo.download", "repo.publish")

		code, out := RunClient(t, pythonImage, ClientOpts{
			Cmd: []string{`set -e
pip install --root-user-action=ignore --no-cache-dir --index-url "$INDEX" iniconfig==2.0.0
python -c "import iniconfig; print('INICONFIG OK')"
`},
			Env: map[string]string{"INDEX": pyIndex(s, proxy, "", ""), "PIP_TRUSTED_HOST": s.Alias},
		})
		if code != 0 || !strings.Contains(out, "INICONFIG OK") {
			t.Fatalf("pip install through the proxy: exit %d\n%s", code, out)
		}
		if paths := mavenAssetPaths(t, s, proxy, "files/", s.Admin()); len(paths) == 0 {
			t.Fatal("the proxy stored no file under files/")
		}
		code, out = RunClient(t, uvImage, ClientOpts{
			Cmd: []string{`set -e
uv venv -q /tmp/venv
uv pip install --no-cache --python /tmp/venv/bin/python --index-url "$INDEX" six==1.17.0
/tmp/venv/bin/python -c "import six; print('SIX', six.__version__)"
`},
			Env: map[string]string{"INDEX": pyIndex(s, proxy, "", "")},
		})
		if code != 0 || !strings.Contains(out, "SIX 1.17.0") {
			t.Fatalf("uv pip install through the proxy: exit %d\n%s", code, out)
		}

		// A private project named like a public one: the hosted member owns the name.
		file, whl := pyWheel("iniconfig", "iniconfig", "0.0.1", "private iniconfig")
		if st, b := pyUpload(t, s, hosted, file, whl, s.PublishAuth(t, writer, hosted), nil); st != http.StatusCreated {
			t.Fatalf("upload of the private iniconfig: status %d: %s", st, b)
		}
		st, page := pyGetPage(t, s, group, "iniconfig", nil)
		if st != http.StatusOK || len(page.Files) != 1 || page.Files[0].Filename != file {
			t.Fatalf("group page of iniconfig: status %d: %+v", st, page)
		}
		if st, page := pyGetPage(t, s, group, "six", nil); st != http.StatusOK || len(page.Files) == 0 {
			t.Fatalf("group page of six: status %d: %+v", st, page)
		}
		code, out = RunClient(t, pythonImage, ClientOpts{
			Cmd: []string{`set -e
pip install --root-user-action=ignore --no-cache-dir --index-url "$INDEX" iniconfig six
python -c "import iniconfig, six; print(iniconfig.MSG, 'SIX', six.__version__)"
`},
			Env: map[string]string{"INDEX": pyIndex(s, group, "", ""), "PIP_TRUSTED_HOST": s.Alias},
		})
		if code != 0 || !strings.Contains(out, "private iniconfig SIX") {
			t.Fatalf("pip install through the group: exit %d\n%s", code, out)
		}
		code, out = RunClient(t, pythonImage, ClientOpts{
			Cmd: []string{`set -e
pip install -q --root-user-action=ignore poetry
cd /work/app
poetry source add --priority=primary tr "$INDEX"
poetry add six==1.17.0
poetry run python -c "import six; print('SIX', six.__version__)"
`},
			Files:   []ContainerFile{poetryApp},
			Env:     map[string]string{"INDEX": pyIndex(s, group, "", "")},
			Timeout: 15 * time.Minute,
		})
		if code != 0 || !strings.Contains(out, "SIX 1.17.0") {
			t.Fatalf("poetry add through the group: exit %d\n%s", code, out)
		}
	})
}
