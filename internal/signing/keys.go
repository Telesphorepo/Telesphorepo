package signing

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/namesearch"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/signing/dbq"
)

// DefaultKey is the key the server creates on first start. A new repo of a format that binds a
// signing key gets it unless it names another key.
const DefaultKey = "default"

// Identity of the keys the server creates.
const (
	keyName  = "Telesphorepo"
	keyEmail = "telesphorepo@localhost"
)

var (
	ErrNotFound = errors.New("signing key not found")
	ErrExists   = errors.New("a signing key with this name already exists")
	ErrInvalid  = errors.New("invalid signing key")
)

// InUseError refuses to delete a key that repos use.
type InUseError struct {
	Repos []string
}

func (e *InUseError) Error() string {
	return "the signing key is used by " + strings.Join(e.Repos, ", ")
}

// Sealer encrypts the private keys at rest (settings.Service).
type Sealer interface {
	Encrypt(aad string, plaintext []byte) []byte
	Decrypt(aad string, sealed []byte) ([]byte, error)
}

// Key is a signing key as the admin API shows it. Error is set when the stored private key
// cannot be used (e.g. the master key changed); repos that use it publish unsigned indexes.
type Key struct {
	Name        string    `json:"name"`
	Fingerprint string    `json:"fingerprint"`
	PublicKey   string    `json:"public_key"` // armored
	CreatedAt   time.Time `json:"created_at"`
	Error       string    `json:"error,omitempty"`
}

type entry struct {
	id     int64
	key    Key
	signer *Signer // nil when Error is set
}

// Keys holds the signing keys and the key of each repo: the tables signing_keys and
// repo_signing_keys, and in-memory snapshots that request paths and the indexers read.
type Keys struct {
	d      *db.DB
	rq, wq *dbq.Queries
	seal   Sealer
	audit  *audit.Log
	log    *slog.Logger

	mu    sync.Mutex // serializes writers
	snap  atomic.Pointer[map[string]*entry]
	bound atomic.Pointer[map[int64]int64] // repo id -> key id
}

// NewKeys returns the key service. Load must run before it is used.
func NewKeys(d *db.DB, seal Sealer, au *audit.Log, log *slog.Logger) *Keys {
	if log == nil {
		log = slog.Default()
	}
	k := &Keys{d: d, rq: dbq.New(d.R), wq: dbq.New(d.W), seal: seal, audit: au, log: log}
	empty := map[string]*entry{}
	k.snap.Store(&empty)
	none := map[int64]int64{}
	k.bound.Store(&none)
	return k
}

func aad(id int64) string { return "signing_keys:" + strconv.FormatInt(id, 10) }

const loadPage = 500

// Load reads every key and every repo binding into memory, and creates the key "default" when
// there is no key at all (the first start).
func (k *Keys) Load(ctx context.Context) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	bound := map[int64]int64{}
	var afterRepo int64
	for {
		rows, err := k.rq.ListRepoSigningKeys(ctx, dbq.ListRepoSigningKeysParams{After: afterRepo, Limit: loadPage})
		if err != nil {
			return fmt.Errorf("load repo signing keys: %w", err)
		}
		for _, r := range rows {
			bound[r.RepoID] = r.KeyID
		}
		if len(rows) < loadPage {
			break
		}
		afterRepo = rows[len(rows)-1].RepoID
	}
	k.bound.Store(&bound)
	m := map[string]*entry{}
	after := ""
	for {
		rows, err := k.rq.ListSigningKeys(ctx, dbq.ListSigningKeysParams{After: after, Limit: loadPage})
		if err != nil {
			return fmt.Errorf("load signing keys: %w", err)
		}
		for _, r := range rows {
			m[r.Name] = k.open(r)
		}
		if len(rows) < loadPage {
			break
		}
		after = rows[len(rows)-1].Name
	}
	k.snap.Store(&m)
	if len(m) > 0 {
		return nil
	}
	priv, _, err := Generate(keyName, keyEmail)
	if err != nil {
		return fmt.Errorf("generate signing key: %w", err)
	}
	e, err := k.insertLocked(ctx, DefaultKey, priv, nil)
	if err != nil {
		return err
	}
	k.log.Info("created signing key", "name", DefaultKey, "fingerprint", e.key.Fingerprint)
	return nil
}

