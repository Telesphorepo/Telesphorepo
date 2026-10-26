// Package raw serves generic hosted files at /raw/{repo}/{path...}: any client that speaks
// HTTP PUT and GET (curl, wget, CI scripts) can publish and fetch files by path. It mounts the
// standard format routes; the asset pipeline stores every file as a content-addressed blob.
package raw

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

const name = "raw"

// Overwrite policies of the repo config.
const (
	OverwriteDeny  = "deny"  // re-upload of identical bytes is 200, different bytes 409
	OverwriteAllow = "allow" // a re-upload replaces the file
)

// maxRules is the most overwrite rules a raw repo takes.
const maxRules = 100

// Config is the repo config of a raw repo: the overwrite policy of each path is that of the first
// rule whose glob matches it, else Overwrite.
type Config struct {
	Overwrite string `json:"overwrite"`
	Rules     []Rule `json:"rules,omitempty"`
}

// Rule is one overwrite rule: the policy of the paths that Match (a format.Glob).
type Rule struct {
	Match     string `json:"match"`
	Overwrite string `json:"overwrite"`
}

// policy is the overwrite policy of path p.
func (c Config) policy(p string) string {
	for _, r := range c.Rules {
		if format.Glob(r.Match, p) {
			return r.Overwrite
		}
	}
	return c.Overwrite
}

// edgePaths classifies the paths of repo for the edge: a path whose policy is deny never changes
// (identical bytes are kept, other bytes refused), so its copies can serve from every region.
func edgePaths(repo *repos.Repo) []format.EdgeRule {
	c, err := repoConfig(repo)
	if err != nil {
		return []format.EdgeRule{{Match: "**", Immutable: false}}
	}
	out := make([]format.EdgeRule, 0, len(c.Rules)+1)
	for _, r := range c.Rules {
		out = append(out, format.EdgeRule{Match: r.Match, Immutable: r.Overwrite != OverwriteAllow})
	}
	return append(out, format.EdgeRule{Match: "**", Immutable: c.Overwrite != OverwriteAllow})
}

// Format is the raw format of the module.
type Format struct {
	h *module.Host
}

// Module returns the raw module: one format with uploads, imports, demo data and seeds.
func Module() module.Module {
	f := &Format{}
	return module.Module{
		ID: name, Title: "Raw files",
		Register: func(x *module.Extender) {
			x.Init(func(ctx context.Context, h *module.Host) error {
				f.h = h
				return nil
			})
			x.Format(format.Spec{
				Name: name, Title: "Raw files", Icon: icon, Kinds: []string{repos.KindHosted},
				Description: "Any files, downloaded by their path.",
				Config: format.ConfigSpec{Default: json.RawMessage(`{"overwrite":"deny"}`), Validate: validateConfig,
					Fields: []format.Field{{Key: "overwrite", Label: "Replace files", Type: format.FieldEnum,
						Options: []string{OverwriteDeny, OverwriteAllow}, Default: json.RawMessage(`"deny"`),
						Help: "deny keeps a stored file: an upload of other bytes to its path fails, the same bytes succeed. allow replaces it. Deleting a file is not replacing it: a person who may delete still can."},
						{Key: "rules", Label: "Rules by path", Type: format.FieldRules, ValueKey: "overwrite",
							Options: []string{OverwriteDeny, OverwriteAllow}, Placeholder: "builds/**",
							Help: "The first rule whose pattern matches a path decides for it; a path that no rule matches follows Replace files. * is any name, ** any depth: builds/** deny with latest.json allow keeps every build and lets latest.json move."}}},
				Describe: describe,
				Admit:    admit,
				Usage:    f.Usage,
				Tools:    []format.Tool{format.CLITool("file.tar.gz")},
				Publish:  format.PublishCLI,
				Upload:   f.uploadSpec(),
				Importer: &format.Importer{Sources: []string{"raw"}, Target: format.SourcePath},
				UI:       format.UIDescriptor{SnippetTools: map[string]string{"curl": "curl", "wget": "wget", "telesphorepo": "Telesphorepo CLI"}},
				// A path under deny never changes, so it serves from the regions.
				Edge: &format.EdgeSpec{Shape: format.EdgeStandard, Paths: edgePaths},
			})
			x.Demo(module.DemoData{
				Repos: []module.DemoRepo{{Name: "raw", Description: "Documentation sites, release archives and build logs", Public: true}},
				Fill:  f.Demo,
			})
			x.Seed(seed)
		},
	}
}

