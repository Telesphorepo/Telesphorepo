package maven

import (
	_ "embed"
	"net/http"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
)

// The project setup scripts (GET /setup.sh for sh, GET /setup.ps1 for PowerShell). Each sets up
// the Gradle or Maven project in the current folder to publish to one repository of this server,
// and adds a GitHub Actions workflow or a Jenkinsfile on request. The server fills in its public
// address, the clients version, and the CI files of the publish snippets. The CI files keep the
// placeholders %REPO%, %BUILD% and %PUBLISH%, which the script fills in for the project.

//go:embed setup.sh
var setupSh string

//go:embed setup.ps1
var setupPs1 string

// tagTrigger is the trigger of the setup workflow: a push of a tag v*.
const tagTrigger = "on:\n  push:\n    tags: [\"v*\"]\n\n"

func (h *Handler) registerSetup(mux *http.ServeMux) {
	mux.HandleFunc("GET /setup.sh", h.setupScript(setupSh, "\n"))
	mux.HandleFunc("GET /setup.ps1", h.setupScript(setupPs1, "\r\n"))
}

// setupScript serves script with the placeholders filled in, with the line end eol.
func (h *Handler) setupScript(script, eol string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		base, ok := h.setupBaseURL(r)
		if !ok {
			httpx.Error(w, http.StatusBadRequest, "the server address cannot go in a script; set the public base URL (base_url)")
			return
		}
		body := strings.NewReplacer(
			"@@URL@@", base,
			"@@VERSION@@", clientsVersion,
			"@@CREDENTIAL@@", format.JenkinsCredential,
			"@@JENKINS_NOTE@@", format.JenkinsNote(base),
			"@@GITHUB_WORKFLOW@@", strings.TrimSuffix(workflow(tagTrigger, "%PUBLISH%"), "\n"),
			"@@JENKINSFILE@@", strings.TrimSuffix(format.JenkinsPipeline("%REPO%", "%BUILD%", "%PUBLISH%"), "\n"),
		).Replace(strings.ReplaceAll(script, "\r\n", "\n"))
		body = strings.ReplaceAll(body, "\n", eol)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte(body))
	}
}

// setupBaseURL is the base_url setting, else the URL the request came in on. The scripts quote
// it in single quotes, so it must be a plain URL: no quote, space, backslash or control character.
func (h *Handler) setupBaseURL(r *http.Request) (string, bool) {
	base := strings.TrimRight(h.cfg().BaseURL, "/")
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
