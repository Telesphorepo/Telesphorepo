package npm

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/version"
)

// maxPackageJSON is the largest package.json that the server reads from a tarball.
const maxPackageJSON = 16 << 20

// badTarball is a tarball that is not an npm package: the message for the person.
type badTarball struct{ msg string }

func (b badTarball) Error() string { return b.msg }

// readPackageJSON returns the package.json of an npm tarball: the file package.json in the one top
// folder of the gzipped tar (npm pack names it "package"). A tarball that ends early is a
// badTarball too: a plan reads only the first bytes.
func readPackageJSON(r io.Reader) ([]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, badTarball{"the file is not a gzipped tarball"}
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, badTarball{"the tarball has no package/package.json"}
		}
		if err != nil {
			return nil, badTarball{"the tarball is damaged or cut short"}
		}
		name := strings.TrimPrefix(h.Name, "./")
		dir, file, ok := strings.Cut(name, "/")
		if !ok || dir == "" || file != "package.json" || h.Typeflag != tar.TypeReg {
			continue
		}
		if h.Size > maxPackageJSON {
			return nil, badTarball{"package.json is too large"}
		}
		b, err := io.ReadAll(io.LimitReader(tr, maxPackageJSON))
		if err != nil {
			return nil, badTarball{"the tarball is damaged or cut short"}
		}
		return b, nil
	}
}

// manifestOf reads a package.json of a tarball (an upload or an import, which no npm client
// normalised): the name, the version cleaned as npm cleans it, and the manifest of the version.
func manifestOf(pkgJSON []byte) (name, ver string, manifest map[string]json.RawMessage, err error) {
	if err := json.Unmarshal(pkgJSON, &manifest); err != nil || manifest == nil {
		return "", "", nil, badTarball{"package.json is not a JSON object"}
	}
	var raw struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	json.Unmarshal(pkgJSON, &raw)
	if err := checkNewName(raw.Name); err != nil {
		return "", "", nil, badTarball{fmt.Sprintf("package.json: %q: %v", raw.Name, err)}
	}
	v, err := version.CleanSemVer(raw.Version)
	if err != nil {
		return "", "", nil, badTarball{fmt.Sprintf("package.json: the version %q is not a semantic version", raw.Version)}
	}
	v.Build = "" // npm drops build metadata
	ver = v.String()
	normalize(manifest, raw.Name, ver)
	return raw.Name, ver, manifest, nil
}

// normalize makes a version manifest what the registry serves: the name and version, no dist (the
// server writes it), bin as an object, and hasInstallScript when an install script runs.
func normalize(m map[string]json.RawMessage, name, ver string) {
	delete(m, "dist")
	delete(m, "_attachments")
	m["name"], _ = json.Marshal(name)
	m["version"], _ = json.Marshal(ver)
	m["_id"], _ = json.Marshal(name + "@" + ver)
	var bin string
	if b, ok := m["bin"]; ok && json.Unmarshal(b, &bin) == nil {
		m["bin"], _ = json.Marshal(map[string]string{unscoped(name): bin})
	}
	if _, ok := m["hasInstallScript"]; !ok {
		var scripts map[string]json.RawMessage
		if json.Unmarshal(m["scripts"], &scripts) == nil {
			for _, s := range []string{"preinstall", "install", "postinstall"} {
				if _, ok := scripts[s]; ok {
					m["hasInstallScript"] = json.RawMessage("true")
					break
				}
			}
		}
	}
}

// integrityOf returns the npm integrity of the bytes of sp: "sha512-<base64>".
func integrityOf(sp *assets.Spooled) (string, error) {
	r, err := sp.Reader()
	if err != nil {
		return "", err
	}
	h := sha512.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return "sha512-" + base64.StdEncoding.EncodeToString(h.Sum(nil)), nil
}
