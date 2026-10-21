package nuget

import (
	"context"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/ci"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Snippets of the NuGet format (format.Spec Usage and ArtifactUsage). dotnet reads a private
// source's credentials from packageSourceCredentials of nuget.config, under the key of the
// source; it sends them after the server's first 401. A source on plain http needs
// allowInsecureConnections.

// sourceKey is the key of the repo as a NuGet source: the repo name, as an XML element name in
// packageSourceCredentials (NuGet encodes a leading digit as _x003N_).
func sourceKey(repo string) string {
	if repo != "" && repo[0] >= '0' && repo[0] <= '9' {
		return "_x003" + repo[:1] + "_" + repo[1:]
	}
	return repo
}

// nugetConfig is the nuget.config that adds the repo as a source, with the read credentials of
// a private repo.
func nugetConfig(repo *repos.Repo, url string) string {
	insecure := ""
	if strings.HasPrefix(url, "http://") {
		insecure = ` allowInsecureConnections="true"`
	}
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<configuration>\n  <packageSources>\n")
	b.WriteString("    <add key=\"" + repo.Name + "\" value=\"" + url + "\"" + insecure + " />\n  </packageSources>\n")
	if !repo.AnonymousRead {
		k := sourceKey(repo.Name)
		b.WriteString("  <packageSourceCredentials>\n    <" + k + ">\n      <add key=\"Username\" value=\"YOUR_USERNAME\" />\n" +
			"      <add key=\"ClearTextPassword\" value=\"YOUR_READ_TOKEN\" />\n    </" + k + ">\n  </packageSourceCredentials>\n")
	}
	b.WriteString("</configuration>\n")
	return b.String()
}

// sourceSnippets are the snippets that add the repo as a source: nuget.config next to the
// solution, or the dotnet command that writes it to the user's config.
func sourceSnippets(repo *repos.Repo, baseURL, snippetContext string) []format.Snippet {
	url := baseURL + "/nuget/" + repo.Name + "/index.json"
	note := "Put the file next to the solution. dotnet reads it for every project below it."
	if !repo.AnonymousRead {
		note += " Create a read token on Settings › Tokens. In CI, write %NUGET_USERNAME% and %NUGET_TOKEN% instead and set those variables."
	}
	out := []format.Snippet{{ID: "nuget-config.source", Tool: "nuget-config", Purpose: format.SnippetUse, Context: snippetContext,
		File: "nuget.config", Title: "nuget.config", Lang: "xml", Code: nugetConfig(repo, url), Note: note}}
	cmd := "dotnet nuget add source " + url + " --name " + repo.Name + "\n"
	cmdNote := "dotnet adds the source to the nuget.config of your user."
	if !repo.AnonymousRead {
		cmd = format.ReadTokenPrompt + "\ndotnet nuget add source " + url + " --name " + repo.Name +
			" \\\n  --username YOUR_USERNAME --password \"$READ_TOKEN\" --store-password-in-clear-text\n"
		cmdNote += " Create a read token on Settings › Tokens. Enter it at the prompt."
	}
	if strings.HasPrefix(url, "http://") {
		cmdNote += " A source on plain http also needs allowInsecureConnections=\"true\" in nuget.config."
	}
	return append(out, format.Snippet{ID: "dotnet.source", Tool: "dotnet", Purpose: format.SnippetUse, Context: snippetContext,
		Title: "Add the source", Lang: "sh", Code: cmd, Note: cmdNote})
}

// Usage returns the source setup of the repo (nuget.config and the dotnet command) and the
// publish command of the Telesphorepo CLI. The core adds the CI files of the tool dotnet.
func (h *Handler) Usage(repo *repos.Repo, baseURL string) []format.Snippet {
	out := sourceSnippets(repo, baseURL, format.SnippetRepo)
	if repo.Kind != repos.KindHosted {
		return out
	}
	return append(out, format.CLISnippet("telesphorepo", format.CLIPublish,
		"In a .NET project the CLI runs dotnet pack -c Release and pushes each package. telesphorepo publish FILE.nupkg pushes packages that you built."))
}

// ciNote goes under the CI files of the dotnet tool.
const ciNote = "The CLI uses the job's identity token, so the job stores no secret. Commit telesphorepo.toml first: Set up a project on this tab writes it."

// tools are the build tools that publish to NuGet repos: the Telesphorepo CLI packs with dotnet
// and pushes. The CI job needs the .NET SDK.
func tools() []format.Tool {
	return []format.Tool{{ID: "dotnet", Label: ".NET", CINote: ciNote,
		Publish: func(_ *repos.Repo, _, system string) ci.PublishSteps {
			if system == ci.Jenkins {
				return ci.PublishSteps{Build: "dotnet build -c Release", Publish: format.CLIPublish}
			}
			return ci.PublishSteps{Toolchain: []ci.WorkflowStep{{Uses: "actions/setup-dotnet@v5", With: [][2]string{{"dotnet-version", "10.0.x"}}}},
				Publish: format.CLIPublish}
		}}}
}

// ArtifactUsage returns the snippets of one package of a hosted repo. p is the package folder
// v3-flatcontainer/<id>/, a version folder in it or a file of a version. The pinned mode names
// the version of the path, else the newest listed release (the newest listed version when there
// is none); the latest mode lets NuGet take the newest version (Version="*").
func (h *Handler) ArtifactUsage(ctx context.Context, repo, from *repos.Repo, baseURL, p, v, mode string) (*format.ArtifactUsage, error) {
	if from.Kind != repos.KindHosted {
		return nil, nil
	}
	segs := strings.Split(strings.Trim(p, "/"), "/")
	if len(segs) < 2 || segs[0]+"/" != flatDir {
		return nil, nil
	}
	lid := segs[1]
	if len(segs) >= 3 && v == "" {
		v = segs[2]
	}
	m, err := h.hostedModel(ctx, from.ID, lid)
	if err != nil || m == nil {
		return nil, err
	}
	var pick *leaf
	for i := len(m.leaves) - 1; i >= 0; i-- {
		l := &m.leaves[i]
		switch {
		case v != "":
			if strings.EqualFold(l.ver.Normalized(), v) {
				pick = l
			}
		case pick == nil && l.listed && !l.ver.Prerelease():
			pick = l
		}
	}
	if pick == nil && v != "" {
		return nil, nil
	}
	if pick == nil {
		pick = &m.leaves[len(m.leaves)-1]
	}
	if mode != format.VersionLatest {
		mode = format.VersionPinned
	}
	ver := pick.ver.Normalized()
	add, ref, note := "dotnet add package "+m.id+" --version "+ver+"\n", "<ItemGroup>\n  <PackageReference Include=\""+m.id+"\" Version=\""+ver+"\" />\n</ItemGroup>\n", ""
	if mode == format.VersionLatest {
		add = "dotnet add package " + m.id + "\n"
		ref = "<ItemGroup>\n  <PackageReference Include=\"" + m.id + "\" Version=\"*\" />\n</ItemGroup>\n"
		note = "Each restore takes the newest version at that moment, so builds are not reproducible. Pin a version for releases."
	}
	out := sourceSnippets(repo, baseURL, format.SnippetArtifact)
	out = append(out,
		format.Snippet{ID: "dotnet.add", Tool: "dotnet", Purpose: format.SnippetUse, Context: format.SnippetArtifact,
			Title: "Add " + m.id, Lang: "sh", Code: add, Note: note},
		format.Snippet{ID: "msbuild.reference", Tool: "msbuild", Purpose: format.SnippetUse, Context: format.SnippetArtifact,
			File: "*.csproj", Title: "Project file", Lang: "xml", Code: ref, Note: note})
	return &format.ArtifactUsage{Artifact: artifactOf(lid, m.id), Version: ver, Mode: mode, Snippets: out}, nil
}
