//go:build clients

package e2e

import (
	"strings"
	"testing"
	"time"
)

// TestCLICargo: setup and publish run cargo publish for a crate in the Rust image, against a
// private repo; the crate is in the index, a second version publishes with a new session, and
// cargo builds a binary from the repo with a read token. The secret scan finds no token.
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

		for _, v := range []string{"0.1.0", "0.2.0"} {
			out := c.mustRun(t, rustImage, "publish "+v, `set -e
cd /work/lib
telesphorepo setup "$E2E_LINK"
telesphorepo publish
`, ClientOpts{
				Files:   cargoLib("/work/lib", crate, v, "", "hello "+v+" from "+crate),
				Env:     cargoEnv(map[string]string{"E2E_LINK": s.NewSetupCode(t, dev, repo).Link}),
				Mounts:  map[string]string{devices: "/root/.telesphorepo"},
				Timeout: 20 * time.Minute,
			})
			wantText(t, "publish "+v, out, "cargo publish")
			requireAssets(t, s, repo, "crates/", "crates/"+crate+"/"+crate+"-"+v+".crate")
		}

		read := s.CreateToken(t, dev, devPass, "cli-cargo")
		code, out := RunClient(t, rustImage, ClientOpts{Cmd: []string{"cd /work/app && cargo run -q"},
			Files: append(cargoBin("/work/app", crate+" = { version = \"0.2\", registry = \"tr\" }\n", crate),
				cargoRegistry("/work", "tr", s.InternalURL+"/cargo/"+repo+"/index/", false)),
			Env: cargoEnv(map[string]string{cargoTokenEnv("tr"): read}), Timeout: 20 * time.Minute})
		if code != 0 || !strings.Contains(out, "hello 0.2.0 from "+crate) {
			t.Fatalf("cargo run of the published crate: exit %d\n%s", code, out)
		}
	})
}
