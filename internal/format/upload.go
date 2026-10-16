package format

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// UploadSpec takes files that a person uploads through the web UI. The server plans every
// upload: POST /api/repos/{name}/upload/plan answers the Plan of the files without storing
// anything, and POST /api/repos/{name}/upload runs the same Plan on the full files, then Store.
type UploadSpec struct {
	// Single and Folder say whether the UI takes chosen files and a dropped or chosen folder.
	Single, Folder bool
	// Accept are the file types of the file picker, e.g. ".deb" (empty: any file).
	Accept []string
	// Hint says what to drop, in one sentence.
	Hint string
	// Keep are the globs of the files of a folder that the browser sends (empty: every file).
	Keep []string
	// One and Many are the words for one item of a plan, e.g. "artifact" and "artifacts".
	One, Many string
	// Note says what the server does with the files besides storing them, in one sentence.
	Note string
	// Ignored says why the plan leaves files out, in one sentence ("" for none).
	Ignored string
	// Empty says what an upload needs when the plan has no item, in one sentence.
	Empty string
	// Fields are the form fields an upload may take. A Plan lists the ones that apply.
	Fields []Field
	// Reads are the files whose first bytes a Plan reads (e.g. a pom). The browser sends at most
	// MaxBytes of each matching file with a plan request.
	Reads []ReadRule
	// Plan works out what an upload stores, and the problems that refuse it. It stores nothing.
	// A client mistake that refuses the whole upload (an unknown field) is an *assets.Refusal.
	Plan func(ctx context.Context, repo *repos.Repo, in PlanInput) (*Plan, error)
	// Store stores the files of plan through the asset pipeline with origin o (source "ui"):
	// files maps each path of the plan's items to its spooled body. The answer lists every file
	// the commit wrote, the generated ones included.
	Store func(ctx context.Context, repo *repos.Repo, plan *Plan, files map[string]*assets.Spooled, o assets.Origin) ([]*assets.Result, error)
}

// ReadRule says how much of the files that match Glob a Plan reads.
type ReadRule struct {
	Glob     string `json:"glob"`
	MaxBytes int64  `json:"max_bytes"`
}

// PlanInput is what a Plan works on: the files with their paths relative to the drop (the base
// name for a chosen file), the fields the person typed, and the folder of the Files view the
// upload started in ("" for the top; it gives the first values of fields).
type PlanInput struct {
	Files  []PlanFile
	Fields map[string]string
	Folder string
}

// PlanFile is one file of a plan: Head holds its first bytes when a ReadRule matches its path
// (at most MaxBytes; the whole file when it is shorter), else nil.
type PlanFile struct {
	Path string
	Size int64
	Head []byte
}

// Name is the base name of the file.
func (f PlanFile) Name() string { return f.Path[strings.LastIndexByte(f.Path, '/')+1:] }

// Truncated reports whether Head holds less than the whole file.
func (f PlanFile) Truncated() bool { return int64(len(f.Head)) < f.Size }

// InFolder reports whether any file came with a folder path, i.e. the person dropped a folder.
func InFolder(files []PlanFile) bool {
	return slices.ContainsFunc(files, func(f PlanFile) bool { return strings.Contains(f.Path, "/") })
}

// Plan is what an upload stores. Ignored are the paths of files that no item takes.
type Plan struct {
	Items   []PlanItem  `json:"items"`
	Ignored []string    `json:"ignored"`
	Fields  []PlanField `json:"fields"`
}

// PlanItem is one artifact or package of an upload: its title (e.g. "com.acme:app:1.0"), its
// files with the path each one gets, facts it read (e.g. its coordinates), and the problems that
// refuse it (empty: it can go).
type PlanItem struct {
	Title    string            `json:"title"`
	Files    []PlanTarget      `json:"files"`
	Facts    map[string]string `json:"facts,omitempty"`
	Problems []string          `json:"problems"`
	// Meta is the format's data for Store (e.g. the coordinates of an artifact).
	Meta any `json:"-"`
}

// PlanTarget is one file of an item: its path in the upload and the repo path it gets ("" while
// the plan cannot tell).
type PlanTarget struct {
	Path   string `json:"path"`
	Target string `json:"target"`
}

// PlanField is a field that applies to an upload: its value, and whether the plan read the value
// from the files (then the UI shows it and the person cannot change it). Problem refuses the
// upload.
type PlanField struct {
	Field
	Value   string `json:"value"`
	Read    bool   `json:"read,omitempty"`
	Problem string `json:"problem,omitempty"`
}

// Blocked returns why the plan refuses an upload: the first field problem, or the first item with
// a problem; "" when every item can go.
func (p *Plan) Blocked() string {
	for _, f := range p.Fields {
		if f.Problem != "" {
			return f.Label + ": " + f.Problem
		}
	}
	for _, it := range p.Items {
		if len(it.Problems) > 0 {
			return it.Title + ": " + strings.Join(it.Problems, "; ")
		}
	}
	return ""
}

// Normalize makes the nil lists of p empty, for JSON.
func (p *Plan) Normalize() {
	if p.Items == nil {
		p.Items = []PlanItem{}
	}
	if p.Ignored == nil {
		p.Ignored = []string{}
	}
	if p.Fields == nil {
		p.Fields = []PlanField{}
	}
	for i := range p.Items {
		if p.Items[i].Files == nil {
			p.Items[i].Files = []PlanTarget{}
		}
		if p.Items[i].Problems == nil {
			p.Items[i].Problems = []string{}
		}
	}
}

// CheckFields refuses a field that fields does not name (400).
func CheckFields(in map[string]string, fields []Field) error {
	for k := range in {
		if !slices.ContainsFunc(fields, func(f Field) bool { return f.Key == k }) {
			names := make([]string, len(fields))
			for i, f := range fields {
				names[i] = f.Key
			}
			if len(names) == 0 {
				return BadUpload("unknown field %q; this upload takes no fields", k)
			}
			return BadUpload("unknown field %q; this upload takes %s", k, strings.Join(names, ", "))
		}
	}
	return nil
}

// Uploaded is one file a manual upload stored or found unchanged. Generated files (checksums,
// metadata) are listed too, marked generated.
type Uploaded struct {
	Path      string `json:"path"`
	Status    string `json:"status"` // "created", "replaced" or "unchanged"
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	Generated bool   `json:"generated,omitempty"`
}

// UploadedOf returns the answer lines of the results of a commit.
func UploadedOf(rs []*assets.Result) []Uploaded {
	out := make([]Uploaded, 0, len(rs))
	for _, r := range rs {
		u := Uploaded{Path: string(r.Write.Path), Status: string(r.Status), Generated: r.Write.Origin.Source == assets.SourceGenerated}
		if r.Asset != nil {
			u.Size, u.SHA256 = r.Asset.Size, r.Asset.SHA256
		}
		out = append(out, u)
	}
	return out
}

// BadUpload returns a 400 refusal of an upload.
func BadUpload(format string, args ...any) error {
	return assets.Refuse(http.StatusBadRequest, format, args...)
}
