//go:build e2e || import || clients

package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The npm clients that several zones share: Node (npm, and pnpm and Yarn through corepack) and
// Bun.
var (
	nodeImage = floatingImage("docker.io/library/node:lts")
	bunImage  = floatingImage("docker.io/oven/bun:latest")
)

// npmInstallTools installs pnpm and Yarn 4 in a node container through corepack (outside any
// project, from the public registry). pnpm from corepack is a wrapper that downloads its native
// binary (@pnpm/exe.<os>-<arch>) on first use from the registry of the project's .npmrc, and
// sends no credentials there on plain HTTP.
const npmInstallTools = `(cd / && npm install -g --silent corepack@latest >/dev/null && corepack enable && corepack prepare pnpm@latest --activate >/dev/null && corepack prepare yarn@stable --activate >/dev/null)
export COREPACK_ENABLE_DOWNLOAD_PROMPT=0 COREPACK_ENABLE_STRICT=0
`

// npmSetupTools is npmInstallTools with the pnpm binary downloaded from the public registry too,
// for a test whose first pnpm run is in a project of a private repo.
const npmSetupTools = npmInstallTools + "(cd / && pnpm --version >/dev/null)\n"

// npmPackage returns the files of a package in dir whose index.js exports message.
func npmPackage(dir, name, version, message string) []ContainerFile {
	pj, _ := json.MarshalIndent(map[string]any{"name": name, "version": version, "main": "index.js", "license": "MIT",
		"description": "telesphorepo e2e package"}, "", "  ")
	return []ContainerFile{
		{Path: dir + "/package.json", Content: string(pj) + "\n"},
		{Path: dir + "/index.js", Content: fmt.Sprintf("module.exports = %q;\n", message)},
	}
}

// npmConsumer returns a project in dir that depends on deps (name -> range).
func npmConsumer(dir string, deps map[string]string) ContainerFile {
	pj, _ := json.MarshalIndent(map[string]any{"name": "consumer", "version": "1.0.0", "private": true, "dependencies": deps}, "", "  ")
	return ContainerFile{Path: dir + "/package.json", Content: string(pj) + "\n"}
}

// npmrc is an .npmrc that installs from registry (with a trailing slash) with token ("" for none).
func npmrc(registry, token string) string {
	out := "registry=" + registry + "\n"
	if token != "" {
		out += "//" + strings.TrimPrefix(registry, "http://") + ":_authToken=" + token + "\n"
	}
	return out
}

// yarnrc is a .yarnrc.yml that installs from registry on plain HTTP into node_modules. The tests
// install versions published seconds before, so it turns off the week-long age gate of Yarn 4.
func yarnrc(registry, host, token string) string {
	out := "nodeLinker: node-modules\nenableTelemetry: false\nnpmMinimalAgeGate: 0\nnpmRegistryServer: \"" + registry + "\"\nunsafeHttpWhitelist:\n  - \"" + host + "\"\n"
	if token != "" {
		out += "npmAlwaysAuth: true\nnpmAuthToken: \"" + token + "\"\n"
	}
	return out
}

// npmRequire is the shell line that prints "REQ <name>: <export>" for each name with node.
func npmRequire(names ...string) string {
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "node -e 'console.log(\"REQ %[1]s: \" + require(\"%[1]s\"))'\n", n)
	}
	return b.String()
}
