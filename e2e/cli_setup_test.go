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
// telesphorepo.toml; git ignores the file, so setup warns; a second use fails with the message of
// the server; on a terminal setup writes the CI file on request; a Docker
// repo gets no project file, and y adds the credential helper to the Docker config file while n
// keeps the file as it is.
func TestCLISetup(t *testing.T) {
	t.Parallel()
	c := newCLI(t)
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
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
	})
}

// addAccountScript logs in as a second account of the server on a terminal: the menu of the
// accounts, then "Log in as another account" and the device flow.
const addAccountScript = `set -e
cd /work/app
printf '2\n' | script -qec 'telesphorepo login "$SERVER"' /dev/null
`

// accountsScript works with the two accounts of the server: the status lists both with the
// active one marked; a publish acts as the active account, and a switch on a terminal changes
// it; logout of the active account leaves the other active, and the logout of the last one
// removes the hook entries of the server and the folder of the server. The Docker config file
// holds the entry of the server and an entry of another tool.
const accountsScript = `set -e
mkdir -p /root/.docker
printf '{"credHelpers": {"%s": "telesphorepo", "other.example": "other"}, "auths": {}}\n' "$HOST" > /root/.docker/config.json
echo '=== modes ==='
stat -c '%a %n' /root/.telesphorepo/* /root/.telesphorepo/*/active /root/.telesphorepo/*/accounts/* /root/.telesphorepo/*/accounts/*/device.json
echo '=== status ==='
cd /work/app
telesphorepo status
echo '=== publish as the publisher ==='
cd /work/pub
telesphorepo publish hello.txt
echo '=== switch ==='
printf '2\n' | script -qec 'telesphorepo login "$SERVER"' /dev/null
echo '=== publish as the developer ==='
if telesphorepo publish hello.txt; then echo 'publish: exit 0'; else echo 'publish: failed'; fi
echo '=== logout one ==='
cd /work/app
telesphorepo logout
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
// publishes. On a terminal login adds a second account of the same server; status names each
// account with its device (the active one marked), the hook and the project. Every publish acts
// as the active account, which login switches. Logout revokes the device of the active account
// on the server and leaves the other account active; the logout of the last account removes
// only the hook entries of the server and deletes the folder of the server.
func TestCLILogin(t *testing.T) {
	t.Parallel()
	c := newCLI(t)
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo, pubRepo := RandName("files"), RandName("files")
		s.CreateRepo(t, repo, "raw", false, nil)
		s.CreateRepo(t, pubRepo, "raw", false, nil)
		dev, pub := RandName("dev"), RandName("pub")
		s.CreateUser(t, dev, "dev-pass-1234")
		s.CreateUser(t, pub, "pub-pass-1234")
		s.SetPerms(t, repo, dev, "repo.download", "repo.publish")
		// Only the publisher publishes to pubRepo.
		s.SetPerms(t, pubRepo, pub, "repo.download", "repo.publish")
		host := registryHost(s)
		home := NewDataVolume(t)
		mounts := map[string]string{home: "/root"}
		files := []ContainerFile{projectFile(s, "/work/app", repo), {Path: "/work/app/hello.txt", Content: "hello from a teammate\n"},
			projectFile(s, "/work/pub", pubRepo), {Path: "/work/pub/hello.txt", Content: "hello from the publisher\n"}}
		env := map[string]string{"HOST": host, "SERVER": s.InternalURL}

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
		type devices struct {
			Items []struct {
				ID    int64  `json:"id"`
				State string `json:"state"`
			} `json:"items"`
		}
		// deviceOf returns the one device of user in state.
		deviceOf := func(sess *Session, user, state string) int64 {
			t.Helper()
			var ds devices
			s.MustAPI(t, http.MethodGet, "/api/devices", nil, sess, &ds)
			if len(ds.Items) != 1 || ds.Items[0].State != state {
				t.Fatalf("GET /api/devices of %s: %+v, want one %s device", user, ds.Items, state)
			}
			return ds.Items[0].ID
		}
		devSess := s.LoginTOTP(t, dev)
		devID := deviceOf(devSess, dev, "active")

		// A second account of the same server, from the menu of login.
		run = c.start(t, gradleImage, addAccountScript, ClientOpts{Files: files, Mounts: mounts, Env: env})
		approveDevice(t, s, pub, run.waitOutput(t, approveCodeRe, 2*time.Minute)[1])
		code, out = run.wait(t, 3*time.Minute)
		out = scanned(t, "login as a second account", out)
		if code != 0 {
			t.Fatalf("login as a second account: exit %d:\n%s", code, out)
		}
		wantText(t, "login as a second account", out, "1) Keep "+dev, "2) Log in as another account",
			"This computer is device ", "of "+s.InternalURL+", as "+pub+".")
		pubSess := s.LoginTOTP(t, pub)
		pubID := deviceOf(pubSess, pub, "active")

		out = c.mustRun(t, gradleImage, "two accounts", accountsScript, ClientOpts{Files: files, Mounts: mounts, Env: env})
		dir := "/root/.telesphorepo/" + s.Alias + "_8080"
		wantText(t, "the modes of the device files", section(out, "modes"), "700 "+dir+"\n", "700 "+dir+"/accounts/"+dev+"\n",
			"600 "+dir+"/accounts/"+dev+"/device.json", "600 "+dir+"/accounts/"+pub+"/device.json", "600 "+dir+"/active\n")
		wantText(t, "status", section(out, "status"), s.InternalURL+"\n",
			"  "+dev+" ", fmt.Sprintf("device %d, created ", devID), "* "+pub+" (active)", fmt.Sprintf("device %d, created ", pubID),
			"the server accepts it",
			"Docker credential helper for "+host+" in /root/.docker/config.json",
			"Project file /work/app/telesphorepo.toml: repo "+repo+" on "+s.InternalURL)
		if b := getFollowS3(t, s, opts.S3, "/raw/"+pubRepo+"/hello.txt"); string(b) != "hello from the publisher\n" {
			t.Fatalf("GET the file that the publisher published: %q", b)
		}
		wantText(t, "switch to the developer", section(out, "switch"), "1) Keep "+pub, "2) Switch to "+dev, "3) Log in as another account",
			dev+" is now the active account of "+s.InternalURL+".")
		wantText(t, "publish as the developer", section(out, "publish as the developer"), "publish: failed")
		wantText(t, "logout of the active account", section(out, "logout one"), fmt.Sprintf("Revoked device %d on %s.", devID, s.InternalURL),
			"Logged out "+dev+".", pub+" is now the active account of "+s.InternalURL+".",
			"* "+pub+" (active)", "Docker credential helper for "+host)
		wantNoText(t, "logout of the active account", section(out, "logout one"), fmt.Sprintf("device %d, created ", devID), "Deleted "+dir+".")
		wantText(t, "logout", section(out, "logout"), fmt.Sprintf("Revoked device %d on %s.", pubID, s.InternalURL),
			"Removed the credential helper of "+host+" from /root/.docker/config.json.", "Deleted "+dir+".")
		after := section(out, "after")
		wantText(t, "after the logout", after, `"other.example": "other"`, `"auths": {}`, "This computer has no device of any server.")
		wantNoText(t, "after the logout", after, `"`+host+`"`, s.Alias+"_8080")

		deviceOf(devSess, dev, "revoked")
		deviceOf(pubSess, pub, "revoked")
		if strings.Contains(out, "trx.") {
			t.Fatalf("status or logout printed a device proof:\n%s", out)
		}
	})
}
