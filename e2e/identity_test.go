//go:build e2e

package e2e

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strings"
	"testing"
)

type identity struct {
	Name       string  `json:"name"`
	Logo       *string `json:"logo"`
	Customized bool    `json:"customized"`
}

func getIdentity(t *testing.T, s *Server) identity {
	t.Helper()
	var id identity
	s.MustAPI(t, http.MethodGet, "/api/identity", nil, nil, &id) // no credential: the route is public
	return id
}

// pngOf encodes a w × h PNG.
func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		img.Set(x, 0, color.NRGBA{R: 0xf5, G: 0xc5, B: 0x18, A: 0xff})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// TestIdentity: the identity is public and starts at its default; an admin sets the name through
// the settings, uploads a PNG that everyone fetches with its type and ETag, and removes it again.
// The server refuses an SVG, an oversize file and an image too large on its long side, and a
// person without settings.general cannot change the logo.
func TestIdentity(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})

	def := getIdentity(t, s)
	if def.Name == "" || def.Logo != nil || def.Customized {
		t.Fatalf("default identity %+v: want a name, no logo, not customized", def)
	}
	if st, b := s.API(t, http.MethodGet, "/identity/logo", nil, nil); st != http.StatusNotFound {
		t.Fatalf("GET /identity/logo without a logo: status %d, want 404: %s", st, b)
	}

	// The name is a setting of the general group.
	name := RandName("Acme packages")
	s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"server_name": "  " + name + " "}, s.Admin(), nil)
	if id := getIdentity(t, s); id.Name != name || !id.Customized {
		t.Fatalf("identity after setting the name: %+v, want name %q, customized", id, name)
	}
	if st, b := s.API(t, http.MethodPatch, "/api/settings", map[string]any{"server_name": "a\x07b"}, s.Admin()); st != http.StatusBadRequest {
		t.Fatalf("PATCH of a name with a control character: status %d, want 400: %s", st, b)
	}

	// A PNG: everyone fetches it with its type, an ETag, nosniff, and a 304 for that ETag.
	logo := pngOf(t, 64, 32)
	var up identity
	s.MustAPI(t, http.MethodPut, "/api/identity/logo", logo, s.Admin(), &up)
	if up.Logo == nil || !strings.HasPrefix(*up.Logo, "/identity/logo") {
		t.Fatalf("PUT /api/identity/logo answered %+v, want the logo URL", up)
	}
	if id := getIdentity(t, s); id.Logo == nil || *id.Logo != *up.Logo {
		t.Fatalf("identity after the upload: %+v, want logo %s", id, *up.Logo)
	}
	resp, body := s.Do(t, s.NewRequest(t, http.MethodGet, *up.Logo, nil), nil)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(body, logo) {
		t.Fatalf("GET %s: status %d, %d bytes, want 200 and the %d bytes uploaded", *up.Logo, resp.StatusCode, len(body), len(logo))
	}
	etag := resp.Header.Get("ETag")
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("logo Content-Type %q, want image/png", ct)
	}
	if ns := resp.Header.Get("X-Content-Type-Options"); ns != "nosniff" {
		t.Errorf("logo X-Content-Type-Options %q, want nosniff", ns)
	}
	if etag == "" || resp.Header.Get("Cache-Control") == "" {
		t.Errorf("logo ETag %q, Cache-Control %q: want both", etag, resp.Header.Get("Cache-Control"))
	}
	req := s.NewRequest(t, http.MethodGet, "/identity/logo", nil)
	req.Header.Set("If-None-Match", etag)
	if resp, _ := s.Do(t, req, nil); resp.StatusCode != http.StatusNotModified {
		t.Errorf("GET /identity/logo with its ETag: status %d, want 304", resp.StatusCode)
	}

	// GET /api/settings says that the logo is set, never its bytes.
	items, raw := listSettings(t, s)
	var set struct {
		Set bool `json:"set"`
	}
	if err := json.Unmarshal(items["server_logo"].Value, &set); err != nil || !set.Set {
		t.Errorf("server_logo in GET /api/settings: %s, want {set: true}", items["server_logo"].Value)
	}
	if strings.Contains(raw, base64.StdEncoding.EncodeToString(logo)) {
		t.Error("GET /api/settings holds the bytes of the logo")
	}

	// Refusals: an SVG (the type comes from the bytes, whatever the request says), a file far over
	// the limit, an image too large on its long side, a person without settings.general.
	svg := `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`
	req = s.NewRequest(t, http.MethodPut, "/api/identity/logo", strings.NewReader(svg))
	req.Header.Set("Content-Type", "image/png")
	if resp, b := s.Do(t, req, s.Admin()); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("PUT of an SVG: status %d, want 400: %s", resp.StatusCode, b)
	}
	big := make([]byte, 4<<20)
	rand.Read(big)
	copy(big, logo[:16]) // a PNG signature, so only the size refuses it
	if st, b := s.API(t, http.MethodPut, "/api/identity/logo", big, s.Admin()); st != http.StatusRequestEntityTooLarge {
		t.Errorf("PUT of 4 MiB: status %d, want 413: %s", st, b)
	}
	if st, b := s.API(t, http.MethodPut, "/api/identity/logo", pngOf(t, 4096, 1), s.Admin()); st != http.StatusBadRequest {
		t.Errorf("PUT of a 4096 × 1 PNG: status %d, want 400: %s", st, b)
	}
	s.CreateUser(t, "plain", "plainpass123")
	plain := s.SignIn(t, "plain", "plainpass123")
	if st, b := s.API(t, http.MethodPut, "/api/identity/logo", pngOf(t, 8, 8), plain); st != http.StatusForbidden {
		t.Errorf("PUT by a person without settings.general: status %d, want 403: %s", st, b)
	}
	if st, b := s.API(t, http.MethodDelete, "/api/identity/logo", nil, plain); st != http.StatusForbidden {
		t.Errorf("DELETE by a person without settings.general: status %d, want 403: %s", st, b)
	}
	if id := getIdentity(t, s); id.Logo == nil || *id.Logo != *up.Logo {
		t.Fatalf("identity after the refusals: %+v, want the logo %s still", id, *up.Logo)
	}

	// Removing the logo and the name gives the default identity again.
	s.MustAPI(t, http.MethodDelete, "/api/identity/logo", nil, s.Admin(), nil)
	s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"server_name": nil}, s.Admin(), nil)
	if id := getIdentity(t, s); id != def {
		t.Fatalf("identity after removing the logo and the name: %+v, want %+v", id, def)
	}
	if st, _ := s.API(t, http.MethodGet, "/identity/logo", nil, nil); st != http.StatusNotFound {
		t.Errorf("GET /identity/logo after the removal: status %d, want 404", st)
	}
}
