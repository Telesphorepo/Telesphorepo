//go:build clients

package e2e

import "testing"

// TestCLIPyPI: setup and publish build the project with python -m build and upload every wheel
// and sdist; publish FILE uploads a wheel without a build.
func TestCLIPyPI(t *testing.T) {
	t.Parallel()
	c := newCLI(t)
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo := RandName("py")
		s.CreateRepo(t, repo, "pypi", true, nil)
		dev := RandName("dev")
		s.CreateUser(t, dev, "dev-pass-1234")
		s.SetPerms(t, repo, dev, "repo.download", "repo.publish")
		devices := NewDataVolume(t)
		mounts := map[string]string{devices: "/root/.telesphorepo"}
		pipName, pipMod := pyNames()
		fileName, fileMod := pyNames()
		extra, whl := pyWheel(fileName, fileMod, "3.0", "file "+fileName)

		out := c.mustRun(t, pythonImage, "publish with python -m build", `set -e
pip install -q --root-user-action=ignore build
cd /work/p
telesphorepo setup "$E2E_LINK"
telesphorepo publish
telesphorepo publish /work/extra/`+extra+`
`, ClientOpts{Files: append(pySetuptoolsProject("/work/p", pipName, pipMod, "1.0.0", "pip "+pipName),
			ContainerFile{Path: "/work/extra/" + extra, Content: string(whl)}),
			Env: map[string]string{"E2E_LINK": s.NewSetupCode(t, dev, repo).Link}, Mounts: mounts})
		wantText(t, "publish with python -m build", out, " -m build", "Uploaded "+pipMod+"-1.0.0.tar.gz", "Uploaded "+extra)
		requireAssets(t, s, repo, "packages/", "packages/"+pipName+"/"+pipMod+"-1.0.0-py3-none-any.whl", "packages/"+fileName+"/"+extra)
	})
}
