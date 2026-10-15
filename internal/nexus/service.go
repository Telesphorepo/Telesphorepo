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
	"github.com/Telesphorepo/Telesphorepo/internal/signing"
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

// Source statuses (Source.Status), derived from the state, the syncs and the mapping.
const (
	StatusNoRepos  = "no_repos"
	StatusSyncing  = "syncing"
	StatusUpToDate = "up_to_date"
	StatusFailed   = "failed"
	StatusCutOver  = "cut_over"
)

// Nexus repo types (Repository.Type, nexus_source_repos.kind).
const (
	TypeHosted = "hosted"
	TypeProxy  = "proxy"
	TypeGroup  = "group"
)

// Signing key choices of a new apt target (Mapping.SigningKey).
const (
	KeyNew   = "new"
	KeyNexus = "nexus"
)

// offlineReason is the reason a repo that Nexus marks offline cannot be copied.
const offlineReason = "Offline in Nexus. Bring it online in Nexus to copy it."

// publicTTL is how long the result of an anonymous check of a Nexus repo is kept.
const publicTTL = 5 * time.Minute

// probeParallel is the number of anonymous checks that run at the same time.
const probeParallel = 8

// Service manages Nexus sources, runs their syncs and cuts them over. Syncs of different
// sources run in parallel; one sync runs at a time per source.
type Service struct {
	db       *db.DB
	settings *settings.Service
	repos    *repos.Service
	keys     *signing.Keys
	log      *slog.Logger

	mu   sync.Mutex
	root context.Context     // the server context, set by Start
	runs map[int64]*runState // the syncs this process runs, by sync id
	wg   sync.WaitGroup      // the runner and scheduler goroutines of this process

	pubMu  sync.Mutex
	public map[string]publicCheck // "<source id>/<repo>" -> the last anonymous check
}

type publicCheck struct {
	public bool
	at     time.Time
}

