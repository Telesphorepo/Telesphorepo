package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// A file of resumableFrom bytes or more goes through the resumable upload routes in chunks of
// chunkSize: a network failure then sends one chunk again, not the whole file.
const (
	resumableFrom = 64 << 20
	chunkSize     = 16 << 20
	chunkTries    = 4
)

// escapePath escapes each segment of the repo path p for a URL.
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// openFiles checks that each FILE argument is a regular file.
func openFiles(files []string) error {
	if len(files) == 0 {
		return errors.New("name the files to upload, for example: telesphorepo publish dist/app.zip")
	}
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil {
			return fmt.Errorf("cannot read %s: %w", f, unwrapPath(err))
		}
		if !st.Mode().IsRegular() {
			return fmt.Errorf("%s is not a file", f)
		}
	}
	return nil
}

// unwrapPath drops the path of a *fs.PathError, which the message names already.
func unwrapPath(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// uploadRaw uploads each FILE into the folder path of the project file of a raw repo: a PUT of the
// file, or the resumable upload routes for a large file.
func uploadRaw(j *job) error {
	if err := openFiles(j.files); err != nil {
		return err
	}
	folder := strings.Trim(strings.ReplaceAll(j.p.Path, `\`, "/"), "/")
	for _, f := range j.files {
		dest := path.Clean(path.Join(folder, filepath.Base(f)))
		st, err := os.Stat(f)
		if err != nil {
			return err
		}
		if st.Size() >= resumableFrom {
			err = j.resumable(f, st.Size(), dest)
		} else {
			err = j.putFile(f, st.Size(), "/raw/"+url.PathEscape(j.p.Repo)+"/"+escapePath(dest), nil)
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Uploaded %s to %s/raw/%s/%s\n", f, j.c.id.base(), j.p.Repo, dest)
	}
	return nil
}

// uploadDebian uploads each FILE (a .deb) to the distribution and the component of the project
// file.
func uploadDebian(j *job) error {
	if j.p.Dist == "" || j.p.Component == "" {
		return fmt.Errorf(`%s needs dist and component for a Debian repo, for example dist = "stable" and component = "main"`, j.p.file)
	}
	if err := openFiles(j.files); err != nil {
		return err
	}
	for _, f := range j.files {
		st, err := os.Stat(f)
		if err != nil {
			return err
		}
		var out struct {
			Package      string `json:"package"`
			Version      string `json:"version"`
			Architecture string `json:"architecture"`
			Filename     string `json:"filename"`
		}
		p := "/debian/" + url.PathEscape(j.p.Repo) + "/upload/" + url.PathEscape(j.p.Dist) + "/" + url.PathEscape(j.p.Component)
		if err := j.putFile(f, st.Size(), p, &out); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Uploaded %s %s (%s) to %s/%s of %s\n", out.Package, out.Version, out.Architecture, j.p.Dist, j.p.Component, j.p.Repo)
	}
	return nil
}

// putFile sends the file f of size bytes with PUT to p.
func (j *job) putFile(f string, size int64, p string, out any) error {
	in, err := os.Open(f)
	if err != nil {
		return err
	}
	defer in.Close()
	fmt.Fprintf(os.Stderr, "PUT %s%s\n", j.c.id.base(), p)
	_, err = j.c.call(j.ctx, request{method: http.MethodPut, path: p, body: in, size: size,
		ctype: "application/octet-stream", token: j.s.Token, out: out, timeout: -1})
	return err
}

// resumable uploads the file f of size bytes to dest of the repo through the resumable upload
// routes: it starts an upload, appends the file in chunks, and finishes it with the SHA-256 of
// the file. A chunk that fails is sent again from the offset that the server holds. A failure
// deletes the upload.
func (j *job) resumable(f string, size int64, dest string) error {
	sum, err := fileSum(f)
	if err != nil {
		return err
	}
	in, err := os.Open(f)
	if err != nil {
		return err
	}
	defer in.Close()
	base := "/api/repos/" + url.PathEscape(j.p.Repo) + "/uploads"
	var up struct {
		ID string `json:"id"`
	}
	if _, err := j.c.call(j.ctx, request{method: http.MethodPost, path: base, token: j.s.Token, out: &up}); err != nil {
		return err
	}
	at := base + "/" + url.PathEscape(up.ID)
	fmt.Fprintf(os.Stderr, "Uploading %s (%d bytes) in chunks of %d MiB\n", f, size, chunkSize>>20)
	done := false
	defer func() {
		if !done {
			// Also after Ctrl-C: the upload and its bytes go at once.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _ = j.c.call(ctx, request{method: http.MethodDelete, path: at, token: j.s.Token})
		}
	}()
	var offset int64
	tries := 0
	for offset < size {
		n := min(int64(chunkSize), size-offset)
		var got struct {
			Received int64 `json:"received"`
		}
		_, err := j.c.call(j.ctx, request{method: http.MethodPatch, path: at, body: io.NewSectionReader(in, offset, n), size: n,
			ctype: "application/octet-stream", token: j.s.Token, out: &got, timeout: -1,
			header: map[string]string{"Content-Range": fmt.Sprintf("bytes %d-%d/*", offset, offset+n-1)}})
		var pe *problemError
		switch {
		case err == nil:
			offset, tries = got.Received, 0
			continue
		case j.ctx.Err() != nil:
			return j.ctx.Err()
		case errors.As(err, &pe) && pe.p.Received != nil:
			offset = *pe.p.Received
		case refused(err):
			return err
		}
		if tries++; tries >= chunkTries {
			return err
		}
		fmt.Fprintf(os.Stderr, "The chunk at byte %d failed (%v); sending it again.\n", offset, err)
	}
	q := url.Values{"path": {dest}, "sha256": {sum}, "size": {strconv.FormatInt(size, 10)}}
	if _, err := j.c.call(j.ctx, request{method: http.MethodPut, path: at + "?" + q.Encode(), token: j.s.Token, timeout: -1}); err != nil {
		return err
	}
	done = true
	return nil
}

// fileSum is the SHA-256 of the file f in lower-case hex.
func fileSum(f string) (string, error) {
	in, err := os.Open(f)
	if err != nil {
		return "", err
	}
	defer in.Close()
	h := sha256.New()
	if _, err := io.Copy(h, in); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
