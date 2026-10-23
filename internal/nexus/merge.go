package nexus

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/nexus/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/ops"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// A merge is the last step of a gradual move: every file of a hosted repo (the source, where
// projects published while they moved) goes into the repo that a Nexus source copies into (the
// target), which then switches over, and the source's name answers for the target from then on.

// OpMerge is the repo operation of a merge, on its target and on its source: writes to both
// answer 409 until it ends. Its ref is the merge id.
const OpMerge = "merge"

// Merge states (nexus_merges.state).
const (
	MergeRunning = "running"
	MergeFailed  = "failed"
	MergeDone    = "done"
	MergeStopped = "stopped"
)

// Merge steps (nexus_merges.step), in order.
const (
	stepSync   = "sync"   // the final sync of the target from Nexus
	stepCopy   = "copy"   // the files of the source, after cursor
	stepAccess = "access" // the grants, the CI pipelines and the groups of the source
	stepSwitch = "switch" // the target switches over
	stepRetire = "retire" // the source's names answer for the target; the source's delete begins
	stepRename = "rename" // the target takes the source's name (keep source)
	stepDone   = "done"
)

// The name a merged repo keeps (MergeInput.Keep), and the choices of a conflict.
const (
	KeepTarget = "target"
	KeepSource = "source"
	ChoiceKeep = "keep" // keep the target's file
	ChoiceTake = "take" // delete the target's file and take the source's
)

const (
	mergePage      = 500             // source files read per page
	mergeBatch     = 100             // files per commit
	mergeSaveEvery = time.Second     // the most time between two saves of the progress
	mergeWaitEvery = time.Second     // how often a merge looks at the sync it waits for
	planConflicts  = 1000            // conflicts a plan lists
	planBlocked    = 100             // blocked files a plan lists
	mergeChoices   = 100_000         // the most choices a start takes
	resumeMergesN  = 100             // running merges a start resumes
	maxMergedBytes = int64(16 << 20) // the largest metadata file that a merge merges
	tombSuffix     = ".merged-"
)

// MergeConflict is a path that both repos hold with different bytes, where the target's format
// keeps a file fixed (a Maven release or a Docker tag), or a label of the format (an npm
// dist-tag) that both hold with other values. Path is the key of its choice: the file's path, or
// "label:" and the label's key.
type MergeConflict struct {
	Kind  string `json:"kind"`  // "file" or "label"
	Title string `json:"title"` // a label for people, e.g. "lib: dist-tag beta"; "" for a file
	// Value and TargetValue are the label's values in the source and the target.
	Value           string    `json:"value"`
	TargetValue     string    `json:"target_value"`
	Path            string    `json:"path"`
	Size            int64     `json:"size"`
	SHA256          string    `json:"sha256"`
	UpdatedAt       time.Time `json:"updated_at"`
	TargetSize      int64     `json:"target_size"`
	TargetSHA256    string    `json:"target_sha256"`
	TargetUpdatedAt time.Time `json:"target_updated_at"`
}

// MergeBlocked is a file that the target's format cannot take from another repo, and why.
type MergeBlocked struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// MergePlan is what a merge of From into Into would do, from the files of both now.
type MergePlan struct {
	From   string `json:"from"`
	Into   string `json:"into"`
	Format string `json:"format"`
	// SourceID and SourceName are the Nexus server that copies into Into; Copying is true while
	// it still copies: the merge then runs a final sync first and switches Into over after.
	SourceID   int64  `json:"source_id"`
	SourceName string `json:"source_name"`
	Copying    bool   `json:"copying"`
	// Files is the file count of From; Copy, Skip and Rebuild split it with the conflicts and
	// the blocked files: copied, skipped (Into has the same bytes), rebuilt (metadata and index
	// files that the format builds or merges).
	Files   int64 `json:"files"`
	Copy    int64 `json:"copy"`
	Skip    int64 `json:"skip"`
	Rebuild int64 `json:"rebuild"`
	// Labels counts the labels of the format (npm dist-tags) that the target takes from the
	// source and that are no conflict.
	Labels int64 `json:"labels"`
	// Conflicts lists the first planConflicts conflicts by path; ConflictCount counts all.
	Conflicts     []MergeConflict `json:"conflicts"`
	ConflictCount int64           `json:"conflict_count"`
	// Blocked lists the first planBlocked files that the format cannot take; a merge with any
	// does not start.
	Blocked      []MergeBlocked `json:"blocked"`
	BlockedCount int64          `json:"blocked_count"`
	// What else moves to Into: the people with a grant on From, its CI pipelines and the groups
	// that read From (each reads Into in its place), and the redirects of each repo.
	People        []string `json:"people"`
	Pipelines     []string `json:"pipelines"`
	Groups        []string `json:"groups"`
	FromRedirects []string `json:"from_redirects"`
	IntoRedirects []string `json:"into_redirects"`
	// Busy says why the merge cannot start now ("" when it can); MergeID is a running or failed
	// merge into Into (0 for none).
	Busy    string `json:"busy"`
	MergeID int64  `json:"merge_id"`
}

// MergeInput is the body of POST /api/nexus/merges. Choices name a choice (ChoiceKeep or
// ChoiceTake) per conflict path; Otherwise is the choice of every other conflict ("" for none).
type MergeInput struct {
	From      string            `json:"from"`
	Into      string            `json:"into"`
	Keep      string            `json:"keep"`
	Choices   map[string]string `json:"choices"`
	Otherwise string            `json:"otherwise"`
}

