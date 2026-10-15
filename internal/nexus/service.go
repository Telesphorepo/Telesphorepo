package nexus

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

var (
	ErrInvalid    = errors.New("invalid request")
	ErrNotFound   = errors.New("not found")
	ErrExists     = errors.New("a Nexus source with this name already exists")
	ErrRunning    = errors.New("a sync of this source is running")
	ErrNotRunning = errors.New("the sync is not running")
	ErrCutOver    = errors.New("the source is cut over")
	ErrMapped     = errors.New("the source mirrors repos; cut over first or remove its repos")
)

// Source states (nexus_sources.state).
const (
	StateMirroring = "mirroring"
	StateCutOver   = "cut_over"
)

// Service manages Nexus sources, runs their syncs and cuts them over. Syncs of different
// sources run in parallel; one sync runs at a time per source.
type Service struct {
	db       *db.DB
	settings *settings.Service
	repos    *repos.Service
	log      *slog.Logger

	mu   sync.Mutex
	root context.Context     // the server context, set by Start
	runs map[int64]*runState // the syncs this process runs, by sync id
	wg   sync.WaitGroup      // the runner and scheduler goroutines of this process
}

// New returns the Nexus service. Call Start before the API serves requests.
func New(d *db.DB, set *settings.Service, rs *repos.Service, log *slog.Logger) *Service {
	return &Service{db: d, settings: set, repos: rs, log: log, runs: map[int64]*runState{}}
}

// Wait blocks until every runner and the scheduler of this process have returned. The server
// calls it after the Start context ends and before it closes the database.
func (s *Service) Wait() { s.wg.Wait() }

// Source is a Nexus source as the API returns it. It never holds the credentials.
type Source struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	BaseURL   string    `json:"base_url"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	LastError string    `json:"last_error"` // error of the last ended sync; "" when it succeeded
	LastSync  *Sync     `json:"last_sync"`  // newest sync, running or ended, without repos; nil before the first
	// CutoverFailed is the number of files the cut-over sync failed to import; CutoverSyncID is
	// that sync, whose failures GET /api/nexus/syncs/{id}/failures lists. Both are 0 before cut-over.
	CutoverSyncID int64 `json:"cutover_sync_id"`
	CutoverFailed int64 `json:"cutover_failed"`
}

// SourceDetail is a source with its Nexus repos and their mapping.
type SourceDetail struct {
	Source
	Repos []SourceRepo `json:"repos"`
	// NexusError says why the list holds only the mapped repos: Nexus could not be reached,
	// or the source is cut over and has no credentials. "" when Nexus listed its repos.
	NexusError string `json:"nexus_error"`
}

// SourceRepo is one repo of the Nexus server. TargetFormat is the plugin that imports its
// format ("" when none does). Supported is true for a hosted repo that a plugin imports;
// otherwise Reason says why not. Target is the repo it is mapped to ("" when not mapped).
type SourceRepo struct {
	Name         string `json:"name"`
	Format       string `json:"format"`
	Type         string `json:"type"` // "" for a mapped repo that Nexus did not list
	Supported    bool   `json:"supported"`
	Reason       string `json:"reason"`
	TargetFormat string `json:"target_format"`
	Target       string `json:"target"`
}

// Mapping maps one Nexus repo to a target repo. With Create, a missing target is created with
// the plugin's default config plus the Nexus repo's distribution, if it has one.
type Mapping struct {
	SourceRepo string `json:"source_repo"`
	Target     string `json:"target"`
	Create     bool   `json:"create"`
}

// NewSource is the body of a source create.
type NewSource struct {
	Name     string `json:"name"`
	BaseURL  string `json:"base_url"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// SourceUpdate changes the connection of a source. A nil field keeps its stored value.
