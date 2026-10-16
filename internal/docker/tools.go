package docker

import (
	"github.com/Telesphorepo/Telesphorepo/internal/ci"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// dockerTool is the CI tool docker: the job builds the image of the repository's Dockerfile and
// pushes it with docker push. A Docker config of its own names the credential helper of the
// Telesphorepo CLI for the registry host; the helper mints the publish session from the job's
// identity token, so the job runs no docker login.
func (f *Format) dockerTool() format.Tool {
	return format.Tool{ID: "docker", Label: "Docker",
		CINote: "Change app to the name of your image. The job needs Docker. The OIDC token pulls public images only: to build from a private image, log in with a read token before the build.",
		Publish: func(repo *repos.Repo, baseURL, system string) ci.PublishSteps {
			host := registryHost(baseURL)
			image := host + "/" + repo.Name + "/app"
			tag, psTag := "$GITHUB_SHA", "$env:GITHUB_SHA"
			if system == ci.Jenkins {
				tag, psTag = "${GIT_COMMIT:-$BUILD_NUMBER}", "$(if ($env:GIT_COMMIT) { $env:GIT_COMMIT } else { $env:BUILD_NUMBER })"
			}
			helpers := `{"credHelpers":{"` + host + `":"telesphorepo"}}`
			return ci.PublishSteps{
				Build: `docker build -t "` + image + ":" + tag + `" .`,
				Publish: `export DOCKER_CONFIG="$(mktemp -d)"` + "\n" +
					"printf '%s\\n' '" + helpers + `' > "$DOCKER_CONFIG/config.json"` + "\n" +
					`docker push "` + image + ":" + tag + "\"\n",
				PublishPS: "$env:DOCKER_CONFIG = Join-Path ([IO.Path]::GetTempPath()) ([guid]::NewGuid())\n" +
					"New-Item -ItemType Directory $env:DOCKER_CONFIG | Out-Null\n" +
					"Set-Content (Join-Path $env:DOCKER_CONFIG 'config.json') '" + helpers + "'\n" +
					`docker push "` + image + ":" + psTag + "\"\n",
			}
		}}
}
