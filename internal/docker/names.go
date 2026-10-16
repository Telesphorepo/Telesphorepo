package docker

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
)

// Actions of a /v2/<name>/... path.
const (
	actManifests = "manifests" // ref: a tag or a digest
	actBlobs     = "blobs"     // ref: a digest
	actUploads   = "uploads"   // ref: an upload id, "" for a new upload
	actTags      = "tags"      // tags/list
	actReferrers = "referrers" // ref: a digest
)

var (
	// segmentRE is one segment of an OCI repository name.
	segmentRE = regexp.MustCompile(`^[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*$`)
	tagRE     = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
	digestRE  = regexp.MustCompile(`^[a-z0-9]+(?:[.+_-][a-z0-9]+)*:[a-zA-Z0-9=_-]+$`)
	hexRE     = regexp.MustCompile(`^[a-f0-9]{64}$`)
	uploadRE  = regexp.MustCompile(`^[a-f0-9]{32}$`)
)

// maxName is the longest OCI repository name in bytes.
const maxName = 255

// target is the parsed path of a /v2/ request: <name>/<action>/<ref>.
type target struct {
	name   string // "<repo>/<image>"
	repo   string
	image  string // "" when the name has one segment
	action string
	ref    string
}

// errNoImage is a name of one segment: the repo without an image.
var errNoImage = errors.New("no image")

// rest returns the part of the raw path of r after /v2/. It refuses an encoded "/", "\" or NUL,
// which decode into a path that the router did not match.
func rest(r *http.Request) (string, error) {
	esc := strings.ToLower(r.URL.EscapedPath())
	for _, bad := range []string{"%2f", "%5c", "%00"} {
		if strings.Contains(esc, bad) {
			return "", repopath.ErrInvalid
		}
	}
	return r.PathValue("rest"), nil
}

// repoOf is the RepoRule.Name of the /v2/ routes: the first segment of the name.
func repoOf(r *http.Request) (string, error) {
	s, err := rest(r)
	if err != nil {
		return "", err
	}
	repo, _, _ := strings.Cut(s, "/")
	if repo == "" {
		return "", repopath.ErrInvalid
	}
	return repo, nil
}

// parseTarget parses the rest of a /v2/ path. The action is found from the end, so an image
// segment may be called "manifests" or "blobs". An unknown action is ok=false (404); a bad name
// is an *ociError.
func parseTarget(r *http.Request) (target, bool, error) {
	s, err := rest(r)
	if err != nil {
		return target{}, false, nameInvalid("invalid path")
	}
	segs := strings.Split(s, "/")
	n := len(segs)
	var t target
	var nameSegs []string
	switch {
	case n >= 3 && segs[n-1] == "" && segs[n-2] == "uploads" && segs[n-3] == "blobs":
		t.action, nameSegs = actUploads, segs[:n-3]
	case n >= 2 && segs[n-1] == "uploads" && segs[n-2] == "blobs" && r.Method == http.MethodPost:
		t.action, nameSegs = actUploads, segs[:n-2]
	case n >= 3 && segs[n-2] == "uploads" && segs[n-3] == "blobs":
		t.action, t.ref, nameSegs = actUploads, segs[n-1], segs[:n-3]
	case n >= 2 && segs[n-1] == "list" && segs[n-2] == "tags":
		t.action, nameSegs = actTags, segs[:n-2]
	case n >= 2 && (segs[n-2] == actManifests || segs[n-2] == actBlobs || segs[n-2] == actReferrers):
		t.action, t.ref, nameSegs = segs[n-2], segs[n-1], segs[:n-2]
	default:
		return target{}, false, nil
	}
	if err := t.setName(nameSegs); err != nil {
		return target{}, true, err
	}
	return t, true, nil
}

// setName checks the name segments and sets name, repo and image.
func (t *target) setName(segs []string) error {
	if len(segs) == 0 || segs[0] == "" {
		return nameInvalid("invalid repository name")
	}
	name := strings.Join(segs, "/")
	if len(name) > maxName {
		return nameInvalid("the repository name is longer than 255 bytes")
	}
	for _, s := range segs {
		if !segmentRE.MatchString(s) {
			return nameInvalid("invalid repository name: " + name)
		}
	}
	t.name, t.repo = name, segs[0]
	if len(segs) > 1 {
		t.image = strings.Join(segs[1:], "/")
	}
	return nil
}

// parseName parses a name such as the from of a mount ("<repo>/<image>").
func parseName(name string) (target, error) {
	var t target
	if err := t.setName(strings.Split(name, "/")); err != nil {
		return t, err
	}
	if t.image == "" {
		return t, errNoImage
	}
	return t, nil
}

// parseDigest checks a digest and returns its hex. Only sha256 is supported.
func parseDigest(d string) (string, error) {
	if !digestRE.MatchString(d) {
		return "", digestInvalid("invalid digest: " + d)
	}
	alg, hex, _ := strings.Cut(d, ":")
	if alg != "sha256" {
		return "", digestInvalid("only sha256 digests are supported")
	}
	if !hexRE.MatchString(hex) {
		return "", digestInvalid("invalid sha256 digest: " + d)
	}
	return hex, nil
}

// isDigest reports whether ref has the form of a digest (an algorithm and ":").
func isDigest(ref string) bool { return strings.Contains(ref, ":") }

// validRepoName is the format's ValidName: a docker repo name is one OCI name segment.
func validRepoName(name string) error {
	if !segmentRE.MatchString(name) {
		return errors.New("a Docker repository name is lower-case letters and digits, separated by one '.', one or two '_', or '-'")
	}
	return nil
}

// Paths of the assets of an image.
func linkPath(image, hex string) string     { return image + "/_blobs/sha256/" + hex }
func manifestPath(image, hex string) string { return image + "/_manifests/sha256/" + hex }
func tagPath(image, tag string) string      { return image + "/_tags/" + tag }

// Kinds of an asset path.
const (
	kindLink     = "link"
	kindManifest = "manifest"
	kindTag      = "tag"
)

// objectPath is a parsed asset path of a docker repo.
type objectPath struct {
	kind  string
	image string
	ref   string // the hex of a link or a manifest, the tag of a tag
}

// parsePath parses an asset path; ok is false for a path that is not an OCI object.
func parsePath(p string) (objectPath, bool) {
	for _, k := range []struct{ marker, kind string }{{"/_blobs/sha256/", kindLink}, {"/_manifests/sha256/", kindManifest}, {"/_tags/", kindTag}} {
		i := strings.Index(p, k.marker)
		if i <= 0 {
			continue
		}
		image, ref := p[:i], p[i+len(k.marker):]
		var t target
		if t.setName(append([]string{"r"}, strings.Split(image, "/")...)) != nil {
			return objectPath{}, false
		}
		switch k.kind {
		case kindTag:
			if !tagRE.MatchString(ref) {
				return objectPath{}, false
			}
		default:
			if !hexRE.MatchString(ref) {
				return objectPath{}, false
			}
		}
		return objectPath{kind: k.kind, image: image, ref: ref}, true
	}
	return objectPath{}, false
}
