//go:build clients

package e2e

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// setupScript runs the setup lines $L1 to $L4 of TestCLISetup in a git work tree. The lines with
// a terminal run under script(1), which gives the CLI a terminal and types the answers.
const setupScript = `set -e
cd /work/app
git init -q
printf 'telesphorepo.toml\n' > .gitignore
echo '=== first ==='
telesphorepo setup "$L1"
cat telesphorepo.toml
echo '=== again ==='
if telesphorepo setup "$L1"; then echo 'second use: exit 0'; else echo "second use: exit $?"; fi
echo '=== ci file ==='
printf '2\n' | script -qec 'telesphorepo setup "$L2"' /dev/null
ls .github/workflows
cat .github/workflows/*
echo '=== docker yes ==='
mkdir -p /work/img
cd /work/img
touch Dockerfile
printf 'y\n\n' | script -qec 'telesphorepo setup "$L3"' /dev/null
cat /root/.docker/config.json
if [ -e telesphorepo.toml ]; then echo 'project file: written'; else echo 'project file: none'; fi
echo '=== docker no ==='
mkdir -p /work/dc2
printf '{"auths": {}}\n' > /work/dc2/config.json
cp /work/dc2/config.json /work/dc2/before.json
printf 'n\n\n' | DOCKER_CONFIG=/work/dc2 script -qec 'telesphorepo setup "$L4"' /dev/null
if cmp -s /work/dc2/config.json /work/dc2/before.json; then echo 'config: kept'; else echo 'config: changed'; fi
echo '=== end ==='
`

// TestCLISetup: the setup line of POST /api/publish/setup-codes enrolls the computer and writes
// telesphorepo.toml; git ignores the file, so setup warns; a second use and an expired code fail
// with the message of the server; on a terminal setup writes the CI file on request; a Docker
// repo gets no project file, and y adds the credential helper to the Docker config file while n
// keeps the file as it is.
func TestCLISetup(t *testing.T) {
	t.Parallel()
	c := newCLI(t)
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		data := NewDataVolume(t)
		opts.DataVolume = data
		s := StartServer(t, opts)
		app, img := RandName("app"), RandName("img")
		s.CreateRepo(t, app, "maven", false, nil)
		s.CreateRepo(t, img, "docker", false, nil)
		dev := RandName("dev")
		s.CreateUser(t, dev, "dev-pass-1234")
		for _, r := range []string{app, img} {
			s.SetPerms(t, r, dev, "repo.download", "repo.publish")
		}
		host := registryHost(s)
		home := NewDataVolume(t)
		files := []ContainerFile{{Path: "/work/app/pom.xml", Content: "<project/>\n"}}
		mounts := map[string]string{home: "/root"}

		out := c.mustRun(t, gradleImage, "setup", setupScript, ClientOpts{Files: files, Mounts: mounts, Env: map[string]string{
			"L1": s.NewSetupCode(t, dev, app).Link,
			"L2": s.NewSetupCode(t, dev, app).Link,
			"L3": s.NewSetupCode(t, dev, img).Link,
			"L4": s.NewSetupCode(t, dev, img).Link,
		}})

		first := section(out, "first")
		wantText(t, "setup", first,
			fmt.Sprintf("publishing to %s (maven) as %s", app, dev),
			"This computer is device ",
			"Warning: git ignores telesphorepo.toml (.gitignore:1:telesphorepo.toml)",
			fmt.Sprintf("server = %q", s.InternalURL), fmt.Sprintf("repo = %q", app))
		wantText(t, "the setup line again", section(out, "again"), "This setup line is already used", "second use: exit 1")
		ci := section(out, "ci file")
		wantText(t, "setup on a terminal", ci, "GitHub Actions", s.InternalURL+"/install.sh", "telesphorepo publish")
		wantNoText(t, "setup on a terminal", ci, "This computer is device ")

		yes := section(out, "docker yes")
		wantText(t, "setup of a Docker repo, yes", yes,
			fmt.Sprintf("publishing to %s (docker) as %s", img, dev),
			`"`+host+`": "telesphorepo"`, "project file: none")
		wantText(t, "setup of a Docker repo, no", section(out, "docker no"),
			"Kept /work/dc2/config.json. Add the entry yourself to push:",
			`"credHelpers": {"`+host+`": "telesphorepo"}`, "config: kept")

		// An expired setup line: the server keeps the code until someone uses it, and then names
		// its repo.
		link := s.NewSetupCode(t, dev, app).Link
		s.RestartAfter(t, func() {
			editDB(t, data, "UPDATE auth_challenges SET expires_at = 1 WHERE kind = 'setup_code'")
		})
		code, out := c.run(t, gradleImage, "setup with an expired line", `cd /work/app && telesphorepo setup "$L5"`,
			ClientOpts{Files: files, Mounts: mounts, Env: map[string]string{"L5": link}})
		if code != 1 {
			t.Fatalf("setup with an expired line: exit %d, want 1:\n%s", code, out)
		}
		wantText(t, "setup with an expired line", out, "This setup line is already used", "This setup line expired",
			s.InternalURL+"/#/repo/"+app)
	})
}

