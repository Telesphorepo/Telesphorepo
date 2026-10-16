# Edge delivery: design spec

Status: for approval. This spec builds on `docs/design/edge-delivery.md` (one origin, Cloudflare
only, path routes on the main hostname, no client part, regional R2 buckets, a Queue-driven copy
Worker, fail open to the origin). Section 0 lists where the code or Cloudflare's limits contradict
that note. Cloudflare facts were checked on 2026-10-16; sources are at the end.

## Goal and rule

Clients far from the server download from Cloudflare's edge. The Worker answers a GET or HEAD
only when it can prove that the origin would give the same answer. Every other request goes to the
origin with `fetch(request)`. The Worker never refuses: a 401, 403, 404, 405, 409, 429 or 502
always comes from the origin, so the origin keeps the audit events, the rate limits and the error
bodies.

## 0. Decisions that the code or Cloudflare contradict

| Note says | Finding | Spec decision |
|---|---|---|
| A pointer lists "the regions that hold the blob" | The copy Worker sees a blob key, not the paths that use it, so it cannot update pointers | No `regions` field. The Worker reads the regional blob and falls back to the home blob on a miss |
| Pointers and access objects live in the home bucket | A far client then pays a cross-region R2 read per request, about as slow as the origin for small Maven files | Immutable pointers are also written to every regional bucket (by the server). Access objects stay in the home bucket behind the Cache API (section 3) |
| One access object per token hash | Core bearer tokens (`trb.`) carry the token ID, not the secret | One object per read token public ID; the object holds the SHA-256 of the secret |
| Regions include South America | R2 location hints are `wnam`, `enam`, `weur`, `eeur`, `apac`, `oc` only | South America reads the `enam` bucket |
| "Past the free limits, the routes fail open" | Fail open/closed is the Workers Free daily-limit setting. Workers Paid has no request cap; a CPU overrun is error 1102, not a pass-through | The Worker calls `ctx.passThroughOnException()` and keeps CPU per request small. Cost grows with traffic; it does not stop |
| The Worker checks credentials | Passwords are argon2id (64 MiB) and UI sessions are server rows; neither may leave the origin | Password and session requests go to the origin |

Two findings outside edge delivery that block it:

1. **Shared cache of private files.** Format reads send no `Cache-Control`. Cloudflare caches the
   extensions `jar`, `zip`, `tar`, `gz` and others for 120 minutes by default when the origin sends
   no cache header. Confirm whether a request with `Authorization` is cached on the zone. If it is,
   a private file can be served to anyone today. Fix before edge delivery: every format read of a
   repo without anonymous read sends `Cache-Control: private`, and the Worker does the same.
2. **Docker group reads with a bearer token.** `assets.Reader.readable` calls
   `access.Engine.Allows` for each member with the bearer principal. `decide` checks
   `ScopeOf(member)`, and the token endpoint grants a scope for the group only, so each member
   looks refused. Run `TestDockerProxy/group` to confirm. The edge copies whatever rule the origin
   has, so fix the origin first if the test fails: a bearer scope of a group covers the reads of
   its members.

## 1. Worker code, release and deploy

**Recommendation.** An `edge/` folder in this repo: TypeScript, bun, `wrangler` as a dev
dependency, one Worker with three handlers (`fetch`, `queue`, `scheduled`).

```
edge/
  package.json, bun.lock, tsconfig.json
  src/index.ts        fetch: route, auth, serve or pass
  src/shapes.ts       URL shapes "standard" and "oci" (path -> repo + asset path)
  src/access.ts       access objects, bearer verify (Ed25519)
  src/serve.ts        headers, Range, HEAD, 304
  src/copy.ts         queue: copy and delete blob copies
  src/gap.ts          scheduled: gap check with a cursor
  scripts/provision.ts  idempotent wrangler calls: buckets, queue, notification rule
  test/               e2e fixtures only (section 7)
```

- **Config.** The server builds `wrangler.jsonc` from its settings: the routes (the base URL host
  with the URL prefix of each format that has `Edge`, e.g. `repo.example.com/maven/*`, `/v2/*`),
  the bucket bindings (`HOME` and one per region), the queue, the cron and the `s3.prefix`. The
  Edge delivery page shows it, and `GET /api/edge/wrangler.jsonc` (`edge.manage`) returns it.
- **Deploy.** The maintainer saves the file in `edge/` and runs
  `CLOUDFLARE_API_TOKEN=... bun run provision` once, then `bun run deploy` for each release. The
  token needs Workers Scripts and Routes edit, R2 edit and Queues edit. No agent touches the
  account.
