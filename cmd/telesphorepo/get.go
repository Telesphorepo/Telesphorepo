package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// get downloads each URL, a file URL of a server as the web UI shows it, into the current folder
// under its last path segment, or into the file of -o (one URL; "-" is stdout). A URL of a server
// that this computer has an account of reads with a device proof of the active account; any
// other URL reads anonymously.
func get(ctx context.Context, args []string) error {
	var dest string
	var urls []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-o" && i+1 < len(args) && dest == "":
			dest = args[i+1]
			i++
		case strings.HasPrefix(a, "-"):
			return usage("get")
		default:
			urls = append(urls, a)
		}
	}
	if len(urls) == 0 || (dest != "" && len(urls) > 1) {
		return usage("get")
	}
	g := &getter{proofs: map[string]*getAuth{}}
	failed := false
	for _, raw := range urls {
		if err := g.download(ctx, raw, dest); err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			report(err)
			failed = true
		}
	}
	if failed {
		return &exitError{code: 1}
	}
	return nil
}

// getter holds the credential of each server that a get talks to: one token exchange nonce per
// server and run.
type getter struct {
	proofs map[string]*getAuth // by the canonical URL of the server
}

// getAuth is the credential of the URLs of one server.
type getAuth struct {
	id     identity
	d      *deviceFile // nil: anonymous
	secret string      // the device proof
}

// download fetches raw into dest ("" for the last path segment of the URL in the current folder).
func (g *getter) download(ctx context.Context, raw, dest string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return fmt.Errorf("%q is not an http or https URL of a file", raw)
	}
	if dest == "" {
		if dest, err = fileName(u); err != nil {
			return err
		}
	}
	auth, err := g.auth(ctx, u)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent())
	if auth.secret != "" {
		req.Header.Set("Authorization", "Bearer "+auth.secret)
	}
	origin := u.Scheme + "://" + u.Host
	hc := &http.Client{CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		// The proof goes only to the server: a redirect to another origin (a presigned URL of
		// the store) gets no Authorization.
		if r.URL.Scheme+"://"+r.URL.Host != origin {
			r.Header.Del("Authorization")
		}
		return nil
	}}
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("cannot download %s: %w", u.Redacted(), unwrapURL(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return statusError(u, resp, auth)
	}
	p := newProgress(path.Base(u.Path), resp.ContentLength)
	if dest == "-" {
		_, err := io.Copy(os.Stdout, p.reader(resp.Body))
		p.end()
		if err != nil {
			return fmt.Errorf("cannot download %s: %w", u.Redacted(), err)
		}
		return nil
	}
	err = writeAtomic(dest, p.reader(resp.Body))
	p.end()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("cannot download %s: %w", u.Redacted(), err)
	}
	if resp.ContentLength >= 0 {
		done("Saved %s (%s)", dest, humanBytes(resp.ContentLength))
	} else {
		done("Saved %s", dest)
	}
	return nil
}

// auth returns the credential for URL u: the device of the active account of the server whose
// canonical URL u starts with (the longest one), when the server accepts it; else anonymous.
func (g *getter) auth(ctx context.Context, u *url.URL) (*getAuth, error) {
	ds, _, err := enrolledDevices()
	if err != nil {
		return nil, err
	}
	var d *deviceFile
	for _, o := range ds {
		if serves(o.id, u) && (d == nil || len(o.id.basePath) > len(d.id.basePath)) {
			d = o
		}
	}
	if d == nil {
		host, port, err := splitHostPort(u.Host, defaultPort(strings.ToLower(u.Scheme)))
		if err != nil {
			return nil, fmt.Errorf("%q is not an http or https URL of a file", u.Redacted())
		}
		return &getAuth{id: identity{scheme: strings.ToLower(u.Scheme), host: host, port: port, basePath: guessBase(u)}}, nil
	}
	if a := g.proofs[d.id.base()]; a != nil {
		return a, nil
	}
	a := &getAuth{id: d.id}
	g.proofs[d.id.base()] = a
	c := newClient(d.id)
	if err := attach(c, false).check(ctx, "get"); err != nil {
		return nil, err
	}
	nonce, _, err := c.challenge(ctx, d, true, "")
	switch {
	case refused(err):
		deviceRefused(c, d, err)
		return a, nil
	case err != nil:
		return nil, err
	}
	a.d, a.secret = d, exchangeSecret(d, nonce)
	return a, nil
}

// guessBase is the base path of the server of a file URL that no account serves: the path before
// its format segment (/<format>/<repo>/...), so the login hint names a server under a sub-path.
func guessBase(u *url.URL) string {
	segs := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	for i, seg := range segs {
		if i+2 >= len(segs) {
			break
		}
		for _, r := range rows {
			if r.format == seg {
				if i == 0 {
					return ""
				}
				return "/" + strings.Join(segs[:i], "/")
			}
		}
	}
	return ""
}

