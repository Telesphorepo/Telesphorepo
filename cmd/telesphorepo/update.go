package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/changelog"
)

// updated is true once this process installed another CLI.
var updated bool

// cliName is the file name of the CLI binary on this OS.
func cliName() string {
	if runtime.GOOS == "windows" {
		return "telesphorepo.exe"
	}
	return "telesphorepo"
}

// helperFile is the file name of the credential helper name on this OS.
func helperFile() string {
	if runtime.GOOS == "windows" {
		return dockerHelperName + ".exe"
	}
	return dockerHelperName
}

// archiveName is the name of the CLI archive of this OS and CPU on the server.
func archiveName() string {
	if runtime.GOOS == "windows" {
		return "telesphorepo_windows_" + runtime.GOARCH + ".zip"
	}
	return "telesphorepo_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
}

// installLine is the install line of the server id for this OS.
func installLine(id identity) string {
	if runtime.GOOS == "windows" {
		return "& ([scriptblock]::Create((irm " + id.base() + "/install.ps1)))"
	}
	return "curl -fsSL " + id.base() + "/install.sh | sh"
}

// removeOld deletes the .old files that an update or the install script left next to the CLI on
// Windows: a running program cannot be replaced there, only renamed.
func removeOld() {
	if runtime.GOOS != "windows" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	dir := filepath.Dir(exe)
	for _, n := range []string{cliName(), helperFile()} {
		_ = os.Remove(filepath.Join(dir, n+".old"))
	}
}

// updateCmd installs the CLI of the server: the server of the project of the current folder, else
// SERVER, else the one enrolled server.
func updateCmd(ctx context.Context, args []string) error {
	if len(args) > 1 || (len(args) == 1 && strings.HasPrefix(args[0], "-")) {
		return usage("update")
	}
	var id identity
	p, err := findProject(".")
	switch {
	case err == nil && p != nil:
		id = p.id
	case len(args) == 1:
		if id, err = parseIdentity(args[0]); err != nil {
			return err
		}
	default:
		if id, err = pickServer("update"); err != nil {
			return err
		}
	}
	return update(ctx, id)
}

// update installs the CLI of the server id in place of the running binary, after it checks the
// SHA-256 of the archive. It never installs an older CLI: a newer CLI works with an older server.
func update(ctx context.Context, id identity) error {
	c := newClient(id)
	sums, err := fetch(ctx, c, "/cli/SHA256SUMS")
	if err != nil {
		return err
	}
	serverVersion := c.serverVersion()
	if s, err1 := changelog.ParseVersion(serverVersion); err1 == nil {
		if mine, err2 := changelog.ParseVersion(version); err2 == nil {
			switch mine.Compare(s) {
			case 1:
				fmt.Fprintf(os.Stderr, "This CLI (%s) is newer than the CLI of %s (%s). A newer CLI works with an older server, so nothing changed.\n",
					version, id.base(), serverVersion)
				return nil
			case 0:
				fmt.Fprintf(os.Stderr, "This CLI is the CLI of %s (%s).\n", id.base(), version)
				return nil
			}
		}
	}
	name := archiveName()
	want := sumOf(sums, name)
	if want == "" {
		return fmt.Errorf("%s has no CLI for %s/%s", id.base(), runtime.GOOS, runtime.GOARCH)
	}
	archive, err := fetch(ctx, c, "/cli/"+name)
	if err != nil {
		return err
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("the SHA-256 of %s is %s, but the server names %s; nothing changed", name, hex.EncodeToString(got[:]), want)
	}
	bin, err := extractCLI(name, archive)
	if err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	if filepath.Base(exe) != cliName() {
		exe = filepath.Join(dir, cliName())
	}
	tmp := filepath.Join(dir, "."+cliName()+".new")
	_ = os.Remove(tmp)
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Cannot write into %s (%v). Install the CLI of the server with:\n  %s\n", dir, unwrapPath(err), installLine(id))
		return &exitError{code: 1}
	}
	if runtime.GOOS == "windows" {
		// A running program cannot be replaced on Windows, but it can be renamed. The next start
		// deletes the .old file.
		old := exe + ".old"
		_ = os.Remove(old)
		if err := os.Rename(exe, old); err != nil && !errors.Is(err, fs.ErrNotExist) {
			os.Remove(tmp)
			return fmt.Errorf("cannot rename %s: %w", exe, unwrapPath(err))
		}
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("cannot replace %s: %w", exe, unwrapPath(err))
	}
	updated = true
	if err := relinkHelper(exe); err != nil {
		fmt.Fprintf(os.Stderr, "Cannot make %s again (%v). Docker pushes need it: run the install line\n  %s\n", helperFile(), err, installLine(id))
	}
	fmt.Fprintf(os.Stderr, "Installed the CLI of %s (%s) in %s.\n", id.base(), serverVersion, exe)
	return nil
}

