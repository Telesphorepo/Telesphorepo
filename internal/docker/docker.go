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

	"github.com/Telesphorepo/Telesphorepo/internal/clients"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/docker/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/jobs"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
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

// PushURL is the setting docker.push_url.
var PushURL = settings.String(&settings.Def{Key: "docker.push_url", Label: "Docker push address",
	Help:      "The address that Docker pushes to, e.g. https://push.repo.example.com. Set it when the main address goes through a proxy that limits request bodies (Cloudflare: 100 MB on Free and Pro). Docker sends each layer in one request, so a larger layer fails there. Point a DNS-only name at the server and enter it here. Snippets and CI files push to it; pulls keep the main address.",
	Normalize: func(v any) any { return strings.TrimRight(strings.TrimSpace(v.(string)), "/") },
	Validate:  validPushURL})

// validPushURL accepts "" or an http(s) URL with a host and no path, user, query or fragment.
func validPushURL(v any) error {
	s := v.(string)
	if s == "" {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("must be an http:// or https:// URL")
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("must be a scheme and a host only, e.g. https://push.repo.example.com: Docker reaches the registry at the root of a host")
	}
	return nil
}

// Format is the docker format of the module.
type Format struct {
	h      *module.Host
	q      *dbq.Queries // on the reader pool
	minter publish.Minter
	tokens tokenCache // the bearer tokens of proxy remotes
}

// Module returns the Docker module: the format docker (hosted, proxy and group repos), its routes
// at /v2/, /docker/ and /api/docker/, the settings docker.link_grace and docker.push_url, the
// job docker.links, the publisher client docker (the credential helper), the Nexus importer,
// the demo data and the seed.
func Module() module.Module {
	f := &Format{}
	return module.Module{
		ID: name, Title: "Docker", Requires: []string{"publish"},
		Register: func(x *module.Extender) {
			x.Schema(db.Component{Schema: schema, Queries: db.Sub(queriesFS, "queries")})
			x.Settings(&settings.Group{ID: "docker", Label: "Docker", Perm: perm.SettingsGeneral,
				Defs: []*settings.Def{PushURL.Def(), LinkGrace.Def()}})
			x.Init(func(ctx context.Context, h *module.Host) error {
				f.h = h
				f.q = dbq.New(h.DB.R)
				f.minter = module.Use[publish.Minter](h)
				return nil
			})
			x.PublisherClient(clients.Client{ID: "docker", Title: "Docker credential helper",
				Help: "The Telesphorepo credential helper signs pushes of docker, buildx, podman, helm and oras on this computer with the device key."})
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
				Importer:      f.importer(),
				UI:            ui,
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
