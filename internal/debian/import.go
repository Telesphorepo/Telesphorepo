package debian

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/debian/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/signing"
)

// importComponent is the component that imported packages go into.
const importComponent = "main"

// importTarget takes the pool/**/*.deb files of a Nexus apt repo (format.Importer Target); every
// other file (dists/ indexes, keys) is ignored: the rebuild builds and signs the indexes. The
// package gets this server's pool path, so the target of a file whose bytes are in the import
// distribution already is that pool file (the runner skips it); any other target is the source
// path, which importStore does not use.
func (h *Handler) importTarget(ctx context.Context, repo *repos.Repo, it format.ImportItem) (repopath.Path, bool, error) {
	if !strings.HasPrefix(it.Path, "pool/") || !strings.HasSuffix(it.Path, ".deb") {
		return "", false, nil
	}
	if it.SHA256 != "" {
		dist, err := importDistribution(repo)
		if err != nil {
			return "", false, err
		}
		pool, err := h.q.GetPackageFileBySHA256(ctx, dbq.GetPackageFileBySHA256Params{RepoID: repo.ID,
			Distribution: dist, Component: importComponent, Sha256: it.SHA256})
		switch {
		case err == nil:
			return repopath.Path(pool), true, nil
		case !errors.Is(err, sql.ErrNoRows):
			return "", false, err
		}
	}
	return format.SourcePath(ctx, repo, it)
}

// importStore ingests a downloaded .deb into the distribution of the repo config and component
// main, with the source's dates (format.Importer Store).
func (h *Handler) importStore(ctx context.Context, repo *repos.Repo, it format.ImportItem, sp *assets.Spooled) (*assets.Result, error) {
	dist, err := importDistribution(repo)
	if err != nil {
		return nil, err
	}
	res, _, err := h.ingest(ctx, repo, dist, importComponent, sp, it.Origin())
	return res, err
}

// The answers of the signing_key question of a new hosted target, besides an existing key name.
const (
	keyNew    = "new"    // a new key named after the target repo
	keySource = "source" // the key pair of the source repo, opened with signing_passphrase
)

// sourceAttributes are the settings of a Nexus apt repo that the plan reads.
type sourceAttributes struct {
	Apt struct {
		Distribution string `json:"distribution"`
	} `json:"apt"`
	AptSigning struct {
		Keypair string `json:"keypair"`
	} `json:"aptSigning"`
}

// importPlan plans the target of a Nexus apt repo (format.Importer Plan). The target's config
// takes the source's distribution: a new target gets the default config with it, an existing
// hosted target without a distribution gets it added. A new hosted target asks for its signing
// key: an existing key (default "default"), a new key named after the repo, or the key pair of
// the source (Nexus gives the pair but never its passphrase, so an encrypted pair needs
// signing_passphrase). The source key is imported as "nexus-<target>" before the save.
func (h *Handler) importPlan(ctx context.Context, src format.SourceRepo, target *repos.Repo, answers map[string]string) (*format.TargetPlan, error) {
	var attrs sourceAttributes
	if len(src.Attributes) > 0 {
		_ = json.Unmarshal(src.Attributes, &attrs) // a source without these settings gives none
	}
	dist := attrs.Apt.Distribution
	if dist != "" && !repos.NameRE.MatchString(dist) {
		dist = ""
	}
	keypair := strings.TrimSpace(attrs.AptSigning.Keypair)
	encrypted := keypair != "" && signing.Encrypted([]byte(keypair))
	plan := &format.TargetPlan{Facts: map[string]any{"distribution": dist, "key_in_source": keypair != "", "key_encrypted": encrypted}}
	if keypair == "" && src.Kind == repos.KindHosted {
		// "note" is the one fact that the UI shows in words, before a save creates the target.
		plan.Facts["note"] = "Nexus does not give the signing key of this repository over its API, so the new repository gets the key you choose here. " +
			"To keep the Nexus key, export the key pair in Nexus (Repository > Repositories > the apt repository > APT Settings > Signing Key), " +
			"import it in Signing keys and choose it on the Signing tab of the new repository."
	}

	if target != nil && target.ID != 0 {
		// An existing target keeps its key. A hosted target without a distribution takes the
		// source's, so that imported packages have a distribution; a distribution it has stays.
		if target.Kind == repos.KindHosted && dist != "" && importDistributionOf(target) == "" {
			cfg, err := withDistribution(target.Config, dist)
			if err != nil {
				return nil, err
			}
			plan.Config = cfg
		}
		return plan, nil
	}
	if src.Kind == repos.KindGroup {
		return plan, nil
	}
	cfg := defaultConfig
	if dist != "" {
		var err error
		if cfg, err = withDistribution(defaultConfig, dist); err != nil {
			return nil, err
		}
	}
	plan.Config = cfg
	if src.Kind != repos.KindHosted {
		return plan, nil
	}
	plan.Questions = h.keyQuestions(keypair != "", encrypted)
	if answers == nil || target == nil {
		return plan, nil
	}
	name, prepare, err := h.planKey(target.Name, answers, keypair)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(name)
	plan.Fields = map[string]json.RawMessage{signing.Field: raw}
	plan.Prepare = prepare
	return plan, nil
}