// fetch GETs path from the server with no time limit beyond 5 minutes and returns the body.
func fetch(ctx context.Context, c *client, path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var buf bytes.Buffer
	_, err := c.download(ctx, path, &buf)
	return buf.Bytes(), err
}

// download GETs path and copies the body of a 200 answer into w.
func (c *client) download(ctx context.Context, path string, w io.Writer) (http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.id.base()+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent())
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach %s: %w", c.id.base(), unwrapURL(err))
	}
	defer resp.Body.Close()
	if v := resp.Header.Get(versionHeader); v != "" {
		c.mu.Lock()
		c.version = v
		c.mu.Unlock()
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return resp.Header, c.answerError(resp, b)
	}
	if _, err := io.Copy(w, io.LimitReader(resp.Body, 256<<20)); err != nil {
		return resp.Header, fmt.Errorf("cannot download %s%s: %w", c.id.base(), path, err)
	}
	return resp.Header, nil
}

// sumOf returns the SHA-256 of name in a SHA256SUMS file, or "".
func sumOf(sums []byte, name string) string {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		sum, n, ok := strings.Cut(strings.TrimSpace(sc.Text()), "  ")
		if ok && strings.TrimSpace(n) == name && len(sum) == 64 {
			return strings.ToLower(sum)
		}
	}
	return ""
}

// extractCLI returns the CLI binary of a .tar.gz or .zip archive.
func extractCLI(name string, archive []byte) ([]byte, error) {
	want := cliName()
	if strings.HasSuffix(name, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, fmt.Errorf("%s is not a valid zip: %w", name, err)
		}
		for _, f := range zr.File {
			if f.Name == want {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(io.LimitReader(rc, 256<<20))
			}
		}
		return nil, fmt.Errorf("%s does not hold %s", name, want)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("%s is not a valid .tar.gz: %w", name, err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("%s does not hold %s", name, want)
		}
		if err != nil {
			return nil, fmt.Errorf("%s is not a valid .tar.gz: %w", name, err)
		}
		if h.Name == want && h.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(tr, 256<<20))
		}
	}
}

// relinkHelper makes docker-credential-telesphorepo next to exe again: a symbolic link on Linux
// and macOS, a hard link on Windows (a hard link keeps the old file), a copy when a link fails.
// A symbolic link that names the CLI stays as it is.
func relinkHelper(exe string) error {
	helper := filepath.Join(filepath.Dir(exe), helperFile())
	if runtime.GOOS != "windows" {
		if target, err := os.Readlink(helper); err == nil && (target == filepath.Base(exe) || target == exe) {
			return nil
		}
	}
	if err := os.Remove(helper); err != nil && !errors.Is(err, fs.ErrNotExist) {
		old := helper + ".old"
		_ = os.Remove(old)
		if err := os.Rename(helper, old); err != nil {
			return err
		}
	}
	return linkOrCopy(exe, helper)
}

// linkOrCopy makes dst a link to src (a symbolic link to its base name on Linux and macOS, a hard
// link on Windows), or a copy when a link fails.
func linkOrCopy(src, dst string) error {
	var err error
	if runtime.GOOS == "windows" {
		err = os.Link(src, dst)
	} else if filepath.Dir(src) == filepath.Dir(dst) {
		err = os.Symlink(filepath.Base(src), dst)
	} else {
		err = os.Symlink(src, dst)
	}
	if err == nil {
		return nil
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o755)
}