// New returns the Nexus service. Call Start before the API serves requests.
func New(d *db.DB, set *settings.Service, rs *repos.Service, keys *signing.Keys, log *slog.Logger) *Service {
	return &Service{db: d, settings: set, repos: rs, keys: keys, log: log, runs: map[int64]*runState{},
		public: map[string]publicCheck{}}
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
	// Status is one of the Status* constants.
	Status string `json:"status"`
	// NextSyncAt is when the scheduler syncs next; nil when none is due: no repos, cut over,
	// or a sync runs now.
	NextSyncAt *time.Time `json:"next_sync_at"`
	// RunningSyncID is the sync that runs now; 0 when none.
	RunningSyncID int64 `json:"running_sync_id"`
	// RepoCount is the number of mapped repos (hosted, proxy and group).
	RepoCount int `json:"repo_count"`
	// SyncInterval is the live nexus.sync_interval in seconds.
	SyncInterval int64 `json:"sync_interval"`
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
// format ("" when none does). Supported is true for a repo that can be copied; otherwise Reason
// says why not. Target is the repo it is mapped to ("" when not mapped).
type SourceRepo struct {
	Name         string `json:"name"`
	Format       string `json:"format"`
	Type         string `json:"type"` // "" for a mapped repo that Nexus did not list
	Supported    bool   `json:"supported"`
	Reason       string `json:"reason"`
	Offline      bool   `json:"offline"`
	Public       bool   `json:"public"` // anyone can download its files in Nexus
	TargetFormat string `json:"target_format"`
	Target       string `json:"target"`
	// A proxy's remote, and the username Nexus sends to it.
	RemoteURL      string `json:"remote_url"`
	RemoteUsername string `json:"remote_username"`
	// A group's Nexus members in Nexus order, whether Nexus gave them, and the members of the
	// mapped group here (as Nexus repo names where a member is a mapped repo).
	Members       []string `json:"members"`
	MembersKnown  bool     `json:"members_known"`
	TargetMembers []string `json:"target_members"`
	// NexusKey is true when Nexus gives the signing key pair of this apt repo;
	// NexusKeyEncrypted when that key pair needs a passphrase, which Nexus never gives.
	NexusKey          bool `json:"nexus_key"`
	NexusKeyEncrypted bool `json:"nexus_key_encrypted"`
}

// Mapping maps one Nexus repo to a target repo. With Create, a missing target is created: a
// hosted repo with the plugin's default config plus the Nexus repo's distribution, a proxy
// with the Nexus remote, or a group of the picked members. AnonymousRead, the signing key and
// the remote credentials apply only to a target that the save creates.
type Mapping struct {
	SourceRepo    string `json:"source_repo"`
	Target        string `json:"target"`
	Create        bool   `json:"create"`
	AnonymousRead bool   `json:"anonymous_read"`
	// SigningKey of a new apt hosted target: KeyNew (the default key here; also "") or KeyNexus
	// (the Nexus key pair, opened with SigningPassphrase).
	SigningKey        string `json:"signing_key"`
	SigningPassphrase string `json:"signing_passphrase"`
	// The remote credentials of a new proxy target.
	RemoteUsername string `json:"remote_username"`
	RemotePassword string `json:"remote_password"`
	// Members of a group whose members Nexus did not give: Nexus repo names picked by hand.
	Members []string `json:"members"`
}

// SetReposInput is the body of PUT /api/nexus/sources/{id}/repos.
type SetReposInput struct {
	Repos []Mapping `json:"repos"`
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

// support returns the plugin name that takes Nexus repo r, and why r cannot be copied ("" when
// it can).
func support(r Repository) (targetFormat, reason string) {
	f, _ := importerFor(r.Format)
	if f == nil {
		return "", "This server does not take " + r.Format + " repositories."
	}
	switch r.Type {
	case TypeHosted:
	case TypeProxy:
		if format.SupportsKind(f.Name(), repos.KindProxy) != nil {
			return f.Name(), "This server has no " + f.Title() + " proxies."
		}
	case TypeGroup:
		if format.SupportsKind(f.Name(), repos.KindGroup) != nil {
			return f.Name(), "This server has no " + f.Title() + " groups."
		}
	default:
		return f.Name(), "This server cannot copy a Nexus " + r.Type + " repository."
	}
	if !r.Online {
		return f.Name(), offlineReason
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
// userID 0 means unknown. The source has no repos yet, so the scheduler does not sync it.
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
	var id int64
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		var err error
		id, err = q.CreateNexusSource(ctx, dbq.CreateNexusSourceParams{Name: in.Name, BaseUrl: baseURL,
			CreatedBy: sql.NullInt64{Int64: userID, Valid: userID != 0}, CreatedAt: time.Now().Unix()})
		if err != nil {
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

// toSource converts a row, adds its newest sync and derives its status.
func (s *Service) toSource(ctx context.Context, row dbq.NexusSource) (*Source, error) {
	interval := s.interval()
	src := &Source{ID: row.ID, Name: row.Name, BaseURL: row.BaseUrl, State: row.State,
		CreatedAt: time.Unix(row.CreatedAt, 0).UTC(), LastError: row.LastError,
		CutoverSyncID: row.CutoverSyncID, CutoverFailed: row.CutoverFailed,
		SyncInterval: int64(interval / time.Second)}
	q := dbq.New(s.db.R)
	last, err := q.GetLatestNexusSync(ctx, row.ID)
	switch {
	case err == nil:
		if src.LastSync, err = s.toSync(ctx, dbq.GetNexusSyncRow(last), false); err != nil {
			return nil, err
		}
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}
	mapped, err := q.ListNexusSourceRepos(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	src.RepoCount = len(mapped)
	// One sync runs at a time per source, so a running sync is the newest one.
	if src.LastSync != nil && src.LastSync.State == SyncRunning {
		src.RunningSyncID = src.LastSync.ID
	}
	switch {
	case row.State == StateCutOver:
		src.Status = StatusCutOver
	case src.RunningSyncID != 0:
		src.Status = StatusSyncing
	case len(mapped) == 0:
		src.Status = StatusNoRepos
	case row.LastError != "":
		src.Status = StatusFailed
	default:
		src.Status = StatusUpToDate
	}
	if row.State == StateMirroring && len(mapped) > 0 && src.RunningSyncID == 0 {
		next := time.Now().UTC()
		if row.LastSyncAt > 0 {
			next = time.Unix(row.LastSyncAt, 0).Add(interval).UTC()
		}
		src.NextSyncAt = &next
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
// cannot be asked, the list holds the mapped repos and NexusError says why. Each supported repo
// says whether anyone can download it in Nexus (an anonymous request; kept for publicTTL).
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
	targets := make(map[string]*repos.Repo, len(mapped))
	sourceOf := make(map[int64]string, len(mapped)) // target repo id -> Nexus repo name
	for _, m := range mapped {
		t, err := s.repos.ByID(ctx, m.TargetRepoID)
		if err != nil {
			return nil, err
		}
		targets[m.SourceRepo] = t
		sourceOf[t.ID] = m.SourceRepo
	}

	var listed []Repository
	cl, err := s.client(row)
	switch {
	case errors.Is(err, ErrCutOver):
		d.NexusError = "the source is cut over; its credentials are deleted"
	case err != nil:
		d.NexusError = err.Error()
	default:
		if listed, err = cl.Repositories(ctx); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			d.NexusError = err.Error()
		}
	}
	seen := make(map[string]bool, len(listed))
	for _, r := range listed {
		seen[r.Name] = true
		tf, reason := support(r)
		sr := SourceRepo{Name: r.Name, Format: r.Format, Type: r.Type, Supported: reason == "", Reason: reason,
			Offline: !r.Online, TargetFormat: tf, RemoteURL: r.RemoteURL, RemoteUsername: r.RemoteUsername,
			Members: r.Members, MembersKnown: r.MembersKnown,
			NexusKey: r.Type == TypeHosted && r.HasKey(), NexusKeyEncrypted: r.Type == TypeHosted && r.KeyEncrypted}
		if sr.Members == nil {
			sr.Members = []string{}
		}
		d.Repos = append(d.Repos, sr)
	}
	for _, m := range mapped {
		if seen[m.SourceRepo] {
			continue
		}
		tf, reason := support(Repository{Format: m.SourceFormat, Type: m.Kind, Online: true})
		if d.NexusError == "" {
			reason = "Nexus no longer lists this repository."
		}
		d.Repos = append(d.Repos, SourceRepo{Name: m.SourceRepo, Format: m.SourceFormat,
			Supported: reason == "", Reason: reason, TargetFormat: tf, Members: []string{}})
	}
	for i := range d.Repos {
		r := &d.Repos[i]
		t := targets[r.Name]
		if t == nil {
			continue
		}
		r.Target = t.Name
		if t.Kind == repos.KindGroup {
			members, err := s.repos.MemberRepos(ctx, t)
			if err != nil {
				return nil, err
			}
			r.TargetMembers = make([]string, 0, len(members))
			for _, m := range members {
				name, ok := sourceOf[m.ID]
				if !ok {
					name = m.Name
				}
				r.TargetMembers = append(r.TargetMembers, name)
			}
		}
	}
	if cl != nil {
		s.checkPublic(ctx, id, cl, d.Repos)
	}
	slices.SortFunc(d.Repos, func(a, b SourceRepo) int {
		if a.Supported != b.Supported {
			if a.Supported {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return d, nil
}

// checkPublic sets Public of each supported repo from an anonymous request to Nexus, at most
// probeParallel at a time. A result is kept for publicTTL; a failed check counts as private and
// is not kept.
func (s *Service) checkPublic(ctx context.Context, sourceID int64, cl *Client, rs []SourceRepo) {
	now := time.Now()
	sem := make(chan struct{}, probeParallel)
	var wg sync.WaitGroup
	for i := range rs {
		r := &rs[i]
		if !r.Supported || r.Type == "" {
			continue
		}
		key := strconv.FormatInt(sourceID, 10) + "/" + r.Name
		s.pubMu.Lock()
		c, ok := s.public[key]
		s.pubMu.Unlock()
		if ok && now.Sub(c.at) < publicTTL {
			r.Public = c.public
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			pub, err := cl.Public(ctx, r.Name)
			if err != nil {
				if ctx.Err() == nil {
					s.log.Warn("nexus: cannot check whether a repo is public", "source_id", sourceID, "source_repo", r.Name, "error", err.Error())
				}
				return
			}
			r.Public = pub
			s.pubMu.Lock()
			s.public[key] = publicCheck{public: pub, at: time.Now()}
			s.pubMu.Unlock()
		}()
	}
	wg.Wait()
}

// plan is one checked mapping of SetRepos.
type plan struct {
	m      Mapping
	src    Repository
	f      format.Format // the plugin of the target
	target *repos.Repo   // nil until created
}

// SetRepos replaces the mapping of a mirroring source and starts a sync of it (trigger save)
// when the new mapping holds a repo. It checks every mapping against the Nexus server, creates
// the missing targets that ask for it (hosted repos, proxies with the Nexus remote, groups of
// the picked members), locks the new hosted targets (uploads answer 409) and unlocks the
// targets it no longer maps. A target mirrors at most one source. userID 0 means unknown.
func (s *Service) SetRepos(ctx context.Context, id, userID int64, maps []Mapping) (*SourceDetail, error) {
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
		if m.SigningKey != "" && m.SigningKey != KeyNew && m.SigningKey != KeyNexus {
			return nil, fmt.Errorf("%w: signing_key must be %q or %q", ErrInvalid, KeyNew, KeyNexus)
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
	plans := make([]plan, 0, len(maps))
	for _, m := range maps {
		r, ok := byName[m.SourceRepo]
		if !ok {
			return nil, fmt.Errorf("%w: Nexus has no repo %q", ErrInvalid, m.SourceRepo)
		}
		if _, reason := support(r); reason != "" {
			return nil, fmt.Errorf("%w: %s: %s", ErrInvalid, r.Name, reason)
		}
		p := plan{m: m, src: r}
		p.f, _ = importerFor(r.Format)
		t, err := s.repos.ByName(ctx, m.Target)
		switch {
		case err == nil:
			if t.Kind != r.Type {
				return nil, fmt.Errorf("%w: %s is a %s repository here; the Nexus repository %s is a %s", ErrInvalid, t.Name, t.Kind, r.Name, r.Type)
			}
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
		default:
			return nil, err
		}
		plans = append(plans, p)
	}
	// The members of each group: the picked Nexus members in Nexus order.
	groupMembers := map[string][]string{} // group source repo -> member source repos
	for _, p := range plans {
		if p.src.Type != TypeGroup {
			continue
		}
		names := p.src.Members
		if !p.src.MembersKnown {
			names = p.m.Members
		}
		var picked []string
		for _, n := range names {
			if seen[n] && byName[n].Type != TypeGroup {
				picked = append(picked, n)
			}
		}
		if len(picked) == 0 {
			return nil, fmt.Errorf("%w: pick at least one member of the group %s", ErrInvalid, p.src.Name)
		}
		groupMembers[p.src.Name] = picked
	}

	// Create the hosted and proxy targets first: the groups name them.
	created := map[string]*repos.Repo{}
	targetOf := map[string]string{} // source repo -> target name
	for i := range plans {
		p := &plans[i]
		targetOf[p.src.Name] = p.m.Target
		if p.src.Type == TypeGroup {
			continue
		}
		if err := s.prepare(ctx, p, created); err != nil {
			return nil, err
		}
	}
	for i := range plans {
		p := &plans[i]
		if p.src.Type != TypeGroup {
			continue
		}
		var members []string
		for _, n := range groupMembers[p.src.Name] {
			members = append(members, targetOf[n])
		}
		if p.target != nil {
			if p.target, err = s.repos.SetMembers(ctx, p.target.Name, members); err != nil {
				return nil, fmt.Errorf("set the members of group %q: %w", p.m.Target, err)
			}
			continue
		}
		if p.target, err = s.repos.CreateGroup(ctx, p.m.Target, p.f.Name(), p.m.AnonymousRead, "", members); err != nil {
			return nil, fmt.Errorf("create group %q: %w", p.m.Target, err)
		}
		s.log.Info("nexus: created group", "repo", p.target.Name, "format", p.target.Format, "members", len(members))
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
				SourceRepo: p.src.Name, SourceFormat: p.src.Format, Kind: p.src.Type, TargetRepoID: p.target.ID}); err != nil {
				return err
			}
			if p.src.Type == TypeHosted {
				if err := s.repos.SetLockedBy(ctx, tx, p.target.ID, lock); err != nil {
					return err
				}
			}
		}
		for _, o := range old {
			if !keep[o.TargetRepoID] && o.Kind == TypeHosted {
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
	if len(plans) > 0 {
		if _, err := s.startSync(ctx, id, userID, TriggerSave); err != nil {
			return nil, err
		}
	}
	return s.Detail(ctx, id)
}

// prepare finds or creates the target of a hosted or proxy plan. Two Nexus repos may share a
// new hosted target only when its format imports both.
func (s *Service) prepare(ctx context.Context, p *plan, created map[string]*repos.Repo) error {
	if p.target != nil {
		if p.src.Type != TypeHosted {
			return nil
		}
		// An existing target without a distribution takes the source's, so that the plugin
		// knows where imported packages go. A distribution it has stays.
		cfg, changed, err := withDistribution(p.target.Config, p.src.Distribution, false)
		if err != nil {
			return fmt.Errorf("target repo %q: %w", p.target.Name, err)
		}
		if changed {
			if _, p.target, err = s.repos.Update(ctx, p.target.Name, nil, cfg); err != nil {
				return fmt.Errorf("set the distribution of target repo %q: %w", p.target.Name, err)
			}
			s.log.Info("nexus: set target repo distribution", "repo", p.target.Name, "distribution", p.src.Distribution)
		}
		return nil
	}
	if t := created[p.m.Target]; t != nil {
		if _, ok := accepts(t.Format, p.src.Format); !ok || t.Kind != p.src.Type {
			return fmt.Errorf("%w: target repo %q (%s) cannot take the Nexus repository %s", ErrInvalid, t.Name, t.Format, p.src.Name)
		}
		p.target = t
		return nil
	}
	cfg, err := targetConfig(p.f, p.src)
	if err != nil {
		return err
	}
	if p.src.Type == TypeProxy {
		in := repos.ProxyInput{RemoteURL: &p.src.RemoteURL}
		if p.src.MetadataTTL >= 0 {
			sec := int64(p.src.MetadataTTL / time.Second)
			in.MetadataTTL = &sec
		}
		if p.src.NotFoundTTL >= 0 {
			sec := int64(p.src.NotFoundTTL / time.Second)
			in.NotFoundTTL = &sec
		}
		if p.m.RemoteUsername != "" {
			in.Username, in.Password = &p.m.RemoteUsername, &p.m.RemotePassword
		}
		px, err := in.Apply(nil)
		if err != nil {
			return fmt.Errorf("proxy %q: %w", p.m.Target, err)
		}
		t, err := s.repos.CreateProxy(ctx, p.m.Target, p.f.Name(), p.m.AnonymousRead, "", cfg, px)
		if err != nil {
			return fmt.Errorf("create proxy %q: %w", p.m.Target, err)
		}
		s.log.Info("nexus: created proxy", "repo", t.Name, "format", t.Format, "remote", px.RemoteURL)
		created[t.Name], p.target = t, t
		return nil
	}
	if p.m.SigningKey == KeyNexus {
		key, err := s.nexusKey(ctx, p)
		if err != nil {
			return err
		}
		if cfg, err = withKey(cfg, key); err != nil {
			return err
		}
	}
	t, err := s.repos.Create(ctx, p.m.Target, p.f.Name(), p.m.AnonymousRead, "", cfg)
	if err != nil {
		return fmt.Errorf("create target repo %q: %w", p.m.Target, err)
	}
	s.log.Info("nexus: created target repo", "repo", t.Name, "format", t.Format)
	created[t.Name], p.target = t, t
	return nil
}

// nexusKey stores the signing key pair that Nexus gives for the apt repo of p as the signing
// key "nexus-<target>", opened with the mapping's passphrase, and returns its name. A key of
// that name with the same fingerprint is used again.
func (s *Service) nexusKey(ctx context.Context, p *plan) (string, error) {
	armored := p.src.keypair
	if armored == "" {
		return "", fmt.Errorf("%w: %s: Nexus does not give the signing key pair of this repository. Import it in Signing keys, then pick it in the repository settings", ErrInvalid, p.src.Name)
	}
	signer, err := signing.Parse([]byte(armored), []byte(p.m.SigningPassphrase))
	if err != nil {
		return "", fmt.Errorf("%w: %s: the passphrase does not open the Nexus signing key", ErrInvalid, p.src.Name)
	}
	name := "nexus-" + p.m.Target
	if len(name) > 64 {
		name = name[:64]
	}
	if k, ok := s.keys.Get(name); ok {
		if k.Fingerprint == signer.Fingerprint() {
			return name, nil
		}
		return "", fmt.Errorf("%w: a different signing key named %s exists; delete it or rename the repository", ErrInvalid, name)
	}
	if _, err := s.keys.Import(ctx, name, armored, p.m.SigningPassphrase); err != nil {
		return "", fmt.Errorf("import the Nexus signing key of %s: %w", p.src.Name, err)
	}
	s.log.Info("nexus: imported signing key", "key", name, "source_repo", p.src.Name)
	return name, nil
}

// withKey returns cfg with "signing_key" set to name.
func withKey(cfg json.RawMessage, name string) (json.RawMessage, error) {
	obj := map[string]json.RawMessage{}
	if len(cfg) > 0 {
		if err := json.Unmarshal(cfg, &obj); err != nil {
			return nil, err
		}
	}
	if obj == nil {
		obj = map[string]json.RawMessage{}
	}
	obj["signing_key"], _ = json.Marshal(name)
	return json.Marshal(obj)
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