- **Release.** `go tool task release` adds `telesphorepo-edge_<version>.tar.gz`: the bundled Worker
  (`wrangler deploy --dry-run --outdir`), `package.json`, the provision script and a README. An
  operator without the repo deploys from it.
- **Version match.** The server writes `edge/v1/config.json` with a `contract` number. A Worker
  with another contract passes every request to the origin, and the page says "Deploy the Worker of
  this server version".
- One line on the alternative: a separate repo, like the clients, splits the contract from the
  server that writes it; rejected.

## 2. How the server writes to R2

**Home bucket.** The home bucket is the S3 storage pointed at R2 (`storage=s3`, endpoint
`https://<account>.r2.cloudflarestorage.com`). Blobs keep their keys
`[s3.prefix/]blobs/sha256/<2>/<hex>`. `edge.enabled` needs that storage; else the group
`Validate` refuses it. A storage location change sets `edge.enabled` to false (the module
subscribes to settings), because the Worker binding names the old bucket. The page then asks for a
new deploy.

**Regional buckets.** The server writes pointers to them with the S3 credentials of the storage
settings, so the R2 API token must cover every edge bucket. The copy Worker writes the blobs.

**Objects** (all under `[s3.prefix/]edge/v1/`, JSON):

| Key | Content | Buckets |
|---|---|---|
| `config.json` | `{contract, enabled, access_ttl, edge_key (Ed25519 public), formats: {name: {shape, prefix, mutable: [globs]}}, regions: {continent: binding}}` | home |
| `p/<repo id>/<sha256 hex of path>` | `{path, sha256, sha1, md5, size, ct, mod (unix), kind}` | home; also regional when the path is immutable |
| `a/repo/<name>` | `{id, format, kind, anon, guarded, members: [names]}` | home |
| `a/user/<id>` | `{enabled, all, read: [repo ids]}` (`all`: a server permission with `AllRepos`; `read`: repos with `repo.download`) | home |
| `a/token/<public id>` | `{id, user, hash (hex SHA-256 of the secret), exp}` | home |
| `a/device/<id>` | `{user, exp}`; deleted on revoke | home |
| `seen/<isolate>-<minute>.json` | Written by the Worker (section 3) | home |
| `gap/state.json` | Written by the Worker (section 2, gap check) | home |

The pointer key hashes the path, so a 1024-byte repo path fits the 1024-byte R2 key limit, and a
reused repo name cannot see old pointers.

**When pointers change** (a kernel hook in `assets.Pipeline`, so every format and every writer is
covered: format PUTs, uploads, imports, proxy fetches, Maven metadata, Debian indexes, Docker tags
and manifests):

1. **Before the transaction** of a `Commit` that replaces an asset, and of each `Remove` batch, the
   pipeline deletes the old pointers in every bucket where they can be (home, plus the regions for
   an immutable path), under the path locks. A failed delete fails the write or the batch.
   Deletes are free R2 operations. So a deleted or replaced file never serves from the edge
   afterwards.
