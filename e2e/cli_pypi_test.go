//go:build clients

package e2e

import (
	"strings"
	"testing"
	"time"
)

// TestCLIPyPI: setup and publish build the project with uv (uv.lock), Poetry ([tool.poetry]) and
// python -m build, and upload every wheel and sdist; publish FILE uploads a wheel without a build;
// pip installs each published package.
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
		uvName, uvMod := pyNames()
		poName, poMod := pyNames()
		pipName, pipMod := pyNames()
		fileName, fileMod := pyNames()
		extra, whl := pyWheel(fileName, fileMod, "3.0", "file "+fileName)

		out := c.mustRun(t, uvImage, "publish with uv", `set -e
cd /work/p
uv lock
telesphorepo setup "$E2E_LINK"
telesphorepo publish
`, ClientOpts{Files: pySetuptoolsProject("/work/p", uvName, uvMod, "1.0.0", "uv "+uvName),
			Env: map[string]string{"E2E_LINK": s.NewSetupCode(t, dev, repo).Link}, Mounts: mounts})
		wantText(t, "publish with uv", out, "uv build", "Uploaded "+uvMod+"-1.0.0-py3-none-any.whl")

		out = c.mustRun(t, pythonImage, "publish with Poetry", `set -e
pip install -q --root-user-action=ignore poetry
cd /work/p
telesphorepo setup "$E2E_LINK"
telesphorepo publish
`, ClientOpts{Files: pyPoetryProject("/work/p", poName, poMod, "1.0.0", "poetry "+poName),
			Env: map[string]string{"E2E_LINK": s.NewSetupCode(t, dev, repo).Link}, Mounts: mounts, Timeout: 20 * time.Minute})
		wantText(t, "publish with Poetry", out, "poetry build", "Uploaded "+poMod+"-1.0.0-py3-none-any.whl")

		out = c.mustRun(t, pythonImage, "publish with python -m build", `set -e
pip install -q --root-user-action=ignore build
cd /work/p
telesphorepo setup "$E2E_LINK"
telesphorepo publish
telesphorepo publish /work/extra/`+extra+`
`, ClientOpts{Files: append(pySetuptoolsProject("/work/p", pipName, pipMod, "1.0.0", "pip "+pipName),
			ContainerFile{Path: "/work/extra/" + extra, Content: string(whl)}),
			Env: map[string]string{"E2E_LINK": s.NewSetupCode(t, dev, repo).Link}, Mounts: mounts})
		wantText(t, "publish with python -m build", out, " -m build", "Uploaded "+pipMod+"-1.0.0.tar.gz", "Uploaded "+extra)
		requireAssets(t, s, repo, "packages/", "packages/"+pipName+"/"+pipMod+"-1.0.0-py3-none-any.whl", "packages/"+fileName+"/"+extra,
			"packages/"+uvName+"/"+uvMod+"-1.0.0.tar.gz", "packages/"+poName+"/"+poMod+"-1.0.0.tar.gz")

		code, out := RunClient(t, pythonImage, ClientOpts{Cmd: []string{`set -e
pip install --root-user-action=ignore --no-cache-dir --index-url "$INDEX" ` + strings.Join([]string{uvName, poName, pipName, fileName}, " ") + `
python -c "import ` + uvMod + `, ` + poMod + `, ` + pipMod + `, ` + fileMod + ` as f; print(` + uvMod + `.MSG, ` + poMod + `.MSG, ` + pipMod + `.MSG, f.MSG)"
`}, Env: map[string]string{"INDEX": pyIndex(s, repo, "", ""), "PIP_TRUSTED_HOST": s.Alias}})
		if code != 0 {
			t.Fatalf("pip install the published packages: exit %d\n%s", code, out)
		}
		wantText(t, "pip install the published packages", out, "uv "+uvName, "poetry "+poName, "pip "+pipName, "file "+fileName)
	})
}