// serves reports whether u is a URL of the server id: the same scheme, host and port, and a path
// under its base path.
func serves(id identity, u *url.URL) bool {
	if strings.ToLower(u.Scheme) != id.scheme {
		return false
	}
	host, port, err := splitHostPort(u.Host, defaultPort(id.scheme))
	if err != nil || host != id.host || port != id.port {
		return false
	}
	return strings.HasPrefix(u.EscapedPath(), id.basePath+"/")
}

// fileName is the last path segment of u, the name of the file in the current folder.
func fileName(u *url.URL) (string, error) {
	name := path.Base(u.Path)
	if strings.HasSuffix(u.Path, "/") || name == "/" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) ||
		filepath.Base(name) != name {
		return "", fmt.Errorf("%s names no file. Name one with: telesphorepo get -o FILE URL", u.Redacted())
	}
	return name, nil
}

// statusError is the error of an answer that is not 2xx: the URL and the status, the reason of
// the server, and for 401 and 403 what the person does.
func statusError(u *url.URL, resp *http.Response, a *getAuth) error {
	msg := fmt.Sprintf("%s answered %s", u.Redacted(), resp.Status)
	if why := reason(resp); why != "" {
		msg += " (" + why + ")"
	}
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		return errors.New(msg)
	}
	if a.d != nil {
		return fmt.Errorf("%s. %s cannot read it on %s; ask an admin of the server for access", msg, a.d.label(), a.id.base())
	}
	return fmt.Errorf("%s. The file is not public; approve this computer for the server first. Run: telesphorepo login %s", msg, a.id.base())
}

// reason reads the reason of a refusal from a JSON answer: the detail or title of a problem
// document, or the {"error"} of a format route. It is "" for any other body.
func reason(resp *http.Response) string {
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if ct != "application/json" && ct != "application/problem+json" {
		return ""
	}
	var b struct {
		Error  string `json:"error"`
		Detail string `json:"detail"`
		Title  string `json:"title"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&b) != nil {
		return ""
	}
	for _, s := range []string{b.Detail, b.Error, b.Title} {
		if s != "" {
			return s
		}
	}
	return ""
}

// writeAtomic writes r to a new file next to dest and renames it over dest, so a failed download
// leaves no file that is half written. The new file takes the mode that the umask allows.
func writeAtomic(dest string, r io.Reader) error {
	dir, base := filepath.Split(dest)
	if dir == "" {
		dir = "."
	}
	var f *os.File
	var tmp string
	for {
		b := make([]byte, 6)
		_, _ = rand.Read(b)
		tmp = filepath.Join(dir, "."+base+"."+hex.EncodeToString(b)+".part")
		var err error
		f, err = os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// progress prints a one-line progress of a download to stderr on a terminal: the file, the bytes
// so far and the total. Without a terminal it prints nothing.
type progress struct {
	name  string
	total int64
	n     int64
	on    bool
	last  time.Time
	width int
}

func newProgress(name string, total int64) *progress {
	return &progress{name: name, total: total, on: isTerminal(os.Stderr)}
}

// reader counts what passes through r.
func (p *progress) reader(r io.Reader) io.Reader {
	if !p.on {
		return r
	}
	return progressReader{r, p}
}

type progressReader struct {
	r io.Reader
	p *progress
}

func (pr progressReader) Read(b []byte) (int, error) {
	n, err := pr.r.Read(b)
	pr.p.n += int64(n)
	if now := time.Now(); now.Sub(pr.p.last) >= 100*time.Millisecond {
		pr.p.last = now
		pr.p.draw()
	}
	return n, err
}

func (p *progress) draw() {
	s := p.name + "  " + humanBytes(p.n)
	if p.total >= 0 {
		s += " / " + humanBytes(p.total)
	}
	pad := ""
	if len(s) < p.width {
		pad = strings.Repeat(" ", p.width-len(s))
	}
	p.width = len(s)
	fmt.Fprint(os.Stderr, "\r"+s+pad)
}

// end clears the line of the progress.
func (p *progress) end() {
	if p.on && p.width > 0 {
		fmt.Fprint(os.Stderr, "\r"+strings.Repeat(" ", p.width)+"\r")
	}
}

// humanBytes is n in B, KB, MB or GB (powers of 1024), with one decimal above B.
func humanBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	f, unit := float64(n)/1024, "KB"
	for _, u := range []string{"MB", "GB", "TB"} {
		if f < 1024 {
			break
		}
		f, unit = f/1024, u
	}
	return fmt.Sprintf("%.1f %s", f, unit)
}
