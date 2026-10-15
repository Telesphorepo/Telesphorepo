package debian

import (
	"fmt"

	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Usage returns the apt setup (key, source, read credentials for a private repo) and the publish
// steps: curl from CI with the job's identity token, and curl with a publish token by hand.
// The dist "stable" and the component "main" are examples: any valid names work.
func (h *Handler) Usage(repo *repos.Repo, baseURL string) []format.Snippet {
	url := baseURL + "/debian/" + repo.Name
	creds := ""
	if !repo.AnonymousRead {
		creds = "-u YOUR_USERNAME:YOUR_READ_TOKEN "
	}
	use := func(title, code, note string) format.Snippet {
		return format.Snippet{Client: "apt", Purpose: format.SnippetUse, Context: format.SnippetRepo,
			Title: title, Lang: "sh", Code: code, Note: note}
	}
	out := []format.Snippet{
		use("Signing key", fmt.Sprintf(
			"sudo install -d -m 0755 /etc/apt/keyrings\n"+
				"sudo curl -fsSL %s%s/key.asc -o /etc/apt/keyrings/%s.asc\n", creds, url, repo.Name), ""),
		use("/etc/apt/sources.list.d/"+repo.Name+".list", fmt.Sprintf(
			"echo \"deb [signed-by=/etc/apt/keyrings/%s.asc] %s stable main\" \\\n"+
				"  | sudo tee /etc/apt/sources.list.d/%s.list\n"+
				"sudo apt update\n", repo.Name, url, repo.Name),
			"stable and main are examples: use the distribution and component the packages were uploaded to."),
	}
	if !repo.AnonymousRead {
		// apt applies an auth.conf entry to plain http only when the scheme is spelled out.
		out = append(out, use("/etc/apt/auth.conf.d/"+repo.Name+".conf", fmt.Sprintf(
			"echo \"machine %s login YOUR_USERNAME password YOUR_READ_TOKEN\" \\\n"+
				"  | sudo tee /etc/apt/auth.conf.d/%s.conf\n"+
				"sudo chmod 600 /etc/apt/auth.conf.d/%s.conf\n", url, repo.Name, repo.Name),
			"Create a read token on Settings › Tokens."))
	}
	upload := "curl -fsS -u publish:$TOKEN -T hello_1.0_amd64.deb " + url + "/upload/stable/main"
	out = append(out, format.CurlCISnippets(baseURL, repo.Name, upload)...)
	return append(out, format.HandSnippet(upload))
}
