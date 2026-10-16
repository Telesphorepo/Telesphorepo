package docker

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net/url"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/docker/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/netrules"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// helperName is the Docker credential helper of Telesphorepo.
const helperName = "docker-credential-telesphorepo"

// notes are the sentences that the snippets of a server add: an http base_url needs an insecure
// registry entry, and a base_url with a path needs the registry at the root of a host.
func notes(baseURL, registry string) string {
	var out []string
	if u, err := url.Parse(baseURL); err == nil {
		if u.Scheme == "http" {
			out = append(out, "Add "+registry+" to insecure-registries in /etc/docker/daemon.json.")
		}
		if p := strings.Trim(u.Path, "/"); p != "" {
			out = append(out, "Docker reaches a registry at the root of a host: let a reverse proxy send /v2/ and /docker/ of a host to the server.")
		}
	}
	return strings.Join(out, " ")
}

// usage returns the snippets of a docker repo: log in for a private repo, pull an image, pull a
// Helm chart; and for a hosted repo the publish snippets: from a computer with the credential
// helper (Docker, Helm and ORAS), and by hand with a publish token. Pulls and Docker pushes name the
// registry host; ORAS and Helm 4 name the upload host when direct uploads are on.
func (f *Format) usage(repo *repos.Repo, baseURL string) []format.Snippet {
	host := registryHost(baseURL)
	note := notes(baseURL, host)
	var out []format.Snippet
	if !repo.AnonymousRead {
		out = append(out, format.Snippet{ID: "docker.login", Tool: "docker", Purpose: format.SnippetUse, Context: format.SnippetRepo,
			Title: "Log in", Lang: "sh", Code: format.ReadTokenPrompt + "\nprintf '%s' \"$READ_TOKEN\" | docker login " + host + " -u YOUR_USERNAME --password-stdin\n",
			Note: "Create a read token on Settings › Tokens. Enter it at the prompt."})
	}
	out = append(out,
		format.Snippet{ID: "docker.pull", Tool: "docker", Purpose: format.SnippetUse, Context: format.SnippetRepo,
			Title: "Pull an image", Lang: "sh", Code: "docker pull " + host + "/" + repo.Name + "/IMAGE:TAG\n", Note: note},
		format.Snippet{ID: "helm.pull", Tool: "helm", Purpose: format.SnippetUse, Context: format.SnippetRepo,
			Title: "Pull a Helm chart", Lang: "sh", Code: "helm pull oci://" + host + "/" + repo.Name + "/CHART --version X\n"},
	)
	if repo.Kind != repos.KindHosted {
		return out
	}
	// With direct uploads, ORAS and Helm 4 name the upload host: they refuse an upload Location on
	// another host, and the upload host answers them on its own host. Without direct uploads,
	// behind Cloudflare, the docker pushes say that a layer must be under 100 MB.
	ociHost, ociNote, pushNote := host, "", ""
	if u, ok := f.directUploads(); ok {
		ociHost, ociNote = u.Host, "ORAS and Helm 4 push to the upload address "+u.Host+", because they send layers only to the host of the push."
	} else if f.network.TrustsList(netrules.PresetCloudflare) {
		pushNote = cloudflareNote
	}
	out = append(out,
		format.Snippet{ID: "docker.helper", Tool: "docker", Purpose: format.SnippetPublish, Context: format.SnippetRepo, Via: format.ViaComputer,
			Title: "Push with the credential helper", Lang: "sh", Code: helperName + " login " + baseURL + "   # once per computer\n" +
				"docker tag IMAGE " + host + "/" + repo.Name + "/IMAGE:TAG\n" +
				"docker push " + host + "/" + repo.Name + "/IMAGE:TAG\n",
			Note: joinNotes("The helper enrolls this computer once; approve it in the browser. Then docker, buildx, podman, helm and oras push without a password.", pushNote)},
		format.Snippet{ID: "helm.push", Tool: "helm", Purpose: format.SnippetPublish, Context: format.SnippetRepo, Via: format.ViaComputer,
			Title: "Push a Helm chart", Lang: "sh", Code: "helm push mychart-0.1.0.tgz oci://" + ociHost + "/" + repo.Name + "\n",
			Note: joinNotes("Run "+helperName+" login "+baseURL+" once first.", ociNote)},
		format.Snippet{ID: "oras.push", Tool: "oras", Purpose: format.SnippetPublish, Context: format.SnippetRepo, Via: format.ViaComputer,
			Title: "Push an OCI artifact", Lang: "sh", Code: "oras push " + ociHost + "/" + repo.Name + "/ARTIFACT:TAG FILE\n",
			Note: joinNotes("Run "+helperName+" login "+baseURL+" once first.", ociNote)},
		format.Snippet{ID: "docker.publish", Tool: "docker", Purpose: format.SnippetPublish, Context: format.SnippetRepo, Via: format.ViaHand,
			Title: "Push with a publish token", Lang: "sh", Code: format.TokenPrompt + "   # Settings › Tokens › New publish token\n" +
				"printf '%s' \"$TOKEN\" | docker login " + host + " -u publish --password-stdin\n" +
				"docker push " + host + "/" + repo.Name + "/IMAGE:TAG\n" +
				"docker logout " + host + "\n",
			Note: joinNotes(joinNotes("A publish token lasts as long as its publish session: the push must end within it.", pushNote), note)},
	)
	return out
}

