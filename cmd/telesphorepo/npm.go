package main

import (
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
)

// npmTool returns the package manager of the npm project in dir, chosen by its lockfile: pnpm,
// yarn (a yarn.lock with .yarnrc.yml: Yarn 2 and later), bun, else npm.
func npmTool(dir string) string {
	switch {
	case fileExists(filepath.Join(dir, "pnpm-lock.yaml")):
		return "pnpm"
	case fileExists(filepath.Join(dir, "yarn.lock")) && fileExists(filepath.Join(dir, ".yarnrc.yml")):
		return "yarn"
	case fileExists(filepath.Join(dir, "bun.lock")), fileExists(filepath.Join(dir, "bun.lockb")):
		return "bun"
	}
	return "npm"
}

// npmPreview is the command that publish runs in dir, for "status".
func npmPreview(dir string) string {
	if t := npmTool(dir); t != "yarn" {
		return t + " publish"
	}
	return "yarn npm publish"
}

// npmRC is the user config of npm and pnpm for a publish: the registry and its token, which
// they read from the env.
const npmRC = "registry=%[1]s\n//%[2]s:_authToken=${TELESPHOREPO_TOKEN}\n"

// runNPM runs the publish of the project's package manager. The package manager packs, so the
// files list, prepack and workspace: ranges stay its job. npm and pnpm read a temp user config
// with the registry and a ${TELESPHOREPO_TOKEN} reference; bun reads NPM_CONFIG_TOKEN and yarn its
// YARN_NPM_* variables from the child env.
func runNPM(j *job) error {
	tool := npmTool(j.p.dir)
	bin, err := exec.LookPath(tool)
	if err != nil {
		return fmt.Errorf("this project publishes with %s (chosen by its lockfile), and %s is not on the PATH. Install %s", tool, tool, tool)
	}
	registry := j.c.id.base() + "/npm/" + j.p.Repo + "/"
	switch tool {
	case "yarn":
		env := []string{"YARN_NPM_PUBLISH_REGISTRY=" + registry, "YARN_NPM_AUTH_TOKEN=" + j.s.Token}
		if u, err := url.Parse(registry); err == nil && u.Scheme == "http" {
			// A server on plain HTTP: Yarn refuses it unless the host is on this list.
			env = append(env, "YARN_UNSAFE_HTTP_WHITELIST="+u.Hostname())
		}
		return j.runTool([]string{bin, "npm", "publish"}, j.env(env...))
	case "bun":
		return j.runTool([]string{bin, "publish", "--registry", registry}, j.env("NPM_CONFIG_TOKEN="+j.s.Token))
	}
	rc, err := j.writeTemp("npmrc", fmt.Sprintf(npmRC, registry, strings.TrimPrefix(strings.TrimPrefix(registry, "https://"), "http://")))
	if err != nil {
		return err
	}
	argv := []string{bin, "publish", "--registry", registry}
	if tool == "pnpm" {
		// The CLI publishes what the person asks for: pnpm's checks of the git branch and of a clean
		// work tree stay out.
		argv = append(argv, "--no-git-checks")
	}
	return j.runTool(argv, j.env("npm_config_userconfig="+rc))
}
