package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/klauspost/compress/zstd"
	"github.com/xi2/xz"
)

// The [[when]] rules of telesphorepo.toml pick the repo by the version that a publish publishes:
//
//	repo = "releases"
//
//	[[when]]
//	version = "*-SNAPSHOT"
//	repo = "snapshots"
//
// The version comes from --version, else from each FILE argument (a .deb, .rpm, Arch package,
// .nupkg or wheel), else from the project of the folder. The CLI reads it only when the project
// file has rules.

// rule is one [[when]] rule: a version glob and what it overrides.
type rule struct {
	Version   string `toml:"version"`
	Repo      string `toml:"repo,omitempty"`
	Path      string `toml:"path,omitempty"`
	Dist      string `toml:"dist,omitempty"`
	Component string `toml:"component,omitempty"`
}

// globMatch reports whether s matches the whole pattern, in which * is any run of characters
// and everything else is literal.
func globMatch(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	last := parts[len(parts)-1]
	if !strings.HasPrefix(s, parts[0]) || len(s) < len(parts[0])+len(last) || !strings.HasSuffix(s, last) {
		return false
	}
	s = s[len(parts[0]) : len(s)-len(last)]
	for _, part := range parts[1 : len(parts)-1] {
		i := strings.Index(s, part)
		if i < 0 {
			return false
		}
		s = s[i+len(part):]
	}
	return true
}

// route returns the project that version v publishes to: p with the overrides of the first rule
// that matches v, and the line that says so.
func (p *project) route(v string) (*project, string) {
	for _, w := range p.When {
		if !globMatch(w.Version, v) {
			continue
		}
		q := *p
		var also []string
		for _, o := range []struct {
			key, val string
			to       *string
		}{{"repo", w.Repo, &q.Repo}, {"path", w.Path, &q.Path}, {"dist", w.Dist, &q.Dist}, {"component", w.Component, &q.Component}} {
			if o.val != "" {
				*o.to = o.val
				if o.key != "repo" {
					also = append(also, o.key+" "+o.val)
				}
			}
		}
		to := q.Repo
		if len(also) > 0 {
			to += " (" + strings.Join(also, ", ") + ")"
		}
		return &q, fmt.Sprintf("%s matches %s: publishing to %s", v, w.Version, to)
	}
	return p, fmt.Sprintf("%s matches no rule: publishing to %s", v, p.Repo)
}

// target is one repo of a publish and the FILE arguments that go to it.
type target struct {
	p     *project
	files []string
}

// routes splits a publish by the rules of p: one target for the project, or one per repo of the
// FILE arguments. version is --version ("": none). It prints the decision of each version, and
// stops when a version that the rules need is missing.
func routes(ctx context.Context, p *project, files []string, version string) ([]target, error) {
	if len(p.When) == 0 {
		return []target{{p, files}}, nil
	}
	if len(files) == 0 || version != "" {
		v := version
		if v == "" {
			var where string
			var err error
			if v, where, err = projectVersion(ctx, p); err != nil {
				return nil, err
			}
			if v == "" {
				return nil, noVersion(p, where)
			}
		}
		q, line := p.route(v)
		step("%s", line)
		return []target{{q, files}}, nil
	}
	var out []target
	at := map[string]int{}
	for _, f := range files {
		v, err := fileVersion(f)
		if err != nil {
			return nil, err
		}
		if v == "" {
			return nil, noVersion(p, f+", which holds no version of its own")
		}
		q, line := p.route(v)
		step("%s: %s", filepath.Base(f), line)
		key := q.Repo + "\x00" + q.Path + "\x00" + q.Dist + "\x00" + q.Component
		if i, ok := at[key]; ok {
			out[i].files = append(out[i].files, f)
			continue
		}
		at[key] = len(out)
		out = append(out, target{q, []string{f}})
	}
	return out, nil
}

// noVersion is the error of a publish whose rules need a version that is not there.
func noVersion(p *project, where string) error {
	return fmt.Errorf("the [[when]] rules of %s pick the repo by version, and no version was found: looked in %s. Give the version with: telesphorepo publish --version VERSION", p.file, where)
}