type SourceUpdate struct {
	BaseURL  *string `json:"base_url"`
	Username *string `json:"username"`
	Password *string `json:"password"`
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func aad(id int64) string { return "nexus_sources:" + strconv.FormatInt(id, 10) }

// lockText is the repos.locked_by text of a repo that mirrors source name.
func lockText(name string) string {
	return "mirrors Nexus source " + name + "; cut over first"
}

// importerFor returns the first registered plugin, in registration order, that imports the
// Nexus format id sourceFormat.
func importerFor(sourceFormat string) (format.Format, format.Importer) {
	for _, f := range format.All() {
		if im, ok := f.(format.Importer); ok && slices.Contains(im.ImportFormats(), sourceFormat) {
			return f, im
		}
	}
	return nil, nil
}

// accepts reports whether the plugin of target format name imports sourceFormat.
func accepts(name, sourceFormat string) (format.Importer, bool) {
	im, ok := format.Get(name).(format.Importer)
	if !ok || !slices.Contains(im.ImportFormats(), sourceFormat) {
		return nil, false
	}
	return im, true
}

// support returns the plugin name that imports a Nexus repo of format and type typ, and why
// it cannot be mirrored ("" when it can).
func support(sourceFormat, typ string) (targetFormat, reason string) {
	f, _ := importerFor(sourceFormat)
	if f == nil {
		return "", "no plugin for format " + sourceFormat
	}
	if typ != "hosted" {
		return f.Name(), "only hosted repos can be mirrored; this is a " + typ + " repo"
	}
	return f.Name(), ""
}

func sourceRow(row dbq.NexusSource, err error) (dbq.NexusSource, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return row, ErrNotFound
	}
	return row, err
}

// client returns a client with the source's stored credentials.
func (s *Service) client(row dbq.NexusSource) (*Client, error) {
	if row.State != StateMirroring || len(row.Credentials) == 0 {
		return nil, ErrCutOver
	}
	plain, err := s.settings.Decrypt(aad(row.ID), row.Credentials)
	if err != nil {
		return nil, fmt.Errorf("cannot read the stored credentials: %w", err)
	}
	var creds Credentials
	if err := json.Unmarshal(plain, &creds); err != nil {
		return nil, fmt.Errorf("cannot read the stored credentials: %w", err)
	}
	return NewClient(row.BaseUrl, creds)
}

// check builds a client for baseURL and creds and lists the server's repos, which checks the
// address and the credentials. It returns the normalized base URL.
func check(ctx context.Context, baseURL string, creds Credentials) (string, error) {
	norm, err := NormalizeBaseURL(baseURL)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if creds.Username == "" {
		return "", fmt.Errorf("%w: username is required", ErrInvalid)
	}
	cl, err := NewClient(norm, creds)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if _, err := cl.Repositories(ctx); err != nil {
		return "", err
	}
	return norm, nil
}

// CreateSource checks the connection, then stores the source with its credentials encrypted.
// userID 0 means unknown.
func (s *Service) CreateSource(ctx context.Context, userID int64, in NewSource) (*Source, error) {
	if !repos.NameRE.MatchString(in.Name) {
		return nil, fmt.Errorf("%w: name must match %s", ErrInvalid, repos.NameRE)
	}
	creds := Credentials{Username: in.Username, Password: in.Password}
	baseURL, err := check(ctx, in.BaseURL, creds)
	if err != nil {
		return nil, err
	}
	plain, err := json.Marshal(creds)
	if err != nil {
		return nil, err
	}
	// last_sync_at starts at the creation time, so the scheduler first syncs the source one
	// interval after it was created; "Sync now" syncs it at once.
	now := time.Now().Unix()
	var id int64
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		var err error
		id, err = q.CreateNexusSource(ctx, dbq.CreateNexusSourceParams{Name: in.Name, BaseUrl: baseURL,
			CreatedBy: sql.NullInt64{Int64: userID, Valid: userID != 0}, CreatedAt: now})
		if err != nil {
			return err
		}
		if err := q.SetNexusSourceSynced(ctx, dbq.SetNexusSourceSyncedParams{LastSyncAt: now, ID: id}); err != nil {
			return err
		}
		return q.SetNexusSourceCredentials(ctx, dbq.SetNexusSourceCredentialsParams{
			BaseUrl: baseURL, Credentials: s.settings.Encrypt(aad(id), plain), ID: id})
	})
	if isUnique(err) {
		return nil, ErrExists
	}
	if err != nil {
		return nil, err
	}
	s.log.Info("nexus: source created", "source", in.Name, "base_url", baseURL)
	return s.Source(ctx, id)
}

