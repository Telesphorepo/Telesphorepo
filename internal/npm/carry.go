package npm

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/npm/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// carryPage is the number of dist-tags that a merge reads per query.
const carryPage = 500

// carry is what a merge of another npm repo needs (format.Spec Carry): the tarballs go through
// the import, and the dist-tags of the source are labels "<name> <tag>" (a space is in no package
// name and no tag).
func (h *Handler) carry() *format.Carry {
	return &format.Carry{Labels: h.mergeLabels, SetLabel: h.setMergeLabel}
}

// mergeLabels lists every dist-tag of from with the version that into's tag of the same package
// names, through the primary keys.
func (h *Handler) mergeLabels(ctx context.Context, from, into *repos.Repo) ([]format.Label, error) {
	var out []format.Label
	owned := map[string]bool{}
	tags := map[string]map[string]string{}
	afterName, afterTag := "", ""
	for {
		rows, err := h.q.ListRepoTagsAfter(ctx, dbq.ListRepoTagsAfterParams{RepoID: from.ID, Name: afterName, Tag: afterTag, Limit: carryPage})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if _, ok := owned[r.Name]; !ok {
				_, err := h.q.GetPackage(ctx, dbq.GetPackageParams{RepoID: into.ID, Name: r.Name})
				switch {
				case err == nil:
					owned[r.Name] = true
					cur, err := h.q.ListTags(ctx, dbq.ListTagsParams{RepoID: into.ID, Name: r.Name})
					if err != nil {
						return nil, err
					}
					tags[r.Name] = map[string]string{}
					for _, t := range cur {
						tags[r.Name][t.Tag] = t.Version
					}
				case errors.Is(err, sql.ErrNoRows):
					owned[r.Name] = false
				default:
					return nil, err
				}
			}
			out = append(out, format.Label{Key: r.Name + " " + r.Tag, Title: r.Name + ": dist-tag " + r.Tag, Value: r.Version,
				Target: tags[r.Name][r.Tag], Owned: owned[r.Name]})
			afterName, afterTag = r.Name, r.Tag
		}
		if len(rows) < carryPage {
			return out, nil
		}
	}
}

// setMergeLabel points the dist-tag of l in into at its version in from, when into has that
// version.
func (h *Handler) setMergeLabel(ctx context.Context, into *repos.Repo, l format.Label) error {
	name, tag, ok := strings.Cut(l.Key, " ")
	if !ok {
		return nil
	}
	return h.change(ctx, into, name, func(q *dbq.Queries) (bool, error) {
		if _, err := q.GetVersionTarball(ctx, dbq.GetVersionTarballParams{RepoID: into.ID, Name: name, Version: l.Value}); errors.Is(err, sql.ErrNoRows) {
			return false, nil
		} else if err != nil {
			return false, err
		}
		return true, q.UpsertTag(ctx, dbq.UpsertTagParams{RepoID: into.ID, Name: name, Tag: tag, Version: l.Value})
	})
}
