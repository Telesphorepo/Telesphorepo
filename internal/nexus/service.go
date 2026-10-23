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

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/nexus/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/ops"
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
	ErrSwitched   = errors.New("the repository has switched over; it no longer copies from Nexus")
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
	assets   *assets.Pipeline
	ops      *ops.Service
	formats  *format.Registry
	audit    *audit.Log
	log      *slog.Logger
	username func(ctx context.Context, id int64) (string, error) // set before the API serves
	auth     *auth.Service                                       // the grants of rehearsal repos; set before the API serves

	// spawn runs a sync as a task of the nexus module, so the server waits for it before the
	// database closes; base is the module's context.
	spawn func(name string, fn func(ctx context.Context))
	base  context.Context

	mu      sync.Mutex
	started bool                // Resume ran: syncs may start
	runs    map[int64]*runState // the syncs this process runs, by sync id

	pubMu  sync.Mutex
	public map[string]publicCheck // "<source id>/<repo>" -> the last anonymous check

	rehMu sync.RWMutex
	reh   map[int64]int64 // rehearsal repo id -> the id of the repo it rehearses (repo_rehearsals)
}

type publicCheck struct {
	public bool
	at     time.Time
}

// New returns the Nexus service. spawn runs a sync under base, the context of the nexus module.
// Resume runs before the API serves requests.
func New(d *db.DB, set *settings.Service, rs *repos.Service, as *assets.Pipeline, os *ops.Service, formats *format.Registry,
	au *audit.Log, log *slog.Logger, base context.Context, spawn func(name string, fn func(ctx context.Context))) *Service {
	return &Service{db: d, settings: set, repos: rs, assets: as, ops: os, formats: formats, audit: au, log: log, base: base, spawn: spawn,
		runs: map[int64]*runState{}, public: map[string]publicCheck{}, reh: map[int64]int64{}}
}

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
	// Copying is the number of mapped hosted repos that still copy from Nexus, SwitchedOver the
	// number that switched over. The source is cut over when none copies any more.
	Copying      int `json:"copying"`
	SwitchedOver int `json:"switched_over"`
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

// SourceRepo is one repo of the Nexus server. TargetFormat is the format that imports its
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
	// Questions are what the target format asks before it creates a target of this repo, and
	// Facts what it read from the repo's settings (format.Importer Plan).
	Questions []format.Question `json:"questions"`
	Facts     map[string]any    `json:"facts"`
	// SwitchOver is the switch-over of a mapped hosted repo; nil while it copies from Nexus (and
	// for a repo that is not mapped, a proxy or a group).
	SwitchOver *SwitchOver `json:"switch_over"`
	// Rehearsals are the names of the rehearsal repos of the target, by creation.
	Rehearsals []string `json:"rehearsals"`
}

// SwitchOver is the switch-over of one mapped hosted repo: its final sync, the files that sync
// failed to import (GET /api/nexus/syncs/{id}/failures lists them) and when it switched over.
type SwitchOver struct {
	SyncID     int64     `json:"sync_id"`
	Failed     int64     `json:"failed"`
	SwitchedAt time.Time `json:"switched_at"`
}

