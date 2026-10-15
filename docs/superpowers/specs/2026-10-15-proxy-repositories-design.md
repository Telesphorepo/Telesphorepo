# Proxy repositories

## Goal

A proxy repository fetches files from a remote repository on first request, keeps them, and
serves them from here after that. Builds get Maven Central, a vendor's Maven repository or a
Debian mirror through this server: faster, and still working when the remote is down for
files fetched before. With groups, one URL serves your own artifacts and Maven Central.

Formats: Maven and Debian. A format joins by implementing the proxy interface below.

## Model

The project has no production data, so `0001_init.sql` changes in place. `repos.kind` holds
`'proxy'` (see the group spec). New table:

```sql
-- The remote of a proxy repo. credentials is sealed (settings.Service.Encrypt, AAD
-- "repo_proxy:<repo id>"); empty when the remote needs none.
CREATE TABLE repo_proxies (
  repo_id        INTEGER PRIMARY KEY REFERENCES repos(id) ON DELETE CASCADE,
  remote_url     TEXT NOT NULL,          -- http(s), ends with "/"
  credentials    BLOB NOT NULL DEFAULT x'',
  metadata_ttl   INTEGER NOT NULL DEFAULT 1800,  -- seconds a changing file is used before a check
  not_found_ttl  INTEGER NOT NULL DEFAULT 300,   -- seconds a remote 404 is remembered
  last_ok_at     INTEGER NOT NULL DEFAULT 0,     -- last successful remote request
  last_error     TEXT NOT NULL DEFAULT '',
  last_error_at  INTEGER NOT NULL DEFAULT 0
) STRICT;
```

`assets` gains two columns for the cached files of a proxy; a hosted file keeps the defaults:

- `checked_at INTEGER NOT NULL DEFAULT 0`: the time the remote last confirmed the file.
- `remote_etag TEXT NOT NULL DEFAULT ''`: the remote's `ETag` for the file, for the next check.

The proxy settings live in the in-memory snapshot of the repos service, refreshed on change,
so a request never reads `repo_proxies`.

## The fetch path (`internal/proxy`)

One core package fetches for every format. A format implements `format.Proxied`:

```go
// Proxied marks a format that serves proxy repos. Classify tells the core how to cache a path.
type Proxied interface {
	Classify(path string) (c ProxyClass, ok bool) // ok=false: refuse the path (404)
}

type ProxyClass int

const (
	ProxyImmutable ProxyClass = iota // fetched once, never checked again (a release jar, a .deb)
	ProxyChanging                    // checked after metadata_ttl (maven-metadata.xml, InRelease)
)
```

`GET /maven/{proxy}/{path}` and `GET /debian/{proxy}/{path}` go through the usual gate (read
access, IP rules). Then:

1. The plugin classifies the path.
2. A cached file that is immutable, or changing and checked within `metadata_ttl`, serves from
   storage as any file does (including presigned redirects on S3).
3. Otherwise the core fetches `remote_url + path`. For a cached changing file it sends
   `If-None-Match` (from `remote_etag`) and `If-Modified-Since` (from `updated_at`); a 304 sets
   `checked_at` and serves the cached file.
4. One fetch per (repo, path) runs at a time (`assets.Lock`); a second request for the same path
   waits for it and then serves the stored file.
5. The fetch streams into the spool (the upload pipeline: size limit, sha256), stores the file
   with `assets.UpsertTx` (`uploaded_by` NULL, the remote's `Last-Modified` as `created_at` and
   `updated_at`), sets `checked_at`, then serves it. The client waits for the whole file; a
   later version may stream while it stores.
6. A remote 404 answers 404 and is remembered in memory for `not_found_ttl` (an LRU of 10,000
   entries per server), so a build that asks for many absent paths does not hit the remote
   each time.
7. A remote error or a time-out (30 s to the first byte): a cached file serves even when stale,
   with a warning in the server log; with no cached file the answer is 502 "The remote
   repository did not answer." `last_error` and `last_error_at` record it; a success sets
   `last_ok_at`.

Writes (`PUT`, uploads, publish sessions) to a proxy answer 405 "A proxy is read-only."
Deleting files from the Files tab works: the next request fetches them again.

Only `http` and `https` remotes are accepted. The remote URL and the credentials are admin
settings; the fetch follows at most 5 redirects, and the credentials go only to the remote's
host (a redirect to another host drops them).