func joinNotes(a, b string) string {
	if a == "" || b == "" {
		return a + b
	}
	return a + " " + b
}

// artifactUsage returns the snippets that pull one image: path is an image folder <image>/, or a
// tag or manifest file of one. version names a tag ("" for the newest). The pinned mode pulls the
// digest that the tag names; the latest mode pulls the tag. A Helm chart pulls with helm, another
// OCI artifact with oras.
func (f *Format) artifactUsage(ctx context.Context, repo *repos.Repo, baseURL, p, version, mode string) (*format.ArtifactUsage, error) {
	folder, tag, digest := p, version, ""
	if op, ok := parsePath(strings.TrimPrefix(p, "/")); ok {
		folder = op.image + "/"
		switch op.kind {
		case kindTag:
			tag = op.ref
		case kindManifest:
			digest = op.ref
		}
	}
	image, art, err := f.imageOf(ctx, repo, folder)
	if err != nil || art == nil {
		return nil, err
	}
	switch {
	case digest != "":
		// A manifest file: the digest, and a tag that names it when there is one.
		tags, err := f.q.SomeTagsOfDigest(ctx, dbq.SomeTagsOfDigestParams{RepoID: repo.ID, Image: image, Digest: digest, Limit: 1})
		if err != nil {
			return nil, err
		}
		tag = ""
		if len(tags) > 0 {
			tag = tags[0]
		}
	case tag == "":
		rows, err := f.q.ListTagsRecent(ctx, dbq.ListTagsRecentParams{RepoID: repo.ID, Image: image, BeforeAt: math.MaxInt64, Limit: 1})
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return nil, nil
		}
		tag, digest = rows[0].Tag, rows[0].Digest
	default:
		t, err := f.q.GetTag(ctx, dbq.GetTagParams{RepoID: repo.ID, Image: image, Tag: tag})
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		digest = t.Digest
	}
	if mode != format.VersionLatest || tag == "" {
		mode = format.VersionPinned // a manifest without a tag pulls by digest only
	}
	host := registryHost(baseURL)
	ref := host + "/" + repo.Name + "/" + image
	comment := ""
	if tag != "" {
		comment = "   # " + tag
	}
	var out []format.Snippet
	if !repo.AnonymousRead {
		out = append(out, format.Snippet{ID: "docker.login", Tool: "docker", Purpose: format.SnippetUse, Context: format.SnippetArtifact,
			Title: "Log in", Lang: "sh", Code: format.ReadTokenPrompt + "\nprintf '%s' \"$READ_TOKEN\" | docker login " + host + " -u YOUR_USERNAME --password-stdin\n",
			Note: "Create a read token on Settings › Tokens. Enter it at the prompt. Helm and ORAS read the same login."})
	}
	pinned := mode == format.VersionPinned
	switch art.Kind {
	case kindHelm:
		out = append(out, format.Snippet{ID: "helm.pull", Tool: "helm", Purpose: format.SnippetUse, Context: format.SnippetArtifact,
			Title: "Pull " + art.Name, Lang: "sh", Code: helmPull(ref, tag, digest)})
	case kindArtifact:
		code := "oras pull " + ref + ":" + tag + "\n"
		if pinned {
			code = "oras pull " + ref + "@sha256:" + digest + comment + "\n"
		}
		out = append(out, format.Snippet{ID: "oras.pull", Tool: "oras", Purpose: format.SnippetUse, Context: format.SnippetArtifact,
			Title: "Pull " + art.Name, Lang: "sh", Code: code})
	default:
		code, note := "docker pull "+ref+":"+tag+"\n", "The tag can move to another image."
		if pinned {
			code, note = "docker pull "+ref+"@sha256:"+digest+comment+"\n", "The digest names this image for good."
		}
		out = append(out, format.Snippet{ID: "docker.pull", Tool: "docker", Purpose: format.SnippetUse, Context: format.SnippetArtifact,
			Title: "Pull " + art.Name, Lang: "sh", Code: code, Note: note})
	}
	return &format.ArtifactUsage{Artifact: *art, Version: tag, Mode: mode, Snippets: out}, nil
}

// helmPull is the helm pull of a chart: by its tag (the chart version), else by digest.
func helmPull(ref, tag, digest string) string {
	if tag == "" {
		return "helm pull oci://" + ref + "@sha256:" + digest + "\n"
	}
	return "helm pull oci://" + ref + " --version " + tag + "\n"
}
