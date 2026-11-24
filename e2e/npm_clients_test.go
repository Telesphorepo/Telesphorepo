//go:build e2e || import || clients

package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The npm clients that several zones share: npm (in the Node image) and Bun.
var (
	nodeImage = floatingImage("docker.io/library/node:lts")
	bunImage  = floatingImage("docker.io/oven/bun:latest")
)

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

// npmRequire is the shell line that prints "REQ <name>: <export>" for each name with node.
func npmRequire(names ...string) string {
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "node -e 'console.log(\"REQ %[1]s: \" + require(\"%[1]s\"))'\n", n)
	}
	return b.String()
}
