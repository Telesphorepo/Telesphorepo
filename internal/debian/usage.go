package debian

import (
	"fmt"

	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Usage returns the apt setup (key, source, credentials for a private repo) and a curl upload.
// The dist "stable" and the component "main" are examples: any valid names work.
func (h *Handler) Usage(repo *repos.Repo, baseURL string) []format.Snippet {
	url := baseURL + "/debian/" + repo.Name
	out := []format.Snippet{
		{Title: "Signing key", Lang: "sh", Code: fmt.Sprintf(
			"sudo install -d -m 0755 /etc/apt/keyrings\n"+
				"sudo curl -fsSL %s/keys/debian.asc -o /etc/apt/keyrings/telesphorepo.asc\n", baseURL)},
		{Title: "/etc/apt/sources.list.d/" + repo.Name + ".list", Lang: "sh", Code: fmt.Sprintf(
			"echo \"deb [signed-by=/etc/apt/keyrings/telesphorepo.asc] %s stable main\" \\\n"+
				"  | sudo tee /etc/apt/sources.list.d/%s.list\n", url, repo.Name)},
	}
	if !repo.AnonymousRead {
		// apt applies an auth.conf entry to plain http only when the scheme is spelled out.
		out = append(out, format.Snippet{Title: "/etc/apt/auth.conf.d/" + repo.Name + ".conf", Lang: "sh", Code: fmt.Sprintf(
			"echo \"machine %s login <user> password <token>\" \\\n"+
				"  | sudo tee /etc/apt/auth.conf.d/%s.conf\n"+
				"sudo chmod 600 /etc/apt/auth.conf.d/%s.conf\n", url, repo.Name, repo.Name)})
	}
	out = append(out, format.Snippet{Title: "Upload a package", Lang: "sh", Code: fmt.Sprintf(
		"curl -fsS -u <user>:<token> -T hello_1.0_amd64.deb %s/upload/stable/main\n", url)})
	return out
}
