//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

// TestSetupScripts checks that the setup scripts are public and carry the server address (the
// base_url setting) and the clients version, with every placeholder filled in.
func TestSetupScripts(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	version := clientsVersionOf(t)
	for _, tc := range []struct{ name, url, version string }{
		{"setup.sh", "URL='" + s.InternalURL + "'", "VERSION='" + version + "'"},
		{"setup.ps1", "$Url = '" + s.InternalURL + "'", "$Version = '" + version + "'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fetchSetup(t, s, tc.name)
			for _, want := range []string{tc.url, tc.version, "tags: [\"v*\"]", "actions/checkout@v7", "actions/setup-java@v6", "telesphorepo-id-token"} {
				if !strings.Contains(body, want) {
					t.Errorf("%s has no %q", tc.name, want)
				}
			}
			if strings.Contains(body, "@@") {
				t.Errorf("%s has a placeholder left", tc.name)
			}
		})
	}
}
