//go:build e2e || import || clients

package e2e

import (
	"bytes"
	"compress/gzip"
	"io"
	"regexp"
	"testing"
)

// indexedFile is a package file that an index names: its path in the repo and its SHA-256.
type indexedFile struct{ path, sum string }

// rpmPrimaryIndex reads the primary metadata that repomd.xml of repo names: the SHA-256 of each
// package, by its location.
func rpmPrimaryIndex(t *testing.T, s *Server, tg *S3Target, repo string) map[string]indexedFile {
	t.Helper()
	repomd := getFollowS3(t, s, tg, "/rpm/"+repo+"/repodata/repomd.xml")
	m := regexp.MustCompile(`<data type="primary">[\s\S]*?<location href="([^"]+)"`).FindSubmatch(repomd)
	if m == nil {
		t.Fatalf("repomd.xml of %s names no primary metadata:\n%s", repo, repomd)
	}
	zr, err := gzip.NewReader(bytes.NewReader(getFollowS3(t, s, tg, "/rpm/"+repo+"/"+string(m[1]))))
	if err != nil {
		t.Fatalf("the primary metadata of %s is not gzip: %v", repo, err)
	}
	primary, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read the primary metadata of %s: %v", repo, err)
	}
	pkg := regexp.MustCompile(`<package type="rpm">[\s\S]*?<checksum type="sha256" pkgid="YES">([0-9a-f]{64})</checksum>[\s\S]*?<location href="([^"]+)"/>`)
	index := map[string]indexedFile{}
	for _, p := range pkg.FindAllSubmatch(primary, -1) {
		index[string(p[2])] = indexedFile{string(p[2]), string(p[1])}
	}
	return index
}
