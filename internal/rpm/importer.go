package rpm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/rpm/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/signing"
)

// The Nexus import of yum repos (format.Importer). Nexus keeps .rpm files at any depth and builds
// its own repodata, which the import leaves out: the target builds and signs its own. Nexus gives
// no signing key of a yum repo over its API.
func (h *Handler) importer() *format.Importer {
	return &format.Importer{Sources: []string{"yum"}, Target: h.importTarget, Present: h.importPresent,
		Store: h.importStore, Plan: h.importPlan}
}

// fileIdentity splits an rpm file name <name>-<version>-<release>.<arch>.rpm.
func fileIdentity(file string) (name, version, release, arch string, ok bool) {
	base, found := strings.CutSuffix(file, ".rpm")
	if !found {
		return "", "", "", "", false
	}
	i := strings.LastIndexByte(base, '.')
	if i <= 0 {
		return "", "", "", "", false
	}
	base, arch = base[:i], base[i+1:]
	j := strings.LastIndexByte(base, '-')
	if j <= 0 {
		return "", "", "", "", false
	}
	base, release = base[:j], base[j+1:]
	k := strings.LastIndexByte(base, '-')
	if k <= 0 {
		return "", "", "", "", false
	}
	return base[:k], base[k+1:], release, arch, true
}

// importTarget takes every .rpm of a Nexus yum repo but source packages (.src.rpm, .nosrc.rpm)
// and Nexus's repodata. The store reads the identity from the package; the target is the path
// that rpm's file name <name>-<version>-<release>.<arch>.rpm gives, or for a file named otherwise
// Packages/<file>, which holds nothing (the file is downloaded, and a package the repo has from
// the same bytes counts as skipped).
func (h *Handler) importTarget(_ context.Context, _ *repos.Repo, it format.ImportItem) (repopath.Path, bool, error) {
	file := it.Path[strings.LastIndexByte(it.Path, '/')+1:]
	if !strings.HasSuffix(file, ".rpm") || strings.HasSuffix(file, ".src.rpm") || strings.HasSuffix(file, ".nosrc.rpm") ||
		strings.Contains("/"+it.Path, "/repodata/") {
		return "", false, nil
	}
	target := "Packages/" + file
	if n, v, r, a, ok := fileIdentity(file); ok && nameRE.MatchString(n) {
		target = "Packages/" + n + "/" + n + "-" + v + "-" + r + "." + a + ".rpm"
	}
	p, err := repopath.Parse(target)
	if err != nil {
		return "", false, fmt.Errorf("the file name %q is not a path of this repository", file)
	}
	return p, true, nil
}
// importPresent finds the stored package that was made from the source file's bytes: the file is
// skipped without a download, though the repo keeps it with its own signature.
func (h *Handler) importPresent(ctx context.Context, repo *repos.Repo, it format.ImportItem) (*assets.Asset, error) {
	n, v, r, a, ok := fileIdentity(it.Path[strings.LastIndexByte(it.Path, '/')+1:])
	if !ok {
		return nil, nil
	}
	f, err := h.q.FindUpload(ctx, dbq.FindUploadParams{RepoID: repo.ID, Name: n, Version: v, Release: r, Arch: a, UploadSha256: it.SHA256})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	asset, err := h.assets.Get(ctx, repo.ID, f)
	if errors.Is(err, assets.ErrNotFound) {
		return nil, nil
	}
	return asset, err
}

// importStore ingests a downloaded package with the source's dates.
func (h *Handler) importStore(ctx context.Context, repo *repos.Repo, it format.ImportItem, sp *assets.Spooled) (*assets.Result, error) {
	st, out, err := h.ingest(ctx, repo, sp, it.Origin())
	if err != nil {
		return nil, err
	}
	a, err := h.assets.Get(ctx, repo.ID, out.Filename)
	if err != nil {
		return nil, err
	}
	return &assets.Result{Asset: a, Status: st}, nil
}

// keyNew is the answer of the signing_key question for a new key named after the target.
const keyNew = "new"

// importPlan plans the target of a Nexus yum repo. Its facts hold the repodata depth: a Nexus repo
// of depth 1 or more keeps several trees (often one per release or architecture), which go into
// the one repodata of the target. A new hosted target asks for its signing key: an existing key
// (default "default") or a new key named after the repo.
func (h *Handler) importPlan(ctx context.Context, src format.SourceRepo, target *repos.Repo, answers map[string]string) (*format.TargetPlan, error) {
	var attrs struct {
		Yum struct {
			RepodataDepth int `json:"repodataDepth"`
		} `json:"yum"`
	}
	if len(src.Attributes) > 0 {
		_ = json.Unmarshal(src.Attributes, &attrs) // a source without these settings gives none
	}
	plan := &format.TargetPlan{Facts: map[string]any{"repodata_depth": attrs.Yum.RepodataDepth}}
	if src.Kind == repos.KindHosted && attrs.Yum.RepodataDepth > 0 {
		plan.Facts["note"] = fmt.Sprintf("Nexus keeps the repodata of this repository %d folders deep, one tree per folder. The new repository has one repodata for all of them; clients point their baseurl at it.", attrs.Yum.RepodataDepth)
	}
	if target != nil && target.ID != 0 || src.Kind != repos.KindHosted {
		return plan, nil
	}
	key := format.Question{Key: "signing_key", Label: "Signing key", Type: "choice", Required: true, Default: signing.DefaultKey,
		Help: "The key that signs the packages and the repodata of the new repository. Nexus does not give the key of a yum repository."}
	for _, k := range h.keys.List() {
		key.Options = append(key.Options, format.Option{Value: k.Name, Label: "Signing key " + k.Name})
	}
	key.Options = append(key.Options, format.Option{Value: keyNew, Label: "A new key for this repository"})
	plan.Questions = []format.Question{key}
	if answers == nil || target == nil {
		return plan, nil
	}
	name := answers["signing_key"]
	switch {
	case name == keyNew:
		name = target.Name
		if len(name) > 64 {
			name = name[:64]
		}
		if h.keys.Exists(name) {
			return nil, fmt.Errorf("a signing key named %s exists; pick it", name)
		}
		plan.Prepare = func(ctx context.Context) error {
			_, err := h.keys.Create(ctx, name)
			return err
		}
	case name == "":
		name = signing.DefaultKey
		fallthrough
	default:
		if !h.keys.Exists(name) {
			return nil, fmt.Errorf("signing key %q does not exist", name)
		}
	}
	raw, _ := json.Marshal(name)
	plan.Fields = map[string]json.RawMessage{signing.Field: raw}
	return plan, nil
}