// validateConfig accepts {"overwrite": "allow"|"deny", "rules": [{"match", "overwrite"}]}; a
// missing overwrite is "deny". A rule needs a glob that format.ValidGlob takes and a policy; an
// error names the rule by its number from 1.
func validateConfig(raw json.RawMessage) (json.RawMessage, error) {
	var c Config
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("invalid raw config: %w", err)
	}
	switch c.Overwrite {
	case "":
		c.Overwrite = OverwriteDeny
	case OverwriteDeny, OverwriteAllow:
	default:
		return nil, fmt.Errorf("overwrite must be %q or %q", OverwriteAllow, OverwriteDeny)
	}
	if len(c.Rules) > maxRules {
		return nil, fmt.Errorf("at most %d rules", maxRules)
	}
	for i, r := range c.Rules {
		if err := format.ValidGlob(r.Match); err != nil {
			return nil, fmt.Errorf("rule %d (%q): %v", i+1, r.Match, err)
		}
		if r.Overwrite != OverwriteDeny && r.Overwrite != OverwriteAllow {
			return nil, fmt.Errorf("rule %d (%s): overwrite must be %q or %q", i+1, r.Match, OverwriteAllow, OverwriteDeny)
		}
	}
	return json.Marshal(c)
}

// contentTypes are the content types of raw files by extension; any other file is
// application/octet-stream.
var contentTypes = map[string]string{
	".txt": "text/plain", ".log": "text/plain", ".md": "text/plain",
	".md5": "text/plain", ".sha1": "text/plain", ".sha256": "text/plain", ".sha512": "text/plain",
	".xml": "application/xml", ".pom": "application/xml", ".json": "application/json",
	".jar": "application/java-archive", ".gz": "application/gzip", ".tgz": "application/gzip",
	".zip": "application/zip", ".asc": "application/pgp-signature", ".sig": "application/pgp-signature",
	".deb": "application/vnd.debian.binary-package",
}

// describe gives every raw file the kind artifact and the content type of its extension.
func describe(p repopath.Path) (assets.Describe, bool) {
	ct := contentTypes[strings.ToLower(path.Ext(string(p)))]
	if ct == "" {
		ct = assets.FallbackContentType
	}
	return assets.Describe{ContentType: ct, Kind: assets.KindArtifact}, true
}

// admit applies the overwrite policy of the path: identical bytes are kept, different bytes
// replace the file only when the policy allows overwrite (409 otherwise).
func admit(_ context.Context, w *assets.Write, existing *assets.Asset) (assets.Decision, error) {
	if existing == nil {
		return assets.Store, nil
	}
	if existing.SHA256 == w.Body.Digest() {
		return assets.Keep, nil
	}
	c, err := repoConfig(w.Repo)
	if err != nil {
		return 0, fmt.Errorf("bad repo config: %w", err)
	}
	if c.policy(string(w.Path)) != OverwriteAllow {
		return 0, assets.Refuse(http.StatusConflict, "%s: file exists and the repo does not allow overwrite", w.Path)
	}
	return assets.Store, nil
}