2. **In the transaction**, the edge module's hook inserts one `edge_outbox` row per new asset
   (module table, like Debian's `OnCommitted`). A crash after the commit loses nothing.
3. **After the transaction**, the outbox worker puts the home pointer at once, then the regional
   pointers of immutable paths. A failed put retries with backoff. Until it lands, the Worker finds
   no pointer and passes to the origin.

R2 allows one write per second to one key. The outbox coalesces rows per key (the latest state
wins) and spaces writes to one key by one second. A 429 on a pre-delete retries for up to 2 s.

**Mutable paths.** A format's `Edge.Mutable` globs (`format.Glob`) name the paths that can change:

| Format | Mutable globs | Immutable (regional pointers) |
|---|---|---|
| maven | `**/maven-metadata.xml*`, `**/*-SNAPSHOT/*` (the existing `mutable` rule) | releases and their checksums |
| debian | `dists/**` except `dists/**/by-hash/**` | `pool/**`, `dists/**/by-hash/**` |
| docker | `**/_tags/*` | `**/_manifests/sha256/*`, `**/_blobs/sha256/*` |
| raw | `**` (the `overwrite` config can change at any time) | none |

A proxy repo gets a pointer only for a path whose `Classify` class is not `Changing`, so a proxy's
metadata always goes to the origin, which revalidates it.

**Access objects.** The edge module keeps them current in two ways. Change notices from
`auth.Service` (tokens, users, permissions, grants, devices through the publish module) and
`repos.Service` (create, update, members, delete, IP rules through the guard) mark the affected
keys, and the outbox writes them. A reconcile job runs every 5 minutes: it computes every access
object from the database, compares its hash with `edge_written`, and writes the difference. The
reconcile covers a crash between a change and its notice. Kernel additions: the notices, and
`RepoGuard.Describe(repo, Read).Restricted` gives `guarded`.

**Backfill.** Turning `edge.enabled` on starts the job `edge.backfill`: it walks `assets` by
`id > cursor` in pages of 500 and queues every pointer. Blobs reach the regions through the gap
check.

**Blob copies.** As in the note. An R2 event notification rule on the home bucket (prefix
`[s3.prefix/]blobs/`, actions `object-create` and `object-delete`) sends to the queue
`<home>-edge-copy`. The consumer acts on the current state of the home bucket, never on the event
alone: it HEADs the home key; present means copy to each region that lacks it (multipart above
4.9 GiB), absent means delete the regional copies. So duplicate and out-of-order messages are safe
(Queues deliver at least once, with no stated order). The scheduled gap check runs nightly: it
lists the home `blobs/` and each region in key order (1000 keys per class A list), merges the
lists, and queues copies and deletes. It saves its cursor in `gap/state.json` and stops before
5,000 subrequests per run.

## 3. Access parity

**What the edge answers.** A GET or HEAD on a format route where all of these hold. Anything else
goes to the origin.

1. `config.json` says `enabled` and the contract matches.
2. The URL parses with the format's shape (`standard`: `/<format>/<repo>/<path>`; `oci`:
   `/v2/<repo>/<image>/manifests/<ref>` and `/v2/<repo>/<image>/blobs/sha256:<hex>`). Every other
   path (`/v2/`, tags list, referrers, catalog, `/docker/token`, `/debian/<repo>/key.asc`,
   uploads) goes to the origin.
3. The repo object exists, has the route's format, and `guarded` is false. (The edge cannot record
   a `network.denied` event, and IP lists can hold 100,000 ranges; so any read rule sends the repo
   to the origin.)
4. The caller may read the repo, by the table below.
5. A pointer exists (section 4 gives the group and proxy rules).

| Credential | Edge check | Same as origin because |
|---|---|---|
| None | `anon` | `repo.download` for anyone on an anonymous-read repo |
| Read token (Basic password with any username, or `Authorization: Bearer trp_...`) | Token object by public ID; constant-time compare of the SHA-256 of the secret; `exp` in the future; user object `enabled`; repo id in `read`, or `all`, or `anon` | `bearerPrincipal`/token verifier: token present, not expired, user enabled, grants |
| Core bearer `trb.` with cred anonymous, password, read token or device | Ed25519 edge signature; `exp`; a scope for this repo with `Read`; then the row above for the credential (password: the user object; device: the device object and its user) | `verifyBearer` reloads the same rows; the password was checked when the token was issued |
| Core bearer of a publish session; `trs_` token; password Basic; session cookie; any other header | none | goes to the origin |
| A credential that fails any edge check | none | goes to the origin, which answers 401 and counts the failed attempt |

**Bearer verification.** The origin's HMAC key never leaves the origin: with it, anyone could mint
a bearer for any user or open publish session. The token endpoint adds a second signature:
`trb.<payload>.<hmac>.<ed25519>`. The Ed25519 key is derived from the master key (HKDF-SHA256, info
`telesphorepo edge v1`, the seed of the key). The public key is in `config.json`, so Cloudflare
holds no secret. The payload adds the read token's public ID. `verifyBearer` accepts the four-part
form. Workers WebCrypto supports Ed25519.

**Revocation speed.** The Worker reads access objects and `config.json` through the Cache API with
the TTL `edge.access_ttl` (default 30 s). The server writes a changed object within a second.

| Change | Reaches the edge | What a stale edge can serve meanwhile |
|---|---|---|
| Read token deleted or expired; user disabled; grant removed; repo made private; device revoked; IP read rule added | ≤ `edge.access_ttl` + write time; ≤ 5 min + TTL after a server crash in that second | Downloads of that repo to that credential, in a data center that cached the object |
| File deleted or replaced | Immediately (pointer deleted first, fail closed) | Nothing |
| Edge turned off | ≤ `edge.access_ttl` | Downloads that the origin would also allow |

`edge.access_ttl` 0 reads the home bucket on every request (slower far away, exact).

**The other rules.**