// Merge is a merge as the API returns it.
type Merge struct {
	ID int64 `json:"id"`
	// From and Into are the names of the repos at the start. Name is the merged repo's name now
	// ("" when it is gone) and Redirect the name that answers for it after the merge.
	From     string `json:"from"`
	Into     string `json:"into"`
	Name     string `json:"name"`
	Redirect string `json:"redirect"`
	Keep     string `json:"keep"`
	Format   string `json:"format"`
	// SourceID and SourceName are the Nexus server that copies into the target.
	SourceID   int64  `json:"source_id"`
	SourceName string `json:"source_name"`
	State      string `json:"state"`
	Step       string `json:"step"`
	Error      string `json:"error"`
	SyncID     int64  `json:"sync_id"`
	// Files is the source's file count at the start, Done the files walked; the rest split Done.
	Files      int64      `json:"files"`
	Done       int64      `json:"done"`
	Copied     int64      `json:"copied"`
	Skipped    int64      `json:"skipped"`
	Rebuilt    int64      `json:"rebuilt"`
	Kept       int64      `json:"kept"`
	Taken      int64      `json:"taken"`
	Groups     []string   `json:"groups"` // the groups that read the merged repo
	StartedBy  string     `json:"started_by"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

// verdict is what a merge does with one file of the source.
type verdict int

const (
	vCopy     verdict = iota // commit it into the target
	vSkip                    // the target has the same bytes
	vConflict                // the target has other bytes that its format keeps
	vRebuild                 // the format builds it (an index, a checksum of a merged file)
	vMerge                   // the format's group merge merges it with the target's file
	vBlocked                 // the format cannot take it from another repo
)

// merger decides and carries the files of one merge.
type merger struct {
	s     *Service
	from  *repos.Repo
	into  *repos.Repo
	hooks *assets.Hooks
	// im is the target format's importer when it stores files itself (Importer.Store): such a
	// format reads each file and builds its own rows and indexes, so a file goes through
	// Importer.Run; nil: a file is committed at its path by its digest.
	im    *format.Importer
	cache *format.RunCache
	// carry is the format's Carry, nil for none.
	carry *format.Carry
}

func (s *Service) newMerger(from, into *repos.Repo) *merger {
	m := &merger{s: s, from: from, into: into, hooks: s.assets.Hooks(into.Format), cache: format.NewRunCache()}
	f := s.formats.Get(into.Format)
	if f != nil {
		m.carry = f.Carry
	}
	if f != nil && f.Importer != nil && f.Importer.Store != nil && (m.carry == nil || m.carry.Prepare == nil) {
		m.im = f.Importer
	}
	return m
}

// write is the commit of source file a into the target by its digest: no bytes are copied. It
// keeps the file's dates, uploader and CI build.
func (m *merger) write(a *assets.Asset) assets.Write {
	return assets.Write{Repo: m.into, Path: repopath.Path(a.Path), Body: assets.Body{Blob: a.SHA256}, ContentType: a.ContentType, Kind: a.Kind,
		Origin: assets.Origin{Source: assets.SourceImport, UserID: a.UploadedBy, BuildID: a.BuildID, Created: a.CreatedAt, Updated: a.UpdatedAt}}
}

// item is source file a as the target format's importer takes it.
func (m *merger) item(a *assets.Asset) format.ImportItem {
	return format.ImportItem{Path: a.Path, Size: a.Size, SHA256: a.SHA256, ContentType: a.ContentType,
		CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt, Cache: m.cache,
		Open: func(ctx context.Context) (io.ReadCloser, error) { return m.s.assets.Open(ctx, a) }}
}

// decide says what the merge does with source file a, where the target holds e (nil: none), and
// why a blocked file is blocked. It writes nothing.
func (m *merger) decide(ctx context.Context, a, e *assets.Asset) (verdict, string, error) {
	if a.Kind != assets.KindArtifact {
		if m.im != nil {
			return vRebuild, "", nil
		}
		if mg := m.hooks.Merge; mg != nil {
			if _, sum, ok := mg.Match(repopath.Path(a.Path)); ok {
				switch {
				case sum != "":
					return vRebuild, "", nil
				case e == nil:
					return vMerge, "", nil
				case e.SHA256 == a.SHA256:
					return vSkip, "", nil
				}
				return vMerge, "", nil
			}
		}
	}
	if m.im != nil {
		p, ok, err := m.im.Target(ctx, m.into, m.item(a))
		switch {
		case err != nil:
			return vBlocked, err.Error(), nil
		case !ok || string(p) != a.Path:
			return vBlocked, "The format takes this file only from its clients, not from another repository.", nil
		}
	}
	switch {
	case e == nil:
		return vCopy, "", nil
	case e.SHA256 == a.SHA256:
		return vSkip, "", nil
	case m.im != nil, m.carry != nil && m.carry.Pinned != nil && m.carry.Pinned(a.Path):
		return vConflict, "", nil
	case m.hooks.Admit == nil:
		return vCopy, "", nil
	}
	w := m.write(a)
	d, err := m.hooks.Admit(ctx, &w, e)
	var ref *assets.Refusal
	switch {
	case errors.As(err, &ref) && (ref.Status == http.StatusConflict || ref.Status == http.StatusForbidden):
		return vConflict, "", nil
	case errors.As(err, &ref):
		return vBlocked, ref.Message, nil
	case err != nil:
		return 0, "", err
	case d == assets.Keep:
		return vSkip, "", nil
	}
	return vCopy, "", nil
}

// walk decides each file of the source after path after, in path order, and calls fn with it,
// the target's file at its path (nil: none) and the verdict.
func (s *Service) walk(ctx context.Context, m *merger, after string, fn func(a, e *assets.Asset, v verdict, reason string) error) error {
	for {
		page, next, err := s.assets.List(ctx, m.from.ID, "", after, mergePage)
		if err != nil {
			return err
		}
		for _, a := range page {
			if err := ctx.Err(); err != nil {
				return err
			}
			e, err := s.assets.Get(ctx, m.into.ID, a.Path)
			if errors.Is(err, assets.ErrNotFound) {
				e, err = nil, nil
			}
			if err != nil {
				return err
			}
			v, reason, err := m.decide(ctx, a, e)
			if err != nil {
				return fmt.Errorf("%s: %w", a.Path, err)
			}
			if err := fn(a, e, v, reason); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		after = next
	}
}

// copyingInto returns the Nexus source that copies into repo id (0 for none) and whether one of
// its mapped hosted repos still copies into it.
func (s *Service) copyingInto(ctx context.Context, id int64) (sourceID int64, copying bool, err error) {
	rows, err := dbq.New(s.db.R).ListNexusTargetRepos(ctx, id)
	if err != nil {
		return 0, false, err
	}
	for _, r := range rows {
		if r.Kind != TypeHosted {
			continue
		}
		sourceID = r.SourceID
		copying = copying || r.Switched == 0
	}
	return sourceID, copying, nil
}

// mergePair reads the source and the target of a merge and checks that they can merge: two
// hosted repos of one format, the target copied from Nexus, the source not. into "" is the repo
// that from moves into.
func (s *Service) mergePair(ctx context.Context, fromName, intoName string) (from, into *repos.Repo, sourceID int64, copying bool, err error) {
	from, err = s.repos.ByName(ctx, fromName)
	if errors.Is(err, repos.ErrNotFound) {
		return nil, nil, 0, false, ErrNotFound
	}
	if err != nil {
		return nil, nil, 0, false, err
	}
	if intoName == "" {
		id := s.movesInto(from.ID)
		if id == 0 {
			return nil, nil, 0, false, fmt.Errorf("%w: %s moves into no repository; name the repository to merge it into", ErrInvalid, from.Name)
		}
		into, err = s.repos.ByID(ctx, id)
	} else {
		into, err = s.repos.ByName(ctx, intoName)
	}
	if errors.Is(err, repos.ErrNotFound) {
		return nil, nil, 0, false, ErrNotFound
	}
	if err != nil {
		return nil, nil, 0, false, err
	}
	switch {
	case from.ID == into.ID:
		return nil, nil, 0, false, fmt.Errorf("%w: %s and %s are the same repository", ErrInvalid, fromName, into.Name)
	case from.Kind != repos.KindHosted || into.Kind != repos.KindHosted:
		return nil, nil, 0, false, fmt.Errorf("%w: only two repositories merge, not a proxy or a group", ErrInvalid)
	case from.Format != into.Format:
		return nil, nil, 0, false, fmt.Errorf("%w: %s is a %s repository and %s a %s one", ErrInvalid, from.Name, from.Format, into.Name, into.Format)
	}
	if id, _, err := s.copyingInto(ctx, from.ID); err != nil {
		return nil, nil, 0, false, err
	} else if id != 0 {
		return nil, nil, 0, false, fmt.Errorf("%w: %s copies from Nexus; merge the repository that projects moved to into it", ErrInvalid, from.Name)
	}
	if sourceID, copying, err = s.copyingInto(ctx, into.ID); err != nil {
		return nil, nil, 0, false, err
	}
	if sourceID == 0 {
		return nil, nil, 0, false, fmt.Errorf("%w: no Nexus server copies into %s", ErrInvalid, into.Name)
	}
	return from, into, sourceID, copying, nil
}

// mergeBusy says why a merge of from into into cannot start now: an operation holds one of them
// (the target's own mirror operation is the one it may have).
func (s *Service) mergeBusy(from, into *repos.Repo) string {
	switch {
	case from.Op != nil:
		return from.Name + ": " + s.ops.Message(from.Op)
	case into.Op != nil && into.Op.Type != OpMirror:
		return into.Name + ": " + s.ops.Message(into.Op)
	}
	return ""
}

// activeMerge returns the newest merge into repo id that runs or failed, 0 for none.
func (s *Service) activeMerge(ctx context.Context, id int64) (int64, error) {
	rows, err := dbq.New(s.db.R).ListNexusMergesOf(ctx, dbq.ListNexusMergesOfParams{TargetRepoID: id, Limit: 1})
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	if rows[0].State == MergeRunning || rows[0].State == MergeFailed {
		return rows[0].ID, nil
	}
	return 0, nil
}

// MergePlan works out what a merge of repo from into repo into does, from the files of both now
// (into "" is the repo that from moves into). It writes nothing. It reads every file of from.
func (s *Service) MergePlan(ctx context.Context, fromName, intoName string) (*MergePlan, error) {
	from, into, sourceID, copying, err := s.mergePair(ctx, fromName, intoName)
	if err != nil {
		return nil, err
	}
	p := &MergePlan{From: from.Name, Into: into.Name, Format: into.Format, SourceID: sourceID, Copying: copying, Files: from.FileCount,
		Conflicts: []MergeConflict{}, Blocked: []MergeBlocked{}, People: []string{}, Pipelines: []string{}, Groups: []string{},
		FromRedirects: []string{}, IntoRedirects: []string{}}
	if src, err := dbq.New(s.db.R).GetNexusSource(ctx, sourceID); err == nil {
		p.SourceName = src.Name
	}
	m := s.newMerger(from, into)
	err = s.walk(ctx, m, "", func(a, e *assets.Asset, v verdict, reason string) error {
		switch v {
		case vCopy:
			p.Copy++
		case vSkip:
			p.Skip++
		case vRebuild, vMerge:
			p.Rebuild++
		case vConflict:
			p.ConflictCount++
			if len(p.Conflicts) < planConflicts {
				p.Conflicts = append(p.Conflicts, MergeConflict{Kind: "file", Path: a.Path, Size: a.Size, SHA256: a.SHA256, UpdatedAt: a.UpdatedAt,
					TargetSize: e.Size, TargetSHA256: e.SHA256, TargetUpdatedAt: e.UpdatedAt})
			}
		case vBlocked:
			p.BlockedCount++
			if len(p.Blocked) < planBlocked {
				p.Blocked = append(p.Blocked, MergeBlocked{Path: a.Path, Reason: reason})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// The walk counts the files of now; the repo's count may lag behind it.
	p.Files = p.Copy + p.Skip + p.Rebuild + p.ConflictCount + p.BlockedCount
	if m.carry != nil && m.carry.Labels != nil {
		labels, err := m.carry.Labels(ctx, from, into)
		if err != nil {
			return nil, err
		}
		for _, l := range labels {
			switch {
			case l.Conflict():
				p.ConflictCount++
				if len(p.Conflicts) < planConflicts {
					p.Conflicts = append(p.Conflicts, MergeConflict{Kind: "label", Path: labelPrefix + l.Key, Title: l.Title, Value: l.Value, TargetValue: l.Target})
				}
			case l.Target != l.Value:
				p.Labels++
			}
		}
	}
	grants, err := s.auth.ListPermissions(ctx, from.ID)
	if err != nil {
		return nil, err
	}
	for _, g := range grants {
		p.People = append(p.People, g.Username)
	}
	if s.publish != nil {
		if p.Pipelines, err = s.publish.PipelinesOf(ctx, from.ID); err != nil {
			return nil, err
		}
	}
	for _, gid := range s.repos.GroupsOf(from.ID) {
		if g, err := s.repos.ByID(ctx, gid); err == nil {
			p.Groups = append(p.Groups, g.Name)
		}
	}
	for _, side := range []struct {
		repo *repos.Repo
		out  *[]string
	}{{from, &p.FromRedirects}, {into, &p.IntoRedirects}} {
		rds, err := s.repos.Redirects(ctx, side.repo.ID)
		if err != nil {
			return nil, err
		}
		for _, rd := range rds {
			*side.out = append(*side.out, rd.Name)
		}
	}
	p.Busy = s.mergeBusy(from, into)
	if p.MergeID, err = s.activeMerge(ctx, into.ID); err != nil {
		return nil, err
	}
	return p, nil
}

// StartMerge begins the merge of in.From into in.Into after the plan of now: it refuses a merge
// with a blocked file or with a conflict that has no choice (ErrInvalid), and one that an
// operation holds (ErrBusy). One transaction stores the merge and its choices, ends the target's
// mirror operation and begins the merge operation on both repos; the merge then runs as a task
// of the module (see runMerge). userID 0 means unknown.
func (s *Service) StartMerge(ctx context.Context, in MergeInput, userID int64) (*Merge, error) {
	if in.Keep != KeepTarget && in.Keep != KeepSource {
		return nil, fmt.Errorf("%w: keep is %q or %q", ErrInvalid, KeepTarget, KeepSource)
	}
	if in.Otherwise != "" && in.Otherwise != ChoiceKeep && in.Otherwise != ChoiceTake {
		return nil, fmt.Errorf("%w: otherwise is %q, %q or empty", ErrInvalid, ChoiceKeep, ChoiceTake)
	}
	if len(in.Choices) > mergeChoices {
		return nil, fmt.Errorf("%w: at most %d choices; choose the rest with otherwise", ErrInvalid, mergeChoices)
	}
	for path, c := range in.Choices {
		if c != ChoiceKeep && c != ChoiceTake {
			return nil, fmt.Errorf("%w: the choice of %s is %q or %q", ErrInvalid, path, ChoiceKeep, ChoiceTake)
		}
	}
	plan, err := s.MergePlan(ctx, in.From, in.Into)
	if err != nil {
		return nil, err
	}
	if plan.Busy != "" {
		return nil, fmt.Errorf("%w: %s", ErrBusy, plan.Busy)
	}
	if plan.BlockedCount > 0 {
		return nil, fmt.Errorf("%w: %s of %s cannot go into %s; the plan lists them", ErrInvalid, files(plan.BlockedCount, "file"), plan.From, plan.Into)
	}
	open := int64(0)
	if in.Otherwise == "" {
		open = plan.ConflictCount - int64(len(plan.Conflicts))
		for _, c := range plan.Conflicts {
			if in.Choices[c.Path] == "" {
				open++
			}
		}
	}
	if open > 0 {
		return nil, fmt.Errorf("%w: choose what happens to %s: keep the file of %s, or take the one of %s", ErrInvalid, files(open, "conflict"), plan.Into, plan.From)
	}
	from, err := s.repos.ByName(ctx, plan.From)
	if err != nil {
		return nil, err
	}
	into, err := s.repos.ByName(ctx, plan.Into)
	if err != nil {
		return nil, err
	}
	step := stepCopy
	if plan.Copying {
		step = stepSync
	}
	var id int64
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		var err error
		id, err = q.InsertNexusMerge(ctx, dbq.InsertNexusMergeParams{TargetRepoID: into.ID, SourceRepoID: from.ID,
			SourceName: from.Name, TargetName: into.Name, Keep: in.Keep, Otherwise: in.Otherwise, Step: step, Files: plan.Files,
			StartedBy: sql.NullInt64{Int64: userID, Valid: userID != 0}, StartedAt: time.Now().Unix()})
		if err != nil {
			return err
		}
		for path, c := range in.Choices {
			take := int64(0)
			if c == ChoiceTake {
				take = 1
			}
			if err := q.InsertNexusMergeChoice(ctx, dbq.InsertNexusMergeChoiceParams{MergeID: id, Path: path, Take: take}); err != nil {
				return err
			}
		}
		op, err := s.ops.Get(ctx, tx, into.ID)
		switch {
		case err == nil && op.Type == OpMirror:
			if err := s.ops.End(ctx, tx, op.ID, "nexus"); err != nil {
				return err
			}
		case err == nil:
			return fmt.Errorf("%w: %s: %s", ErrBusy, into.Name, s.ops.Message(op))
		case !errors.Is(err, ops.ErrNone):
			return err
		}
		ref := strconv.FormatInt(id, 10)
		for _, r := range []*repos.Repo{into, from} {
			if _, err := s.ops.Begin(ctx, tx, r.ID, OpMerge, "nexus", ref, ops.Running, userID); errors.Is(err, ops.ErrBusy) {
				return fmt.Errorf("%w: %s has an operation already", ErrBusy, r.Name)
			} else if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.log.Info("nexus: merge started", "merge", id, "from", from.Name, "into", into.Name, "keep", in.Keep)
	_ = s.audit.Record(ctx, audit.Event{Action: ActionMergeStart, RepoID: into.ID, Repo: into.Name, Target: from.Name,
		Detail: map[string]any{"merge_id": id, "from": from.Name, "into": into.Name, "keep": in.Keep, "files": plan.Files,
			"conflicts": plan.ConflictCount}})
	s.startMerge(id)
	return s.Merge(ctx, id)
}

// Merge returns merge id.
func (s *Service) Merge(ctx context.Context, id int64) (*Merge, error) {
	row, err := dbq.New(s.db.R).GetNexusMerge(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	out := &Merge{ID: row.ID, From: row.SourceName, Into: row.TargetName, Keep: row.Keep, State: row.State, Step: row.Step,
		Error: row.Error, SyncID: row.SyncID, Files: row.Files, Done: row.Done, Copied: row.Copied, Skipped: row.Skipped,
		Rebuilt: row.Rebuilt, Kept: row.Kept, Taken: row.Taken, Groups: []string{}, StartedBy: row.StartedByName,
		StartedAt: time.Unix(row.StartedAt, 0).UTC(), Redirect: row.SourceName}
	if row.Keep == KeepSource {
		out.Redirect = row.TargetName
	}
	if row.FinishedAt.Valid {
		t := time.Unix(row.FinishedAt.Int64, 0).UTC()
		out.FinishedAt = &t
	}
	into, err := s.repos.ByID(ctx, row.TargetRepoID)
	switch {
	case err == nil:
		out.Name, out.Format = into.Name, into.Format
		for _, gid := range s.repos.GroupsOf(into.ID) {
			if g, err := s.repos.ByID(ctx, gid); err == nil {
				out.Groups = append(out.Groups, g.Name)
			}
		}
	case !errors.Is(err, repos.ErrNotFound):
		return nil, err
	}
	if out.SourceID, _, err = s.copyingInto(ctx, row.TargetRepoID); err != nil {
		return nil, err
	}
	if src, err := dbq.New(s.db.R).GetNexusSource(ctx, out.SourceID); err == nil {
		out.SourceName = src.Name
	}
	return out, nil
}

// startMerge runs merge id as a task of the module.
func (s *Service) startMerge(id int64) {
	s.spawn("merge", func(ctx context.Context) { s.runMerge(ctx, id) })
}

// runMerge runs the steps of merge id from the step it is at, until it is done, it fails or the
// server stops (it stays running then, and the next start resumes it). A failure before the
// switch-over leaves the target copying from Nexus and the source with its files.
func (s *Service) runMerge(ctx context.Context, id int64) {
	log := s.log.With("merge", id)
	work := context.WithoutCancel(ctx)
	retired := false
	for {
		row, err := dbq.New(s.db.R).GetNexusMerge(work, id)
		if err != nil {
			log.Error("nexus: cannot load the merge", "error", err.Error())
			return
		}
		if row.State != MergeRunning {
			return
		}
		next := ""
		switch row.Step {
		case stepSync:
			next, err = s.mergeSync(ctx, row)
		case stepCopy:
			next, err = s.mergeCopy(ctx, row)
		case stepAccess:
			next, err = s.mergeAccess(ctx, row)
		case stepSwitch:
			err = s.mergeSwitch(ctx, row)
		case stepRetire:
			err = s.mergeRetire(ctx, row)
			retired = err == nil
		case stepRename:
			next, err = s.mergeRename(ctx, row)
		case stepDone:
			s.mergeEnd(work, row, retired)
			return
		default:
			err = fmt.Errorf("unknown step %q", row.Step)
		}
		if ctx.Err() != nil {
			log.Info("nexus: merge stopped with the server; it resumes at the next start", "step", row.Step)
			return
		}
		if err != nil {
			log.Error("nexus: merge failed", "step", row.Step, "error", err.Error())
			s.failMerge(work, row, err)
			return
		}
		if next != "" {
			if err := dbq.New(s.db.W).SetNexusMergeStep(work, dbq.SetNexusMergeStepParams{Step: next, SyncID: row.SyncID, ID: id}); err != nil {
				s.failMerge(work, row, err)
				return
			}
		}
	}
}

// sleep waits d or until ctx ends; false when ctx ended.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// mergeSync runs the final sync of the target from Nexus (trigger cutover; the runner leaves the
// switch-over to the merge) and waits for it. A sync of the source that runs already is waited
// for first. A target that no longer copies goes on at once.
func (s *Service) mergeSync(ctx context.Context, row dbq.GetNexusMergeRow) (string, error) {
	q := dbq.New(s.db.R)
	for row.SyncID == 0 {
		sourceID, copying, err := s.copyingInto(ctx, row.TargetRepoID)
		if err != nil {
			return "", err
		}
		if !copying {
			return stepCopy, nil
		}
		into, err := s.repos.ByID(ctx, row.TargetRepoID)
		if err != nil {
			return "", err
		}
		line := "Final sync of the merge of " + row.SourceName + " into " + row.TargetName + "."
		syncID, err := s.startSyncTx(ctx, sourceID, row.StartedBy.Int64, TriggerCutover, into, line, func(q *dbq.Queries, id int64) error {
			return q.SetNexusMergeStep(ctx, dbq.SetNexusMergeStepParams{Step: stepSync, SyncID: id, ID: row.ID})
		})
		switch {
		case err == nil:
			row.SyncID = syncID
		case errors.Is(err, ErrRunning):
			if !sleep(ctx, 2*mergeWaitEvery) {
				return "", ctx.Err()
			}
		case errors.Is(err, ErrSwitched), errors.Is(err, ErrCutOver):
			return stepCopy, nil
		default:
			return "", fmt.Errorf("start the final sync from Nexus: %w", err)
		}
	}
	for {
		sy, err := q.GetNexusSync(ctx, row.SyncID)
		if errors.Is(err, sql.ErrNoRows) {
			return "", errors.New("the final sync from Nexus is gone")
		}
		if err != nil {
			return "", err
		}
		switch sy.State {
		case SyncDone:
			return stepCopy, nil
		case SyncFailed:
			return "", fmt.Errorf("the final sync from Nexus failed: %s", sy.Error)
		case SyncCancelled:
			return "", errors.New("the final sync from Nexus was cancelled")
		}
		if !sleep(ctx, mergeWaitEvery) {
			return "", ctx.Err()
		}
	}
}

// mergeWaits reports whether a merge waits for the end of sync id, its final sync.
func (s *Service) mergeWaits(ctx context.Context, id int64) bool {
	_, err := dbq.New(s.db.R).GetNexusMergeOfSync(ctx, id)
	return err == nil
}

// mergeCounts are the counters of a merge's copy.
type mergeCounts struct{ done, copied, skipped, rebuilt, kept, taken int64 }

func (c *mergeCounts) add(d mergeCounts) {
	c.done += d.done
	c.copied += d.copied
	c.skipped += d.skipped
	c.rebuilt += d.rebuilt
	c.kept += d.kept
	c.taken += d.taken
}

// mergeCopy commits every file of the source after the saved cursor into the target through the
// byte pipeline: mergeBatch files per CommitAll by their digests (a format that stores files
// itself takes each through Importer.Run), a metadata file that both repos hold through the
// format's group merge, a conflict as its choice says. It saves the cursor and the counters after
// each commit, at most mergeSaveEvery apart, so a resume continues from the files not yet copied.
func (s *Service) mergeCopy(ctx context.Context, row dbq.GetNexusMergeRow) (string, error) {
	from, err := s.repos.ByID(ctx, row.SourceRepoID)
	if err != nil {
		return "", err
	}
	into, err := s.repos.ByID(ctx, row.TargetRepoID)
	if err != nil {
		return "", err
	}
	m := s.newMerger(from, into)
	var opID int64
	if op, err := s.ops.Get(ctx, nil, into.ID); err == nil && op.Type == OpMerge {
		opID = op.ID
	}
	total := max(row.Files, from.FileCount)
	c := mergeCounts{row.Done, row.Copied, row.Skipped, row.Rebuilt, row.Kept, row.Taken}
	cursor := row.Cursor
	if row.Cursor == "" && row.Done == 0 {
		if err := s.snapshotLabels(ctx, m, row); err != nil {
			return "", err
		}
	}
	var batch []assets.Write
	var pend mergeCounts
	last := cursor
	saved := time.Now()
	unit := "" // the Carry unit of the files in batch
	flush := func() error {
		if len(batch) > 0 {
			if m.carry != nil && m.carry.Prepare != nil {
				m.carry.Prepare(batch)
			}
			if _, err := s.assets.CommitAll(ctx, batch); err != nil {
				return err
			}
		}
		batch = batch[:0]
		c.add(pend)
		pend = mergeCounts{}
		cursor = last
		saved = time.Now()
		work := context.WithoutCancel(ctx)
		if err := dbq.New(s.db.W).SetNexusMergeProgress(work, dbq.SetNexusMergeProgressParams{Cursor: cursor, Done: c.done, Copied: c.copied,
			Skipped: c.skipped, Rebuilt: c.rebuilt, Kept: c.kept, Taken: c.taken, ID: row.ID}); err != nil {
			return err
		}
		if opID != 0 {
			_ = s.ops.Progress(work, opID, c.done, max(total, c.done))
		}
		return nil
	}
	err = s.walk(ctx, m, cursor, func(a, e *assets.Asset, v verdict, reason string) error {
		// The files of one unit commit together: a new unit commits the one before.
		if m.carry != nil && m.carry.Unit != nil {
			if u := m.carry.Unit(a.Path); u != unit {
				if err := flush(); err != nil {
					return err
				}
				unit = u
			}
		}
		taken := false
		if v == vConflict {
			take, err := s.choice(ctx, row, into, a.Path)
			if err != nil {
				return err
			}
			if !take {
				pend.kept++
				pend.done++
				last = a.Path
				return nil
			}
			if _, err := s.assets.Remove(ctx, assets.Removal{Repo: into, Paths: []repopath.Path{repopath.Path(a.Path)},
				Origin: assets.Origin{Source: assets.SourceUI, UserID: row.StartedBy.Int64}}); err != nil {
				return fmt.Errorf("delete %s of %s: %w", a.Path, into.Name, err)
			}
			v, taken = vCopy, true
		}
		switch v {
		case vSkip:
			pend.skipped++
		case vRebuild:
			pend.rebuilt++
		case vBlocked:
			return fmt.Errorf("%s cannot go into %s: %s", a.Path, into.Name, reason)
		case vMerge:
			if err := flush(); err != nil {
				return err
			}
			if err := m.mergeFile(ctx, a, e); err != nil {
				return fmt.Errorf("merge %s: %w", a.Path, err)
			}
			pend.rebuilt++
		case vCopy:
			if m.im == nil {
				batch = append(batch, m.write(a))
			} else {
				if err := flush(); err != nil {
					return err
				}
				out, err := m.im.Run(ctx, s.assets, into, m.item(a))
				if err != nil {
					return fmt.Errorf("%s: %w", a.Path, err)
				}
				if out == format.ImportSkipped && !taken {
					pend.skipped++
					break
				}
			}
			if taken {
				pend.taken++
			} else {
				pend.copied++
			}
		}
		pend.done++
		last = a.Path
		if unit == "" && (len(batch) >= mergeBatch || time.Since(saved) >= mergeSaveEvery) {
			return flush()
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if err := flush(); err != nil {
		return "", err
	}
	s.log.Info("nexus: merge copied the files", "merge", row.ID, "from", from.Name, "into", into.Name,
		"copied", c.copied, "skipped", c.skipped, "rebuilt", c.rebuilt, "kept", c.kept, "taken", c.taken)
	return stepAccess, nil
}

// labelPrefix starts the choice key of a label (format.Carry Labels): no file path starts with it.
const labelPrefix = "label:"

// labelSnapshot is the choice key that marks a merge whose labels are saved.
const labelSnapshot = labelPrefix

// snapshotLabels saves, before the first file goes into the target, which labels of the source
// the target takes after the copy: each one that is no conflict and whose value differs (the
// target has no value, or not what it belongs to). The copy changes what the target has, so the
// labels are judged on the target as it was. A conflict keeps its own choice.
func (s *Service) snapshotLabels(ctx context.Context, m *merger, row dbq.GetNexusMergeRow) error {
	if m.carry == nil || m.carry.Labels == nil {
		return nil
	}
	if _, err := dbq.New(s.db.R).GetNexusMergeChoice(ctx, dbq.GetNexusMergeChoiceParams{MergeID: row.ID, Path: labelSnapshot}); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	labels, err := m.carry.Labels(ctx, m.from, m.into)
	if err != nil {
		return fmt.Errorf("read the labels of %s: %w", m.from.Name, err)
	}
	return s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		for _, l := range labels {
			if l.Conflict() || l.Target == l.Value {
				continue
			}
			if err := q.InsertNexusMergeChoiceIfNone(ctx, dbq.InsertNexusMergeChoiceIfNoneParams{MergeID: row.ID, Path: labelPrefix + l.Key, Take: 1}); err != nil {
				return err
			}
		}
		return q.InsertNexusMergeChoiceIfNone(ctx, dbq.InsertNexusMergeChoiceIfNoneParams{MergeID: row.ID, Path: labelSnapshot, Take: 0})
	})
}

// settleLabels gives the target each label of the source that the merge takes: a saved choice of
// take, or a conflict that otherwise takes. A conflict without a choice fails the merge.
func (s *Service) settleLabels(ctx context.Context, m *merger, row dbq.GetNexusMergeRow) error {
	if m.carry == nil || m.carry.Labels == nil || m.carry.SetLabel == nil {
		return nil
	}
	labels, err := m.carry.Labels(ctx, m.from, m.into)
	if err != nil {
		return fmt.Errorf("read the labels of %s: %w", m.from.Name, err)
	}
	q := dbq.New(s.db.R)
	for _, l := range labels {
		if l.Target == l.Value {
			continue
		}
		take, err := q.GetNexusMergeChoice(ctx, dbq.GetNexusMergeChoiceParams{MergeID: row.ID, Path: labelPrefix + l.Key})
		switch {
		case err == nil && take != 1, err != nil && errors.Is(err, sql.ErrNoRows) && !l.Conflict():
			continue
		case err != nil && errors.Is(err, sql.ErrNoRows):
			ok, err := s.choice(ctx, row, m.into, labelPrefix+l.Key)
			if err != nil {
				return fmt.Errorf("%s: %w", l.Title, err)
			}
			if !ok {
				continue
			}
		case err != nil:
			return err
		}
		if err := m.carry.SetLabel(ctx, m.into, l); err != nil {
			return fmt.Errorf("set %s in %s: %w", l.Title, m.into.Name, err)
		}
	}
	return nil
}

// choice reports whether the merge takes the source's file at path, a conflict: the choice the
// start stored, else the merge's otherwise. A conflict without either fails the merge.
func (s *Service) choice(ctx context.Context, row dbq.GetNexusMergeRow, into *repos.Repo, path string) (bool, error) {
	take, err := dbq.New(s.db.R).GetNexusMergeChoice(ctx, dbq.GetNexusMergeChoiceParams{MergeID: row.ID, Path: path})
	switch {
	case err == nil:
		return take == 1, nil
	case !errors.Is(err, sql.ErrNoRows):
		return false, err
	}
	switch row.Otherwise {
	case ChoiceTake:
		return true, nil
	case ChoiceKeep:
		return false, nil
	}
	return false, fmt.Errorf("%s holds %s with other bytes, and the plan did not list it. Stop the merge, open its plan again and choose", into.Name, path)
}

// mergeFile writes a metadata file that the format's group merge merges (format.Spec Merge):
// the source's file when the target has none, else the merge of both. Then the checksum files of
// it that either repo has get the checksums of what the target now holds.
func (m *merger) mergeFile(ctx context.Context, a, e *assets.Asset) error {
	mg := m.hooks.Merge
	base := repopath.Path(a.Path)
	if e == nil {
		if _, err := m.s.assets.Commit(ctx, m.write(a)); err != nil {
			return err
		}
	} else {
		src, err := m.s.assets.ReadAll(ctx, a, maxMergedBytes)
		if err != nil {
			return err
		}
		dst, err := m.s.assets.ReadAll(ctx, e, maxMergedBytes)
		if err != nil {
			return err
		}
		merged, err := mg.Merge(ctx, base, [][]byte{src, dst})
		if err != nil {
			return err
		}
		if _, err := m.s.assets.Commit(ctx, assets.Write{Repo: m.into, Path: base, Body: assets.Body{Spool: blobs.Bytes(merged)},
			Origin: assets.Origin{Source: assets.SourceGenerated}}); err != nil {
			return err
		}
	}
	cur, err := m.s.assets.Get(ctx, m.into.ID, string(base))
	if err != nil {
		return err
	}
	data, err := m.s.assets.ReadAll(ctx, cur, maxMergedBytes)
	if err != nil {
		return err
	}
	sums := map[string]string{} // checksum path -> checksum name
	for _, r := range []*repos.Repo{m.from, m.into} {
		list, _, err := m.s.assets.List(ctx, r.ID, string(base), "", 20)
		if err != nil {
			return err
		}
		for _, x := range list {
			if b, sum, ok := mg.Match(repopath.Path(x.Path)); ok && b == base && sum != "" {
				sums[x.Path] = sum
			}
		}
	}
	var ws []assets.Write
	for p, sum := range sums {
		h := checksum(sum)
		if h == nil {
			continue
		}
		h.Write(data)
		ws = append(ws, assets.Write{Repo: m.into, Path: repopath.Path(p), Body: assets.Body{Spool: blobs.Bytes([]byte(hex.EncodeToString(h.Sum(nil))))},
			Origin: assets.Origin{Source: assets.SourceGenerated}})
	}
	if len(ws) == 0 {
		return nil
	}
	_, err = m.s.assets.CommitAll(ctx, ws)
	return err
}

// checksum returns the hash of a checksum name of assets.Merger Match, nil for another.
func checksum(name string) hash.Hash {
	switch name {
	case "md5":
		return md5.New()
	case "sha1":
		return sha1.New()
	case "sha256":
		return sha256.New()
	case "sha512":
		return sha512.New()
	}
	return nil
}

// mergeAccess gives the target what the source has: each person's grants on the source (added
// to their grants on the target), the source's CI pipelines, and the place of the source in each
// group that reads the source but not the target.
func (s *Service) mergeAccess(ctx context.Context, row dbq.GetNexusMergeRow) (string, error) {
	from, err := s.repos.ByID(ctx, row.SourceRepoID)
	if err != nil {
		return "", err
	}
	into, err := s.repos.ByID(ctx, row.TargetRepoID)
	if err != nil {
		return "", err
	}
	// The labels of the format (npm dist-tags) first: they name the files the copy put there.
	if err := s.settleLabels(ctx, s.newMerger(from, into), row); err != nil {
		return "", err
	}
	have := map[string]perm.Set{}
	cur, err := s.auth.ListPermissions(ctx, into.ID)
	if err != nil {
		return "", err
	}
	for _, g := range cur {
		have[g.Username] = perm.SetOfStrings(g.Perms)
	}
	grants, err := s.auth.ListPermissions(ctx, from.ID)
	if err != nil {
		return "", err
	}
	for _, g := range grants {
		want := have[g.Username].Union(perm.SetOfStrings(g.Perms))
		if want == have[g.Username] {
			continue
		}
		if _, err := s.auth.SetPermissions(ctx, nil, []string{g.Username}, []string{into.Name}, want.Strings()); err != nil {
			return "", fmt.Errorf("give %s the grants of %s on %s: %w", g.Username, from.Name, into.Name, err)
		}
	}
	if s.publish != nil {
		if _, err := s.publish.AddPipelinesOf(ctx, from, into); err != nil {
			return "", fmt.Errorf("add %s to the CI pipelines of %s: %w", into.Name, from.Name, err)
		}
	}
	for _, gid := range s.repos.GroupsOf(from.ID) {
		g, err := s.repos.ByID(ctx, gid)
		if errors.Is(err, repos.ErrNotFound) {
			continue
		}
		if err != nil {
			return "", err
		}
		members, err := s.repos.MemberRepos(ctx, g)
		if err != nil {
			return "", err
		}
		names := make([]string, 0, len(members))
		reads := false
		for _, mm := range members {
			reads = reads || mm.ID == into.ID
			if mm.ID == from.ID {
				names = append(names, into.Name)
			} else {
				names = append(names, mm.Name)
			}
		}
		if reads {
			continue // the group loses the source with its delete and keeps reading the target
		}
		if _, err := s.repos.Update(ctx, g.Name, repos.Patch{Members: names}); err != nil {
			return "", fmt.Errorf("let the group %s read %s: %w", g.Name, into.Name, err)
		}
	}
	return stepSwitch, nil
}

// mergeSwitch switches the target over in one transaction with the next step: each mapped
// hosted repo that copies into it gets its switch-over (the merge's final sync and the files that
// sync failed to import), and the source cuts over when no mapped hosted repo copies any more,
// as at the end of a switch-over's final sync.
func (s *Service) mergeSwitch(ctx context.Context, row dbq.GetNexusMergeRow) error {
	return s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		rows, err := q.ListNexusTargetRepos(ctx, row.TargetRepoID)
		if err != nil {
			return err
		}
		failed := map[string]int64{}
		if row.SyncID != 0 {
			rs, err := q.ListNexusSyncRepos(ctx, row.SyncID)
			if err != nil {
				return err
			}
			for _, r := range rs {
				failed[r.SourceRepo] = r.Failed
			}
		}
		now := time.Now().Unix()
		var names []string
		var sourceID, total int64
		for _, r := range rows {
			if r.Kind != TypeHosted || r.Switched != 0 {
				continue
			}
			if err := q.InsertNexusSwitchOver(ctx, dbq.InsertNexusSwitchOverParams{SourceID: r.SourceID, SourceRepo: r.SourceRepo,
				SyncID: row.SyncID, Failed: failed[r.SourceRepo], SwitchedAt: now}); err != nil {
				return err
			}
			names = append(names, r.SourceRepo)
			sourceID = r.SourceID
			total += failed[r.SourceRepo]
		}
		if len(names) > 0 {
			copying, err := q.CountCopyingNexusRepos(ctx, sourceID)
			if err != nil {
				return err
			}
			msg := "Switched over " + strings.Join(names, ", ") + " after the merge of " + row.SourceName + " into " + row.TargetName + ": uploads to it go to this server."
			if copying == 0 {
				if _, err := q.CutOverNexusSource(ctx, dbq.CutOverNexusSourceParams{CutoverSyncID: row.SyncID, CutoverFailed: total, ID: sourceID}); err != nil {
					return err
				}
				msg += " Every repository switched over, so the server deleted the saved Nexus password."
			}
			if row.SyncID != 0 {
				if err := writeLog(ctx, q, row.SyncID, "", LevelInfo, msg); err != nil {
					return err
				}
			}
		}
		return q.SetNexusMergeStep(ctx, dbq.SetNexusMergeStepParams{Step: stepRetire, SyncID: row.SyncID, ID: row.ID})
	})
}

// tombName is the name a source takes when its own name goes to the target: <name>.merged-<id>,
// the name cut to fit.
func tombName(name string, id int64) string {
	suffix := tombSuffix + strconv.FormatInt(id, 10)
	if n := 64 - len(suffix); len(name) > n {
		name = strings.TrimRight(name[:n], ".-_")
	}
	return name + suffix
}

// mergeRetire gives the source's names to the target (repos.Service.Retire): the source takes
// its tomb name, and its name and its redirects answer for the target. In the same transaction
// the source's merge operation ends, its delete operation begins (the core's repo delete, so a
// restart resumes it there), and the merge goes on to its next step.
func (s *Service) mergeRetire(ctx context.Context, row dbq.GetNexusMergeRow) error {
	next := stepDone
	if row.Keep == KeepSource {
		next = stepRename
	}
	_, err := s.repos.Retire(ctx, row.SourceRepoID, row.TargetRepoID, tombName(row.SourceName, row.ID), func(tx *sql.Tx) error {
		op, err := s.ops.Get(ctx, tx, row.SourceRepoID)
		switch {
		case err == nil && op.Type == OpMerge:
			if err := s.ops.End(ctx, tx, op.ID, "nexus"); err != nil {
				return err
			}
		case err == nil:
			return fmt.Errorf("%w: %s", ErrBusy, s.ops.Message(op))
		case !errors.Is(err, ops.ErrNone):
			return err
		}
		if _, err := s.ops.Begin(ctx, tx, row.SourceRepoID, ops.TypeDelete, "core", "", ops.Running, row.StartedBy.Int64); err != nil {
			return err
		}
		return dbq.New(tx).SetNexusMergeStep(ctx, dbq.SetNexusMergeStepParams{Step: next, SyncID: row.SyncID, ID: row.ID})
	})
	return err
}

// mergeRename gives the target the source's name (keep source): the redirect of that name goes,
// and the target's old name becomes one (repos.Service.Rename).
func (s *Service) mergeRename(ctx context.Context, row dbq.GetNexusMergeRow) (string, error) {
	into, err := s.repos.ByID(ctx, row.TargetRepoID)
	if err != nil {
		return "", err
	}
	if into.Name != row.SourceName {
		if _, err := s.repos.Rename(ctx, into.Name, row.SourceName); err != nil {
			return "", fmt.Errorf("rename %s to %s: %w", into.Name, row.SourceName, err)
		}
	}
	return stepDone, nil
}

// mergeEnd ends the merge: the target's operation ends and the merge is done, then, in the run
// that retired the source, the source is deleted under its delete operation, as the repo delete
// does. Another run leaves that delete to the core, which resumes running deletes at its start.
func (s *Service) mergeEnd(ctx context.Context, row dbq.GetNexusMergeRow, retired bool) {
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		if op, err := s.ops.Get(ctx, tx, row.TargetRepoID); err == nil && op.Type == OpMerge {
			if err := s.ops.End(ctx, tx, op.ID, "nexus"); err != nil {
				return err
			}
		} else if err != nil && !errors.Is(err, ops.ErrNone) {
			return err
		}
		return dbq.New(tx).SetNexusMergeState(ctx, dbq.SetNexusMergeStateParams{State: MergeDone,
			FinishedAt: sql.NullInt64{Int64: time.Now().Unix(), Valid: true}, ID: row.ID})
	})
	if err != nil {
		s.log.Error("nexus: cannot end the merge", "merge", row.ID, "error", err.Error())
		return
	}
	m, _ := s.Merge(ctx, row.ID)
	name := row.TargetName
	if m != nil && m.Name != "" {
		name = m.Name
	}
	s.log.Info("nexus: merge done", "merge", row.ID, "from", row.SourceName, "into", name)
	_ = s.audit.Record(ctx, audit.Event{Action: ActionMergeDone, RepoID: row.TargetRepoID, Repo: name, Target: row.SourceName,
		Detail: map[string]any{"merge_id": row.ID, "from": row.SourceName, "into": row.TargetName, "name": name,
			"copied": row.Copied, "skipped": row.Skipped, "rebuilt": row.Rebuilt, "kept": row.Kept, "taken": row.Taken}})
	if !retired {
		return
	}
	from, err := s.repos.ByID(ctx, row.SourceRepoID)
	if err != nil || from.Op == nil || from.Op.Type != ops.TypeDelete || from.Op.State != ops.Running {
		return
	}
	s.deleteRetired(ctx, from, from.Op)
}

// deleteRetired deletes the retired source of a merge under its delete operation op, as the repo
// delete does: its files, then its row, then repo.delete. A failure sets op failed; the repo's
// page then offers Resume and Stop as for any repo delete.
func (s *Service) deleteRetired(ctx context.Context, repo *repos.Repo, op *ops.Op) {
	total := repo.FileCount
	_ = s.ops.Progress(ctx, op.ID, 0, total)
	n, err := s.assets.Remove(ctx, assets.Removal{Repo: repo, Prefixes: []repopath.Path{""},
		Origin: assets.Origin{Source: assets.SourceGenerated},
		Progress: func(done int) {
			_ = s.ops.Progress(ctx, op.ID, int64(done), max(total, int64(done)))
		}})
	if err == nil {
		err = s.repos.DeleteRow(ctx, repo.ID)
	}
	if err != nil {
		if s.base.Err() != nil {
			return // the server stops; the core resumes the delete at the next start
		}
		s.log.Error("nexus: delete the merged repo", "repo", repo.Name, "error", err.Error())
		_ = s.ops.Fail(ctx, op.ID, err)
		return
	}
	_ = s.audit.Record(ctx, audit.Event{Action: audit.RepoDelete, Repo: repo.Name, Target: repo.Name, Detail: map[string]any{"count": n}})
}

// failMerge sets merge row failed with err, and the merge operations of its repos with it.
func (s *Service) failMerge(ctx context.Context, row dbq.GetNexusMergeRow, cause error) {
	if err := dbq.New(s.db.W).SetNexusMergeState(ctx, dbq.SetNexusMergeStateParams{State: MergeFailed, Error: truncate(cause.Error(), 1000), ID: row.ID}); err != nil {
		s.log.Error("nexus: cannot save the failure of a merge", "merge", row.ID, "error", err.Error())
	}
	for _, id := range []int64{row.TargetRepoID, row.SourceRepoID} {
		if op, err := s.ops.Get(ctx, nil, id); err == nil && op.Type == OpMerge {
			_ = s.ops.Fail(ctx, op.ID, errors.New(truncate(cause.Error(), 1000)))
		}
	}
	_ = s.audit.Record(ctx, audit.Event{Action: ActionMergeFail, RepoID: row.TargetRepoID, Repo: row.TargetName, Target: row.SourceName,
		Detail: map[string]any{"merge_id": row.ID, "step": row.Step, "error": truncate(cause.Error(), 1000)}})
}

// mergeOf returns the merge of a merge operation.
func (s *Service) mergeOf(ctx context.Context, op *ops.Op) (dbq.GetNexusMergeRow, error) {
	id, err := strconv.ParseInt(op.Ref, 10, 64)
	if err != nil {
		return dbq.GetNexusMergeRow{}, fmt.Errorf("the merge operation names no merge: %q", op.Ref)
	}
	return dbq.New(s.db.R).GetNexusMerge(ctx, id)
}

// resumeMerge sets the failed merge of op running again (ops.Type Resume), on either repo. A
// merge that failed before its copy ended runs a new final sync first when the target still
// copies, then copies the files after its cursor.
func (s *Service) resumeMerge(ctx context.Context, op *ops.Op) error {
	row, err := s.mergeOf(ctx, op)
	if err != nil {
		return err
	}
	if row.State != MergeFailed {
		return fmt.Errorf("%w: the merge has not failed", ErrBusy)
	}
	step, syncID := row.Step, row.SyncID
	if step == stepSync || step == stepCopy {
		if _, copying, err := s.copyingInto(ctx, row.TargetRepoID); err != nil {
			return err
		} else if copying {
			step, syncID = stepSync, 0
		}
	}
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		if err := q.SetNexusMergeStep(ctx, dbq.SetNexusMergeStepParams{Step: step, SyncID: syncID, ID: row.ID}); err != nil {
			return err
		}
		return q.SetNexusMergeState(ctx, dbq.SetNexusMergeStateParams{State: MergeRunning, ID: row.ID})
	})
	if err != nil {
		return err
	}
	for _, id := range []int64{row.TargetRepoID, row.SourceRepoID} {
		if o, err := s.ops.Get(ctx, nil, id); err == nil && o.Type == OpMerge {
			if err := s.ops.SetState(ctx, o.ID, ops.Running, nil); err != nil {
				return err
			}
		}
	}
	_ = s.audit.Record(ctx, audit.Event{Action: ActionMergeResume, RepoID: row.TargetRepoID, Repo: row.TargetName, Target: row.SourceName,
		Detail: map[string]any{"merge_id": row.ID, "step": step}})
	s.startMerge(row.ID)
	return nil
}

// clearMerge stops the failed merge of op (ops.Type Clear), on either repo: the merge operations
// end, a target that still copies from Nexus gets its mirror operation back, and the source keeps
// its files. The files copied so far stay in the target.
func (s *Service) clearMerge(ctx context.Context, op *ops.Op) error {
	row, err := s.mergeOf(ctx, op)
	if err != nil {
		return err
	}
	if row.State != MergeFailed {
		return fmt.Errorf("%w: the merge has not failed", ErrBusy)
	}
	sourceID, copying, err := s.copyingInto(ctx, row.TargetRepoID)
	if err != nil {
		return err
	}
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		for _, id := range []int64{row.TargetRepoID, row.SourceRepoID} {
			o, err := s.ops.Get(ctx, tx, id)
			if errors.Is(err, ops.ErrNone) {
				continue
			}
			if err != nil {
				return err
			}
			if o.Type == OpMerge {
				if err := s.ops.End(ctx, tx, o.ID, "nexus"); err != nil {
					return err
				}
			}
		}
		if copying {
			if _, err := s.ops.Begin(ctx, tx, row.TargetRepoID, OpMirror, "nexus", mirrorRef(sourceID), ops.Held, row.StartedBy.Int64); err != nil && !errors.Is(err, ops.ErrBusy) {
				return err
			}
		}
		return dbq.New(tx).SetNexusMergeState(ctx, dbq.SetNexusMergeStateParams{State: MergeStopped, Error: row.Error,
			FinishedAt: sql.NullInt64{Int64: time.Now().Unix(), Valid: true}, ID: row.ID})
	})
	if err != nil {
		return err
	}
	_ = s.audit.Record(ctx, audit.Event{Action: ActionMergeStop, RepoID: row.TargetRepoID, Repo: row.TargetName, Target: row.SourceName,
		Detail: map[string]any{"merge_id": row.ID, "step": row.Step}})
	return nil
}

// resumeMerges continues the merges that a stop interrupted.
func (s *Service) resumeMerges(ctx context.Context) error {
	running, err := s.ops.Running(ctx, OpMerge, resumeMergesN)
	if err != nil {
		return fmt.Errorf("nexus: look up running merges: %w", err)
	}
	seen := map[string]bool{}
	for _, op := range running {
		if seen[op.Ref] {
			continue
		}
		seen[op.Ref] = true
		row, err := s.mergeOf(ctx, op)
		if err != nil {
			return err
		}
		if row.State == MergeRunning {
			s.log.Info("nexus: resuming merge", "merge", row.ID, "step", row.Step)
			s.startMerge(row.ID)
		}
	}
	return nil
}

// mergeMessage is the text of a merge operation, in full sentences.
func (s *Service) mergeMessage(op *ops.Op) string {
	row, err := s.mergeOf(context.Background(), op)
	if err != nil {
		return "A merge holds the repository."
	}
	if op.State == ops.Failed {
		return "The merge of " + row.SourceName + " into " + row.TargetName + " failed (" + op.Error + "). Resume it, or stop it, on the repository's page."
	}
	if op.RepoID == row.TargetRepoID {
		return row.SourceName + " is being merged into this repository. Uploads start again when the merge is done."
	}
	return "This repository is being merged into " + row.TargetName + ". Its name answers for " + row.TargetName + " when the merge is done."
}
