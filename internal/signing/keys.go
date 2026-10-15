package signing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// DefaultKey is the key the server creates on first start. A repo config without a
// "signing_key" names it.
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

// Keys holds the signing keys: the table signing_keys and an in-memory snapshot that request
// paths and the indexers read.
type Keys struct {
	d      *db.DB
	rq, wq *dbq.Queries
	seal   Sealer
	log    *slog.Logger

	mu   sync.Mutex // serializes writers
	snap atomic.Pointer[map[string]*entry]
}

// NewKeys returns the key service. Load must run before it is used.
func NewKeys(d *db.DB, seal Sealer, log *slog.Logger) *Keys {
	if log == nil {
		log = slog.Default()
	}
	k := &Keys{d: d, rq: dbq.New(d.R), wq: dbq.New(d.W), seal: seal, log: log}
	empty := map[string]*entry{}
	k.snap.Store(&empty)
	return k
}

func aad(id int64) string { return "signing_keys:" + strconv.FormatInt(id, 10) }

const loadPage = 500

// Load reads every key into memory and creates the key "default" when there is no key at all
// (the first start).
func (k *Keys) Load(ctx context.Context) error {
	k.mu.Lock()
	defer k.mu.Unlock()
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

// insertLocked parses armored (opened with pass), stores it and adds it to the snapshot.
// Caller holds k.mu.
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
		return q.SetSigningKeyPrivate(ctx, dbq.SetSigningKeyPrivateParams{PrivateKey: k.seal.Encrypt(aad(id), plain), ID: id})
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

// UsedBy returns the names of the repos whose config names key, in id order.
func (k *Keys) UsedBy(ctx context.Context, name string) ([]string, error) {
	return usedBy(ctx, k.rq, name)
}

func usedBy(ctx context.Context, q *dbq.Queries, name string) ([]string, error) {
	out := []string{}
	var after int64
	for {
		rows, err := q.ListReposUsingSigningKey(ctx, dbq.ListReposUsingSigningKeyParams{Key: name, After: after, Limit: loadPage})
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

// Delete removes key name. A key that a repo uses is refused with an *InUseError.
func (k *Keys) Delete(ctx context.Context, name string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	e := (*k.snap.Load())[name]
	if e == nil {
		return ErrNotFound
	}
	// The check and the delete share the write transaction, so no repo can start to use the key
	// in between (the repos triggers refuse a config that names a missing key).
	err := k.d.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		used, err := usedBy(ctx, q, name)
		if err != nil {
			return err
		}
		if len(used) > 0 {
			return &InUseError{Repos: used}
		}
		return q.DeleteSigningKey(ctx, e.id)
	})
	if err != nil {
		return err
	}
	k.store(func(m map[string]*entry) { delete(m, name) })
	return nil
}