// UpdateSource replaces the base URL or the credentials of a mirroring source after a
// connection check with the new values.
func (s *Service) UpdateSource(ctx context.Context, id int64, in SourceUpdate) (*Source, error) {
	row, err := sourceRow(dbq.New(s.db.R).GetNexusSource(ctx, id))
	if err != nil {
		return nil, err
	}
	if row.State != StateMirroring {
		return nil, ErrCutOver
	}
	var creds Credentials
	if in.Username == nil || in.Password == nil {
		plain, err := s.settings.Decrypt(aad(id), row.Credentials)
		if err == nil {
			err = json.Unmarshal(plain, &creds)
		}
		if err != nil {
			return nil, fmt.Errorf("%w: the stored credentials cannot be read; give username and password", ErrInvalid)
		}
	}
	if in.Username != nil {
		creds.Username = *in.Username
	}
	if in.Password != nil {
		creds.Password = *in.Password
	}
	baseURL := row.BaseUrl
	if in.BaseURL != nil {
		baseURL = *in.BaseURL
	}
	if baseURL, err = check(ctx, baseURL, creds); err != nil {
		return nil, err
	}
	plain, err := json.Marshal(creds)
	if err != nil {
		return nil, err
	}
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		cur, err := sourceRow(q.GetNexusSource(ctx, id))
		if err != nil {
			return err
		}
		if cur.State != StateMirroring {
			return ErrCutOver
		}
		return q.SetNexusSourceCredentials(ctx, dbq.SetNexusSourceCredentialsParams{
			BaseUrl: baseURL, Credentials: s.settings.Encrypt(aad(id), plain), ID: id})
	})
	if err != nil {
		return nil, err
	}
	s.log.Info("nexus: source connection changed", "source", row.Name, "base_url", baseURL)
	return s.Source(ctx, id)
}

// toSource converts a row and adds its newest sync.
func (s *Service) toSource(ctx context.Context, row dbq.NexusSource) (*Source, error) {
	src := &Source{ID: row.ID, Name: row.Name, BaseURL: row.BaseUrl, State: row.State,
		CreatedAt: time.Unix(row.CreatedAt, 0).UTC(), LastError: row.LastError,
		CutoverSyncID: row.CutoverSyncID, CutoverFailed: row.CutoverFailed}
	last, err := dbq.New(s.db.R).GetLatestNexusSync(ctx, row.ID)
	switch {
	case err == nil:
		if src.LastSync, err = s.toSync(ctx, last, false); err != nil {
			return nil, err
		}
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}
	return src, nil
}

// Source returns source id.
func (s *Service) Source(ctx context.Context, id int64) (*Source, error) {
	row, err := sourceRow(dbq.New(s.db.R).GetNexusSource(ctx, id))
	if err != nil {
		return nil, err
	}
	return s.toSource(ctx, row)
}

// Sources returns a keyset page of the sources by name after name after. next is the cursor of
// the next page, "" on the last page.
func (s *Service) Sources(ctx context.Context, after string, limit int) (out []Source, next string, err error) {
	rows, err := dbq.New(s.db.R).ListNexusSources(ctx, dbq.ListNexusSourcesParams{After: after, Limit: int64(limit)})
	if err != nil {
		return nil, "", err
	}
	out = make([]Source, 0, len(rows))
	for _, row := range rows {
		src, err := s.toSource(ctx, row)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *src)
	}
	if len(rows) == limit && limit > 0 {
		next = rows[len(rows)-1].Name
	}
	return out, next, nil
}

// Detail returns source id with every repo of its Nexus server and the mapping. When Nexus
// cannot be asked, the list holds the mapped repos and NexusError says why.
func (s *Service) Detail(ctx context.Context, id int64) (*SourceDetail, error) {
	row, err := sourceRow(dbq.New(s.db.R).GetNexusSource(ctx, id))
	if err != nil {
		return nil, err
	}
	src, err := s.toSource(ctx, row)
	if err != nil {
		return nil, err
	}
	d := &SourceDetail{Source: *src, Repos: []SourceRepo{}}
	mapped, err := dbq.New(s.db.R).ListNexusSourceRepos(ctx, id)
	if err != nil {
		return nil, err
	}
	targets := make(map[string]string, len(mapped))
	for _, m := range mapped {
		t, err := s.repos.ByID(ctx, m.TargetRepoID)
		if err != nil {
			return nil, err
		}
		targets[m.SourceRepo] = t.Name
	}

	var listed []Repository
	if cl, err := s.client(row); errors.Is(err, ErrCutOver) {
		d.NexusError = "the source is cut over; its credentials are deleted"
	} else if err != nil {
		d.NexusError = err.Error()
	} else if listed, err = cl.Repositories(ctx); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		d.NexusError = err.Error()
	}
	seen := make(map[string]bool, len(listed))
	for _, r := range listed {
		seen[r.Name] = true
		tf, reason := support(r.Format, r.Type)
		d.Repos = append(d.Repos, SourceRepo{Name: r.Name, Format: r.Format, Type: r.Type,
			Supported: reason == "", Reason: reason, TargetFormat: tf, Target: targets[r.Name]})
	}
	for _, m := range mapped {
		if seen[m.SourceRepo] {
			continue
		}
		tf, reason := support(m.SourceFormat, "hosted")
		if d.NexusError == "" {
			reason = "Nexus no longer lists this repo"
		}
		d.Repos = append(d.Repos, SourceRepo{Name: m.SourceRepo, Format: m.SourceFormat,
			Supported: reason == "", Reason: reason, TargetFormat: tf, Target: targets[m.SourceRepo]})
	}
	slices.SortFunc(d.Repos, func(a, b SourceRepo) int { return strings.Compare(a.Name, b.Name) })
	return d, nil
}