// keyQuestions are the questions about the signing key of a new hosted target. When Nexus gives
// the key pair, the answer starts at that key, so apt clients that trust the Nexus key keep
// trusting the repository after the switch-over.
func (h *Handler) keyQuestions(inSource, encrypted bool) []format.Question {
	def := signing.DefaultKey
	if inSource {
		def = keySource
	}
	key := format.Question{Key: "signing_key", Label: "Signing key", Type: "choice", Required: true, Default: def,
		Help: "The key that signs the indexes of the new repository. apt clients trust the key they have."}
	for _, k := range h.keys.List() {
		key.Options = append(key.Options, format.Option{Value: k.Name, Label: "Signing key " + k.Name})
	}
	key.Options = append(key.Options, format.Option{Value: keyNew, Label: "A new key for this repository"})
	if inSource {
		key.Options = append(key.Options, format.Option{Value: keySource, Label: "The key of the Nexus repository, so apt clients keep their trust"})
	}
	out := []format.Question{key}
	if encrypted {
		out = append(out, format.Question{Key: "signing_passphrase", Label: "Passphrase of the Nexus key", Type: "secret",
			Help:     "Nexus gives the key pair but never its passphrase.",
			ShowWhen: &format.ShowWhen{Key: "signing_key", Value: keySource}})
	}
	return out
}

// planKey returns the signing key of a new hosted target for the answer to signing_key, and the
// work that creates it before the save (nil when the key exists).
func (h *Handler) planKey(target string, answers map[string]string, keypair string) (string, func(context.Context) error, error) {
	choice := answers["signing_key"]
	if choice == "" && keypair != "" {
		choice = keySource // the default of the question
	}
	// A passphrase opens only the Nexus key: with another key it would be dropped without a word.
	if answers["signing_passphrase"] != "" && choice != keySource {
		return "", nil, fmt.Errorf("the passphrase opens the key of the Nexus repository, but %s signs with %s: choose the key of the Nexus repository, or leave the passphrase empty", target, choice)
	}
	switch choice {
	case keyNew:
		name := keyName(target)
		if h.keys.Exists(name) {
			return "", nil, fmt.Errorf("a signing key named %s exists; pick it", name)
		}
		return name, func(ctx context.Context) error {
			_, err := h.keys.Create(ctx, name)
			return err
		}, nil
	case keySource:
		if keypair == "" {
			return "", nil, errors.New("Nexus does not give the signing key pair of this repository; import it in Signing keys, then pick it")
		}
		pass := answers["signing_passphrase"]
		signer, err := signing.Parse([]byte(keypair), []byte(pass))
		if err != nil {
			return "", nil, errors.New("the passphrase does not open the Nexus signing key")
		}
		name := keyName("nexus-" + target)
		if k, ok := h.keys.Get(name); ok {
			if k.Fingerprint != signer.Fingerprint() {
				return "", nil, fmt.Errorf("a different signing key named %s exists; delete it or rename the repository", name)
			}
			return name, nil, nil
		}
		return name, func(ctx context.Context) error {
			if _, err := h.keys.Import(ctx, name, keypair, pass); err != nil {
				return fmt.Errorf("import the Nexus signing key: %w", err)
			}
			h.log.Info("imported the signing key of a Nexus repository", "key", name, "repo", target)
			return nil
		}, nil
	default:
		name := choice
		if name == "" {
			name = signing.DefaultKey
		}
		if !h.keys.Exists(name) {
			return "", nil, fmt.Errorf("signing key %q does not exist", name)
		}
		return name, nil, nil
	}
}

// keyName cuts a key name to the 64 characters of a name.
func keyName(s string) string {
	if len(s) > 64 {
		return s[:64]
	}
	return s
}

// withDistribution returns cfg with "distribution" set to dist.
func withDistribution(cfg json.RawMessage, dist string) (json.RawMessage, error) {
	obj := map[string]json.RawMessage{}
	if len(cfg) > 0 {
		if err := json.Unmarshal(cfg, &obj); err != nil {
			return nil, err
		}
	}
	if obj == nil {
		obj = map[string]json.RawMessage{}
	}
	obj["distribution"], _ = json.Marshal(dist)
	return json.Marshal(obj)
}
