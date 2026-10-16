package docker

import (
	"bytes"
	"encoding/json"
	"mime"
	"strings"
)

// Manifest media types.
const (
	mtOCIManifest    = "application/vnd.oci.image.manifest.v1+json"
	mtOCIIndex       = "application/vnd.oci.image.index.v1+json"
	mtDockerManifest = "application/vnd.docker.distribution.manifest.v2+json"
	mtDockerList     = "application/vnd.docker.distribution.manifest.list.v2+json"
	mtOCIEmpty       = "application/vnd.oci.empty.v1+json"
)

// schema1Types are the Docker schema 1 manifest types, which the registry refuses.
var schema1Types = map[string]bool{
	"application/vnd.docker.distribution.manifest.v1+json":      true,
	"application/vnd.docker.distribution.manifest.v1+prettyjws": true,
}

// isIndex reports whether a manifest media type is an index (a list of manifests).
func isIndex(mt string) bool { return mt == mtOCIIndex || mt == mtDockerList }

// maxManifest is the largest manifest the registry takes.
const maxManifest = 4 << 20

// Platform is the platform of a manifest in an index.
type Platform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant,omitempty"`
}

// Descriptor is an OCI content descriptor.
type Descriptor struct {
	MediaType    string            `json:"mediaType"`
	Digest       string            `json:"digest"`
	Size         int64             `json:"size"`
	URLs         []string          `json:"urls,omitempty"`
	Platform     *Platform         `json:"platform,omitempty"`
	ArtifactType string            `json:"artifactType,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`

	hex string // the hex of Digest, set by ParseManifest
}

// distributable reports whether clients push the blob of d: a foreign or non-distributable layer
// (with urls) stays outside the registry.
func (d Descriptor) distributable() bool {
	return len(d.URLs) == 0 && !strings.Contains(d.MediaType, "foreign") && !strings.Contains(d.MediaType, "nondistributable")
}

// Manifest is a parsed image manifest or index.
type Manifest struct {
	MediaType    string
	ArtifactType string
	Config       *Descriptor
	Layers       []Descriptor
	Manifests    []Descriptor
	Subject      *Descriptor
	Annotations  map[string]string
	Size         int64 // bytes of the manifest
}

// child is an object that a manifest references: a blob (config, layer) or a manifest (a child
// of an index).
type child struct {
	hex  string
	kind string // "blob" or "manifest"
	size int64
}

// Children returns the distinct objects that m references and that the registry stores: the
// config and the distributable layers of an image manifest, the manifests of an index. A blob
// named twice counts once.
func (m *Manifest) Children() []child {
	var out []child
	seen := map[string]bool{}
	add := func(d Descriptor, kind string) {
		if seen[kind+d.hex] {
			return
		}
		seen[kind+d.hex] = true
		out = append(out, child{d.hex, kind, d.Size})
	}
	if m.Config != nil {
		add(*m.Config, "blob")
	}
	for _, l := range m.Layers {
		if l.distributable() {
			add(l, "blob")
		}
	}
	for _, c := range m.Manifests {
		add(c, "manifest")
	}
	return out
}

// TotalSize is the manifest with its config and layers; an index counts only itself.
func (m *Manifest) TotalSize() int64 {
	n := m.Size
	if m.Config != nil {
		n += m.Config.Size
	}
	for _, l := range m.Layers {
		n += l.Size
	}
	return n
}

// Kind of artifact: artifactType, else the config media type unless it is the OCI empty config.
func (m *Manifest) artifactType() string {
	if m.ArtifactType != "" {
		return m.ArtifactType
	}
	if m.Config != nil && m.Config.MediaType != mtOCIEmpty {
		return m.Config.MediaType
	}
	return ""
}

func (m *Manifest) configType() string {
	if m.Config == nil {
		return ""
	}
	return m.Config.MediaType
}