// SetRepos replaces the mapping of a mirroring source. It checks every mapping against the
// Nexus server, creates missing targets that ask for it, locks the new targets (uploads answer
// 409) and unlocks the targets it no longer maps. A target mirrors at most one source.
func (s *Service) SetRepos(ctx context.Context, id int64, maps []Mapping) (*SourceDetail, error) {
	seen := map[string]bool{}
	for _, m := range maps {
		if m.SourceRepo == "" {
			return nil, fmt.Errorf("%w: source_repo is required", ErrInvalid)
		}
		if seen[m.SourceRepo] {
			return nil, fmt.Errorf("%w: source repo %q is mapped twice", ErrInvalid, m.SourceRepo)
		}
		seen[m.SourceRepo] = true
		if !repos.NameRE.MatchString(m.Target) {
			return nil, fmt.Errorf("%w: invalid target repo name %q", ErrInvalid, m.Target)
		}
	}
	q := dbq.New(s.db.R)
	row, err := sourceRow(q.GetNexusSource(ctx, id))
	if err != nil {
		return nil, err
	}
	if _, err := q.GetRunningNexusSync(ctx, id); err == nil {
		return nil, ErrRunning
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	cl, err := s.client(row)
	if err != nil {
		return nil, err
	}
	src, err := cl.Repositories(ctx)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]Repository, len(src))
	for _, r := range src {
		byName[r.Name] = r
	}

	// Check every mapping before creating any repo.
	type plan struct {
		m      Mapping
		src    Repository
		target *repos.Repo // nil: create with format f
		f      format.Format
	}
	plans := make([]plan, 0, len(maps))
	for _, m := range maps {
		r, ok := byName[m.SourceRepo]
		if !ok {
			return nil, fmt.Errorf("%w: Nexus has no repo %q", ErrInvalid, m.SourceRepo)
		}
		if _, reason := support(r.Format, r.Type); reason != "" {
			return nil, fmt.Errorf("%w: %s: %s", ErrInvalid, r.Name, reason)
		}
		p := plan{m: m, src: r}
		t, err := s.repos.ByName(ctx, m.Target)
		switch {
		case err == nil:
			if _, ok := accepts(t.Format, r.Format); !ok {
				return nil, fmt.Errorf("%w: target repo %q (%s) cannot import %s content", ErrInvalid, t.Name, t.Format, r.Format)
			}
			if other, err := q.GetNexusSourceByTarget(ctx, t.ID); err == nil && other.ID != id {
				return nil, fmt.Errorf("%w: target repo %q already mirrors Nexus source %s", ErrInvalid, t.Name, other.Name)
			} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			p.target = t
		case errors.Is(err, repos.ErrNotFound):
			if !m.Create {
				return nil, fmt.Errorf("%w: target repo %q does not exist", ErrInvalid, m.Target)
			}
			p.f, _ = importerFor(r.Format)
		default:
			return nil, err
		}
		plans = append(plans, p)
	}
	// Two source repos may create the same target only with the same format.
	created := map[string]*repos.Repo{}
	for i := range plans {
		p := &plans[i]
		if p.target != nil {
			// An existing target without a distribution takes the source's, so that the
			// plugin knows where imported packages go. A distribution it has stays.
			cfg, changed, err := withDistribution(p.target.Config, p.src.Distribution, false)
			if err != nil {
				return nil, fmt.Errorf("target repo %q: %w", p.target.Name, err)
			}
			if changed {
				if _, p.target, err = s.repos.Update(ctx, p.target.Name, nil, cfg); err != nil {
					return nil, fmt.Errorf("set the distribution of target repo %q: %w", p.target.Name, err)
				}
				s.log.Info("nexus: set target repo distribution", "repo", p.target.Name, "distribution", p.src.Distribution)
			}
			continue
		}
		if t := created[p.m.Target]; t != nil {
			if _, ok := accepts(t.Format, p.src.Format); !ok {
				return nil, fmt.Errorf("%w: target repo %q (%s) cannot import %s content", ErrInvalid, t.Name, t.Format, p.src.Format)
			}
			p.target = t
			continue
		}
		cfg, err := targetConfig(p.f, p.src)
		if err != nil {
			return nil, err
		}
		t, err := s.repos.Create(ctx, p.m.Target, p.f.Name(), false, "", cfg)
		if err != nil {
			return nil, fmt.Errorf("create target repo %q: %w", p.m.Target, err)
		}
		s.log.Info("nexus: created target repo", "repo", t.Name, "format", t.Format)
		created[t.Name] = t
		p.target = t
	}

	lock := lockText(row.Name)
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		cur, err := sourceRow(q.GetNexusSource(ctx, id))
		if err != nil {
			return err
		}
		if cur.State != StateMirroring {
			return ErrCutOver
		}
		if _, err := q.GetRunningNexusSync(ctx, id); err == nil {
			return ErrRunning
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		old, err := q.ListNexusSourceRepos(ctx, id)
		if err != nil {
			return err
		}
		if err := q.DeleteNexusSourceRepos(ctx, id); err != nil {
			return err
		}
		keep := map[int64]bool{}
		for _, p := range plans {
			if other, err := q.GetNexusSourceByTarget(ctx, p.target.ID); err == nil {
				return fmt.Errorf("%w: target repo %q already mirrors Nexus source %s", ErrInvalid, p.target.Name, other.Name)
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			keep[p.target.ID] = true
		}
		for _, p := range plans {
			if err := q.InsertNexusSourceRepo(ctx, dbq.InsertNexusSourceRepoParams{SourceID: id,
				SourceRepo: p.src.Name, SourceFormat: p.src.Format, TargetRepoID: p.target.ID}); err != nil {
				return err
			}
			if err := s.repos.SetLockedBy(ctx, tx, p.target.ID, lock); err != nil {
				return err
			}
		}
		for _, o := range old {
			if !keep[o.TargetRepoID] {
				if err := s.repos.SetLockedBy(ctx, tx, o.TargetRepoID, ""); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.log.Info("nexus: source repos set", "source", row.Name, "repos", len(plans))
	return s.Detail(ctx, id)
}

// targetConfig is the config of a new target repo: the plugin's default config, plus
// "distribution" when the source repo has one.
func targetConfig(f format.Format, src Repository) (json.RawMessage, error) {
	cfg, _, err := withDistribution(f.DefaultConfig(), src.Distribution, true)
	if err != nil {
		return nil, fmt.Errorf("format %s: default config: %w", f.Name(), err)
	}
	return cfg, nil
}

// withDistribution returns cfg with "distribution" set to dist, and whether it changed. An
// empty dist changes nothing; without replace, a distribution cfg already has stays.
func withDistribution(cfg json.RawMessage, dist string, replace bool) (json.RawMessage, bool, error) {
	if dist == "" {
		return cfg, false, nil
	}
	obj := map[string]json.RawMessage{}
	if len(cfg) > 0 {
		if err := json.Unmarshal(cfg, &obj); err != nil {
			return nil, false, err
		}
	}
	if obj == nil { // cfg was JSON null
		obj = map[string]json.RawMessage{}
	}
	if _, ok := obj["distribution"]; ok && !replace {
		return cfg, false, nil
	}
	obj["distribution"], _ = json.Marshal(dist)
	out, err := json.Marshal(obj)
	return out, err == nil, err
}

// DeleteSource deletes a source with its mapping and its sync history. A mirroring source
// with mapped repos must cut over (or drop its repos) first; a running sync must end first.
func (s *Service) DeleteSource(ctx context.Context, id int64) error {
	var name string
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		row, err := sourceRow(q.GetNexusSource(ctx, id))
		if err != nil {
			return err
		}
		name = row.Name
		if _, err := q.GetRunningNexusSync(ctx, id); err == nil {
			return ErrRunning
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if row.State == StateMirroring {
			mapped, err := q.ListNexusSourceRepos(ctx, id)
			if err != nil {
				return err
			}
			if len(mapped) > 0 {
				return ErrMapped
			}
		}
		return q.DeleteNexusSource(ctx, id)
	})
	if err != nil {
		return err
	}
	s.log.Info("nexus: source deleted", "source", name)
	return nil
}