// projectVersion reads the version of the project in the folder of p, of the kind that p names
// or the one kind whose marker is there. It returns "" and where it looked when the project has
// no version.
func projectVersion(ctx context.Context, p *project) (v, where string, err error) {
	var found []row
	for _, r := range rows {
		if len(r.markers) > 0 && (p.Kind == "" || p.Kind == r.kind) && r.marker(p.dir) != "" {
			found = append(found, r)
		}
	}
	switch len(found) {
	case 0:
		return "", p.dir + ", which has no project marker", nil
	case 1:
	default:
		kinds := make([]string, len(found))
		for i, r := range found {
			kinds[i] = fmt.Sprintf("kind = %q", r.kind)
		}
		return "", "", fmt.Errorf("%s has projects of several kinds, and the [[when]] rules of %s need the version of one. Add one line to %s: %s",
			p.dir, p.file, projectFileName, strings.Join(kinds, " or "))
	}
	dir := p.dir
	switch r := found[0]; r.kind {
	case "gradle":
		return toolVersion(ctx, dir, "gradlew", "gradle", []string{"-q", "properties", "--property", "version"}, func(out string) string {
			for _, line := range strings.Split(out, "\n") {
				if v, ok := strings.CutPrefix(strings.TrimSpace(line), "version: "); ok {
					if v == "unspecified" {
						return ""
					}
					return v
				}
			}
			return ""
		})
	case "maven":
		return toolVersion(ctx, dir, "mvnw", "mvn", []string{"-q", "help:evaluate", "-Dexpression=project.version", "-DforceStdout"}, func(out string) string {
			lines := strings.Split(strings.TrimSpace(out), "\n")
			return strings.TrimSpace(lines[len(lines)-1])
		})
	case "npm":
		var pkg struct {
			Version string `json:"version"`
		}
		b, err := os.ReadFile(filepath.Join(dir, "package.json"))
		if err == nil {
			err = json.Unmarshal(b, &pkg)
		}
		return pkg.Version, `"version" in package.json`, err
	case "cargo":
		var c struct {
			Package struct {
				Version any `toml:"version"`
			} `toml:"package"`
		}
		_, err := toml.DecodeFile(filepath.Join(dir, "Cargo.toml"), &c)
		s, _ := c.Package.Version.(string)
		return s, "version of [package] in Cargo.toml", err
	case "python":
		var py struct {
			Project struct {
				Version string `toml:"version"`
			} `toml:"project"`
			Tool struct {
				Poetry struct {
					Version string `toml:"version"`
				} `toml:"poetry"`
			} `toml:"tool"`
		}
		where := "version of [project] and of [tool.poetry] in pyproject.toml"
		f := filepath.Join(dir, "pyproject.toml")
		if !fileExists(f) {
			return "", "pyproject.toml, which this project does not have", nil
		}
		if _, err := toml.DecodeFile(f, &py); err != nil {
			return "", where, err
		}
		if py.Project.Version != "" {
			return py.Project.Version, where, nil
		}
		return py.Tool.Poetry.Version, where, nil
	case "helm":
		b, err := os.ReadFile(filepath.Join(dir, "Chart.yaml"))
		for _, line := range strings.Split(string(b), "\n") {
			if s, ok := strings.CutPrefix(line, "version:"); ok {
				return strings.Trim(strings.TrimSpace(s), `"'`), "version in Chart.yaml", err
			}
		}
		return "", "version in Chart.yaml", err
	case "dotnet":
		m := r.marker(dir)
		where := "<Version> in " + m
		if ext := filepath.Ext(m); ext != ".csproj" && ext != ".fsproj" {
			return "", m + ", which holds no version", nil
		}
		b, err := os.ReadFile(filepath.Join(dir, m))
		if s := xmlVersion.FindSubmatch(b); s != nil {
			return strings.TrimSpace(string(s[1])), where, err
		}
		return "", where, err
	}
	return "", p.dir, nil
}

var xmlVersion = regexp.MustCompile(`<Version>([^<]+)</Version>`)

