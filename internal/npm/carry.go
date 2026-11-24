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
// names, through the primary keys: one query per page of carryPage tags.
func (h *Handler) mergeLabels(ctx context.Context, from, into *repos.Repo) ([]format.Label, error) {
	var out []format.Label
	afterName, afterTag := "", ""
	for {
		rows, err := h.q.ListRepoTagsAfter(ctx, dbq.ListRepoTagsAfterParams{IntoID: into.ID, RepoID: from.ID, Name: afterName, Tag: afterTag, Limit: carryPage})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			target := ""
			if r.Owned {
				target = r.Target
			}
			out = append(out, format.Label{Key: r.Name + " " + r.Tag, Title: r.Name + ": dist-tag " + r.Tag, Value: r.Version,
				Target: target, Owned: r.Owned})
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
