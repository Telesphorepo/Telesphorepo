//go:build clients

package e2e

import (
	"testing"
	"time"
)

// TestCLICargo: setup and publish run cargo publish for a crate in the Rust image, against a
// private repo. The secret scan finds no token.
func TestCLICargo(t *testing.T) {
	t.Parallel()
	c := newCLI(t)
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo := RandName("crates")
		s.CreateRepo(t, repo, "cargo", false, nil)
		dev, devPass := RandName("dev"), "dev-pass-1234"
		s.CreateUser(t, dev, devPass)
		s.SetPerms(t, repo, dev, "repo.download", "repo.publish")
		devices := NewDataVolume(t)
		crate := "cli_" + randHex(4)

		out := c.mustRun(t, rustImage, "publish", `set -e
cd /work/lib
telesphorepo setup "$E2E_LINK"
telesphorepo publish
`, ClientOpts{
			Files:   cargoLib("/work/lib", crate, "0.1.0", "", "hello from "+crate),
			Env:     cargoEnv(map[string]string{"E2E_LINK": s.NewSetupCode(t, dev, repo).Link}),
			Mounts:  map[string]string{devices: "/root/.telesphorepo"},
			Timeout: 20 * time.Minute,
		})
		wantText(t, "publish", out, "cargo publish")
		requireAssets(t, s, repo, "crates/", "crates/"+crate+"/"+crate+"-0.1.0.crate")
	})
}