// open decrypts and parses one stored key.
func (k *Keys) open(r dbq.SigningKey) *entry {
	e := &entry{id: r.ID, key: Key{Name: r.Name, Fingerprint: r.Fingerprint, PublicKey: r.PublicKey, CreatedAt: time.Unix(r.CreatedAt, 0).UTC()}}
	plain, err := k.seal.Decrypt(aad(r.ID), r.PrivateKey)
	if err == nil {
		e.signer, err = Parse(plain, nil)
	}
	if err != nil {
		e.key.Error = err.Error()
		k.log.Error("signing key unusable; repos that use it publish unsigned indexes", "name", r.Name, "err", err.Error())
	}
	return e
}

// List returns every key, sorted by name.
func (k *Keys) List() []Key {
	m := *k.snap.Load()
	out := make([]Key, 0, len(m))
	for _, e := range m {
		out = append(out, e.key)
	}
	slices.SortFunc(out, func(a, b Key) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Search returns the keys whose name holds text (folded, namesearch.Text), or whose fingerprint
// holds it when it is a key ID (keyID), those whose name starts with it first, then by name
// (namesearch.Compare): the rows of one gram of signing_key_grams for each, the keys then read
// from memory.
func (k *Keys) Search(ctx context.Context, text string) ([]Key, error) {
	count := func(ctx context.Context, g string, limit int64) (int64, error) {
		return k.rq.CountSigningKeyGram(ctx, dbq.CountSigningKeyGramParams{Gram: g, Limit: limit})
	}
	var names []string
	g, ok, err := namesearch.Gram(ctx, text, count)
	if err != nil {
		return nil, err
	}
	if ok {
		if names, err = k.rq.SearchSigningKeyNames(ctx, dbq.SearchSigningKeyNamesParams{Gram: g, Text: text}); err != nil {
			return nil, err
		}
	}
	if id := keyID(text); id != "" {
		g, ok, err := namesearch.Gram(ctx, id, count)
		if err != nil {
			return nil, err
		}
		if ok {
			more, err := k.rq.SearchSigningKeyFingerprints(ctx, dbq.SearchSigningKeyFingerprintsParams{Gram: g, Text: id})
			if err != nil {
				return nil, err
			}
			names = append(names, more...)
		}
	}
	m := *k.snap.Load()
	out := make([]Key, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		if e := m[name]; e != nil && !seen[name] {
			seen[name] = true
			out = append(out, e.key)
		}
	}
	slices.SortFunc(out, func(a, b Key) int { return namesearch.Compare(text, a.Name, b.Name) })
	return out, nil
}

// keyID returns text without its spaces when it is a key ID or a fingerprint (8 hex digits or
// more, as gpg and apt print them), else "".
func keyID(text string) string {
	id := strings.ReplaceAll(text, " ", "")
	if len(id) < 8 || strings.Trim(id, "0123456789abcdef") != "" {
		return ""
	}
	return id
}

// writeGrams writes the grams of key id (signing_key_grams) in the transaction q of its create:
// those of its name, and the trigrams of its fingerprint (a key ID search reads one).
func writeGrams(ctx context.Context, q *dbq.Queries, id int64, name, fingerprint string) error {
	n, fp := namesearch.Fold(name), namesearch.Fold(fingerprint)
	grams := namesearch.Grams(n)
	for i := 0; i+namesearch.MaxGram <= len(fp); i++ {
		grams = append(grams, fp[i:i+namesearch.MaxGram])
	}
	for _, g := range grams {
		if err := q.InsertSigningKeyGram(ctx, dbq.InsertSigningKeyGramParams{Gram: g, KeyID: id, Name: n, Fingerprint: fp}); err != nil {
			return err
		}
	}
	return nil
}

// Get returns key name.
func (k *Keys) Get(name string) (Key, bool) {
	e := (*k.snap.Load())[name]
	if e == nil {
		return Key{}, false
	}
	return e.key, true
}

// Exists reports whether key name exists.
func (k *Keys) Exists(name string) bool { return (*k.snap.Load())[name] != nil }

// Signer returns the signer of key name, or nil when the key does not exist or is unusable.
func (k *Keys) Signer(name string) *Signer {
	if e := (*k.snap.Load())[name]; e != nil {
		return e.signer
	}
	return nil
}

func checkName(name string) error {
	if !repos.NameRE.MatchString(name) {
		return fmt.Errorf("%w: the name must be 1 to 64 lower-case letters, digits, '.', '_' or '-', starting with a letter or digit", ErrInvalid)
	}
	return nil
}

// Create generates a new Ed25519 key called name.
func (k *Keys) Create(ctx context.Context, name string) (Key, error) {
	if err := checkName(name); err != nil {
		return Key{}, err
	}
	priv, _, err := Generate(keyName, keyEmail)
	if err != nil {
		return Key{}, fmt.Errorf("generate signing key: %w", err)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	e, err := k.insertLocked(ctx, name, priv, nil)
	if err != nil {
		return Key{}, err
	}
	return e.key, nil
}

// Import stores an armored OpenPGP private key as name. An encrypted key is opened with
// passphrase; the stored copy has no passphrase and is sealed with the master key.
func (k *Keys) Import(ctx context.Context, name, armored, passphrase string) (Key, error) {
	if err := checkName(name); err != nil {
		return Key{}, err
	}
	armored = strings.TrimSpace(armored)
	if armored == "" {
		return Key{}, fmt.Errorf("%w: paste an armored PGP PRIVATE KEY block", ErrInvalid)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	e, err := k.insertLocked(ctx, name, []byte(armored), []byte(passphrase))
	if err != nil {
		return Key{}, err
	}
	return e.key, nil
}

// insertLocked parses armored (opened with pass), stores it, records signing.key.create and adds
// it to the snapshot. Caller holds k.mu.
func (k *Keys) insertLocked(ctx context.Context, name string, armored, pass []byte) (*entry, error) {
	if k.Exists(name) {
		return nil, ErrExists
	}
	signer, err := Parse(armored, pass)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	plain, err := signer.privateArmored()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	now := time.Now().Unix()
	e := &entry{key: Key{Name: name, Fingerprint: signer.Fingerprint(), PublicKey: string(signer.PublicKeyArmored()),
		CreatedAt: time.Unix(now, 0).UTC()}, signer: signer}
	err = k.d.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		id, err := q.CreateSigningKey(ctx, dbq.CreateSigningKeyParams{Name: name, Fingerprint: e.key.Fingerprint,
			PublicKey: e.key.PublicKey, CreatedAt: now})
		if err != nil {
			return err
		}
		e.id = id
		if err := q.SetSigningKeyPrivate(ctx, dbq.SetSigningKeyPrivateParams{PrivateKey: k.seal.Encrypt(aad(id), plain), ID: id}); err != nil {
			return err
		}
		if err := writeGrams(ctx, q, id, name, e.key.Fingerprint); err != nil {
			return err
		}
		return k.audit.RecordTx(ctx, tx, audit.Event{Action: ActionKeyCreate, Target: name,
			Detail: map[string]any{"fingerprint": e.key.Fingerprint, "imported": pass != nil}})
	})
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return nil, ErrExists
	}
	if err != nil {
		return nil, err
	}
	k.store(func(m map[string]*entry) { m[name] = e })
	return e, nil
}

// store applies change to a copy of the snapshot and publishes it. Caller holds k.mu.
func (k *Keys) store(change func(m map[string]*entry)) {
	old := *k.snap.Load()
	m := make(map[string]*entry, len(old)+1)
	for n, e := range old {
		m[n] = e
	}
	change(m)
	k.snap.Store(&m)
}

// ReposOf returns the names of the repos bound to each of the keys names, each in repo id order
// ([] for a key without repos or one that does not exist): one query for every key.
func (k *Keys) ReposOf(ctx context.Context, names []string) (map[string][]string, error) {
	m := *k.snap.Load()
	out := make(map[string][]string, len(names))
	nameOf := make(map[int64]string, len(names))
	ids := make([]int64, 0, len(names))
	for _, name := range names {
		out[name] = []string{}
		if e := m[name]; e != nil {
			nameOf[e.id] = name
			ids = append(ids, e.id)
		}
	}
	if len(ids) == 0 {
		return out, nil
	}
	js, _ := json.Marshal(ids)
	rows, err := k.rq.ListReposOfSigningKeys(ctx, string(js))
	if err != nil {
		return nil, err
	}
	slices.SortFunc(rows, func(a, b dbq.ListReposOfSigningKeysRow) int { return cmp.Compare(a.RepoID, b.RepoID) })
	for _, r := range rows {
		if name, ok := nameOf[r.KeyID]; ok {
			out[name] = append(out[name], r.Name)
		}
	}
	return out, nil
}

func usedBy(ctx context.Context, q *dbq.Queries, keyID int64) ([]string, error) {
	out := []string{}
	var after int64
	for {
		rows, err := q.ListReposOfSigningKey(ctx, dbq.ListReposOfSigningKeyParams{KeyID: keyID, After: after, Limit: loadPage})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, r.Name)
		}
		if len(rows) < loadPage {
			return out, nil
		}
		after = rows[len(rows)-1].ID
	}
}

