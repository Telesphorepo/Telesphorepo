//go:build clients

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// dindScan runs the secret scan in the Docker engine's container, with secret as $E2E_SECRET.
func dindScan(t *testing.T, d *Dind, what, secret string) {
	t.Helper()
	scanned(t, what, d.MustExec(t, what+": secret scan", "E2E_SECRET='"+secret+"'\nE2E_SCAN=''\n"+secretScan))
}

// TestCLIDocker: after setup, Docker in Docker pushes and pulls with no docker login (a device
// session); the helper answers only the hosts of the server; publish packs and pushes a Helm
// chart; ORAS pushes and pulls through the helper. TestCLIJenkins pushes from CI.
func TestCLIDocker(t *testing.T) {
	c := newCLI(t)
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		host := registryHost(s)
		own := RandName("own")
		s.CreateRepo(t, own, "docker", false, nil)
		s.CreateUser(t, "builder", "builder-pass-1234")
		s.SetPerms(t, own, "builder", "repo.download", "repo.publish")

		t.Run("docker", func(t *testing.T) {
			d := StartDind(t, host)
			d.CopyFile(t, c.bin, cliPath, 0o755)
			out := d.MustExec(t, "setup", fmt.Sprintf("set -e\nln -sf telesphorepo /usr/local/bin/docker-credential-telesphorepo\nmkdir -p /work\ncd /work\ntelesphorepo setup '%s' 2>&1\nls -A /work\n",
				s.NewSetupCode(t, "builder", own).Link))
			line := `"credHelpers": {"` + host + `": "telesphorepo"}`
			wantText(t, "setup of a Docker repo", out, fmt.Sprintf("publishing to %s (docker) as builder", own),
				"Kept /root/.docker/config.json. Add the entry yourself to push:\n  "+line,
				"Push with: docker push "+host+"/"+own+"/app:1.0")
			wantNoText(t, "setup of a Docker repo", out, "telesphorepo.toml")

			// The person adds the entry that setup printed, and Docker pushes and pulls through the
			// helper.
			img := host + "/" + own + "/app"
			d.MustExec(t, "push and pull", "set -e\nmkdir -p /root/.docker\nprintf '%s\\n' '{"+line+"}' > /root/.docker/config.json\n"+
				DockerScratchImage("/ctx", 1<<20, 3<<20)+fmt.Sprintf("docker build -q -t %[1]s:v1 /ctx\ndocker push %[1]s:v1\ndocker rmi %[1]s:v1\ndocker pull %[1]s:v1\n", img))
			found := false
			for _, it := range publishLog(t, s, own) {
				found = found || (it.Via == "device" && it.Subject == "builder" && it.DeviceID != nil)
			}
			if !found {
				t.Fatalf("publish log of %s: %+v, want a device session of builder", own, publishLog(t, s, own))
			}

			code, out := d.Exec(t, "echo other.example:5000 | docker-credential-telesphorepo get")
			if code != 1 || strings.TrimSpace(out) != "credentials not found in native keychain" {
				t.Fatalf("get for another host: exit %d: %s", code, out)
			}
			var cred struct{ ServerURL, Username, Secret string }
			out = d.MustExec(t, "get", "echo "+host+" | docker-credential-telesphorepo get")
			if json.Unmarshal([]byte(out), &cred) != nil || cred.ServerURL != host || cred.Username != "telesphorepo-device" || !strings.HasPrefix(cred.Secret, "trx.") {
				t.Fatalf("get: %s, want a device proof for %s", out, host)
			}
			var hosts map[string]string
			out = d.MustExec(t, "list", "echo unused | docker-credential-telesphorepo list")
			if json.Unmarshal([]byte(out), &hosts) != nil || hosts[host] != "telesphorepo-device" {
				t.Fatalf("list: %s, want %s", out, host)
			}
			code, out = d.Exec(t, "echo '{}' | docker-credential-telesphorepo store")
			if code != 1 || !strings.Contains(out, "Run: telesphorepo login <server URL>") {
				t.Fatalf("store: exit %d: %s", code, out)
			}
			dindScan(t, d, "push and pull", "")
		})

		t.Run("helm chart", func(t *testing.T) {
			out := c.mustRun(t, helmImage, "publish a chart", `set -e
mkdir -p /work
cd /work
helm create mychart >/dev/null
cd mychart
echo '=== setup ==='
telesphorepo setup "$E2E_LINK"
cat telesphorepo.toml
echo '=== publish ==='
telesphorepo publish
`, ClientOpts{Env: map[string]string{"E2E_LINK": s.NewSetupCode(t, "builder", own).Link}})
			wantText(t, "setup of a chart", section(out, "setup"), fmt.Sprintf("publishing to %s (docker) as builder", own), fmt.Sprintf("repo = %q", own))
			wantText(t, "publish a chart", section(out, "publish"), "Publishing to "+own+" (docker)", "helm push ", "oci://"+host+"/"+own)
			resp, b := s.Do(t, s.NewRequest(t, http.MethodGet, "/v2/"+own+"/mychart/tags/list", nil), s.AdminBasic())
			if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), `"0.1.0"`) {
				t.Fatalf("tags of the chart: status %d: %s, want 0.1.0", resp.StatusCode, b)
			}
		})

		t.Run("oras", func(t *testing.T) {
			c.mustRun(t, orasImage, "oras push and pull", `set -e
mkdir -p /work /root/.docker
cd /work
telesphorepo setup "$E2E_LINK"
printf '{"credHelpers": {"%s": "telesphorepo"}}\n' "$HOST" > /root/.docker/config.json
echo hello > a.txt
oras push --plain-http "$HOST/$REPO/art:v1" a.txt
oras pull --plain-http -o out "$HOST/$REPO/art:v1"
cmp out/a.txt a.txt
`, ClientOpts{Env: map[string]string{"E2E_LINK": s.NewSetupCode(t, "builder", own).Link, "HOST": host, "REPO": own}})
		})
	})
}
