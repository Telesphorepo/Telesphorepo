// Package docker is the Docker module: an OCI registry (the format "docker") that serves the OCI
// Distribution Spec 1.1 at /v2/. It holds container images, Helm charts and any other OCI
// artifact. The repo is the first segment of an OCI repository name: an image is
// <host>/<repo>/<image>[:tag|@digest]. Pushes need a publish session, which the token endpoint
// /docker/token puts into a core bearer token. Proxy repos cache another registry; group repos
// read their members in order.
package docker

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/docker/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/jobs"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/netrules"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/publish"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// name is the module ID and the format name: the URL segment of the token endpoint, the
// repos.format value and the schema component.
const name = "docker"

//go:embed schema.sql
var schema string

//go:embed queries/*.sql
var queriesFS embed.FS

// LinkGrace is the setting docker.link_grace.
var LinkGrace = settings.Duration(&settings.Def{Key: "docker.link_grace", Default: "24h", Label: "Unused layer grace period",
	Help: "How long a layer that no manifest uses stays. It must outlast the slowest push, because a client uploads the layers before the manifest.",
	Validate: func(v any) error {
		if v.(time.Duration) < time.Second {
			return errors.New("must be at least 1s")
		}
		return nil
	}})

// cloudflareNote is the 100 MB warning of the settings and of the push snippets.
const cloudflareNote = "Behind Cloudflare (Free and Pro plans), each image layer must be under 100 MB. Pulls have no limit."

// DirectUploads is the setting docker.direct_uploads.
var DirectUploads = settings.Bool(&settings.Def{Key: "docker.direct_uploads", Default: "false", Label: "Expose the origin IP for Docker uploads",
	Help: cloudflareNote + " A larger layer fails, unless this is on. When it is on, a push still starts on the main address, and the server tells the client to send the layer bytes to the Docker upload address. Clients need no setup. This makes the IP address of the server public. Open the port of the upload address in the firewall of the server for the clients that push. Not behind Cloudflare: leave it off."})

// UploadURL is the setting docker.upload_url.
var UploadURL = settings.String(&settings.Def{Key: "docker.upload_url", Label: "Docker upload address",
	Help:      "The address of the server that takes the layer bytes when Expose the origin IP for Docker uploads is on, e.g. https://upload.repo.example.com: a DNS-only name (in Cloudflare, DNS only) or the address of the server. ORAS and Helm 4 push to this address.",
	Normalize: func(v any) any { return strings.TrimRight(strings.TrimSpace(v.(string)), "/") },
	Validate:  validUploadURL})

// validate is the Validate of the docker settings group: direct uploads need an upload address.
func validate(d *settings.Draft, _ settings.Mode) error {
	if DirectUploads.Get(d.Next) && UploadURL.Get(d.Next) == "" {
		d.Fail(UploadURL.Name(), "set the upload address to expose the origin IP for Docker uploads")
	}
	return nil
}

// validUploadURL accepts "" or an http(s) URL with a host and no path, user, query or fragment.
func validUploadURL(v any) error {
	s := v.(string)
	if s == "" {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("must be an http:// or https:// URL")
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("must be a scheme and a host only, e.g. https://upload.repo.example.com")
	}
	return nil
}

// Format is the docker format of the module.
type Format struct {
	h       *module.Host
	q       *dbq.Queries // on the reader pool
	minter  publish.Minter
	network *netrules.Service // reads whether the server is behind Cloudflare
	tokens  tokenCache        // the bearer tokens of proxy remotes
}