// Delete removes key name and records signing.key.delete. A key that a repo uses is refused with
// an *InUseError.
func (k *Keys) Delete(ctx context.Context, name string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	e := (*k.snap.Load())[name]
	if e == nil {
		return ErrNotFound
	}
	// The check and the delete share the write transaction, so no repo can start to use the key
	// in between (the foreign key of repo_signing_keys refuses the delete of a bound key).
	err := k.d.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		used, err := usedBy(ctx, q, e.id)
		if err != nil {
			return err
		}
		if len(used) > 0 {
			return &InUseError{Repos: used}
		}
		if err := q.DeleteSigningKey(ctx, e.id); err != nil {
			return err
		}
		return k.audit.RecordTx(ctx, tx, audit.Event{Action: ActionKeyDelete, Target: name, Detail: map[string]any{"fingerprint": e.key.Fingerprint}})
	})
	if err != nil {
		return err
	}
	k.store(func(m map[string]*entry) { delete(m, name) })
	return nil
}

// nameOf returns the name of key id ("" when it does not exist).
func (k *Keys) nameOf(id int64) string {
	for name, e := range *k.snap.Load() {
		if e.id == id {
			return name
		}
	}
	return ""
}

// RepoKey returns the name of the key bound to repo id; "" when the repo has none.
func (k *Keys) RepoKey(repoID int64) string {
	id, ok := (*k.bound.Load())[repoID]
	if !ok {
		return ""
	}
	return k.nameOf(id)
}

