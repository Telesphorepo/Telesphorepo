package repos

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
)

// Field is a repo value that a module owns and keeps in its own table, e.g. "signing_key" of the
// signing module. Repo.Fields holds the value of each field that applies to the repo's format and kind;
// Create and Update take new values in Create.Fields and Patch.Fields.
type Field struct {
	Name string
	// Applies reports whether repos of format and kind (KindHosted, KindProxy or KindGroup) have the
	// field.
	Applies func(format, kind string) bool
	// Validate checks a new value for repo and returns its normalized form. On a create, repo has
	// no ID yet.
	Validate func(ctx context.Context, repo *Repo, raw json.RawMessage) (json.RawMessage, error)
	// Default is the value of a new repo that sets none; nil: no value.
	Default func(ctx context.Context, format string) json.RawMessage
	// Save writes the value in the transaction of the create or update.
	Save func(ctx context.Context, tx *sql.Tx, repo *Repo, v json.RawMessage) error
	// Committed runs after the commit, so the module updates its in-memory copy.
	Committed func(repoID int64, v json.RawMessage)
	// Load returns the value of repoID from the module's memory; nil: none.
	Load func(repoID int64) json.RawMessage
}

// AddField registers f. It runs before the server serves.
func (s *Service) AddField(f Field) {
	if f.Name == "" || f.Applies == nil || f.Validate == nil || f.Save == nil || f.Load == nil {
		panic(fmt.Sprintf("repos: field %q needs Applies, Validate, Save and Load", f.Name))
	}
	if slices.ContainsFunc(s.fields, func(x Field) bool { return x.Name == f.Name }) {
		panic(fmt.Sprintf("repos: field %q registered twice", f.Name))
	}
	s.fields = append(s.fields, f)
}

// FieldNames returns the names of the fields that apply to repos of format and kind, in
// registration order.
func (s *Service) FieldNames(format, kind string) []string {
	var out []string
	for _, f := range s.fields {
		if f.Applies(format, kind) {
			out = append(out, f.Name)
		}
	}
	return out
}

// loadFields fills r.Fields from the modules' memory.
func (s *Service) loadFields(r *Repo) {
	for _, f := range s.fields {
		if !f.Applies(r.Format, r.Kind) {
			continue
		}
		if v := f.Load(r.ID); v != nil {
			if r.Fields == nil {
				r.Fields = map[string]json.RawMessage{}
			}
			r.Fields[f.Name] = v
		}
	}
}

// fieldChange is a field value to save.
type fieldChange struct {
	f Field
	v json.RawMessage
}

// planFields validates the field values in of repo (new values over the current ones in
// repo.Fields) and returns the values to save. With create, a field without a value gets its
// default. It sets repo.Fields to the new values.
func (s *Service) planFields(ctx context.Context, repo *Repo, in map[string]json.RawMessage, create bool) ([]fieldChange, error) {
	for name := range in {
		i := slices.IndexFunc(s.fields, func(f Field) bool { return f.Name == name })
		if i < 0 || !s.fields[i].Applies(repo.Format, repo.Kind) {
			return nil, invalid("%s repositories of format %s have no field %s", repo.Kind, repo.Format, name)
		}
	}
	var out []fieldChange
	next := maps.Clone(repo.Fields)
	for _, f := range s.fields {
		if !f.Applies(repo.Format, repo.Kind) {
			continue
		}
		raw, ok := in[f.Name]
		if !ok && create && f.Default != nil {
			raw, ok = f.Default(ctx, repo.Format), true
		}
		if !ok || raw == nil {
			continue
		}
		v, err := f.Validate(ctx, repo, raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrInvalid, f.Name, err)
		}
		if !create && string(v) == string(repo.Fields[f.Name]) {
			continue
		}
		if next == nil {
			next = map[string]json.RawMessage{}
		}
		next[f.Name] = v
		out = append(out, fieldChange{f, v})
	}
	repo.Fields = next
	return out, nil
}

// saveFields writes the field values of repo in tx.
func saveFields(ctx context.Context, tx *sql.Tx, repo *Repo, cs []fieldChange) error {
	for _, c := range cs {
		if err := c.f.Save(ctx, tx, repo, c.v); err != nil {
			return err
		}
	}
	return nil
}

// committedFields tells the modules the saved values of repoID.
func committedFields(repoID int64, cs []fieldChange) {
	for _, c := range cs {
		if c.f.Committed != nil {
			c.f.Committed(repoID, c.v)
		}
	}
}