// Mapping maps one Nexus repo to a target repo. With Create, a missing target is created: a
// hosted repo with the config that the target format plans, a proxy with the Nexus remote, or a
// group of the picked members. AnonymousRead, the answers and the remote credentials apply only
// to a target that the save creates.
type Mapping struct {
	SourceRepo    string `json:"source_repo"`
	Target        string `json:"target"`
	Create        bool   `json:"create"`
	AnonymousRead bool   `json:"anonymous_read"`
	// Answers answer the questions of the target format (SourceRepo.Questions), by key.
	Answers map[string]string `json:"answers"`
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

// OpMirror is the repo operation of a hosted repo that a Nexus source mirrors: writes answer 409
// until the cut-over. Its ref is the source ID.
const OpMirror = "mirror"

// mirrorRef is the ref of the mirror operation of source id.
func mirrorRef(id int64) string { return strconv.FormatInt(id, 10) }

// accepts returns the importer of target format name when it imports sourceFormat.
func (s *Service) accepts(name, sourceFormat string) (*format.Importer, bool) {
	f := s.formats.Get(name)
	if f == nil || f.Importer == nil || !slices.Contains(f.Importer.Sources, sourceFormat) {
		return nil, false
	}
	return f.Importer, true
}

// support returns the format that takes Nexus repo r, and why r cannot be copied ("" when it
// can).
func (s *Service) support(r Repository) (targetFormat, reason string) {
	f := s.formats.Importer(r.Format)
	if f == nil {
		return "", "This server does not take " + r.Format + " repositories."
	}
	switch r.Type {
	case TypeHosted:
	case TypeProxy:
		if !f.Takes(repos.KindProxy) {
			return f.Name, "This server has no " + f.Title + " proxies."
		}
	case TypeGroup:
		if !f.Takes(repos.KindGroup) {
			return f.Name, "This server has no " + f.Title + " groups."
		}
	default:
		return f.Name, "This server cannot copy a Nexus " + r.Type + " repository."
	}
	if !r.Online {
		return f.Name, offlineReason
	}
	return f.Name, ""
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
	s.record(ctx, ActionSourceCreate, in.Name, map[string]any{"id": id, "base_url": baseURL})
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
	s.record(ctx, ActionSourceUpdate, row.Name, map[string]any{"id": id, "base_url": baseURL})
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
	switched, err := q.ListNexusSwitchOvers(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	src.SwitchedOver = len(switched)
	for _, m := range mapped {
		if m.Kind == TypeHosted {
			src.Copying++
		}
	}
	src.Copying -= len(switched)
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
		tf, reason := s.support(r)
		sr := SourceRepo{Name: r.Name, Format: r.Format, Type: r.Type, Supported: reason == "", Reason: reason,
			Offline: !r.Online, TargetFormat: tf, RemoteURL: r.RemoteURL, RemoteUsername: r.RemoteUsername,
			Members: r.Members, MembersKnown: r.MembersKnown, TargetMembers: []string{},
			Questions: []format.Question{}, Facts: map[string]any{}}
		if sr.Members == nil {
			sr.Members = []string{}
		}
		if f := s.formats.Get(tf); reason == "" && f != nil && f.Importer != nil && f.Importer.Plan != nil {
			tp, err := f.Importer.Plan(ctx, sourceRepo(r), targets[r.Name], nil)
			if err != nil {
				return nil, fmt.Errorf("plan %s: %w", r.Name, err)
			}
			if tp.Questions != nil {
				sr.Questions = tp.Questions
			}
			if tp.Facts != nil {
				sr.Facts = tp.Facts
			}
		}
		d.Repos = append(d.Repos, sr)
	}
	for _, m := range mapped {
		if seen[m.SourceRepo] {
			continue
		}
		tf, reason := s.support(Repository{Format: m.SourceFormat, Type: m.Kind, Online: true})
		if d.NexusError == "" {
			reason = "Nexus no longer lists this repository."
		}
		d.Repos = append(d.Repos, SourceRepo{Name: m.SourceRepo, Format: m.SourceFormat,
			Supported: reason == "", Reason: reason, TargetFormat: tf, Members: []string{}, TargetMembers: []string{},
			Questions: []format.Question{}, Facts: map[string]any{}})
	}
	switched, err := s.switchOvers(ctx, id)
	if err != nil {
		return nil, err
	}
	for i := range d.Repos {
		r := &d.Repos[i]
		r.Rehearsals = []string{}
		t := targets[r.Name]
		if t == nil {
			continue
		}
		r.Target = t.Name
		r.SwitchOver = switched[r.Name]
		if t.Kind == repos.KindHosted {
			if r.Rehearsals, err = s.rehearsalsOf(ctx, t.ID); err != nil {
				return nil, err
			}
		}
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
	f      *format.Spec       // the format of the target
	target *repos.Repo        // nil until created
	tp     *format.TargetPlan // what the target format plans; nil when it has no Plan
}

// sourceRepo is r as the importer of a target format sees it.
func sourceRepo(r Repository) format.SourceRepo {
	return format.SourceRepo{Name: r.Name, Format: r.Format, Kind: r.Type, Attributes: r.Attributes}
}

// planTarget asks the target format of p to plan the target with the mapping's answers.
func (p *plan) planTarget(ctx context.Context) error {
	im := p.f.Importer
	if im == nil || im.Plan == nil {
		return nil
	}
	t := p.target
	if t == nil {
		t = &repos.Repo{Name: p.m.Target, Format: p.f.Name, Kind: p.src.Type}
	}
	answers := p.m.Answers
	if answers == nil {
		answers = map[string]string{}
	}
	tp, err := im.Plan(ctx, sourceRepo(p.src), t, answers)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrInvalid, p.src.Name, err)
	}
	p.tp = tp
	return nil
}