// RepoSigner returns the signer of the key bound to repo id, or nil when the repo has no key or
// its key is unusable.
func (k *Keys) RepoSigner(repoID int64) *Signer {
	if name := k.RepoKey(repoID); name != "" {
		return k.Signer(name)
	}
	return nil
}

// bind binds repo to key name in tx and records signing.key.bind.
func (k *Keys) bind(ctx context.Context, tx *sql.Tx, repoID int64, repoName, name string) error {
	e := (*k.snap.Load())[name]
	if e == nil {
		return ErrNotFound
	}
	if err := dbq.New(tx).SetRepoSigningKey(ctx, dbq.SetRepoSigningKeyParams{RepoID: repoID, KeyID: e.id}); err != nil {
		return err
	}
	return k.audit.RecordTx(ctx, tx, audit.Event{Action: ActionKeyBind, RepoID: repoID, Repo: repoName, Target: name,
		Detail: map[string]any{"fingerprint": e.key.Fingerprint}})
}

// setBound sets the in-memory binding of repo after the commit of a bind.
func (k *Keys) setBound(repoID int64, name string) {
	e := (*k.snap.Load())[name]
	if e == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	old := *k.bound.Load()
	m := make(map[int64]int64, len(old)+1)
	for r, id := range old {
		m[r] = id
	}
	m[repoID] = e.id
	k.bound.Store(&m)
}

// forget drops the in-memory binding of a deleted repo (its row cascades with the repo).
func (k *Keys) forget(repoID int64) {
	k.mu.Lock()
	defer k.mu.Unlock()
	old := *k.bound.Load()
	if _, ok := old[repoID]; !ok {
		return
	}
	m := make(map[int64]int64, len(old))
	for r, id := range old {
		if r != repoID {
			m[r] = id
		}
	}
	k.bound.Store(&m)
}
