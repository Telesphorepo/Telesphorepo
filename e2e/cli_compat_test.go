//go:build clients

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// The change logs of TestCLICompat. The server v0.9.0 and its CLI hold compatServerChanges; the
// newer CLI v1.0.0 adds two entries that need a server v1.0.0. The older CLI v0.8.0 holds the
// change log of the repo.
const compatServerChanges = `[[change]]
version = "v0.9.0"
scope = "publish/raw"
needs = "cli"
summary = "E2E: publishing to a raw repository needs CLI v0.9.0."
e2e_field = "a field that no version knows"
`

const compatNewChanges = compatServerChanges + `
[[change]]
version = "v1.0.0"
scope = "publish/raw"
needs = "server"
summary = "E2E: publishing to a raw repository needs server v1.0.0."

[[change]]
id = "e2e-login"
version = "v1.0.0"
scope = "login"
needs = "server"
summary = "E2E: login needs server v1.0.0."
`

// compatOlderPaths is the extra file of the newer CLI: it keeps the older path of the login entry,
// by its id.
const compatOlderPaths = `package main

func init() { olderPaths["e2e-login"] = true }
`

// compatStubConf is a server of the CLI tests at /<path>: /hash/ is the compat server with a
// SHA256SUMS that names another hash, /future/ answers a problem with fields and a remedy action
// that no CLI knows, and /down/ is the HTML 502 page of a proxy.
const compatStubConf = `{
	admin off
	auto_https off
}
:8080 {
	handle /hash/cli/SHA256SUMS {
		respond "%[1]s  %[2]s" 200
	}
	handle_path /hash/* {
		reverse_proxy %[3]s:8080
	}
	handle /future/* {
		header Content-Type application/problem+json
		respond ` + "`" + `{"type":"urn:telesphorepo:problem:e2e-future","title":"A problem of a newer server","detail":"The CLI prints the title and the detail of a problem that it does not know.","status":409,"e2e_field":{"x":1},"remedy":{"action":"e2e-teleport","url":"http://docs.example/e2e","e2e":true}}` + "`" + ` 409
	}
	handle /down/* {
		header Content-Type "text/html; charset=utf-8"
		respond "<html><body><h1>502 Bad Gateway</h1></body></html>" 502
	}
}
`

// compatScript runs the CLI v0.8.0 (on the PATH) and the CLI v1.0.0 (/opt/new) against the
// server v0.9.0 ($SERVER) and the stub ($STUB). update takes the server of the project file
// first, so the update from the stub runs outside the project folder.
const compatScript = `cd /work/raw
echo '=== setup ==='
telesphorepo setup "$E2E_LINK"; echo "rc=$?"
echo '=== login ==='
telesphorepo login "$SERVER"; echo "rc=$?"
echo '=== old publish ==='
telesphorepo publish a.txt; echo "rc=$?"
echo '=== new publish ==='
/opt/new/telesphorepo publish a.txt; echo "rc=$?"
echo '=== new login ==='
/opt/new/telesphorepo login "$SERVER"; echo "rc=$?"
echo '=== new update ==='
before=$(sha256sum /opt/new/telesphorepo | cut -d ' ' -f 1)
/opt/new/telesphorepo update "$SERVER"; echo "rc=$?"
if [ "$(sha256sum /opt/new/telesphorepo | cut -d ' ' -f 1)" = "$before" ]; then echo 'new binary: kept'; fi
echo '=== future ==='
telesphorepo login "$STUB/future"; echo "rc=$?"
echo '=== down ==='
telesphorepo login "$STUB/down"; echo "rc=$?"
echo '=== bad hash ==='
(cd / && telesphorepo update "$STUB/hash"); echo "rc=$?"
telesphorepo version
echo '=== update on a terminal ==='
rm -f /usr/local/bin/docker-credential-telesphorepo
printf 'y\n' | script -qec 'telesphorepo publish a.txt' /dev/null; echo "rc=$?"
telesphorepo version
readlink /usr/local/bin/docker-credential-telesphorepo
echo '=== end ==='
`

