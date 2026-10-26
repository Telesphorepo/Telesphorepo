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
	"sync"
	"time"
)

// A file of resumableFrom bytes or more goes through the resumable upload routes in chunks of
// chunkSize: a network failure then sends one chunk again, not the whole file.
const (
	resumableFrom = 64 << 20
	chunkSize     = 16 << 20
	chunkTries    = 4
)

// uploadWorkers is how many files the CLI uploads at the same time.
const uploadWorkers = 4

// uploadEach runs up for each file, uploadWorkers files at a time, and prints the line that up
// returns for each file in the order of files. The first error cancels the uploads that run and
// starts no other; it is the error of uploadEach. up gets a job whose context ends then.
func (j *job) uploadEach(files []string, up func(j *job, f string) (string, error)) error {
	ctx, cancel := context.WithCancel(j.ctx)
	defer cancel()
	wj := *j
	wj.ctx = ctx
	var (
		mu      sync.Mutex
		first   error
		lines   = make([]string, len(files))
		done    = make([]bool, len(files))
		printed int
		wg      sync.WaitGroup
	)
	next := make(chan int)
	for range min(uploadWorkers, len(files)) {
		wg.Go(func() {
			for i := range next {
				line, err := up(&wj, files[i])
				mu.Lock()
				switch {
				case err != nil && first == nil:
					first = err
					cancel()
				case err == nil:
					lines[i], done[i] = line, true
					for printed < len(files) && done[printed] {
						fmt.Fprintln(os.Stderr, lines[printed])
						printed++
					}
				}
				mu.Unlock()
			}
		})
	}
feed:
	for i := range files {
		select {
		case next <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(next)
	wg.Wait()
	// After a failure, the files after it that were uploaded all the same.
	for i := printed; i < len(files); i++ {
		if done[i] {
			fmt.Fprintln(os.Stderr, lines[i])
		}
	}
	if first == nil {
		first = j.ctx.Err()
	}
	return first
}

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

// putFlags are the flags of a raw publish.
type putFlags struct {
	ifMatch string  // --if-match: "*" or the SHA-256 in lower-case hex; "": none
	ifNew   bool    // --if-new
	into    *string // --into: the folder of this run in place of the project's path
}

// set reports whether a flag is given.
func (pf putFlags) set() bool { return pf.ifMatch != "" || pf.ifNew || pf.into != nil }

// conditional reports whether the flags send a precondition.
func (pf putFlags) conditional() bool { return pf.ifMatch != "" || pf.ifNew }

// header is the precondition header of each PUT and finish.
func (pf putFlags) header() map[string]string {
	switch {
	case pf.ifMatch == "*":
		return map[string]string{"If-Match": "*"}
	case pf.ifMatch != "":
		return map[string]string{"If-Match": `"` + pf.ifMatch + `"`}
	case pf.ifNew:
		return map[string]string{"If-None-Match": "*"}
	}
	return nil
}

// parsePutFlags reads --if-match ETAG, --if-new and --into FOLDER of the publish arguments and
// returns them with the FILE arguments.
func parsePutFlags(args []string) (putFlags, []string, error) {
	var pf putFlags
	var files []string
	value := func(i *int, name string) (string, bool) {
		a := args[*i]
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			return v, true
		}
		if a == name && *i+1 < len(args) {
			*i++
			return args[*i], true
		}
		return "", false
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if v, ok := value(&i, "--if-match"); ok {
			etag := strings.ToLower(strings.Trim(strings.TrimSpace(v), `"`))
			if etag != "*" && (len(etag) != sha256.Size*2 || strings.Trim(etag, "0123456789abcdef") != "") {
				return pf, nil, fmt.Errorf(`--if-match takes * or the SHA-256 of the file on the server (its ETag), not %q`, v)
			}
			pf.ifMatch = etag
			continue
		}
		if v, ok := value(&i, "--into"); ok {
			pf.into = &v
			continue
		}
		switch {
		case a == "--if-new":
			pf.ifNew = true
		case strings.HasPrefix(a, "-") && a != "-":
			return pf, nil, usage("publish")
		default:
			files = append(files, a)
		}
	}
	if pf.ifMatch != "" && pf.ifNew {
		return pf, nil, errors.New("give --if-match or --if-new, not both")
	}
	if pf.ifMatch != "" && len(files) != 1 {
		return pf, nil, errors.New("--if-match names the file on the server that one FILE replaces: give one FILE")
	}
	return pf, files, nil
}

// conditionalPut is the change log entry of the preconditions of a raw publish.
const conditionalPut = "conditional-put"

// changedError is a raw publish whose precondition the server found false (412).
type changedError struct {
	dest string
	etag *string // the ETag of the file on the server now; nil: none
}

func (e *changedError) Error() string {
	now := "it holds no file now"
	if e.etag != nil {
		now = "now " + *e.etag
	}
	return fmt.Sprintf("%s changed on the server (%s); read it again and publish again.", e.dest, now)
}

// changed returns a *changedError for a 412 of the server on dest, else err.
func changed(dest string, err error) error {
	var pe *problemError
	if errors.As(err, &pe) && pe.p.Status == http.StatusPreconditionFailed {
		return &changedError{dest: dest, etag: pe.p.ETag}
	}
	return err
}

// uploadRaw uploads each FILE into the folder path of the project file of a raw repo (--into
// FOLDER in its place): a PUT of the file, or the resumable upload routes for a large file. The
// precondition of --if-match or --if-new goes on the PUT and on the finish of an upload.
func uploadRaw(j *job) error {
	if err := openFiles(j.files); err != nil {
		return err
	}
	if j.put.conditional() && j.cp != nil {
		if err := j.cp.needs(conditionalPut); err != nil {
			return err
		}
	}
	folder := j.p.Path
	if j.put.into != nil {
		folder = *j.put.into
	}
	folder = strings.Trim(strings.ReplaceAll(folder, `\`, "/"), "/")
	// The files upload at the same time, so two of them may not go to one path.
	from := map[string]string{}
	for _, f := range j.files {
		dest := path.Clean(path.Join(folder, filepath.Base(f)))
		if o, dup := from[dest]; dup {
			return fmt.Errorf("%s and %s both go to %s; upload them one at a time", o, f, dest)
		}
		from[dest] = f
	}
	return j.uploadEach(j.files, func(j *job, f string) (string, error) {
		dest := path.Clean(path.Join(folder, filepath.Base(f)))
		st, err := os.Stat(f)
		if err != nil {
			return "", err
		}
		if st.Size() >= resumableFrom {
			err = j.resumable(f, st.Size(), func(id string, q url.Values) error {
				q.Set("path", dest)
				_, err := j.c.call(j.ctx, request{method: http.MethodPut, path: "/api/repos/" + url.PathEscape(j.p.Repo) + "/uploads/" + url.PathEscape(id) + "?" + q.Encode(),
					token: j.s.Token, timeout: -1, header: j.put.header()})
				return err
			})
		} else {
			err = j.putFile(f, st.Size(), "/raw/"+url.PathEscape(j.p.Repo)+"/"+escapePath(dest), j.put.header(), nil)
		}
		if err != nil {
			return "", changed(dest, err)
		}
		return fmt.Sprintf("Uploaded %s to %s/raw/%s/%s", f, j.c.id.base(), j.p.Repo, dest), nil
	})
}

// uploadDebian uploads each FILE (a .deb) to the distribution and the component of the project
// file: a PUT of the file, or for a large file the resumable upload routes, which the upload
// route of the distribution then finishes. Uploads to one distribution may run at the same time:
// each marks the indexes of its units, and the rebuild queue builds them once after the last mark.
func uploadDebian(j *job) error {
	if j.p.Dist == "" || j.p.Component == "" {
		return fmt.Errorf(`%s needs dist and component for a Debian repo, for example dist = "stable" and component = "main"`, j.p.file)
	}
	if err := openFiles(j.files); err != nil {
		return err
	}
	p := "/debian/" + url.PathEscape(j.p.Repo) + "/upload/" + url.PathEscape(j.p.Dist) + "/" + url.PathEscape(j.p.Component)
	return j.uploadEach(j.files, func(j *job, f string) (string, error) {
		st, err := os.Stat(f)
		if err != nil {
			return "", err
		}
		var out struct {
			Package      string `json:"package"`
			Version      string `json:"version"`
			Architecture string `json:"architecture"`
			Filename     string `json:"filename"`
		}
		if st.Size() >= resumableFrom {
			err = j.resumable(f, st.Size(), func(id string, q url.Values) error {
				q.Set("upload", id)
				_, err := j.c.call(j.ctx, request{method: http.MethodPut, path: p + "?" + q.Encode(), token: j.s.Token, out: &out, timeout: -1})
				return err
			})
		} else {
			err = j.putFile(f, st.Size(), p, nil, &out)
		}
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Uploaded %s %s (%s) to %s/%s of %s", out.Package, out.Version, out.Architecture, j.p.Dist, j.p.Component, j.p.Repo), nil
	})
}

// putFile sends the file f of size bytes with PUT to p, with the extra headers header.
func (j *job) putFile(f string, size int64, p string, header map[string]string, out any) error {
	in, err := os.Open(f)
	if err != nil {
		return err
	}
	defer in.Close()
	fmt.Fprintf(os.Stderr, "PUT %s%s\n", j.c.id.base(), p)
	_, err = j.c.call(j.ctx, request{method: http.MethodPut, path: p, body: in, size: size,
		ctype: "application/octet-stream", token: j.s.Token, header: header, out: out, timeout: -1})
	return err
}

// resumable uploads the file f of size bytes through the resumable upload routes of the repo: it
// starts an upload, appends the file in chunks, and calls finish with the upload ID and the query
// sha256 and size of the file. A chunk that fails is sent again from the offset that the server
// holds. A failure deletes the upload.
func (j *job) resumable(f string, size int64, finish func(id string, q url.Values) error) error {
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
	if err := finish(up.ID, url.Values{"sha256": {sum}, "size": {strconv.FormatInt(size, 10)}}); err != nil {
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
