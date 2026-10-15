// Package nexus mirrors hosted repositories of a Sonatype Nexus Repository 3 server until an
// admin cuts over (spec §12). It holds the Nexus REST client, the sources, the sync scheduler
// and the sync runner. It names no format: it offers each Nexus asset to the format plugin of
// the target repo through format.Importer.
package nexus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/signing"
)

// maxJSONBytes caps a Nexus JSON response.
const maxJSONBytes = 64 << 20

// maxProxyTTL caps a proxy time taken from Nexus (the repos service takes at most 30 days).
const maxProxyTTL = 30 * 24 * time.Hour

// listTimeout bounds one repository list or asset page request.
const listTimeout = 2 * time.Minute

// transport is shared by every client: it bounds the wait for response headers, not the body,
// so large downloads are not cut off.
var transport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = time.Minute
	t.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	return t
}()

// Credentials are the Nexus user that a source authenticates as. They are never logged and never
// returned by the API.
type Credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Client calls the Nexus REST API with HTTP Basic authentication.
type Client struct {
	base  *url.URL // scheme and host, with the path of the Nexus root (no trailing slash)
	creds Credentials
	http  *http.Client
}

// NormalizeBaseURL checks that raw is an absolute http(s) URL without user info, query or
// fragment, and returns it without a trailing slash.
func NormalizeBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("base_url must be an absolute http or https URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("base_url must not contain user info, a query or a fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}

// NewClient returns a client for the Nexus server at baseURL (see NormalizeBaseURL).
func NewClient(baseURL string, creds Credentials) (*Client, error) {
	norm, err := NormalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(norm)
	return &Client{base: u, creds: creds, http: &http.Client{Transport: transport}}, nil
}

// SourceError is a failed call to the Nexus server. Its message names the request and the
// HTTP status, never the credentials.
type SourceError struct {
	Status int // HTTP status; 0 when no response arrived
	Msg    string
}

func (e *SourceError) Error() string { return e.Msg }

// Unauthorized reports whether Nexus rejected the credentials or their privileges.
func (e *SourceError) Unauthorized() bool {
	return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

// Repository is one Nexus repository.
type Repository struct {
	Name   string
	Format string // Nexus format id, e.g. "maven2", "apt", "raw"
	Type   string // "hosted", "proxy" or "group"
	// Distribution is the "distribution" attribute of the repository's format settings, e.g.
	// the apt distribution; "" when the format has none or the server did not report settings.
	Distribution string
	// Online is false when Nexus marks the repository offline: its files do not download.
	Online bool
	// RemoteURL is the remote of a proxy (proxy.remoteUrl); RemoteUsername is the username that
	// Nexus sends to it (httpClient.authentication.username). Nexus never gives the password.
	RemoteURL      string
	RemoteUsername string
	// MetadataTTL is the proxy's metadataMaxAge and NotFoundTTL its negative cache time to
	// live (0 when the negative cache is off); -1 when Nexus does not report them.
	MetadataTTL time.Duration
	NotFoundTTL time.Duration
	// Members are the member names of a group in Nexus order (group.memberNames). MembersKnown
	// is false when Nexus did not report them.
	Members      []string
	MembersKnown bool
	// keypair is the armored apt signing key pair (aptSigning.keypair), "" when Nexus does not
	// give it; Nexus never gives its passphrase. It is never logged or returned by the API.
	keypair string
	// KeyEncrypted is true when the key pair needs a passphrase.
	KeyEncrypted bool
}

// HasKey reports whether Nexus gave the apt signing key pair.
func (r Repository) HasKey() bool { return r.keypair != "" }

// nexusMinutes converts a Nexus age in minutes (-1: forever) to a duration, capped at max.
func nexusMinutes(raw json.RawMessage, max time.Duration) time.Duration {
	var n int64
	if json.Unmarshal(raw, &n) != nil {
		return -1
	}
	if n < 0 || time.Duration(n)*time.Minute > max {
		return max
	}
	return time.Duration(n) * time.Minute
}

// Asset is one file of a Nexus repository.
type Asset struct {
	Path        string // repo-relative, forward slashes, no leading slash
	DownloadURL string
	Size        int64  // -1 when Nexus does not report it
	SHA256      string // lowercase hex; "" when Nexus does not report it
	// CreatedAt and UpdatedAt are the blob dates (blobCreated, and blobUpdated or else
	// lastModified); zero when Nexus does not report them.
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AssetPage is one page of a repository's assets. Next is the continuation token of the next
// page; "" on the last page.
type AssetPage struct {
	Items []Asset
	Next  string
}

func (c *Client) endpoint(p string, q url.Values) string {
	u := *c.base
	u.Path = c.base.Path + p
	u.RawQuery = q.Encode()
	return u.String()
}

func (c *Client) do(ctx context.Context, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, &SourceError{Msg: "invalid Nexus URL"}
	}
	req.SetBasicAuth(c.creds.Username, c.creds.Password)
	req.Header.Set("Accept", "application/json, */*")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL is named by the caller's message
		}
		return nil, &SourceError{Msg: fmt.Sprintf("cannot reach Nexus: %v", err)}
	}
	return resp, nil
}

