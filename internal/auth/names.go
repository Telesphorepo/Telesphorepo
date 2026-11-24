package auth

import (
	"context"
	"encoding/json"

	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/namesearch"
)

// The search of the usernames (GET /api/users?q=, GET /api/people?q=): user_name_grams holds the
// grams of each username (internal/namesearch), written in the transaction of the account's
// create. A username is never renamed; the account's row takes its grams with it.

// writeUserGrams writes the grams of username, the account id, in the transaction of q: one
// statement.
func writeUserGrams(ctx context.Context, q *dbq.Queries, id int64, username string) error {
	grams, err := json.Marshal(namesearch.Grams(username))
	if err != nil {
		return err
	}
	return q.InsertUserGrams(ctx, dbq.InsertUserGramsParams{Name: username, UserID: id, Grams: string(grams)})
}

// searchUsers returns at most limit items of a search of text (folded) after the name after, by
// read: the rows of the gram that namesearch.Gram picks, in the ranges of namesearch.Steps.
func searchUsers[T any](ctx context.Context, s *Service, text, after string, limit int,
	read func(ctx context.Context, p dbq.ListUserGramsParams) ([]T, error)) ([]T, error) {
	g, ok, err := namesearch.Gram(ctx, text, func(ctx context.Context, g string, limit int64) (int64, error) {
		return s.rq.CountUserGram(ctx, dbq.CountUserGramParams{Gram: g, Limit: limit})
	})
	if err != nil || !ok {
		return nil, err
	}
	return namesearch.Page(ctx, text, after, limit, func(ctx context.Context, st namesearch.Step, n int) ([]T, error) {
		return read(ctx, dbq.ListUserGramsParams{Gram: g, From: st.Lower(), To: st.To, Text: text, Limit: int64(n)})
	})
}
