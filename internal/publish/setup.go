package publish

import (
	_ "embed"
	"net/http"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/ci"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// The project setup scripts (GET /setup.sh for sh, GET /setup.ps1 for PowerShell). Each sets up
// the project in the current folder to publish to one repository of this server, and adds a
// GitHub Actions workflow or a Jenkinsfile on request. This module holds the frame: the
// arguments, the questions, the staged changes with their confirmation, and the CI files. Each
// build tool with a format.SetupFragment adds its detection and its changes of the project files.
// The server fills in its public address, the fragments and the CI files of each tool (built by
// internal/ci for the repository placeholder %REPO%, which the script fills in).

//go:embed setup.sh
var setupSh string

//go:embed setup.ps1
var setupPs1 string

// setupTrigger is the trigger of the setup workflow: a push of a tag v*.
var setupTrigger = ci.Trigger{Tags: []string{"v*"}}

func (m *mod) registerSetup(r *route.Router) {
	r.Handle("GET /setup.sh", route.Public, m.setupScript(false))
	r.Handle("GET /setup.ps1", route.Public, m.setupScript(true))
}

// setupTool is a build tool with a part in the setup scripts, and the format it publishes to.
type setupTool struct {
	format string
	tool   format.Tool
}

// setupTools returns the tools of every format that have a SetupFragment, once each, in module
// order.
func (m *mod) setupTools() []setupTool {
	var out []setupTool
	seen := map[string]bool{}
	for _, f := range m.h.Formats.All() {
		for _, t := range f.Tools {
			if t.Setup != nil && !seen[t.ID] {
				seen[t.ID] = true
				out = append(out, setupTool{f.Name, t})
			}
		}
	}
	return out
}

// setupScript serves the sh script, or the PowerShell script with ps1, with the placeholders
// filled in. The PowerShell script has Windows line ends.
func (m *mod) setupScript(ps1 bool) route.Handler {
	return func(w http.ResponseWriter, r *http.Request, _ *route.Call) {
		base, ok := m.setupBaseURL(r)
		if !ok {
			httpx.Error(w, http.StatusBadRequest, "the server address cannot go in a script; set the public base URL (base_url)")
			return
		}
		tools := m.setupTools()
		if len(tools) == 0 {
			httpx.Error(w, http.StatusNotFound, "no build tool of this server has a project setup")
			return
		}
		var ids, projects, fragments []string
		var ciFiles strings.Builder
		for _, st := range tools {
			t := st.tool
			ids = append(ids, t.ID)
			projects = append(projects, t.Setup.Project)
			repo := &repos.Repo{Name: "%REPO%", Format: st.format, Kind: repos.KindHosted}
			gh := strings.TrimSuffix(ci.GitHubWorkflow(repo.Name, base, t.Publish(repo, base, ci.GitHubActions), setupTrigger), "\n")
			jk := t.Publish(repo, base, ci.Jenkins)
			jf := strings.TrimSuffix(ci.Jenkinsfile(repo.Name, jk, ci.Trigger{}), "\n")
			freestyle := jk.Publish
			if jk.Build != "" {
				freestyle = jk.Build + " && " + jk.Publish
			}
			if ps1 {
				fragments = append(fragments, t.Setup.PS1)
				ciFiles.WriteString("$CiFiles['" + t.ID + "'] = @{}\n")
				for _, kv := range [][2]string{{"github", gh}, {"jenkins", jf}, {"freestyle", freestyle}} {
					ciFiles.WriteString("$CiFiles['" + t.ID + "']['" + kv[0] + "'] = @'\n" + kv[1] + "\n'@\n")
				}
			} else {
				fragments = append(fragments, t.Setup.Sh)
				for _, kv := range [][2]string{{"github", gh}, {"jenkins", jf}, {"freestyle", freestyle}} {
					ciFiles.WriteString("ci_" + kv[0] + "_" + t.ID + "() {\n\tcat <<'TELESPHOREPO_EOF'\n" + kv[1] + "\nTELESPHOREPO_EOF\n}\n\n")
				}
			}
		}
		script, toolList := setupSh, strings.Join(ids, " ")
		if ps1 {
			script = setupPs1
			quoted := make([]string, len(ids))
			for i, id := range ids {
				quoted[i] = "'" + id + "'"
			}
			toolList = strings.Join(quoted, ", ")
		}
		body := strings.NewReplacer(
			"@@URL@@", base,
			"@@CREDENTIAL@@", ci.JenkinsCredential,
			"@@JENKINS_NOTE@@", ci.JenkinsNote(base),
			"@@TOOLS@@", toolList,
			"@@PROJECTS@@", orList(projects),
			"@@FRAGMENTS@@", strings.TrimSuffix(strings.ReplaceAll(strings.Join(fragments, "\n"), "\r\n", "\n"), "\n"),
			"@@CI_FILES@@", strings.TrimSuffix(ciFiles.String(), "\n"),
		).Replace(strings.ReplaceAll(script, "\r\n", "\n"))
		if ps1 {
			body = strings.ReplaceAll(body, "\n", "\r\n")
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte(body))
	}
}

// orList joins names as "a, b or c".
func orList(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}

// setupBaseURL is the base_url setting, else the URL the request came in on. The scripts quote
// it in single quotes, so it must be a plain URL: no quote, space, backslash or control character.
func (m *mod) setupBaseURL(r *http.Request) (string, bool) {
	base := strings.TrimRight(settings.BaseURL.Get(m.h.Settings.Current()), "/")
	if base == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	for _, c := range base {
		if c <= ' ' || c == 0x7f || strings.ContainsRune("'\"`$\\", c) {
			return "", false
		}
	}
	return base, true
}