## Maven

`Classify`: `maven-metadata.xml` and its checksums, and files under a `-SNAPSHOT` version, are
changing; everything else is immutable. A path that fails the repo path rules is refused.

## Debian

`Classify`:

- `pool/**` and `dists/**/by-hash/**` are immutable (a by-hash file's name is its checksum).
- The rest of `dists/**` (`InRelease`, `Release`, `Release.gpg`, `Packages*`, `Sources*`,
  `Contents*`, `Translation-*`) is changing.
- Other paths are refused.

A Debian proxy serves the remote's files unchanged. It does not run the indexer and does not
sign: clients keep trusting the remote archive's key (for Debian, `debian-archive-keyring`).
The repo's Overview shows that ("Signed by the remote archive. Clients use its key.") and the
apt snippet without a `signed-by` key from this server.

## API

- `POST /api/repos` with `kind: "proxy"` takes `proxy: {remote_url, username?, password?,
  metadata_ttl?, not_found_ttl?}`. The server checks the remote once (a `HEAD` or `GET` of the
  remote root, or of `dists/<distribution>/InRelease` for Debian) and refuses with the error
  when it does not answer; `force: true` saves anyway.
- `PATCH /api/repos/{name}` takes `proxy` for a proxy repo. The password is write-only.
- `GET /api/repos/{name}` returns `proxy` without the credentials (`has_credentials: true`) and
  `last_ok_at`, `last_error`, `last_error_at`.
- `POST /api/repos/{name}/proxy/test` (admin) checks the remote now.

## UI

- **New repository**: the first choice is "A repository", "A proxy of a remote repository" or
  "A group of repositories". A proxy asks for the format, the name, the remote URL (with
  presets: Maven Central `https://repo1.maven.org/maven2/`, Gradle Plugin Portal
  `https://plugins.gradle.org/m2/`, Debian `https://deb.debian.org/debian/`), and optional
  credentials. It tests the remote before it creates the repository.
- **A proxy's Overview**: the remote, "Last answered 2 minutes ago" or the last error, "Test
  the remote", the usage snippet, and the size of the cache.
- **A proxy's settings**: the remote, the credentials, the two times (with units, as the other
  duration settings).
- The Files tab shows the cached files, as for a hosted repository, with Delete.
- The repository list shows a proxy with the proxy icon.

## Nexus import

A Nexus proxy repository (`type: proxy`, `attributes.proxy.remoteUrl`) of a supported format
can be picked. The import creates a proxy repository here with the same remote and the Nexus
name; it copies no files, because the proxy fetches them on demand. Syncs skip proxies; the
log says so once per sync.

Whether Nexus returns a proxy's remote credentials over its API is not checked yet. The
implementation checks it against the Nexus container first (`GET
/service/rest/v1/repositories/maven/proxy/{name}`, `httpClient.authentication`). If Nexus
returns them, the import copies them, sealed. If not, the row has a field: "The remote needs a
username and password" with both.

## Tests (e2e)

`e2e/proxy_test.go`, on every storage backend. The remote is a second server started by the
harness (a hosted Maven repository and a hosted Debian repository with known files), so the
tests need no internet:

- Maven: `mvn` resolves a release and a snapshot through the proxy. The second resolve makes no
  remote request (the remote's access log shows one GET per file).
- A changed `maven-metadata.xml` upstream shows after `metadata_ttl` (set to 1 s in the test),
  and not before. A 304 path is used on the recheck.
- A remote 404 is remembered for `not_found_ttl`.
- The remote stops: a cached file still serves; an uncached one answers 502; `last_error` is set.
- Remote credentials: a private remote repository resolves with credentials and answers 502
  without them. A redirect to another host does not carry them.
- `PUT` to the proxy answers 405.
- Debian: `apt-get update` and `apt-get install` through the proxy, with the remote's key
  trusted and none from this server.
- Through a group: a hosted member's file wins over the proxy's.

`e2e/nexus_test.go` (import zone): a Nexus Maven proxy imports as a proxy here with its remote.

## Docs

`SPEC.md`: the kind, the table, the fetch rules, the routes and the import of proxies.
`docs/design/rationale.md`: an entry "Proxy repositories" with screenshots of the new
repository choice and a proxy's Overview.