// config is the config of a new target: the planned one, or the format's default.
func (p *plan) config() json.RawMessage {
	if p.tp != nil && p.tp.Config != nil {
		return p.tp.Config
	}
	return p.f.Config.Default
}

// fields are the planned field values of the target.
func (p *plan) fields() map[string]json.RawMessage {
	if p.tp == nil {
		return nil
	}
	return p.tp.Fields
}

// SetRepos replaces the mapping of a mirroring source and starts a sync of it (trigger save)
// when the new mapping holds a repo. It checks every mapping against the Nexus server, creates
// the missing targets that ask for it (hosted repos, proxies with the Nexus remote, groups of
// the picked members), locks the new hosted targets (uploads answer 409) and unlocks the
// targets it no longer maps. A target mirrors at most one source. A repo that switched over
// keeps its mapping and stays unlocked, whatever maps holds. userID 0 means unknown.
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
	// A switched-over repo keeps its mapping whatever the save holds: it cannot copy again.
	kept, err := s.switchedMappings(ctx, id)
	if err != nil {
		return nil, err
	}
	keptTargets := map[int64]string{} // target repo id -> target name
	targetOf := map[string]string{}   // source repo -> target name
	for name, k := range kept {
		t, err := s.repos.ByID(ctx, k.TargetRepoID)
		if err != nil {
			return nil, err
		}
		keptTargets[t.ID], targetOf[name] = t.Name, t.Name
		seen[name] = true
	}

	// Check every mapping before creating any repo.
	plans := make([]plan, 0, len(maps))
	for _, m := range maps {
		if _, ok := kept[m.SourceRepo]; ok {
			if targetOf[m.SourceRepo] != m.Target {
				return nil, fmt.Errorf("%w: %s has switched over into %s; it keeps that repository", ErrInvalid, m.SourceRepo, targetOf[m.SourceRepo])
			}
			continue
		}
		r, ok := byName[m.SourceRepo]
		if !ok {
			return nil, fmt.Errorf("%w: Nexus has no repo %q", ErrInvalid, m.SourceRepo)
		}
		if _, reason := s.support(r); reason != "" {
			return nil, fmt.Errorf("%w: %s: %s", ErrInvalid, r.Name, reason)
		}
		p := plan{m: m, src: r}
		p.f = s.formats.Importer(r.Format)
		t, err := s.repos.ByName(ctx, m.Target)
		switch {
		case err == nil:
			if t.Kind != r.Type {
				return nil, fmt.Errorf("%w: %s is a %s repository here; the Nexus repository %s is a %s", ErrInvalid, t.Name, t.Kind, r.Name, r.Type)
			}
			if _, ok := s.accepts(t.Format, r.Format); !ok {
				return nil, fmt.Errorf("%w: target repo %q (%s) cannot import %s content", ErrInvalid, t.Name, t.Format, r.Format)
			}
			if name, ok := keptTargets[t.ID]; ok {
				return nil, fmt.Errorf("%w: %s has switched over; copy the Nexus repository %s into another repository", ErrInvalid, name, r.Name)
			}
			if other, err := q.GetNexusSourceByTarget(ctx, t.ID); err == nil && other.ID != id {
				return nil, fmt.Errorf("%w: target repo %q already mirrors Nexus source %s", ErrInvalid, t.Name, other.Name)
			} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			p.target = t
			p.f = s.formats.Get(t.Format)
		case errors.Is(err, repos.ErrNotFound):
			if !m.Create {
				return nil, fmt.Errorf("%w: target repo %q does not exist", ErrInvalid, m.Target)
			}
		default:
			return nil, err
		}
		if err := p.planTarget(ctx); err != nil {
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
			if p.target, err = s.repos.Update(ctx, p.target.Name, repos.Patch{Members: members}); err != nil {
				return nil, fmt.Errorf("set the members of group %q: %w", p.m.Target, err)
			}
			continue
		}
		if p.target, err = s.repos.Create(ctx, repos.Create{Name: p.m.Target, Format: p.f.Name, Kind: repos.KindGroup,
			AnonymousRead: p.m.AnonymousRead, Members: members}); err != nil {
			return nil, fmt.Errorf("create group %q: %w", p.m.Target, err)
		}
		s.log.Info("nexus: created group", "repo", p.target.Name, "format", p.target.Format, "members", len(members))
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
		if _, err := q.GetRunningNexusSync(ctx, id); err == nil {
			return ErrRunning
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		all, err := q.ListNexusSourceRepos(ctx, id)
		if err != nil {
			return err
		}
		now, err := q.ListNexusSwitchOvers(ctx, id)
		if err != nil {
			return err
		}
		if len(now) != len(kept) {
			return fmt.Errorf("%w: a repository switched over meanwhile; load the page again", ErrInvalid)
		}
		var old []dbq.NexusSourceRepo
		for _, o := range all {
			if _, ok := kept[o.SourceRepo]; ok {
				continue
			}
			old = append(old, o)
			if err := q.DeleteNexusSourceRepo(ctx, dbq.DeleteNexusSourceRepoParams{SourceID: id, SourceRepo: o.SourceRepo}); err != nil {
				return err
			}
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
				if err := s.hold(ctx, tx, p.target, id, userID); err != nil {
					return err
				}
			}
		}
		for _, o := range old {
			if !keep[o.TargetRepoID] && o.Kind == TypeHosted {
				if err := s.release(ctx, tx, o.TargetRepoID, id); err != nil {
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
	targets := make([]string, len(plans))
	for i, p := range plans {
		targets[i] = p.m.SourceRepo + " -> " + p.m.Target
	}
	s.record(ctx, ActionMappingSave, row.Name, map[string]any{"id": id, "repos": targets})
	if len(plans) > 0 {
		syncID, err := s.startSync(ctx, id, userID, TriggerSave, nil)
		if err != nil {
			return nil, err
		}
		s.record(ctx, ActionSyncStart, row.Name, map[string]any{"source_id": id, "sync_id": syncID, "trigger": TriggerSave})
	}
	return s.Detail(ctx, id)
}

// prepare finds or creates the target of a hosted or proxy plan. Two Nexus repos may share a
// new hosted target only when its format imports both. An existing target gets the config and
// the field values that its format planned (e.g. a missing distribution).
func (s *Service) prepare(ctx context.Context, p *plan, created map[string]*repos.Repo) error {
	if p.target != nil {
		if p.tp == nil || (p.tp.Config == nil && p.tp.Fields == nil) {
			return nil
		}
		t, err := s.repos.Update(ctx, p.target.Name, repos.Patch{Config: p.tp.Config, Fields: p.tp.Fields})
		if err != nil {
			return fmt.Errorf("update target repo %q: %w", p.target.Name, err)
		}
		p.target = t
		s.log.Info("nexus: updated target repo", "repo", t.Name)
		return nil
	}
	if t := created[p.m.Target]; t != nil {
		if _, ok := s.accepts(t.Format, p.src.Format); !ok || t.Kind != p.src.Type {
			return fmt.Errorf("%w: target repo %q (%s) cannot take the Nexus repository %s", ErrInvalid, t.Name, t.Format, p.src.Name)
		}
		p.target = t
		return nil
	}
	if p.tp != nil && p.tp.Prepare != nil {
		if err := p.tp.Prepare(ctx); err != nil {
			return fmt.Errorf("%s: %w", p.src.Name, err)
		}
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
		t, err := s.repos.Create(ctx, repos.Create{Name: p.m.Target, Format: p.f.Name, Kind: repos.KindProxy,
			AnonymousRead: p.m.AnonymousRead, Config: p.config(), Proxy: px, Fields: p.fields()})
		if err != nil {
			return fmt.Errorf("create proxy %q: %w", p.m.Target, err)
		}
		s.log.Info("nexus: created proxy", "repo", t.Name, "format", t.Format, "remote", px.RemoteURL)
		created[t.Name], p.target = t, t
		return nil
	}
	t, err := s.repos.Create(ctx, repos.Create{Name: p.m.Target, Format: p.f.Name, AnonymousRead: p.m.AnonymousRead,
		Config: p.config(), Fields: p.fields()})
	if err != nil {
		return fmt.Errorf("create target repo %q: %w", p.m.Target, err)
	}
	s.log.Info("nexus: created target repo", "repo", t.Name, "format", t.Format)
	created[t.Name], p.target = t, t
	return nil
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
	s.record(ctx, ActionSourceDelete, name, map[string]any{"id": id})
	return nil
}

// hold begins the mirror operation of source on hosted repo target in tx (state held), so
// writes, deletes and mints of the repo answer 409 until the cut-over. A repo that this source
// holds already stays held; a repo with another operation cannot be mapped.
func (s *Service) hold(ctx context.Context, tx *sql.Tx, target *repos.Repo, source, userID int64) error {
	op, err := s.ops.Get(ctx, tx, target.ID)
	switch {
	case err == nil && op.Type == OpMirror && op.Ref == mirrorRef(source):
		return nil
	case err == nil:
		return fmt.Errorf("%w: target repo %q is busy. %s", ErrInvalid, target.Name, s.ops.Message(op))
	case !errors.Is(err, ops.ErrNone):
		return err
	}
	_, err = s.ops.Begin(ctx, tx, target.ID, OpMirror, "nexus", mirrorRef(source), ops.Held, userID)
	return err
}

// release ends the mirror operation of source on repo repoID in tx, when it has one.
func (s *Service) release(ctx context.Context, tx *sql.Tx, repoID, source int64) error {
	op, err := s.ops.Get(ctx, tx, repoID)
	if errors.Is(err, ops.ErrNone) {
		return nil
	}
	if err != nil {
		return err
	}
	if op.Type != OpMirror || op.Ref != mirrorRef(source) {
		return nil
	}
	return s.ops.End(ctx, tx, op.ID, "nexus")
}

// mirrorMessage is the text of a mirror operation, one full sentence: the 409 answer of a write,
// the access report and the line on the repository page.
func (s *Service) mirrorMessage(op *ops.Op) string {
	name := "a Nexus server"
	if id, err := strconv.ParseInt(op.Ref, 10, 64); err == nil {
		if row, err := dbq.New(s.db.R).GetNexusSource(context.Background(), id); err == nil {
			name = "Nexus server " + row.Name
		}
	}
	return "Uploads and deletes start after the switch-over from " + name + "."
}