// TestCLICompat: the server v0.9.0 sends its version on every answer; the older CLI v0.8.0 prints
// the drift notice once a day, stops a command whose entry needs a newer CLI (and on a terminal
// updates itself and runs the command again), and ignores an unknown field of an entry; the newer
// CLI v1.0.0 takes the older path of an entry that needs a newer server, or stops with the
// summary; a problem with an unknown remedy action prints its text and link; an HTML 502 gets the
// generic message; update checks the hash, replaces the binary and the helper name, and changes
// nothing for a newer CLI.
func TestCLICompat(t *testing.T) {
	old := &cli{bin: BuildCLI(t, "v0.8.0", "", nil)}
	newer := BuildCLI(t, "v1.0.0", compatNewChanges, map[string]string{"e2e_olderpaths.go": compatOlderPaths})
	server := BuildServer(t, "v0.9.0", compatServerChanges, BuildCLI(t, "v0.9.0", compatServerChanges, nil))
	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		opts.Files = append(opts.Files, ServerBinary(server))
		s := StartServer(t, opts)
		stub := RandName("stub")
		startCaddy(t, fmt.Sprintf(compatStubConf, strings.Repeat("0", 64), cliArchiveName, s.Alias), "Caddy stub", stub)
		repo := RandName("files")
		s.CreateRepo(t, repo, "raw", false, nil)
		dev := RandName("dev")
		s.CreateUser(t, dev, "dev-pass-1234")
		s.SetPerms(t, repo, dev, "repo.download", "repo.publish")

		resp, b := s.Do(t, s.NewRequest(t, http.MethodGet, "/api/changes?since=v0.8.0", nil), nil)
		var changes struct {
			Version string `json:"version"`
			Changes []struct {
				Version, Scope, Needs, Summary string
			} `json:"changes"`
		}
		if resp.Header.Get("Telesphorepo-Version") != "v0.9.0" || json.Unmarshal(b, &changes) != nil || changes.Version != "v0.9.0" ||
			len(changes.Changes) != 1 || changes.Changes[0].Scope != "publish/raw" {
			t.Fatalf("GET /api/changes?since=v0.8.0: Telesphorepo-Version %q: %s, want v0.9.0 and the publish/raw entry", resp.Header.Get("Telesphorepo-Version"), b)
		}

		out := old.mustRun(t, debImage, "the CLI v0.8.0 and v1.0.0 with the server v0.9.0", compatScript, ClientOpts{
			Files: []ContainerFile{{HostPath: newer, Path: "/opt/new/telesphorepo", Mode: 0o755}, {Path: "/work/raw/a.txt", Content: "compat\n"}},
			Env: map[string]string{"E2E_LINK": s.NewSetupCode(t, dev, repo).Link, "SERVER": s.InternalURL,
				"STUB": "http://" + stub + ":8080"},
		})
		drift := fmt.Sprintf("telesphorepo: the server %[1]s has CLI v0.9.0, you have v0.8.0. Run: telesphorepo update %[1]s", s.InternalURL)
		setup, login := section(out, "setup"), section(out, "login")
		wantText(t, "setup with the CLI v0.8.0", setup, fmt.Sprintf("publishing to %s (raw) as %s", repo, dev), drift, "rc=0")
		wantText(t, "login with the CLI v0.8.0", login, "This computer is already device ", "rc=0")
		wantNoText(t, "login with the CLI v0.8.0 on the same day", login, drift)

		wantText(t, "publish with the CLI v0.8.0", section(out, "old publish"),
			"E2E: publishing to a raw repository needs CLI v0.9.0.",
			"This needs CLI v0.9.0; you have v0.8.0. Install the CLI of the server:\n  curl -fsSL "+s.InternalURL+"/install.sh | sh", "rc=1")
		wantText(t, "publish with the CLI v1.0.0", section(out, "new publish"),
			"E2E: publishing to a raw repository needs server v1.0.0.",
			"This needs server v1.0.0; the server runs v0.9.0. Ask the server admin to update.", "rc=1")
		newLogin := section(out, "new login")
		wantText(t, "login with the CLI v1.0.0", newLogin, "This computer is already device ", "rc=0")
		wantNoText(t, "login with the CLI v1.0.0", newLogin, "E2E: login needs server v1.0.0.")
		wantText(t, "update of the CLI v1.0.0", section(out, "new update"),
			"This CLI (v1.0.0) is newer than the CLI of "+s.InternalURL+" (v0.9.0). A newer CLI works with an older server, so nothing changed.",
			"rc=0", "new binary: kept")

		wantText(t, "a problem that the CLI does not know", section(out, "future"), "A problem of a newer server",
			"The CLI prints the title and the detail of a problem that it does not know.", "http://docs.example/e2e", "rc=1")
		wantText(t, "an HTML 502 of a proxy", section(out, "down"),
			"the server at http://"+stub+":8080/down answered 502 with text/html; a proxy in front of it may be down", "rc=1")
		wantText(t, "update with a wrong hash", section(out, "bad hash"), "the SHA-256 of "+cliArchiveName+" is ",
			"but the server names "+strings.Repeat("0", 64)+"; nothing changed", "rc=1", "telesphorepo v0.8.0")

		term := section(out, "update on a terminal")
		wantText(t, "publish with the CLI v0.8.0 on a terminal", term,
			"E2E: publishing to a raw repository needs CLI v0.9.0.", "Update now? [Y/n]",
			"Installed the CLI of "+s.InternalURL+" (v0.9.0) in /usr/local/bin/telesphorepo.",
			"Uploaded a.txt to "+s.InternalURL+"/raw/"+repo+"/a.txt", "rc=0", "telesphorepo v0.9.0\n", "telesphorepo\n")
		if b := getFollowS3(t, s, opts.S3, "/raw/"+repo+"/a.txt"); string(b) != "compat\n" {
			t.Fatalf("GET a.txt after the update: %q", b)
		}
	})
}
