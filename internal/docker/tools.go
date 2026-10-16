package docker

import (
	"github.com/Telesphorepo/Telesphorepo/internal/ci"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// dockerTool is the CI tool docker: the job builds the image of the repository's Dockerfile, logs
// in to the push host with its identity token right before the push (so the token is fresh when
// the server mints the publish session), pushes and logs out.
func (f *Format) dockerTool() format.Tool {
	return format.Tool{ID: "docker", Label: "Docker",
		CINote: "Change app to the name of your image. The job needs Docker. The OIDC token pulls public images only: to build from a private image, log in with a read token before the build.",
		Publish: func(repo *repos.Repo, baseURL, system string) ci.PublishSteps {
			_, push := f.hostsOf(baseURL)
			image := push + "/" + repo.Name + "/app"
			tag := "$GITHUB_SHA"
			if system == ci.Jenkins {
				tag = "${GIT_COMMIT:-$BUILD_NUMBER}"
			}
			return ci.PublishSteps{
				Build: `docker build -t "` + image + ":" + tag + `" .`,
				Token: true,
				Publish: "printf '%s' \"$OIDC_TOKEN\" | docker login " + push + " -u oidc --password-stdin\n" +
					`docker push "` + image + ":" + tag + "\"\n" +
					"docker logout " + push + "\n",
			}
		}}
}