// statusScript shows the status, logs out and shows the status again. The Docker config file
// holds the entry of the server and an entry of another tool.
const statusScript = `set -e
cd /work/app
mkdir -p /root/.docker
printf '{"credHelpers": {"%s": "telesphorepo", "other.example": "other"}, "auths": {}}\n' "$HOST" > /root/.docker/config.json
echo '=== modes ==='
stat -c '%a %n' /root/.telesphorepo/* /root/.telesphorepo/*/device.json
echo '=== status ==='
telesphorepo status
echo '=== logout ==='
telesphorepo logout
echo '=== after ==='
cat /root/.docker/config.json
ls -A /root/.telesphorepo
telesphorepo status
echo '=== end ==='
`

// TestCLILogin: a teammate's publish with no device runs the device flow inline and then
// publishes; status names the device, the hook and the project; logout revokes the device on the
// server, removes only the hook entries of the server and deletes the folder of the server.
func TestCLILogin(t *testing.T) {
	t.Parallel()
	c := newCLI(t)
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo := RandName("files")
		s.CreateRepo(t, repo, "raw", false, nil)
		dev := RandName("dev")
		s.CreateUser(t, dev, "dev-pass-1234")
		s.SetPerms(t, repo, dev, "repo.download", "repo.publish")
		host := registryHost(s)
		home := NewDataVolume(t)
		mounts := map[string]string{home: "/root"}
		files := []ContainerFile{projectFile(s, "/work/app", repo), {Path: "/work/app/hello.txt", Content: "hello from a teammate\n"}}

		run := c.start(t, curlImage, "cd /work/app && telesphorepo publish hello.txt", ClientOpts{Files: files, Mounts: mounts})
		approveDevice(t, s, dev, run.waitOutput(t, approveCodeRe, 2*time.Minute)[1])
		code, out := run.wait(t, 3*time.Minute)
		out = scanned(t, "publish with the device flow", out)
		if code != 0 {
			t.Fatalf("publish with the device flow: exit %d:\n%s", code, out)
		}
		if b := getFollowS3(t, s, opts.S3, "/raw/"+repo+"/hello.txt"); string(b) != "hello from a teammate\n" {
			t.Fatalf("GET the published file: %q", b)
		}
		var devices struct {
			Items []struct {
				ID    int64  `json:"id"`
				State string `json:"state"`
			} `json:"items"`
		}
		sess := s.LoginTOTP(t, dev)
		s.MustAPI(t, http.MethodGet, "/api/devices", nil, sess, &devices)
		if len(devices.Items) != 1 || devices.Items[0].State != "active" {
			t.Fatalf("GET /api/devices after the device flow: %+v, want one active device", devices.Items)
		}
		id := devices.Items[0].ID

		out = c.mustRun(t, curlImage, "status and logout", statusScript, ClientOpts{Files: files, Mounts: mounts, Env: map[string]string{"HOST": host}})
		dir := "/root/.telesphorepo/" + s.Alias + "_8080"
		wantText(t, "the modes of the device files", section(out, "modes"), "700 "+dir+"\n", "600 "+dir+"/device.json")
		wantText(t, "status", section(out, "status"), s.InternalURL+"\n", fmt.Sprintf("device %d, created ", id), "the server accepts it",
			"Docker credential helper for "+host+" in /root/.docker/config.json",
			"Project file /work/app/telesphorepo.toml: repo "+repo+" on "+s.InternalURL)
		wantText(t, "logout", section(out, "logout"), fmt.Sprintf("Revoked device %d on %s.", id, s.InternalURL),
			"Removed the credential helper of "+host+" from /root/.docker/config.json.", "Deleted "+dir+".")
		after := section(out, "after")
		wantText(t, "after the logout", after, `"other.example": "other"`, `"auths": {}`, "This computer has no device of any server.")
		wantNoText(t, "after the logout", after, `"`+host+`"`, s.Alias+"_8080")

		devices.Items = nil
		s.MustAPI(t, http.MethodGet, "/api/devices", nil, sess, &devices)
		if len(devices.Items) != 1 || devices.Items[0].State != "revoked" {
			t.Fatalf("GET /api/devices after the logout: %+v, want device %d revoked", devices.Items, id)
		}
		if strings.Contains(out, "trx.") {
			t.Fatalf("status or logout printed a device proof:\n%s", out)
		}
	})
}