// toolVersion runs the build tool of the project in dir (its wrapper when it has one) with args
// and reads the version from what it prints with parse.
func toolVersion(ctx context.Context, dir, wrapper, name string, args []string, parse func(string) string) (string, string, error) {
	tool, err := findTool(dir, wrapper, name)
	if err != nil {
		return "", "", err
	}
	argv := append(tool, args...)
	line := strings.Join(append([]string{filepath.Base(tool[len(tool)-1])}, args...), " ")
	note("$ %s", line)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = childEnv()
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("cannot read the version of the project: %s failed: %w", line, err)
	}
	return parse(strings.ReplaceAll(string(out), "\r", "")), "the output of " + line, nil
}

// fileVersion reads the version of a package file: a .deb, an .rpm, an Arch package, a .nupkg
// or a wheel. It returns "" for any other file.
func fileVersion(f string) (string, error) {
	name := strings.ToLower(filepath.Base(f))
	var read func(io.ReaderAt, int64) (string, error)
	switch {
	case strings.HasSuffix(name, ".deb"):
		read = debVersion
	case strings.HasSuffix(name, ".rpm"):
		read = rpmVersion
	case strings.Contains(name, ".pkg.tar."):
		read = archVersion
	case strings.HasSuffix(name, ".nupkg"):
		read = nupkgVersion
	case strings.HasSuffix(name, ".whl"):
		// {name}-{version}(-{build})?-{python}-{abi}-{platform}.whl
		if parts := strings.Split(filepath.Base(f), "-"); len(parts) >= 5 {
			return parts[1], nil
		}
		return "", nil
	default:
		return "", nil
	}
	in, err := os.Open(f)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", f, unwrapPath(err))
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return "", err
	}
	v, err := read(in, st.Size())
	if err != nil {
		return "", fmt.Errorf("cannot read the version of %s: %w", f, err)
	}
	return v, nil
}

// debVersion reads Version of the control file of a .deb: an ar archive whose member
// control.tar(.gz, .xz or .zst) holds ./control.
func debVersion(r io.ReaderAt, size int64) (string, error) {
	magic := make([]byte, 8)
	if _, err := r.ReadAt(magic, 0); err != nil || string(magic) != "!<arch>\n" {
		return "", errors.New("not a .deb (ar) archive")
	}
	hdr := make([]byte, 60)
	for off := int64(8); off+60 <= size; {
		if _, err := r.ReadAt(hdr, off); err != nil {
			return "", err
		}
		name := strings.TrimRight(string(hdr[:16]), " /")
		n, err := strconv.ParseInt(strings.TrimSpace(string(hdr[48:58])), 10, 64)
		if err != nil || n < 0 {
			return "", errors.New("malformed ar archive")
		}
		off += 60
		if ext, ok := strings.CutPrefix(name, "control.tar"); ok {
			dec, closeDec, err := decompress(strings.TrimPrefix(ext, "."), io.NewSectionReader(r, off, n))
			if err != nil {
				return "", err
			}
			defer closeDec()
			return controlField(dec, "control", "Version:")
		}
		off += n + n%2
	}
	return "", errors.New("the .deb has no control.tar")
}

// archVersion reads pkgver of the .PKGINFO of an Arch package: [epoch:]pkgver-pkgrel.
func archVersion(r io.ReaderAt, size int64) (string, error) {
	head := make([]byte, 6)
	if _, err := r.ReadAt(head, 0); err != nil {
		return "", err
	}
	ext := "gz"
	switch {
	case bytes.HasPrefix(head, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		ext = "zst"
	case bytes.HasPrefix(head, []byte{0xfd, '7', 'z', 'X', 'Z', 0}):
		ext = "xz"
	}
	dec, closeDec, err := decompress(ext, io.NewSectionReader(r, 0, size))
	if err != nil {
		return "", err
	}
	defer closeDec()
	return controlField(dec, ".PKGINFO", "pkgver = ")
}

// decompress returns the stream r decompressed by ext ("gz", "xz", "zst" or "" for none) and
// its close.
func decompress(ext string, r io.Reader) (io.Reader, func(), error) {
	switch ext {
	case "":
		return r, func() {}, nil
	case "gz":
		g, err := gzip.NewReader(r)
		if err != nil {
			return nil, nil, err
		}
		return g, func() { g.Close() }, nil
	case "xz":
		x, err := xz.NewReader(r, 0)
		return x, func() {}, err
	case "zst":
		z, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true))
		if err != nil {
			return nil, nil, err
		}
		return z, z.Close, nil
	}
	return nil, nil, fmt.Errorf("unknown compression %q", ext)
}