// getJSON GETs path (relative to the Nexus root) and decodes the JSON response into v.
func (c *Client) getJSON(ctx context.Context, path string, q url.Values, v any) error {
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	resp, err := c.do(ctx, c.endpoint(path, q))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return statusError("GET "+path, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSONBytes)).Decode(v); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &SourceError{Status: resp.StatusCode, Msg: fmt.Sprintf("GET %s: invalid JSON response: %v", path, err)}
	}
	return nil
}

func statusError(what string, status int) *SourceError {
	msg := fmt.Sprintf("%s: Nexus answered %d %s", what, status, http.StatusText(status))
	switch status {
	case http.StatusUnauthorized:
		msg = fmt.Sprintf("%s: Nexus rejected the credentials (401)", what)
	case http.StatusForbidden:
		msg = fmt.Sprintf("%s: the Nexus user lacks the privilege (403)", what)
	}
	return &SourceError{Status: status, Msg: msg}
}

// Repositories lists the server's repositories. It reads /service/rest/v1/repositorySettings,
// which includes each format's settings, and falls back to /service/rest/v1/repositories
// (without settings) when the server does not offer it or the user may not read it.
func (c *Client) Repositories(ctx context.Context) ([]Repository, error) {
	var settings []map[string]json.RawMessage
	err := c.getJSON(ctx, "/service/rest/v1/repositorySettings", nil, &settings)
	var se *SourceError
	if errors.As(err, &se) && (se.Status == http.StatusNotFound || se.Status == http.StatusForbidden) {
		var plain []map[string]json.RawMessage
		if err := c.getJSON(ctx, "/service/rest/v1/repositories", nil, &plain); err != nil {
			return nil, err
		}
		settings = plain
	} else if err != nil {
		return nil, err
	}
	out := make([]Repository, 0, len(settings))
	for _, s := range settings {
		r := Repository{Name: str(s["name"]), Format: str(s["format"]), Type: str(s["type"]), Online: true}
		if r.Name == "" {
			continue
		}
		r.Distribution = distribution(s)
		if v, ok := s["online"]; ok && string(v) == "false" {
			r.Online = false
		}
		// The plain list keeps the proxy remote under attributes.
		attrs := obj(s["attributes"])
		r.RemoteURL = str(obj(s["proxy"])["remoteUrl"])
		if r.RemoteURL == "" {
			r.RemoteURL = str(obj(attrs["proxy"])["remoteUrl"])
		}
		r.RemoteUsername = str(obj(obj(s["httpClient"])["authentication"])["username"])
		r.MetadataTTL = nexusMinutes(obj(s["proxy"])["metadataMaxAge"], maxProxyTTL)
		neg := obj(s["negativeCache"])
		r.NotFoundTTL = nexusMinutes(neg["timeToLive"], maxProxyTTL)
		if string(neg["enabled"]) == "false" {
			r.NotFoundTTL = 0
		}
		if g, ok := obj(s["group"])["memberNames"]; ok {
			r.MembersKnown = json.Unmarshal(g, &r.Members) == nil
		}
		r.keypair = strings.TrimSpace(str(obj(s["aptSigning"])["keypair"]))
		r.KeyEncrypted = r.keypair != "" && signing.Encrypted([]byte(r.keypair))
		out = append(out, r)
	}
	return out, nil
}