- *Repo kinds.* The edge only reads, so 405 for writes to proxies and groups stays with the origin.
- *Mirror lock and repo operations.* They refuse `Write`, `Delete` and `Mint` only; reads are not
  changed. A repo delete removes the pointers batch by batch (step 1 runs during a `delete`
  operation too) and then the repo object.
- *Audit.* The server records no download events, so nothing is lost. Guard refusals stay at the
  origin. Edge downloads are absent from the origin's access log; Workers Logs can sample them, and
  the Worker never logs `Authorization`, cookies or query strings.
- *`last_used_at`.* The Worker collects the read tokens and devices it served, per isolate, and
  writes `seen/<isolate>-<minute>.json` at most once a minute (`waitUntil`). The job
  `edge.seen` lists `seen/` every minute, applies `last_used_at`, notes the Worker version and
  deletes the files.
- *Client IP.* A same-zone `fetch(request)` keeps `CF-Connecting-IP` as the client address, so the
  origin's IP rules, rate limits and audit see the real client (with `cloudflare` in the trusted
  proxies, as today).
- *Known difference.* The origin refuses even a valid token from an address that it blocks after
  failed attempts (`authenticate` checks `limits.blocked` first). The block lives in origin memory
  and changes every few seconds; copying it to the edge costs a write per change against the
  one-write-per-second key limit. A valid 256-bit token is not a guess, and every wrong guess still
  reaches the origin and counts. Recommendation: accept; see open question 3.

## 4. Formats

- **Maven.** Releases: regional pointer and blob. `maven-metadata.xml*` and snapshot files: home
  pointer, regional blob. Checksum files are assets, so they serve like any file.
- **Debian.** `pool/**` and `by-hash/**` from the regions; `Release`, `InRelease`, `Packages*`
  through the home pointer. apt with `Acquire-By-Hash` reads the indexes by hash, so most bytes
  come from the region. `key.asc` and uploads go to the origin.
- **Raw.** Home pointer, regional blob.
- **Docker/OCI.** Manifests by digest and blobs: regional. Manifests by tag: home pointer. The
  answer adds `Docker-Content-Digest` (`sha256:` + pointer `sha256`) and
  `Docker-Distribution-API-Version: registry/2.0`; it never redirects. A pull still gets its token
  from the origin (`/v2/` and `/docker/token`), once per pull.
- **Proxy repos.** A cached immutable file serves from the edge. A miss or a `Changing` path goes
  to the origin, which fetches, commits, and so writes the pointer for the next client.
- **Group repos.** The Worker walks the members in order, like `Reader.Resolve`:
  - The whole request goes to the origin when the path is mutable for the format (this covers every
    `Merger` path; a format must keep its merged paths mutable), or when a readable member is
    `guarded`.
  - It skips a member the caller may not read (the same table, without an audit event, like
    `Allows`).
  - A hosted member with a pointer serves. A hosted member without one is skipped.
  - A proxy member with a pointer serves; without one the request goes to the origin, because the
    origin would fetch at that member before it tries the next one.
  - No member serves: the origin answers (404 or 502). It reads member pointers in parallel, at
    most 6 at a time.

## 5. Headers at the edge

The Worker sets what `assets.Reader.ServeAsset` sets, from the pointer:

- `Content-Type` (`ct`), `ETag: "<sha256>"`, `Last-Modified` (`mod`), `X-Content-Type-Options:
  nosniff`, `Content-Security-Policy: default-src 'none'; sandbox`, `Content-Disposition:
  attachment` unless the type is `text/plain`, `Accept-Ranges: bytes`, `X-Checksum-SHA256`, and
  `X-Checksum-SHA1` and `X-Checksum-MD5` when the pointer has them. `Cache-Control: private` for a
  repo without anonymous read (finding 1).
- `If-None-Match` that matches: 304. HEAD: the headers and `Content-Length` from the pointer, with
  no R2 read of the blob.
- One range (`bytes=a-b`, `a-`, `-n`) with a matching or absent `If-Range`: 206 with
  `Content-Range` from an R2 ranged get. A range outside the file: 416 with `Content-Range:
  bytes */<size>`. Several ranges or an `If-Range` that does not match: the whole file.
- `Telesphorepo-Edge: hit; region=<hint>` marks an edge answer. Tests use it; it names no bucket
  and no secret.

## 6. Uploads and the 100 MB limit