// controlField finds the file name in the tar stream r and returns the value of its first line
// that starts with prefix.
func controlField(r io.Reader, name, prefix string) (string, error) {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err != nil {
			return "", fmt.Errorf("no %s in the package: %w", name, err)
		}
		if strings.TrimPrefix(h.Name, "./") != name {
			continue
		}
		sc := bufio.NewScanner(tr)
		for sc.Scan() {
			if v, ok := strings.CutPrefix(sc.Text(), prefix); ok {
				return strings.TrimSpace(v), nil
			}
		}
		return "", sc.Err()
	}
}

// rpmVersion reads [epoch:]version-release of an .rpm: a 96-byte lead, the signature header
// padded to 8 bytes, then the header, whose tags 1001, 1002 and 1003 are the version, the
// release and the epoch.
func rpmVersion(r io.ReaderAt, size int64) (string, error) {
	off := int64(96)
	intro := make([]byte, 16)
	header := func() ([]byte, []byte, error) {
		if _, err := r.ReadAt(intro, off); err != nil || !bytes.Equal(intro[:4], []byte{0x8e, 0xad, 0xe8, 0x01}) {
			return nil, nil, errors.New("not an RPM package")
		}
		il, dl := int64(binary.BigEndian.Uint32(intro[8:])), int64(binary.BigEndian.Uint32(intro[12:]))
		if 16+il*16+dl > size-off {
			return nil, nil, errors.New("not an RPM package")
		}
		b := make([]byte, il*16+dl)
		if _, err := r.ReadAt(b, off+16); err != nil {
			return nil, nil, err
		}
		off += 16 + il*16 + dl
		return b[:il*16], b[il*16:], nil
	}
	if _, _, err := header(); err != nil {
		return "", err
	}
	off += (8 - off%8) % 8
	index, data, err := header()
	if err != nil {
		return "", err
	}
	tags := map[uint32]string{}
	for e := index; len(e) >= 16; e = e[16:] {
		tag, typ, at := binary.BigEndian.Uint32(e), binary.BigEndian.Uint32(e[4:]), binary.BigEndian.Uint32(e[8:])
		if tag < 1001 || tag > 1003 || int64(at) >= int64(len(data)) {
			continue
		}
		switch d := data[at:]; {
		case typ == 6:
			s, _, _ := bytes.Cut(d, []byte{0})
			tags[tag] = string(s)
		case typ == 4 && len(d) >= 4:
			tags[tag] = strconv.FormatUint(uint64(binary.BigEndian.Uint32(d)), 10)
		}
	}
	if tags[1001] == "" {
		return "", errors.New("the header has no version")
	}
	v := tags[1001] + "-" + tags[1002]
	if e := tags[1003]; e != "" && e != "0" {
		v = e + ":" + v
	}
	return v, nil
}

// nupkgVersion reads <version> of the .nuspec at the root of a .nupkg.
func nupkgVersion(r io.ReaderAt, size int64) (string, error) {
	z, err := zip.NewReader(r, size)
	if err != nil {
		return "", err
	}
	for _, f := range z.File {
		if strings.Contains(f.Name, "/") || !strings.HasSuffix(strings.ToLower(f.Name), ".nuspec") {
			continue
		}
		in, err := f.Open()
		if err != nil {
			return "", err
		}
		defer in.Close()
		var spec struct {
			Version string `xml:"metadata>version"`
		}
		if err := xml.NewDecoder(in).Decode(&spec); err != nil {
			return "", err
		}
		return strings.TrimSpace(spec.Version), nil
	}
	return "", errors.New("the package has no .nuspec")
}