// Module returns the Docker module: the format docker (hosted, proxy and group repos), its routes
// at /v2/, /docker/ and /api/docker/, the settings docker.direct_uploads, docker.upload_url and docker.link_grace, the
// job docker.links, the Nexus importer, the demo data and the seed. The native tools push
// through the credential helper of the Telesphorepo CLI (format.PublishHelper).
func Module() module.Module {
	f := &Format{}
	return module.Module{
		ID: name, Title: "Docker", Requires: []string{"network", "publish"},
		Register: func(x *module.Extender) {
			x.Schema(db.Component{Schema: schema, Queries: db.Sub(queriesFS, "queries")})
			x.Settings(&settings.Group{ID: "docker", Label: "Docker", Perm: perm.SettingsGeneral,
				Defs: []*settings.Def{DirectUploads.Def(), UploadURL.Def(), LinkGrace.Def()}, Validate: validate})
			x.Init(func(ctx context.Context, h *module.Host) error {
				f.h = h
				f.q = dbq.New(h.DB.R)
				f.minter = module.Use[publish.Minter](h)
				f.network = module.Use[*netrules.Service](h)
				return nil
			})
			x.RootPrefix("/v2/")
			x.Routes(f.apiRoutes)
			x.Format(format.Spec{
				Name: name, Title: "Docker", Icon: icon,
				Description:   "Container images, Helm charts and other OCI artifacts.",
				Kinds:         []string{repos.KindHosted, repos.KindProxy, repos.KindGroup},
				ValidName:     validRepoName,
				Config:        format.ConfigSpec{Default: json.RawMessage(`{}`), Validate: validateConfig},
				Routes:        f.routes,
				Describe:      describe,
				Admit:         f.admit,
				OnCommitted:   f.onCommitted,
				AfterCommit:   f.afterCommit,
				OnRemoved:     f.onRemoved,
				Classify:      f.classify,
				ProxyProbe:    proxyProbe,
				RemoteAuth:    f.remoteAuth,
				Versions:      f.versions,
				Builds:        f.builds,
				Usage:         f.usage,
				ArtifactUsage: f.artifactUsage,
				Tools:         []format.Tool{f.dockerTool()},
				Publish:       format.PublishHelper,
				Importer:      f.importer(),
				UI:            ui,
				// Tags (_tags/) match no immutable glob, so they are mutable.
				Edge: &format.EdgeSpec{Shape: format.EdgeOCI, Immutable: []string{"**/_manifests/sha256/*", "**/_blobs/sha256/*"}},
			})
			x.Job(jobs.Job{Name: "docker.links", Run: f.sweepLinks})
			x.Demo(module.DemoData{Repos: demoRepos, Fill: f.demo})
			x.Seed(f.seed)
		},
	}
}

// validateConfig accepts the empty config {}: a docker repo has no options.
func validateConfig(raw json.RawMessage) (json.RawMessage, error) {
	var c struct{}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("a docker repository has no settings: %w", err)
	}
	return json.RawMessage(`{}`), nil
}

// ui is what the web UI shows of the format (GET /api/formats).
var ui = format.UIDescriptor{
	DeleteNotes: []format.DeleteNote{
		{Match: "**/_tags/*", One: "tag", Many: "tags", Text: "Deleting a tag keeps its manifest."},
		{Match: "**/_manifests/sha256/*", One: "manifest", Many: "manifests", Text: "Deleting a manifest deletes its tags."},
		{Match: "**/_blobs/sha256/*", One: "layer", Many: "layers", Text: "A layer that a manifest uses cannot be deleted; unused layers go after docker.link_grace."},
	},
	FileDetails: []format.FileDetail{
		{Match: "**/_tags/*", Part: "manifest"},
		{Match: "**/_manifests/sha256/*", Part: "manifest"},
	},
	RemotePresets: []format.RemotePreset{
		{Label: "Docker Hub", URL: "https://" + dockerHub + "/"},
		{Label: "GitHub Container Registry", URL: "https://ghcr.io/"},
		{Label: "Quay", URL: "https://quay.io/"},
	},
	ProxyNote:      "Images keep the digests of the remote registry.",
	ArtifactKinds:  map[string]string{kindImage: "Container image", kindHelm: "Helm chart", kindArtifact: "OCI artifact"},
	ArtifactName:   map[string]string{kindImage: "Image", kindHelm: "Chart", kindArtifact: "Artifact"},
	SnippetTools:   map[string]string{"docker": "Docker", "helm": "Helm", "oras": "ORAS"},
	InstallHeading: "Pull it",
	BuildFiles: []format.BuildFileLabel{
		{Extension: "manifest", Classifier: "", Label: "Manifest"},
		{Extension: "manifest", Classifier: "*", Label: "Manifest for {classifier}"},
		{Extension: "config", Classifier: "*", Label: "Config"},
		{Extension: "layer", Classifier: "*", Label: "Layer"},
	},
}
