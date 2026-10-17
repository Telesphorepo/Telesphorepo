//go:build clients

package e2e

import (
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"
)

var (
	// installDebianImage is Debian with curl.
	installDebianImage = floatingImage("docker.io/library/buildpack-deps:curl")
	// installAlpineImage is Alpine with BusyBox only: the script downloads with wget.
	installAlpineImage = floatingImage("docker.io/library/alpine:latest")
)

// newestPowerShell returns the URL of the newest PowerShell release for Linux on the CPU of the
// test containers: the archive of the GitHub release that /releases/latest names. Microsoft's
// container images of PowerShell no longer follow its releases.
func newestPowerShell(t testing.TB) string {
	t.Helper()
	c := &http.Client{Timeout: time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Head("https://github.com/PowerShell/PowerShell/releases/latest")
	if err != nil {
		t.Fatalf("the newest PowerShell release: %v", err)
	}
	resp.Body.Close()
	_, tag, ok := strings.Cut(resp.Header.Get("Location"), "/releases/tag/")
	if !ok || !strings.HasPrefix(tag, "v") {
		t.Fatalf("the newest PowerShell release: %d, Location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	v := strings.TrimPrefix(tag, "v")
	t.Logf("PowerShell %s", v)
	return "https://github.com/PowerShell/PowerShell/releases/download/" + tag + "/powershell-" + v + "-linux-" + arch + ".tar.gz"
}

// installShScript installs the CLI with install.sh of $SERVER (fetch is the download command),
// runs "version" through it, and then runs install.sh with another SHA-256 for each archive.
const installShScript = `set -e
export GITHUB_PATH=/tmp/github_path
echo '=== install ==='
%[1]s "$SERVER/install.sh" | sh -s -- version
echo '=== helper ==='
ls -l "$HOME/.local/bin"
"$HOME/.local/bin/docker-credential-telesphorepo" version
cat /tmp/github_path
echo '=== wrong hash ==='
rm -rf "$HOME/.local/bin"
if %[1]s "$SERVER/install.sh" | sed "s/echo '[0-9a-f]\{64\}'/echo '%[2]s'/" | sh -s -- version; then echo 'wrong hash: installed'; else echo "wrong hash: exit $?"; fi
if [ -e "$HOME/.local/bin/telesphorepo" ]; then echo 'wrong hash: binary present'; else echo 'wrong hash: no binary'; fi
echo '=== end ==='
`

// installPs1Script is installShScript for PowerShell with install.ps1.
const installPs1Script = `$ErrorActionPreference = 'Stop'
$env:GITHUB_PATH = '/tmp/github_path'
Write-Output '=== install ==='
& ([scriptblock]::Create((Invoke-RestMethod "$env:SERVER/install.ps1"))) version
Write-Output '=== helper ==='
$helper = Join-Path $HOME '.local/bin/docker-credential-telesphorepo'
Write-Output "link: $((Get-Item $helper).LinkType) $((Get-Item $helper).Target)"
& $helper version
Get-Content /tmp/github_path
Write-Output '=== wrong hash ==='
Remove-Item -Recurse -Force (Join-Path $HOME '.local/bin')
$script = (Invoke-RestMethod "$env:SERVER/install.ps1") -replace "= '[0-9a-f]{64}'", "= '%[1]s'"
try {
	& ([scriptblock]::Create($script)) version
	Write-Output 'wrong hash: installed'
} catch {
	Write-Output "wrong hash: $($_.Exception.Message)"
}
if (Test-Path (Join-Path $HOME '.local/bin/telesphorepo')) { Write-Output 'wrong hash: binary present' } else { Write-Output 'wrong hash: no binary' }
Write-Output '=== end ==='
`

// TestInstall: install.sh (Debian with curl, Alpine with wget) and install.ps1 (PowerShell on
// Linux) install the CLI of the server into ~/.local/bin, make the helper name next to it, add
// the folder to $GITHUB_PATH, pass the arguments to the CLI, and install nothing when the hash
// of the archive is not the one of the script.
func TestInstall(t *testing.T) {
	t.Parallel()
	zeros := strings.Repeat("0", 64)
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		installed := "telesphorepo: installed the CLI of " + s.InternalURL + " in /root/.local/bin"
		// PowerShell runs from its release archive on Debian, without ICU (invariant globalization).
		pwsh := `mkdir -p /opt/pwsh && curl -fsSL "$PWSH_URL" | tar -xz -C /opt/pwsh && /opt/pwsh/pwsh -NoProfile -NonInteractive -File /e2e/install.ps1`
		pwshEnv := map[string]string{"PWSH_URL": newestPowerShell(t), "DOTNET_SYSTEM_GLOBALIZATION_INVARIANT": "1"}
		for _, c := range []struct {
			name, image, cmd, script string
			env                      map[string]string
		}{
			{"debian", installDebianImage, "sh /e2e/install.sh", fmt.Sprintf(installShScript, "curl -fsSL", zeros), nil},
			{"alpine", installAlpineImage, "sh /e2e/install.sh", fmt.Sprintf(installShScript, "wget -qO-", zeros), nil},
			{"powershell", installDebianImage, pwsh, fmt.Sprintf(installPs1Script, zeros), pwshEnv},
		} {
			t.Run(c.name, func(t *testing.T) {
				file := "/e2e/install.sh"
				if c.name == "powershell" {
					file = "/e2e/install.ps1"
				}
				env := map[string]string{"SERVER": s.InternalURL, "HOME": "/root"}
				for k, v := range c.env {
					env[k] = v
				}
				code, out := RunClient(t, c.image, ClientOpts{
					Cmd:   []string{c.cmd},
					Files: []ContainerFile{{Path: file, Content: c.script}},
					Env:   env,
					User:  "0",
				})
				if code != 0 {
					t.Fatalf("install: exit %d:\n%s", code, out)
				}
				wantText(t, "install", section(out, "install"), installed, "telesphorepo ")
				helper := section(out, "helper")
				wantText(t, "the helper name", helper, "docker-credential-telesphorepo ", "/root/.local/bin\n")
				if c.name == "powershell" {
					wantText(t, "the helper name", helper, "link: SymbolicLink telesphorepo")
				} else {
					wantText(t, "the helper name", helper, "docker-credential-telesphorepo -> telesphorepo")
				}
				wrong := section(out, "wrong hash")
				wantText(t, "install with a wrong hash", wrong, "but the server names "+zeros+"; nothing was installed", "wrong hash: no binary")
				wantNoText(t, "install with a wrong hash", wrong, "wrong hash: installed")
			})
		}
	})
}
