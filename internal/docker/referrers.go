package docker

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/docker/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
)

// referrersPage is how many referrers one answer lists.
const referrersPage = 500

// referrer is one manifest whose subject is the digest of a referrers request.
type referrer struct {
	digest, mediaType, artifactType, annotations string
	size                                         int64
}

// referrers answers GET <name>/referrers/<digest>?artifactType=: always 200 with an OCI index of
// the manifests of the image whose subject is the digest (empty when none), in digest order. With
// artifactType it lists that type only and says so in OCI-Filters-Applied. A full page adds the
// Link of the next one (?last=<hex>). A proxy lists the manifests it has cached; a group merges
// the manifests of its readable members by digest.
func (f *Format) referrers(w http.ResponseWriter, r *http.Request, c *route.Call, t target) {
	subject, err := parseDigest(t.ref)
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	after, typ := q.Get("last"), q.Get("artifactType")
	if after != "" && !hexRE.MatchString(after) {
		writeOCI(w, ociErr(http.StatusBadRequest, codePaginationInvalid, "last must be the hex of a sha256 digest"))
		return
	}
	list := []*repos.Repo{c.Repo}
	if c.Repo.Kind == repos.KindGroup {
		if list, err = f.readableMembers(r, c); err != nil {
			fail(w, r, err)
			return
		}
	}
	ctx := r.Context()
	seen := map[string]bool{}
	var rows []referrer
	add := func(x referrer) {
		if !seen[x.digest] {
			seen[x.digest] = true
			rows = append(rows, x)
		}
	}
	for _, repo := range list {
		if typ != "" {
			rs, err := f.q.ListReferrersOfType(ctx, dbq.ListReferrersOfTypeParams{RepoID: repo.ID, Image: t.image, Subject: subject,
				After: after, ArtifactType: typ, Limit: referrersPage})
			if err != nil {
				fail(w, r, err)
				return
			}
			for _, x := range rs {
				add(referrer{x.Digest, x.MediaType, x.ArtifactType, x.Annotations, x.Size})
			}
			continue
		}
		rs, err := f.q.ListReferrers(ctx, dbq.ListReferrersParams{RepoID: repo.ID, Image: t.image, Subject: subject,
			After: after, Limit: referrersPage})
		if err != nil {
			fail(w, r, err)
			return
		}
		for _, x := range rs {
			add(referrer{x.Digest, x.MediaType, x.ArtifactType, x.Annotations, x.Size})
		}
	}
	slices.SortFunc(rows, func(a, b referrer) int { return strings.Compare(a.digest, b.digest) })
	if len(rows) > referrersPage {
		rows = rows[:referrersPage]
	}
	if typ != "" {
		w.Header().Set("OCI-Filters-Applied", "artifactType")
	}
	manifests := make([]Descriptor, 0, len(rows))
	for _, x := range rows {
		d := Descriptor{MediaType: x.mediaType, Digest: "sha256:" + x.digest, Size: x.size, ArtifactType: x.artifactType}
		_ = json.Unmarshal([]byte(x.annotations), &d.Annotations)
		if len(d.Annotations) == 0 {
			d.Annotations = nil
		}
		manifests = append(manifests, d)
	}
	if len(rows) == referrersPage {
		v := url.Values{"last": {rows[len(rows)-1].digest}}
		if typ != "" {
			v.Set("artifactType", typ)
		}
		w.Header().Set("Link", "</v2/"+t.name+"/referrers/"+t.ref+"?"+v.Encode()+`>; rel="next"`)
	}
	h := w.Header()
	h.Set("Content-Type", mtOCIIndex)
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 2, "mediaType": mtOCIIndex, "manifests": manifests})
}
