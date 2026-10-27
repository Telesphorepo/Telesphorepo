//go:build import

package e2e

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The Nexus import of yum repos (part of TestNexus): three packages that nfpm built in a Nexus yum
// hosted repo, one in a folder under a name that is not rpm's; the sync imports them into an RPM
// repo, which signs them with its own key; dnf installs from the target; a second sync skips all
// three (the two with rpm's names without a download).

// nexusYum runs the yum part of TestNexus against n and s.
func nexusYum(t *testing.T, n *Nexus, s *Server) {
	pkgs := buildRPMPackages(t)
	src := RandName("src-yum")
	n.mustAPI(t, http.MethodPost, "/service/rest/v1/repositories/yum/hosted", map[string]any{
		"name": src, "online": true, "storage": nexusStorage, "yum": map[string]any{"repodataDepth": 0, "deployPolicy": "STRICT"},
	}, nil)
	for file, b := range map[string][]byte{"e2e-anchor-1.0.0-1.x86_64.rpm": pkgs.anchor10, "e2e-data-1.0.0-1.noarch.rpm": pkgs.data,
		"releases/anchor_1.1.0_linux_amd64.rpm": pkgs.anchor11} {
		req, err := http.NewRequest(http.MethodPut, n.URL+"/repository/"+src+"/"+file, bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		if st, body := n.do(t, req); st < 200 || st > 299 {
			t.Fatalf("PUT %s to the Nexus yum repo: status %d: %s", file, st, body)
		}
	}
	var paths []string
	deadline := time.Now().Add(2 * time.Minute)
	for {
		paths = n.assetPaths(t, src)
		rpms := 0
		for _, p := range paths {
			if strings.HasSuffix(p, ".rpm") {
				rpms++
			}
		}
		if rpms >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the Nexus yum repo %s lists %v after 2 minutes, want the 3 packages", src, paths)
		}
		time.Sleep(2 * time.Second)
	}

	source := nexusCreateSource(t, s, n)
	if r := nexusGetSource(t, s, source.ID).repo(t, src); r.SourceFormat != "yum" || r.Type != "hosted" || !r.Supported {
		t.Fatalf("the Nexus yum repo: %+v, want a supported yum hosted repo", r)
	}
	target := RandName("nxyum")
	first := nexusSaveAndWait(t, s, source.ID, []map[string]any{{"source_repo": src, "target": target, "create": true, "anonymous_read": true}})
	if r := first.repo(t, src); r.Imported != 3 || r.Failed != 0 {
		t.Fatalf("first sync of the yum repo: %+v, want the 3 packages imported (Nexus assets %v)", r, paths)
	}
	if info := nexusRepoInfoOf(t, s, target); info.Format != "rpm" || info.Kind != "hosted" || !info.AnonymousRead {
		t.Fatalf("the rpm target: %+v", info)
	}
	s.WaitDebianIndexed(t, target)
	d := StartDnf(t, fedoraImage)
	d.Trust(t, target, s.RPMKey(t, target, nil))
	d.AddRepo(t, target, rpmBaseURL(s, target), "", "")
	out := d.MustExec(t, "dnf install from the imported repo", "dnf -y install e2e-anchor e2e-data && e2e-anchor")
	if !strings.Contains(out, "anchor 1.1.0") {
		t.Fatalf("dnf install from the imported repo:\n%s", out)
	}

	again := nexusSyncNow(t, s, source.ID)
	if r := again.repo(t, src); r.Imported != 0 || r.Failed != 0 || r.Skipped != 3 {
		t.Fatalf("second sync of the yum repo: %+v, want the 3 packages skipped", r)
	}
}
