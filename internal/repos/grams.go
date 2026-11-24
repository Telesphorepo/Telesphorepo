package repos

import (
	"context"
	"encoding/json"

	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/namesearch"
)

// The grams of the repo names (repo_name_grams, internal/namesearch): the service writes them in
// the transaction of each change of a repo row, by the grams of the name, one statement for all
// of them (a seek per gram).

// writeGrams writes the grams of r's name in the transaction of q, in place of those of the name
// old ("" for none): a create, a rename and a retire call it after the row changed.
func writeGrams(ctx context.Context, q *dbq.Queries, old string, r *Repo) error {
	if old != "" {
		if err := dropGrams(ctx, q, old); err != nil {
			return err
		}
	}
	grams, err := json.Marshal(namesearch.Grams(r.Name))
	if err != nil {
		return err
	}
	return q.InsertRepoGrams(ctx, dbq.InsertRepoGramsParams{Name: r.Name, Format: r.Format, Kind: r.Kind,
		Anonymous: anonymous(r), Grams: string(grams)})
}

// dropGrams deletes the grams of name, in the transaction of the delete or rename of its repo.
func dropGrams(ctx context.Context, q *dbq.Queries, name string) error {
	return q.DeleteRepoGrams(ctx, dbq.DeleteRepoGramsParams{Name: name, Grams: namesearch.Grams(name)})
}

// publicGrams sets the public flag of r's grams, in the transaction of the update that changes
// it.
func publicGrams(ctx context.Context, q *dbq.Queries, r *Repo) error {
	return q.SetRepoGramsAnonymous(ctx, dbq.SetRepoGramsAnonymousParams{Anonymous: anonymous(r), Name: r.Name,
		Grams: namesearch.Grams(r.Name)})
}

func anonymous(r *Repo) int64 {
	if r.AnonymousRead {
		return 1
	}
	return 0
}