// Usage returns curl and wget commands to download a file (a private repo takes a username and a
// read token), and the publish command of the Telesphorepo CLI. The core adds the CI files of the
// tool telesphorepo.
func (f *Format) Usage(repo *repos.Repo, baseURL string) []format.Snippet {
	u := baseURL + "/raw/" + repo.Name + "/path/to/file.tar.gz"
	curl, wget := "curl -fLO "+u, "wget "+u
	note := ""
	if !repo.AnonymousRead {
		curl = format.ReadTokenPrompt + "\n" + format.ReadTokenConfig + " | curl -fLO -K - " + u
		wget = "wget --user YOUR_USERNAME --ask-password " + u
		note = "Create a read token on Your account › Tokens; it works where a password does. Enter it at the prompt."
	}
	return []format.Snippet{
		{ID: "curl.download", Tool: "curl", Purpose: format.SnippetUse, Context: format.SnippetRepo, Title: "Download a file", Lang: "sh", Code: curl + "\n", Note: note},
		{ID: "wget.download", Tool: "wget", Purpose: format.SnippetUse, Context: format.SnippetRepo, Title: "Download a file", Lang: "sh", Code: wget + "\n", Note: note},
		format.CLISnippet("telesphorepo", format.CLIPublish+" file.tar.gz",
			"The CLI uploads each file into the folder that path names in telesphorepo.toml. Or drop the files on the Files view in the browser."),
	}
}

// uploadFields are the fields of a web UI upload: the folder the files go into.
var uploadFields = []format.Field{{Key: "folder", Label: "Folder", Type: format.FieldString, Placeholder: "Empty for the repository root",
	Help: "Files keep their paths inside a dropped folder, under this folder."}}

func (f *Format) uploadSpec() *format.UploadSpec {
	return &format.UploadSpec{
		Single: true, Folder: true,
		Hint:   "Any files, or a folder with its subfolders.",
		One:    "file",
		Many:   "files",
		Note:   "A file with the same name is replaced where the repository's rules allow it.",
		Empty:  "The folder has no files.",
		Fields: uploadFields,
		Plan:   f.Plan,
		Store:  f.Store,
	}
}

// Plan plans a web UI upload: every file is one item that goes to its relative path (its name, or
// its path inside a dropped folder) under the folder in the field "folder" (first value: the open
// folder of the Files view; "": the repo root).
func (f *Format) Plan(ctx context.Context, repo *repos.Repo, in format.PlanInput) (*format.Plan, error) {
	if err := format.CheckFields(in.Fields, uploadFields); err != nil {
		return nil, err
	}
	folder, ok := in.Fields["folder"]
	if !ok {
		folder = in.Folder
	}
	folder = strings.Trim(strings.TrimSpace(folder), "/")
	plan := &format.Plan{Fields: []format.PlanField{{Field: uploadFields[0], Value: folder}}}
	dir, err := repopath.Folder(folder)
	if err != nil {
		plan.Fields[0].Problem = "Enter a folder such as releases/1.0, without empty, . or .. parts."
	}
	for _, file := range in.Files {
		item := format.PlanItem{Title: file.Path, Files: []format.PlanTarget{{Path: file.Path}}}
		if err == nil {
			if p, perr := repopath.Join(dir, file.Path); perr != nil {
				item.Problems = append(item.Problems, "the path is not valid in the folder")
			} else {
				item.Files[0].Target = string(p)
			}
		}
		plan.Items = append(plan.Items, item)
	}
	return plan, nil
}

// Store stores the files of a plan in one commit, at their targets, under the overwrite policy
// of each path.
func (f *Format) Store(ctx context.Context, repo *repos.Repo, plan *format.Plan, files map[string]*assets.Spooled, o assets.Origin) ([]*assets.Result, error) {
	var ws []assets.Write
	for _, it := range plan.Items {
		for _, t := range it.Files {
			p, err := repopath.Parse(t.Target)
			if err != nil || files[t.Path] == nil {
				return nil, format.BadUpload("the upload has no file for %s", t.Path)
			}
			ws = append(ws, assets.Write{Repo: repo, Path: p, Body: assets.Body{Spool: files[t.Path]}, Origin: o})
		}
	}
	return f.h.Assets.CommitAll(ctx, ws)
}

// repoConfig decodes the repo config, which was validated when it was stored.
func repoConfig(repo *repos.Repo) (Config, error) {
	var c Config
	err := json.Unmarshal(repo.Config, &c)
	return c, err
}
