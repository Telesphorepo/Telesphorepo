package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Python projects: the CLI builds the project with its own tool into the temp folder of the job
// and uploads every wheel and sdist through the legacy upload of the repo, with the session token
// as a Bearer token. FILE arguments upload the named files without a build.

// pythonTool is the build tool of the Python project in dir: "poetry" for a pyproject.toml with
// [tool.poetry], "uv" with a uv.lock, else "pip" (python -m build). It is also the CI tool of the
// project.
func pythonTool(dir string) string {
	var py struct {
		Tool struct {
			Poetry map[string]any `toml:"poetry"`
		} `toml:"tool"`
	}
	if b, err := os.ReadFile(filepath.Join(dir, "pyproject.toml")); err == nil {
		if _, err := toml.Decode(string(b), &py); err == nil && py.Tool.Poetry != nil {
			return "poetry"
		}
	}
	if fileExists(filepath.Join(dir, "uv.lock")) {
		return "uv"
	}
	return "pip"
}

// pythonCommand returns python3, else python on the PATH.
func pythonCommand() (string, error) {
	names := []string{"python3", "python"}
	if runtime.GOOS == "windows" {
		names = []string{"python", "py"}
	}
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p, nil
		}
	}
	return "", errors.New("python is not on the PATH. Install Python 3 and the build package (python -m pip install build)")
}

// pythonBuild returns the build command of the project in dir that writes into out.
func pythonBuild(dir, out string) ([]string, error) {
	switch pythonTool(dir) {
	case "poetry":
		p, err := exec.LookPath("poetry")
		if err != nil {
			return nil, errors.New("this is a Poetry project ([tool.poetry] in pyproject.toml) and poetry is not on the PATH. Install Poetry 2")
		}
		return []string{p, "build", "--output", out}, nil
	case "uv":
		p, err := exec.LookPath("uv")
		if err != nil {
			return nil, errors.New("this is a uv project (uv.lock) and uv is not on the PATH. Install uv")
		}
		return []string{p, "build", "--out-dir", out}, nil
	}
	p, err := pythonCommand()
	if err != nil {
		return nil, err
	}
	return []string{p, "-m", "build", "--outdir", out}, nil
}

// pythonPreview is the command that "publish" runs, for "status".
func pythonPreview(dir string) string {
	switch pythonTool(dir) {
	case "poetry":
		return "poetry build, then the upload of each file"
	case "uv":
		return "uv build, then the upload of each file"
	}
	return "python -m build, then the upload of each file"
}

// distFile reports whether name is a wheel or an sdist.
func distFile(name string) bool {
	return strings.HasSuffix(name, ".whl") || strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".zip")
}

// runPython builds the project and uploads its files, or uploads the FILE arguments.
func runPython(j *job) error {
	files := j.files
	if len(files) == 0 {
		out := filepath.Join(j.tmp, "dist")
		if err := os.Mkdir(out, 0o700); err != nil {
			return err
		}
		argv, err := pythonBuild(j.p.dir, out)
		if err != nil {
			return err
		}
		// The build needs no token.
		if err := j.runTool(argv, childEnv()); err != nil {
			return err
		}
		entries, err := os.ReadDir(out)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if !e.IsDir() && distFile(e.Name()) {
				files = append(files, filepath.Join(out, e.Name()))
			}
		}
		if len(files) == 0 {
			return fmt.Errorf("%s wrote no wheel or sdist into %s", filepath.Base(argv[0]), out)
		}
	} else if err := openFiles(files); err != nil {
		return err
	}
	for _, f := range files {
		if !distFile(filepath.Base(f)) {
			return fmt.Errorf("%s is not a wheel (.whl) or an sdist (.tar.gz)", f)
		}
	}
	return j.uploadEach(files, (*job).uploadPython)
}

// uploadPython uploads the distribution file f to the legacy upload of the repo: one multipart
// POST, or for a large file the resumable upload routes, which the upload route then finishes.
// It returns the line that says what the server stored.
func (j *job) uploadPython(f string) (string, error) {
	st, err := os.Stat(f)
	if err != nil {
		return "", err
	}
	name := filepath.Base(f)
	route := "/pypi/" + url.PathEscape(j.p.Repo) + "/legacy/"
	var out struct {
		Project string `json:"project"`
		Version string `json:"version"`
	}
	if st.Size() >= resumableFrom {
		err = j.resumable(f, st.Size(), func(id string, q url.Values) error {
			q.Set("upload", id)
			q.Set("filename", name)
			_, err := j.c.call(j.ctx, request{method: http.MethodPost, path: route + "?" + q.Encode(), token: j.s.Token, out: &out, timeout: -1})
			return err
		})
	} else {
		err = j.postDist(f, name, st.Size(), route, &out)
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Uploaded %s (%s %s) to %s", name, out.Project, out.Version, j.p.Repo), nil
}

// postDist sends the file f of size bytes as the content of a legacy upload form to p.
func (j *job) postDist(f, name string, size int64, p string, out any) error {
	sum, err := fileSum(f)
	if err != nil {
		return err
	}
	in, err := os.Open(f)
	if err != nil {
		return err
	}
	defer in.Close()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for _, kv := range [][2]string{{":action", "file_upload"}, {"protocol_version", "1"}, {"sha256_digest", sum}} {
		if err := mw.WriteField(kv[0], kv[1]); err != nil {
			return err
		}
	}
	if _, err := mw.CreateFormFile("content", name); err != nil {
		return err
	}
	head := b.Len()
	if err := mw.Close(); err != nil {
		return err
	}
	all := b.Bytes()
	body := io.MultiReader(bytes.NewReader(all[:head]), in, bytes.NewReader(all[head:]))
	note("POST %s%s (%s, %s bytes)", j.c.id.base(), p, name, strconv.FormatInt(size, 10))
	_, err = j.c.call(j.ctx, request{method: http.MethodPost, path: p, body: body, size: int64(len(all)) + size,
		ctype: mw.FormDataContentType(), token: j.s.Token, out: out, timeout: -1})
	return err
}
