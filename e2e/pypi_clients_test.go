//go:build e2e || import || clients

package e2e

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
)

// The Python clients of the PyPI tests: the official python image (pip, and twine, build and
// poetry installed with pip from pypi.org) and uv's image with Python.
var (
	pythonImage = floatingImage("docker.io/library/python:3-slim")
	uvImage     = floatingImage("ghcr.io/astral-sh/uv:python3.13-bookworm-slim")
)

// pyNames returns a fresh project name and its import name, e.g. "tr-e2e-1a2b" and
// "tr_e2e_1a2b".
func pyNames() (string, string) {
	n := "tr-e2e-" + randHex(4)
	return n, strings.ReplaceAll(n, "-", "_")
}

// pyIndex is the simple index URL of repo on s for a client container, with user and pass in
// the URL ("" for none). On an http server pip needs PIP_TRUSTED_HOST=s.Alias: pip ignores an
// http index on any host but localhost (SECURE_ORIGINS in pip/_internal/network/session.py).
func pyIndex(s *Server, repo, user, pass string) string {
	u, err := url.Parse(s.InternalURL)
	if err != nil {
		panic(err)
	}
	if user != "" {
		u.User = url.UserPassword(user, pass)
	}
	return u.JoinPath("pypi", repo, "simple").String() + "/"
}

// pySetuptoolsProject is a project at dir that python -m build, uv build and twine take: module
// mod with MSG = msg.
func pySetuptoolsProject(dir, name, mod, version, msg string) []ContainerFile {
	return []ContainerFile{
		{Path: dir + "/pyproject.toml", Content: fmt.Sprintf(`[build-system]
requires = ["setuptools>=68"]
build-backend = "setuptools.build_meta"

[project]
name = %q
version = %q
description = "e2e package"
requires-python = ">=3.8"

[tool.setuptools]
packages = [%q]
`, name, version, mod)},
		{Path: dir + "/" + mod + "/__init__.py", Content: fmt.Sprintf("MSG = %q\n", msg)},
	}
}

// pyPoetryProject is a Poetry project at dir ([tool.poetry]) with module mod.
func pyPoetryProject(dir, name, mod, version, msg string) []ContainerFile {
	return []ContainerFile{
		{Path: dir + "/pyproject.toml", Content: fmt.Sprintf(`[tool.poetry]
name = %q
version = %q
description = "e2e package"
authors = ["e2e <e2e@example.com>"]
packages = [{ include = %q }]

[tool.poetry.dependencies]
python = ">=3.9"

[build-system]
requires = ["poetry-core>=2.0"]
build-backend = "poetry.core.masonry.api"
`, name, version, mod)},
		{Path: dir + "/" + mod + "/__init__.py", Content: fmt.Sprintf("MSG = %q\n", msg)},
	}
}

// pyWheel builds a pure-Python wheel of project name at version: module mod with MSG = msg, its
// METADATA, WHEEL and RECORD. It returns the file name and the bytes.
func pyWheel(name, mod, version, msg string) (string, []byte) {
	dist := strings.ReplaceAll(name, "-", "_")
	info := dist + "-" + version + ".dist-info/"
	files := [][2]string{
		{mod + "/__init__.py", fmt.Sprintf("MSG = %q\n", msg)},
		{info + "METADATA", "Metadata-Version: 2.1\nName: " + name + "\nVersion: " + version + "\nSummary: e2e wheel\nRequires-Python: >=3.8\n\ne2e wheel\n"},
		{info + "WHEEL", "Wheel-Version: 1.0\nGenerator: e2e\nRoot-Is-Purelib: true\nTag: py3-none-any\n\n"},
	}
	var record strings.Builder
	for _, f := range files {
		sum := sha256.Sum256([]byte(f[1]))
		fmt.Fprintf(&record, "%s,sha256=%s,%d\n", f[0], base64.RawURLEncoding.EncodeToString(sum[:]), len(f[1]))
	}
	record.WriteString(info + "RECORD,,\n")
	files = append(files, [2]string{info + "RECORD", record.String()})
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f[0])
		if err != nil {
			panic(err)
		}
		w.Write([]byte(f[1]))
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	return dist + "-" + version + "-py3-none-any.whl", buf.Bytes()
}