// platforms is the JSON [{digest, os, architecture, variant}] of an index, "[]" otherwise.
func (m *Manifest) platforms() string {
	type item struct {
		Digest       string `json:"digest"`
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		Variant      string `json:"variant,omitempty"`
	}
	out := []item{}
	for _, c := range m.Manifests {
		it := item{Digest: c.Digest}
		if c.Platform != nil {
			it.OS, it.Architecture, it.Variant = c.Platform.OS, c.Platform.Architecture, c.Platform.Variant
		}
		out = append(out, it)
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func (m *Manifest) annotations() string {
	if len(m.Annotations) == 0 {
		return "{}"
	}
	b, _ := json.Marshal(m.Annotations)
	return string(b)
}

// ParseManifest parses body, sent with the Content-Type contentType ("" when unknown). The
// media type is the content type, else the JSON mediaType; both must agree when both are set.
func ParseManifest(contentType string, body []byte) (*Manifest, error) {
	ct := ""
	if contentType != "" {
		if mt, _, err := mime.ParseMediaType(contentType); err == nil {
			ct = mt
		}
	}
	var raw struct {
		SchemaVersion int               `json:"schemaVersion"`
		MediaType     string            `json:"mediaType"`
		ArtifactType  string            `json:"artifactType"`
		Config        *Descriptor       `json:"config"`
		Layers        []Descriptor      `json:"layers"`
		Manifests     []Descriptor      `json:"manifests"`
		Subject       *Descriptor       `json:"subject"`
		Annotations   map[string]string `json:"annotations"`
	}
	if schema1Types[ct] {
		return nil, manifestInvalid("Docker schema 1 manifests are not supported")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&raw); err != nil {
		return nil, manifestInvalid("the manifest is not valid JSON: " + err.Error())
	}
	if schema1Types[raw.MediaType] || raw.SchemaVersion == 1 {
		return nil, manifestInvalid("Docker schema 1 manifests are not supported")
	}
	if raw.SchemaVersion != 2 {
		return nil, manifestInvalid("the manifest needs schemaVersion 2")
	}
	mt := ct
	switch {
	case mt == "" || mt == "application/json" || mt == "application/octet-stream":
		mt = raw.MediaType
	case raw.MediaType != "" && raw.MediaType != mt:
		return nil, manifestInvalid("the mediaType of the manifest is " + raw.MediaType + ", not the Content-Type " + mt)
	}
	if mt == "" {
		mt = mtOCIManifest
		if raw.Manifests != nil {
			mt = mtOCIIndex
		}
	}
	switch mt {
	case mtOCIManifest, mtDockerManifest:
		if raw.Config == nil {
			return nil, manifestInvalid("an image manifest needs a config")
		}
		if raw.Manifests != nil {
			return nil, manifestInvalid("an image manifest has no manifests")
		}
	case mtOCIIndex, mtDockerList:
		if raw.Config != nil || raw.Layers != nil {
			return nil, manifestInvalid("an index has no config and no layers")
		}
	default:
		return nil, manifestInvalid("unsupported manifest media type " + mt)
	}
	m := &Manifest{MediaType: mt, ArtifactType: raw.ArtifactType, Config: raw.Config, Layers: raw.Layers,
		Manifests: raw.Manifests, Subject: raw.Subject, Annotations: raw.Annotations, Size: int64(len(body))}
	check := func(d *Descriptor) error {
		hex, err := parseDigest(d.Digest)
		if err != nil {
			return err
		}
		if d.Size < 0 {
			return manifestInvalid("a descriptor has a negative size")
		}
		d.hex = hex
		return nil
	}
	if m.Config != nil {
		if err := check(m.Config); err != nil {
			return nil, err
		}
	}
	for i := range m.Layers {
		if !m.Layers[i].distributable() && !strings.HasPrefix(m.Layers[i].Digest, "sha256:") {
			continue
		}
		if err := check(&m.Layers[i]); err != nil {
			return nil, err
		}
	}
	for i := range m.Manifests {
		if err := check(&m.Manifests[i]); err != nil {
			return nil, err
		}
	}
	if m.Subject != nil {
		if err := check(m.Subject); err != nil {
			return nil, err
		}
	}
	return m, nil
}
