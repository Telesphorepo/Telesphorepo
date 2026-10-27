package rpm

import (
	"context"
	"database/sql"
	"encoding/xml"
	"errors"
	"net/http"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
)

// apiRoutes mounts the JSON routes of the web UI under /api/rpm/.
func (h *Handler) apiRoutes(r *route.Router) {
	r.Handle("GET /api/rpm/{name}/package", route.RepoRead(), h.packageDetails)
}

// packageDetails answers GET /api/rpm/{name}/package?path=: the identity of the package file path
// of a hosted repo and the facts of its index entry.
func (h *Handler) packageDetails(w http.ResponseWriter, r *http.Request, c *route.Call) {
	if c.Repo.Format != name {
		httpx.Error(w, r, http.StatusNotFound, "not an RPM repository")
		return
	}
	p, err := repopath.Parse(strings.TrimPrefix(r.URL.Query().Get("path"), "/"))
	if err != nil || !strings.HasSuffix(string(p), ".rpm") {
		httpx.Error(w, r, http.StatusBadRequest, "path must be a package file of the repository")
		return
	}
	d, err := h.detailsOf(r.Context(), c.Repo, string(p))
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	if d == nil {
		httpx.Error(w, r, http.StatusNotFound, "not found")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, d)
}

// packageDetails is the answer of GET /api/rpm/{name}/package.
type packageDetails struct {
	Name          string              `json:"name"`
	Version       string              `json:"version"`
	Architecture  string              `json:"architecture"`
	Summary       string              `json:"summary"`
	InstalledSize int64               `json:"installed_size"`
	BuildTime     int64               `json:"build_time"`
	Format        int64               `json:"rpm_format"`
	Fields        map[string]string   `json:"fields"`       // description, url, license, vendor, group, packager, sourcerpm
	Dependencies  map[string][]string `json:"dependencies"` // requires, provides, ...: "name OP [epoch:]version-release"
}

// detailsOf returns the details of the package file p of repo, or nil when p is none.
func (h *Handler) detailsOf(ctx context.Context, repo *repos.Repo, p string) (*packageDetails, error) {
	a, err := h.assets.Get(ctx, repo.ID, p)
	if errors.Is(err, assets.ErrNotFound) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	row, err := h.q.GetPackageOfAsset(ctx, a.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	pt, err := h.q.GetParts(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	d := &packageDetails{Name: row.Name, Version: row.Evr, Architecture: row.Arch, Summary: row.Summary,
		InstalledSize: row.InstalledSize, BuildTime: row.BuildTime, Format: row.Format,
		Fields: map[string]string{}, Dependencies: map[string][]string{}}
	var doc struct {
		Description string `xml:"description"`
		URL         string `xml:"url"`
		Packager    string `xml:"packager"`
		License     string `xml:"license"`
		Vendor      string `xml:"vendor"`
		Group       string `xml:"group"`
		SourceRPM   string `xml:"sourcerpm"`
		Deps        []struct {
			XMLName xml.Name
			Entries []struct {
				Name  string `xml:"name,attr"`
				Flags string `xml:"flags,attr"`
				Epoch string `xml:"epoch,attr"`
				Ver   string `xml:"ver,attr"`
				Rel   string `xml:"rel,attr"`
			} `xml:"entry"`
		} `xml:",any"`
	}
	src := `<x xmlns:rpm="` + nsRPM + `">` + pt.Text + pt.FormatA + pt.FormatB + `</x>`
	if err := xml.Unmarshal([]byte(src), &doc); err != nil {
		return nil, err
	}
	for k, v := range map[string]string{"description": doc.Description, "url": doc.URL, "packager": doc.Packager,
		"license": doc.License, "vendor": doc.Vendor, "group": doc.Group, "sourcerpm": doc.SourceRPM} {
		if v != "" {
			d.Fields[k] = v
		}
	}
	ops := map[string]string{"EQ": "=", "LT": "<", "GT": ">", "LE": "<=", "GE": ">="}
	for _, dep := range doc.Deps {
		kind := dep.XMLName.Local
		if len(dep.Entries) == 0 {
			continue
		}
		for _, e := range dep.Entries {
			s := e.Name
			if op := ops[e.Flags]; op != "" {
				v := e.Ver
				if e.Epoch != "" && e.Epoch != "0" {
					v = e.Epoch + ":" + v
				}
				if e.Rel != "" {
					v += "-" + e.Rel
				}
				s += " " + op + " " + v
			}
			d.Dependencies[kind] = append(d.Dependencies[kind], s)
		}
	}
	return d, nil
}
