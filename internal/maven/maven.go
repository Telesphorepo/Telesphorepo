// Package maven serves hosted Maven repos at /maven/{repo}/{path...} (spec §8).
package maven

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Handler is the Maven format plugin.
type Handler struct {
	auth   *auth.Service
	assets *assets.Service
	cfg    config.Provider
}

// New returns the Maven format.
func New() format.Format { return &Handler{} }

func (h *Handler) Name() string  { return "maven" }
func (h *Handler) Title() string { return "Maven / Gradle" }

func (h *Handler) DefaultConfig() json.RawMessage { return json.RawMessage(`{}`) }

// ValidateConfig accepts only an empty JSON object: Maven repos have no options.
func (h *Handler) ValidateConfig(raw json.RawMessage) (json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, errors.New("maven config must be a JSON object")
	}
	if len(m) > 0 {
		return nil, errors.New("maven repos take no config options")
	}
	return h.DefaultConfig(), nil
}

// Usage returns the Maven and Gradle client setup for a repo.
func (h *Handler) Usage(repo *repos.Repo, baseURL string) []format.Snippet {
	url := fmt.Sprintf("%s/maven/%s", baseURL, repo.Name)
	return []format.Snippet{
		{Title: "~/.m2/settings.xml", Lang: "xml", Code: fmt.Sprintf(`<settings>
  <servers>
    <server>
      <id>%[1]s</id>
      <username>YOUR_USERNAME</username>
      <password>YOUR_TOKEN</password>
    </server>
  </servers>
</settings>`, repo.Name)},
		{Title: "pom.xml", Lang: "xml", Code: fmt.Sprintf(`<repositories>
  <repository>
    <id>%[1]s</id>
    <url>%[2]s</url>
  </repository>
</repositories>
<distributionManagement>
  <repository>
    <id>%[1]s</id>
    <url>%[2]s</url>
  </repository>
</distributionManagement>`, repo.Name, url)},
		{Title: "build.gradle.kts (publishing)", Lang: "kotlin", Code: fmt.Sprintf(`publishing {
    repositories {
        maven {
            name = "%[1]s"
            url = uri("%[2]s")
            credentials {
                username = providers.gradleProperty("telesphorepoUser").get()
                password = providers.gradleProperty("telesphorepoToken").get()
            }
        }
    }
}`, repo.Name, url)},
		{Title: "settings.gradle.kts (pluginManagement)", Lang: "kotlin", Code: fmt.Sprintf(`pluginManagement {
    repositories {
        maven {
            url = uri("%s")
            credentials {
                username = providers.gradleProperty("telesphorepoUser").get()
                password = providers.gradleProperty("telesphorepoToken").get()
            }
        }
        gradlePluginPortal()
    }
}`, url)},
	}
}

// Register mounts GET/HEAD/PUT /maven/{repo}/{path...}.
func (h *Handler) Register(mux *http.ServeMux, host format.Host) {
	h.auth, h.assets, h.cfg = host.Auth, host.Assets, host.Config
	mux.HandleFunc("GET /maven/{repo}/{path...}", h.handle)
	mux.HandleFunc("PUT /maven/{repo}/{path...}", h.handle)
}

func (h *Handler) handle(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("path")
	if !httpx.ValidRepoPath(r, p) {
		httpx.Error(w, http.StatusBadRequest, "invalid path")
		return
	}
	need := auth.Read
	if r.Method == http.MethodPut {
		need = auth.Write
	}
	repo, pr, ok := h.auth.RepoGate(w, r, r.PathValue("repo"), "maven", need, false)
	if !ok {
		return
	}
	if need == auth.Read {
		h.assets.Serve(w, r, repo, p)
		return
	}
	if r.ContentLength > h.cfg().MaxUploadBytes {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "upload too large")
		return
	}
	sp, err := h.assets.Spool(http.MaxBytesReader(w, r.Body, h.cfg().MaxUploadBytes), h.cfg().MaxUploadBytes)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.Is(err, assets.ErrTooLarge) || errors.As(err, &mbe) {
			httpx.Error(w, http.StatusRequestEntityTooLarge, "upload too large")
		} else {
			httpx.Error(w, http.StatusInternalServerError, "upload failed")
		}
		return
	}
	defer sp.Close()

	var uid int64
	if pr != nil {
		uid = pr.UserID
	}
	created, _, err := h.put(r.Context(), repo, p, sp, uid)
	switch {
	case errors.Is(err, errImmutable):
		httpx.Error(w, http.StatusConflict, errImmutable.Error())
	case errors.Is(err, errLookup):
		httpx.Error(w, http.StatusInternalServerError, "lookup failed")
	case err != nil:
		httpx.Error(w, http.StatusInternalServerError, "store failed")
	case created:
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

var (
	// errImmutable rejects a release file that exists with different bytes.
	errImmutable = errors.New("release artifacts are immutable")
	// errLookup wraps a failed lookup of the asset at the target path.
	errLookup = errors.New("lookup failed")
)

// put stores the spooled file at p under the PUT rules and holds the path lock while it does.
// A release file that exists with the same bytes stays as it is (unchanged); with different
// bytes put returns errImmutable. maven-metadata.xml* and SNAPSHOT files are replaced.
// Uploads (PUT) and imports share it.
func (h *Handler) put(ctx context.Context, repo *repos.Repo, p string, sp *assets.Spooled, uid int64) (created, unchanged bool, err error) {
	unlock := h.assets.Lock(repo.ID, p)
	defer unlock()
	existing, err := h.assets.Get(ctx, repo.ID, p)
	if err != nil && !errors.Is(err, assets.ErrNotFound) {
		return false, false, fmt.Errorf("%w: %w", errLookup, err)
	}
	if existing != nil && !mutable(p) {
		if existing.SHA256 == sp.SHA256 {
			return false, true, nil
		}
		return false, false, errImmutable
	}
	kind := assets.KindArtifact
	if strings.HasPrefix(path.Base(p), "maven-metadata.xml") {
		kind = assets.KindMetadata
	}
	_, created, err = h.assets.Put(ctx, repo, p, sp, assets.ContentType(p), kind, uid)
	return created, false, err
}

func mutable(p string) bool {
	return strings.HasPrefix(path.Base(p), "maven-metadata.xml") || strings.HasSuffix(path.Base(path.Dir(p)), "-SNAPSHOT")
}
