//go:build e2e

package e2e

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The Docker credential helper (cmd/docker-credential-telesphorepo) in Docker in Docker: it
// enrolls a device that a person approves, and Docker pushes and pulls through it without a
// docker login.

// helperBin is where the tests put the helper in the Docker engine's container.
const helperBin = "/usr/local/bin/docker-credential-telesphorepo"

var helperCodeRe = regexp.MustCompile(`approve code ([A-Z]{4}-[A-Z]{4})`)

// TestDockerCredentialHelper: "login" enrolls the computer as a device of the "docker" client and
// sets up credHelpers; the device file has the schema of the Java clients and only its owner can
// read it; Docker pushes and pulls with no docker login (a device session in the publish log);
// the device reads a private repo where its user holds only download, and cannot push there;
// "get" checks the host and gives one process the same proof each time, a new token exchange nonce
// needs the device signature, "list" names the hosts; after a revoke "get" finds nothing, a push
// fails and an anonymous pull of a public repo still works.
func TestDockerCredentialHelper(t *testing.T) {
	bin := BuildHelper(t)
	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		host := registryHost(s)
		own, priv, pub := RandName("own"), RandName("priv"), RandName("pub")
		s.CreateRepo(t, own, "docker", false, nil)
		s.CreateRepo(t, priv, "docker", false, nil)
		s.CreateRepo(t, pub, "docker", true, nil)
		s.CreateUser(t, "builder", "builder-pass-1234")
		s.SetPerms(t, own, "builder", "repo.download", "repo.publish", "repo.delete")
		s.SetPerms(t, priv, "builder", "repo.download")
		s.CreateUser(t, "seeder", "seeder-pass-1234")
		s.SetPerms(t, priv, "seeder", "repo.download", "repo.publish")
		s.SetPerms(t, pub, "seeder", "repo.download", "repo.publish")

		d := StartDind(t, host)
		d.CopyFile(t, bin, helperBin, 0o755)

		// The seeder fills the private and the public repo with a publish session token, in a
		// Docker config of its own.
		seed := func(repo string) string {
			return fmt.Sprintf("printf '%%s' '%s' | DOCKER_CONFIG=/seed docker login %s -u seeder --password-stdin\n", PublishToken(t, s, "seeder", repo), host) +
				fmt.Sprintf("docker tag base:v1 %[1]s/%[2]s/base:v1\nDOCKER_CONFIG=/seed docker push %[1]s/%[2]s/base:v1\ndocker rmi %[1]s/%[2]s/base:v1\n", host, repo)
		}
		d.MustExec(t, "seed the repos", "set -e\n"+DockerScratchImage("/seedctx", 64<<10, 8<<10)+
			"docker build -q -t base:v1 /seedctx\n"+seed(priv)+seed(pub))

		var deviceID int64
		var deviceKey ed25519.PrivateKey
		t.Run("login enrolls a device", func(t *testing.T) {
			d.MustExec(t, "start the login", fmt.Sprintf(
				"(%s login http://%s; echo \"rc=$?\" > /login.rc) > /login.log 2>&1 < /dev/null &\n", helperBin, host))
			var code string
			deadline := time.Now().Add(time.Minute)
			for code == "" {
				if time.Now().After(deadline) {
					t.Fatalf("no user code in the output of the login:\n%s", d.MustExec(t, "read the login output", "cat /login.log"))
				}
				time.Sleep(500 * time.Millisecond)
				if m := helperCodeRe.FindStringSubmatch(d.MustExec(t, "read the login output", "cat /login.log")); m != nil {
					code = m[1]
				}
			}
			sess := s.LoginTOTP(t, "builder")
			if st, b := s.ApproveEnrollment(t, sess, "builder", code); st/100 != 2 {
				t.Fatalf("approve the helper's code %s: status %d: %s", code, st, b)
			}
			deadline = time.Now().Add(time.Minute)
			for {
				out := d.MustExec(t, "read the login result", "cat /login.rc 2>/dev/null || true")
				if strings.Contains(out, "rc=") {
					if !strings.Contains(out, "rc=0") {
						t.Fatalf("login: %s\n%s", out, d.MustExec(t, "read the login output", "cat /login.log"))
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("the login did not end after the approval:\n%s", d.MustExec(t, "read the login output", "cat /login.log"))
				}
				time.Sleep(time.Second)
			}

			// Docker's config names the helper for the registry host.
			var cfg struct {
				CredHelpers map[string]string `json:"credHelpers"`
			}
			if err := json.Unmarshal([]byte(d.MustExec(t, "read config.json", "cat /root/.docker/config.json")), &cfg); err != nil {
				t.Fatalf("config.json: %v", err)
			}
			if cfg.CredHelpers[host] != "telesphorepo" {
				t.Fatalf("config.json credHelpers %v, want %s: telesphorepo", cfg.CredHelpers, host)
			}

			// The device file: the schema of the Java clients, mode 600 in a 700 folder.
			dir := "/root/.telesphorepo/" + strings.ReplaceAll(host, ":", "_") + "/docker"
			modes := strings.Fields(d.MustExec(t, "read the modes", fmt.Sprintf("stat -c %%a %[1]s %[1]s/device.json", dir)))
			if len(modes) != 2 || modes[0] != "700" || modes[1] != "600" {
				t.Fatalf("modes of the device folder and file %v, want 700 and 600", modes)
			}
			var dev struct {
				Server     string `json:"server"`
				DeviceID   int64  `json:"device_id"`
				Client     string `json:"client"`
				PrivateKey string `json:"private_key"`
				PublicKey  string `json:"public_key"`
				CreatedAt  string `json:"created_at"`
			}
			raw := d.MustExec(t, "read the device file", "cat "+dir+"/device.json")
			if err := json.Unmarshal([]byte(raw), &dev); err != nil {
				t.Fatalf("device.json %s: %v", raw, err)
			}
			if dev.Server != "http://"+host || dev.Client != "docker" || dev.DeviceID <= 0 {
				t.Fatalf("device.json %s: want server http://%s, client docker and a device id", raw, host)
			}
			der, err := base64.StdEncoding.DecodeString(dev.PrivateKey)
			if err != nil {
				t.Fatalf("device.json private_key: %v", err)
			}
			key, err := x509.ParsePKCS8PrivateKey(der)
			edKey, ok := key.(ed25519.PrivateKey)
			if err != nil || !ok {
				t.Fatalf("device.json private_key is not a PKCS#8 Ed25519 key: %v", err)
			}
			pubKey, err := base64.StdEncoding.DecodeString(dev.PublicKey)
			if err != nil || !edKey.Public().(ed25519.PublicKey).Equal(ed25519.PublicKey(pubKey)) {
				t.Fatalf("device.json public_key %q is not the public key of private_key", dev.PublicKey)
			}
			if _, err := time.Parse(time.RFC3339, dev.CreatedAt); err != nil {
				t.Fatalf("device.json created_at %q: %v", dev.CreatedAt, err)
			}
			deviceID, deviceKey = dev.DeviceID, edKey

			// The server lists the device with the client docker.
			var devices struct {
				Items []struct {
					ID          int64  `json:"id"`
					Client      string `json:"client"`
					ClientTitle string `json:"client_title"`
					State       string `json:"state"`
				} `json:"items"`
			}
			s.MustAPI(t, http.MethodGet, "/api/devices", nil, Basic{"builder", "builder-pass-1234"}, &devices)
			found := false
			for _, x := range devices.Items {
				if x.ID == deviceID {
					found = x.Client == "docker" && x.ClientTitle == "Docker credential helper" && x.State == "active"
				}
			}
			if !found {
				t.Fatalf("GET /api/devices %+v: want device %d of the client docker, active", devices.Items, deviceID)
			}
		})
		if deviceID == 0 {
			t.FailNow()
		}

		img := host + "/" + own + "/app"
		t.Run("push and pull without docker login", func(t *testing.T) {
			d.MustExec(t, "push and pull", "set -e\n"+DockerScratchImage("/ctx", 1<<20, 3<<20)+fmt.Sprintf(`docker build -q -t %[1]s:v1 /ctx
docker push %[1]s:v1
docker rmi %[1]s:v1
docker pull %[1]s:v1
`, img))
			var log struct {
				Items []struct {
					Repo     string `json:"repo"`
					Via      string `json:"via"`
					Subject  string `json:"subject"`
					DeviceID *int64 `json:"device_id"`
				} `json:"items"`
			}
			s.MustAPI(t, http.MethodGet, "/api/publish/log", nil, s.Admin(), &log)
			found := false
			for _, x := range log.Items {
				if x.Repo == own && x.Via == "device" && x.Subject == "builder" && x.DeviceID != nil && *x.DeviceID == deviceID {
					found = true
				}
			}
			if !found {
				t.Fatalf("publish log %+v: want a device session of device %d on %s", log.Items, deviceID, own)
			}
		})

		t.Run("the device reads what its user reads", func(t *testing.T) {
			d.MustExec(t, "pull the private repo", fmt.Sprintf("set -e\ndocker pull %[1]s/%[2]s/base:v1\ndocker rmi %[1]s/%[2]s/base:v1\n", host, priv))
			code, out := d.Exec(t, fmt.Sprintf("docker tag %[1]s:v1 %[2]s/%[3]s/app:x && docker push %[2]s/%[3]s/app:x 2>&1", img, host, priv))
			if low := strings.ToLower(out); code == 0 || !(strings.Contains(low, "denied") || strings.Contains(low, "permission required") || strings.Contains(low, "403") || strings.Contains(low, "forbidden")) {
				t.Fatalf("push to a repo where the user holds only download: exit %d, want a denial:\n%s", code, out)
			}
		})

		t.Run("get and list", func(t *testing.T) {
			code, out := d.Exec(t, "echo other.example:5000 | "+helperBin+" get")
			if code != 1 || strings.TrimSpace(out) != "credentials not found in native keychain" {
				t.Fatalf("get for another host: exit %d: %s", code, out)
			}
			var cred struct {
				ServerURL, Username, Secret string
			}
			out = d.MustExec(t, "get", "echo "+host+" | "+helperBin+" get")
			if err := json.Unmarshal([]byte(out), &cred); err != nil {
				t.Fatalf("get: %s: %v", out, err)
			}
			if cred.ServerURL != host || cred.Username != "telesphorepo-device" || !strings.HasPrefix(cred.Secret, fmt.Sprintf("trx.%d.", deviceID)) {
				t.Fatalf("get: %s: want a device proof of device %d", out, deviceID)
			}

			// A new token exchange nonce needs the device signature over a recent time.
			_, otherKey, _ := ed25519.GenerateKey(nil)
			sign := func(key ed25519.PrivateKey, ts int64) string {
				msg := fmt.Sprintf("telesphorepo challenge v1\n%d\n%d", deviceID, ts)
				return base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(msg)))
			}
			now := time.Now().Unix()
			for _, c := range []struct {
				what string
				body map[string]any
			}{
				{"no signature", map[string]any{"device_id": deviceID, "exchange": true}},
				{"the signature of another key", map[string]any{"device_id": deviceID, "exchange": true, "ts": now, "signature": sign(otherKey, now)}},
				{"a time 10 minutes ago", map[string]any{"device_id": deviceID, "exchange": true, "ts": now - 600, "signature": sign(deviceKey, now-600)}},
			} {
				st, b := s.API(t, http.MethodPost, "/api/publish/challenge", c.body, nil)
				wantAPIStatus(t, "a token exchange nonce with "+c.what, st, b, http.StatusUnauthorized)
			}
			var signed struct {
				Nonce string `json:"nonce"`
			}
			s.MustAPI(t, http.MethodPost, "/api/publish/challenge", map[string]any{"device_id": deviceID, "exchange": true, "ts": now, "signature": sign(deviceKey, now)}, nil, &signed)
			if signed.Nonce == "" {
				t.Fatal("a token exchange nonce with the device signature: empty nonce")
			}
			var kept struct {
				Nonce string `json:"nonce"`
			}
			s.MustAPI(t, http.MethodPost, "/api/publish/challenge", map[string]any{"device_id": deviceID, "exchange": true, "nonce": signed.Nonce}, nil, &kept)
			if kept.Nonce != signed.Nonce {
				t.Fatalf("a kept token exchange nonce without a signature: answer %q, want the kept nonce", kept.Nonce)
			}

			// One process that runs "get" twice gets the same proof, so the token requests of one
			// push get one publish session. Another process gets another proof.
			out = d.MustExec(t, "get twice", fmt.Sprintf("set -e\necho %[1]s | %[2]s get > /get1\necho %[1]s | %[2]s get > /get2\ncat /get1 /get2\n", host, helperBin))
			var twice []string
			for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
				var c struct{ Secret string }
				if err := json.Unmarshal([]byte(line), &c); err != nil || c.Secret == "" {
					t.Fatalf("get twice: %s: %v", out, err)
				}
				twice = append(twice, c.Secret)
			}
			if len(twice) != 2 || twice[0] != twice[1] {
				t.Fatalf("get twice in one process: %q, want the same secret twice", twice)
			}
			if twice[0] == cred.Secret {
				t.Fatalf("get in another process: secret %q, want another nonce than the first process", cred.Secret)
			}
			dir := "/root/.telesphorepo/" + strings.ReplaceAll(host, ":", "_") + "/docker"
			if mode := strings.TrimSpace(d.MustExec(t, "read the mode of exchange.json", "stat -c %a "+dir+"/exchange.json")); mode != "600" {
				t.Fatalf("mode of exchange.json %s, want 600", mode)
			}

			var hosts map[string]string
			out = d.MustExec(t, "list", "echo unused | "+helperBin+" list")
			if err := json.Unmarshal([]byte(out), &hosts); err != nil || hosts[host] != "telesphorepo-device" {
				t.Fatalf("list: %s: want %s", out, host)
			}
			code, out = d.Exec(t, "echo '{}' | "+helperBin+" store")
			if code != 1 || !strings.Contains(out, "keeps no passwords") {
				t.Fatalf("store: exit %d: %s", code, out)
			}
		})

		t.Run("a revoked device", func(t *testing.T) {
			st, b := s.API(t, http.MethodDelete, fmt.Sprintf("/api/devices/%d", deviceID), nil, s.LoginTOTP(t, "builder"))
			wantAPIStatus(t, "revoke the device", st, b, http.StatusNoContent)
			code, out := d.Exec(t, "echo "+host+" | "+helperBin+" get")
			if code != 1 || !strings.Contains(out, "credentials not found in native keychain") {
				t.Fatalf("get after the revoke: exit %d: %s", code, out)
			}
			code, out = d.Exec(t, fmt.Sprintf("docker tag %[1]s:v1 %[1]s:v2 && docker push %[1]s:v2 2>&1", img))
			if code == 0 {
				t.Fatalf("push after the revoke: exit 0, want a refusal:\n%s", out)
			}
			d.MustExec(t, "anonymous pull after the revoke", fmt.Sprintf("set -e\ndocker pull %[1]s/%[2]s/base:v1\n", host, pub))
		})
	})
}