func str(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// obj decodes a JSON object; nil when raw is not one.
func obj(raw json.RawMessage) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

// probeTimeout bounds one anonymous check of a repository.
const probeTimeout = 15 * time.Second

// Public reports whether anyone can download the files of repo in Nexus. It takes the first
// asset of the repo (with the source's account) and asks for it without credentials: 200 means
// public, 401 or 403 private. A repo with no asset asks for a path that does not exist: Nexus
// answers 404 to a reader who may read the repo and 401 to one who may not.
func (c *Client) Public(ctx context.Context, repo string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	page, err := c.Assets(ctx, repo, "")
	if err != nil {
		return false, err
	}
	target, ok := c.base.String()+"/repository/"+url.PathEscape(repo)+"/.telesphorepo-public-check", http.StatusNotFound
	if len(page.Items) > 0 {
		target, ok = c.downloadURL(repo, page.Items[0]), http.StatusOK
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, target, nil)
	if err != nil {
		return false, &SourceError{Msg: "invalid Nexus URL"}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false, &SourceError{Msg: "cannot reach Nexus"}
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case ok:
		return true, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return false, nil
	}
	return false, statusError("anonymous HEAD", resp.StatusCode)
}

// distribution returns the "distribution" string of the first nested settings object that has
// one (Nexus keeps format settings in an object named after the format).
func distribution(s map[string]json.RawMessage) string {
	for _, v := range s {
		var obj map[string]json.RawMessage
		if json.Unmarshal(v, &obj) != nil {
			continue
		}
		if d := str(obj["distribution"]); d != "" {
			return d
		}
	}
	return ""
}

// Assets returns the page of repo's assets that continuation names ("" for the first page).
func (c *Client) Assets(ctx context.Context, repo, continuation string) (AssetPage, error) {
	q := url.Values{"repository": {repo}}
	if continuation != "" {
		q.Set("continuationToken", continuation)
	}
	var body struct {
		Items []struct {
			Path         string            `json:"path"`
			DownloadURL  string            `json:"downloadUrl"`
			FileSize     *int64            `json:"fileSize"`
			Checksum     map[string]string `json:"checksum"`
			BlobCreated  string            `json:"blobCreated"`
			BlobUpdated  string            `json:"blobUpdated"`
			LastModified string            `json:"lastModified"`
		} `json:"items"`
		ContinuationToken *string `json:"continuationToken"`
	}
	if err := c.getJSON(ctx, "/service/rest/v1/assets", q, &body); err != nil {
		return AssetPage{}, err
	}
	page := AssetPage{Items: make([]Asset, 0, len(body.Items))}
	for _, it := range body.Items {
		a := Asset{
			Path:        strings.TrimLeft(it.Path, "/"),
			DownloadURL: it.DownloadURL,
			Size:        -1,
			SHA256:      strings.ToLower(it.Checksum["sha256"]),
			CreatedAt:   nexusTime(it.BlobCreated),
			UpdatedAt:   nexusTime(it.BlobUpdated),
		}
		if a.UpdatedAt.IsZero() {
			a.UpdatedAt = nexusTime(it.LastModified)
		}
		if it.FileSize != nil {
			a.Size = *it.FileSize
		}
		page.Items = append(page.Items, a)
	}
	if body.ContinuationToken != nil {
		page.Next = *body.ContinuationToken
	}
	return page, nil
}

// nexusTime parses a Nexus date such as "2023-03-31T19:28:22.647-04:00"; zero when it is
// missing or not a date.
func nexusTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// downloadURL returns the URL to fetch asset a of repo from. It uses the asset's downloadUrl
// only when it points at the configured server (same scheme and host), so the credentials go
// nowhere else; otherwise it builds {base}/repository/{repo}/{path}.
func (c *Client) downloadURL(repo string, a Asset) string {
	if u, err := url.Parse(a.DownloadURL); err == nil && u.Scheme == c.base.Scheme && u.Host == c.base.Host && u.User == nil {
		return u.String()
	}
	segs := strings.Split(a.Path, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	u := *c.base
	u.RawQuery = ""
	return u.String() + "/repository/" + url.PathEscape(repo) + "/" + strings.Join(segs, "/")
}

// Open downloads asset a of repo. The caller closes the body.
func (c *Client) Open(ctx context.Context, repo string, a Asset) (io.ReadCloser, error) {
	resp, err := c.do(ctx, c.downloadURL(repo, a))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		return nil, statusError("download", resp.StatusCode)
	}
	return resp.Body, nil
}