The Worker passes every method other than GET and HEAD to the origin unchanged, with its body
streamed. Cloudflare limits a request body to 100 MB on Free and Pro, with or without a Worker, so
nothing changes for uploads. With `docker.direct_uploads` on, the upload session `Location`s of
Docker pushes point at the DNS-only host of `docker.upload_url`, and the Worker has no route on
that host. Maven, raw and Debian uploads over 100 MB need the resumable upload routes in chunks, as
today (open question 4).

## 7. Testing

A new zone `edge` (tag `edge`, `go tool task test edge`) in `e2e/edge_test.go`. No unit tests.

**Fixture.** A container built from `edge/test/Containerfile` (Node, bun, the `edge/` sources) runs
`wrangler dev --local --ip 0.0.0.0 --local-upstream <server>:8080` with a test `wrangler.jsonc`:
a home bucket and two regional buckets (local R2), the queue, and
`local_dev.experimental_s3_credentials` on each bucket. The server under test uses `storage=s3` with
the endpoint `http://edge:8787/cdn-cgi/local/r2/s3` and path style, so it writes the same local R2
that the Worker reads. Clients (`mvn`, Gradle, `apt`, `docker`, curl) point at the Worker's
address; the `fetch(request)` pass-through reaches the server through `--local-upstream`.

**Spike first** (one day, before the build): the local S3 endpoint is experimental (Wrangler
4.115.0, July 2026), and the docs do not say whether local R2 emits event notifications. If the
endpoint fails the server's storage probe, the fallback is a SeaweedFS home bucket plus a fixture
that copies it into local R2; that tests less of the real path. If local notifications do not
exist, a test-only producer Worker in `edge/test/` sends messages of the R2 event shape to the same
queue; the consumer code stays the real one.

**What the tests prove.**

- *Parity matrix.* For each format, each credential (none, read token by Basic and by Bearer, core
  bearer of each kind, password, session, publish session, bad token), each repo setting (public,
  private with and without a grant, `repositories.all`, read IP rule) and each kind (hosted, proxy,
  group with a private member, a guarded member and a proxy member): the same request to the
  Worker and to the server directly gives the same status, the same body bytes and the same values
  of an allowlist of headers. Each case also asserts `Telesphorepo-Edge`, so the matrix proves which
  requests the edge answers and that it does not pass everything.
- *Revocation* (with `edge.access_ttl=1s`): token delete, user disable, grant removal, repo made
  private, device revoke and an IP rule each turn edge hits into origin answers within 2 s.
- *Freshness.* A file delete and a Maven republish show at the edge at once; a Docker tag move
  serves the new digest.
- *Copies.* A new blob reaches both regions; the Worker reads the home blob before that; deleting a
  regional copy by hand and running the cron (`/cdn-cgi/handler/scheduled`) restores it; a blob GC
  removes the regional copies.
- *Range, HEAD, 304, 416, If-Range* on both paths.
- *Fail open.* `edge.enabled=false`, a contract mismatch, and a malformed pointer that the test
  writes through S3 each give origin answers, not errors.
- *Real clients.* `mvn` and Gradle resolve, `apt-get install` and `docker pull` through the Worker,
  for a public and a private repo.
- *`last_used_at`* moves after a read-token download at the edge.

Run the `edge` zone when a change touches `internal/edge`, `internal/assets`, `internal/access`,
`internal/auth` or `edge/`. A subagent runs `bun run build` and `bunx tsc --noEmit` in `edge/`.

## 8. Costs and limits

Workers Paid: $5 a month with 10 M requests and 30 M CPU ms; then $0.30 per million requests and
$0.02 per million CPU ms; subrequests are free. Limits: 30 s CPU per request by default (5 min
max), 128 MB memory, 10,000 subrequests per invocation, 6 connections waiting at once. R2 Standard:
$0.015 per GB-month, class A $4.50 per million (PutObject, ListObjects, multipart), class B $0.36
per million (GetObject, HeadObject), deletes free, egress free; free tier 10 GB, 1 M class A, 10 M
class B a month. Single upload 5 GiB, object 5 TiB, one write per second per key, strong
read-after-write consistency. Queues: 1 M operations a month included, then $0.40 per million;
each message costs a write, a read and a delete per 64 KB; 5,000 messages per second per queue,
batches of 100, 15 min wall time per consumer run. Event notifications: 100 rules per bucket, no
overlapping rules.

Operations per edge download: one pointer read (two for a group), one blob read, and 0 to 3
access-object reads on a Cache API miss. Per stored file: one home pointer put plus one per region
for an immutable path; per new blob: one queue message and one put per region.

Estimate (not a measurement): 10 M downloads and 12 M Worker requests a month, 200 GB stored, 4
regions, 100,000 new files and 50,000 new blobs a month.

