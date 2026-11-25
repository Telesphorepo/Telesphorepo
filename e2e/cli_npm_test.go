//go:build clients

package e2e

import "testing"

// TestCLINPM: setup and publish of an npm project with each package manager, picked by the
// lockfile: npm (no lockfile) and Bun (bun.lock). The package manager packs and publishes with
// the session token from the env.
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
		for _, k := range []struct {
			tool, image, command string
			extra                []ContainerFile
		}{
			{"npm", nodeImage, "npm publish --registry", nil},
			{"bun", bunImage, "bun publish --registry",
				[]ContainerFile{{Path: "/work/app/bun.lock", Content: `{"lockfileVersion": 1, "workspaces": {"": {"name": "app"}}, "packages": {}}` + "\n"}}},
		} {
			t.Run(k.tool, func(t *testing.T) {
				name := "@cli/" + RandName(k.tool)
				files := append(npmPackage("/work/app", name, "1.0.0", "published with "+k.tool), k.extra...)
				out := c.mustRun(t, k.image, "publish with "+k.tool, publish, ClientOpts{
					Files:  files,
					Env:    map[string]string{"E2E_LINK": s.NewSetupCode(t, dev, repo).Link},
					Mounts: map[string]string{devices: "/root/.telesphorepo"},
				})
				wantText(t, "publish with "+k.tool, out, k.command)
				requireAssets(t, s, repo, name+"/-/", name+"/-/"+name[len("@cli/"):]+"-1.0.0.tgz")
			})
		}
	})
}
