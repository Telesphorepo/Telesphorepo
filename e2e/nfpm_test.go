//go:build e2e || import || clients

package e2e

// The packager of the Arch and RPM packages that Anchor's release uses.
var nfpmImage = floatingImage("docker.io/goreleaser/nfpm:latest")