| Item | Amount | Cost |
|---|---|---|
| Workers base | | $5.00 |
| Requests above 10 M | 2 M | $0.60 |
| CPU (about 2 ms each) | 24 M ms, inside 30 M | $0 |
| R2 class B | about 30 M, minus 10 M free | $7.20 |
| R2 class A | about 0.8 M, inside 1 M | $0 |
| R2 storage | 1,000 GB (home + 4 copies), minus 10 GB | $14.85 |
| Queues | about 0.3 M operations | $0 |
| Egress | 2 TB | $0 |
| **Total** | | **about $28 a month** |

Storage of the regional copies is the largest cost. Fewer regions cost less.

## 9. Settings, permission and admin UI

**Module.** `internal/edge`, module `edge` (after `storage` and `network` in `modules.go`). It owns
the schema (`edge_outbox`, `edge_written`, `edge_state`), the jobs `edge.outbox`, `edge.backfill`,
`edge.reconcile` (5 min) and `edge.seen` (1 min), and `GET /api/edge/status` and
`GET /api/edge/wrangler.jsonc`. A format opts in with `format.Spec.Edge{Shape, Mutable}`; a format
without it gets no route.

**Settings group `edge`** (permission `edge.manage`, page `/settings/edge`):

| Key | Type | Default | Rule |
|---|---|---|---|
| `edge.enabled` | bool | false | Needs R2 storage (endpoint host ends in `.r2.cloudflarestorage.com`) and a `base_url` without a path |
| `edge.regions` | list | `[]` | Entries `<hint>=<bucket>`, hint one of `wnam enam weur eeur apac oc`, each hint once |
| `edge.access_ttl` | duration | `30s` | 0 to 5 min |

`Test` runs `storage.Probe` on each regional bucket. Permission `edge.manage` ("Manage edge
delivery", subsystem `edge`, second factor). No new audit actions: `settings.update` covers the
changes.

**Page "Edge delivery"** (Settings, Server group):

- Status: on or off; the Worker version and when the server last saw it (from `seen/`); a contract
  mismatch; the home bucket.
- Regions: hint, bucket, pointers waiting in the outbox, last error; the last gap check and the
  copies it queued (from `gap/state.json`).
- Backfill progress while it runs.
- The settings form.
- Deploy: the generated `wrangler.jsonc` in a `CodeBlock`, the commands `bun run provision` and
  `bun run deploy`, and the token permissions they need.

The access report of a repo adds one line: "Downloads come from the edge" or "Downloads go to the
server: read rules apply".

## Decisions

1. Regions: the admin picks them in `edge.regions`. The server has no default region list.
2. `edge.access_ttl` defaults to 30 s.
3. A valid read token from an address that the origin blocked for failed attempts still downloads
   at the edge. The failed attempts still reach the origin and count there.
4. Uploads stay on the origin. Docker uploads over 100 MB use `docker.direct_uploads`.
5. Download snippets for private repos use a read token, never a password.

## Sources

- Workers limits: https://developers.cloudflare.com/workers/platform/limits/
- Workers pricing: https://developers.cloudflare.com/workers/platform/pricing/
- R2 pricing: https://developers.cloudflare.com/r2/pricing/
- R2 limits: https://developers.cloudflare.com/r2/platform/limits/
- R2 consistency: https://developers.cloudflare.com/r2/reference/consistency/
- R2 data location (hints): https://developers.cloudflare.com/r2/reference/data-location/
- R2 event notifications: https://developers.cloudflare.com/r2/buckets/event-notifications/
- R2 Workers API (ranged reads): https://developers.cloudflare.com/r2/api/workers/workers-api-reference/
- Queues pricing: https://developers.cloudflare.com/queues/platform/pricing/
- Queues limits: https://developers.cloudflare.com/queues/platform/limits/
- Queues delivery guarantees: https://developers.cloudflare.com/queues/reference/delivery-guarantees/
- Queues local development: https://developers.cloudflare.com/queues/configuration/local-development/
- `CF-Connecting-IP` in Worker subrequests: https://developers.cloudflare.com/fundamentals/reference/http-headers/
- Default cache behavior: https://developers.cloudflare.com/cache/concepts/default-cache-behavior/
- Web Crypto (Ed25519): https://developers.cloudflare.com/workers/runtime-apis/web-crypto/
- Wrangler 4.115.0 local R2 S3 endpoint: https://releases.sh/release/rel_Ep-sFGOtu356Mx2P2iRwq
