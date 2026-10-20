//go:build clients

package e2e

import (
	"net/http"
	"testing"
)

// TestCLINPM: setup and publish of an npm project with each package manager, picked by the
// lockfile: npm (no lockfile), pnpm (pnpm-lock.yaml), Yarn 4 (yarn.lock with .yarnrc.yml) and Bun
// (bun.lock). The package manager packs and publishes with the session token from the env; npm
// then installs every package from the repo.
func TestCLINPM(t *testing.T) {
	t.Parallel()
	c := newCLI(t)
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo := RandName("npm")
		s.CreateRepo(t, repo, "npm", false, nil)
		dev := RandName("dev")
		s.CreateUser(t, dev, "dev-pass-1234")
		s.SetPerms(t, repo, dev, "repo.download", "repo.publish")
		devices := NewDataVolume(t)
		const publish = `set -e
cd /work/app
telesphorepo setup "$E2E_LINK"
telesphorepo publish
`
		names := map[string]string{}
		for _, k := range []struct {
			tool, image, prelude, command string
			extra                         []ContainerFile
		}{
			{"npm", nodeImage, "", "npm publish --registry", nil},
			{"pnpm", nodeImage, npmSetupTools, "pnpm publish --registry",
				[]ContainerFile{{Path: "/work/app/pnpm-lock.yaml", Content: "lockfileVersion: '9.0'\n"}}},
			{"yarn", nodeImage, npmSetupTools + "(cd /work/app && yarn install)\n", "yarn npm publish",
				[]ContainerFile{{Path: "/work/app/.yarnrc.yml", Content: "nodeLinker: node-modules\nenableTelemetry: false\n"}}},
			{"bun", bunImage, "", "bun publish --registry",
				[]ContainerFile{{Path: "/work/app/bun.lock", Content: `{"lockfileVersion": 1, "workspaces": {"": {"name": "app"}}, "packages": {}}` + "\n"}}},
		} {
			t.Run(k.tool, func(t *testing.T) {
				name := "@cli/" + RandName(k.tool)
				names[name] = k.tool
				files := append(npmPackage("/work/app", name, "1.0.0", "published with "+k.tool), k.extra...)
				out := c.mustRun(t, k.image, "publish with "+k.tool, k.prelude+publish, ClientOpts{
					Files:  files,
					Env:    map[string]string{"E2E_LINK": s.NewSetupCode(t, dev, repo).Link},
					Mounts: map[string]string{devices: "/root/.telesphorepo"},
				})
				wantText(t, "publish with "+k.tool, out, "Publishing to "+repo+" (npm)", k.command)
				requireAssets(t, s, repo, name+"/-/", name+"/-/"+name[len("@cli/"):]+"-1.0.0.tgz")
			})
		}

		deps := map[string]string{}
		var check string
		for name := range names {
			deps[name] = "1.0.0"
			check += npmRequire(name)
		}
		read := s.CreateToken(t, dev, "dev-pass-1234", "npm-cli-e2e")
		code, out := RunClient(t, nodeImage, ClientOpts{Cmd: []string{"set -e\ncd /work/consumer && npm install --no-audit --no-fund\n" + check},
			Files: []ContainerFile{npmConsumer("/work/consumer", deps),
				{Path: "/work/consumer/.npmrc", Content: npmrc(s.InternalURL+"/npm/"+repo+"/", read)}}})
		if code != 0 {
			t.Fatalf("install the published packages: exit %d\n%s", code, out)
		}
		for name, tool := range names {
			wantText(t, "install the published packages", out, "REQ "+name+": published with "+tool)
		}
		if st, _ := s.API(t, http.MethodGet, "/npm/"+repo+"/-/ping", nil, nil); st != http.StatusOK {
			t.Fatalf("npm ping: status %d", st)
		}
	})
}
