# Edge delivery: design spec

Status: for approval. This spec replaces the note `docs/design/edge-delivery.md`. Cloudflare facts
were checked on 2026-10-19; the sources are at the end.

## Goal and rule

Clients far from the server download from Cloudflare's edge. Cloudflare is the only provider: R2,
Queues and Workers. The Worker answers a GET or HEAD only when it can prove that the origin would
give the same answer. Every other request goes to the origin. The Worker never refuses: a 401,
403, 404, 405, 409, 416 or 429 always comes from the origin, so the origin keeps the audit events,
the rate limits and the error bodies. The Worker answers 502 itself only when it cannot reach the
origin.

Two rules shape the design:

- **Consistency before latency.** A path that can change (Maven metadata and snapshots, Debian
  `dists/` outside `by-hash/`, Docker tags, every raw path) has its pointer in the home bucket only.
  So every region reads the same current version. An immutable path has a pointer in every
  regional bucket, and its blob is in every regional bucket.
- **Warm everywhere from the first request.** The server writes every pointer, and the copy Worker
  copies every blob, when the file is stored, before any client asks for it. No region gets a file
  only after a first miss.

## 1. Worker code, release and deploy

An `edge/` folder in this repo holds the Worker: TypeScript, bun, and `wrangler` as a dev
dependency at an exact version (no range), because the local S3 endpoint of the tests is
experimental and its config key changed upstream (section 7). One Worker has three handlers:
`fetch`, `queue` and `scheduled`.

```
edge/
  package.json, bun.lock, tsconfig.json
  src/index.ts        fetch: route, auth, serve or pass
  src/shapes.ts       URL shapes "standard" and "oci" (path -> repo + asset path)
  src/glob.ts         an exact port of format.Glob
  src/access.ts       access objects, bearer verify (Ed25519)
  src/serve.ts        headers, Range, HEAD, 304
  src/copy.ts         queue: copy and delete blob copies
  src/gap.ts          scheduled: gap check with a cursor
  scripts/provision.ts  idempotent wrangler calls: buckets (with location hints), queue, notification rule
  test/               the e2e fixture (section 7)
```

- **Config.** The server builds `wrangler.jsonc` from its settings: the routes (the base URL host
  with the URL prefix of each format that has `Edge`, for example `repo.example.com/maven/*` and
  `repo.example.com/v2/*`), the bucket bindings (`HOME` and `R2_<HINT>` for each chosen region),
  the queue, the cron and `s3.prefix`. The Edge delivery page shows it, and
  `GET /api/edge/wrangler.jsonc` returns it.
- **Same zone.** The Worker runs on a route of the zone of the base URL host. A same-zone
  `fetch()` goes to the origin and does not run the Worker again. A cross-zone setup breaks the
  client address (section 3), so the provision script refuses it.
- **Deploy.** The maintainer saves the file in `edge/` and runs `CLOUDFLARE_API_TOKEN=... bun run
  provision`, then `bun run deploy`. The maintainer runs provision again after a region change, and
  deploy for each release. The token needs Workers Scripts and Routes edit, R2 edit and Queues
  edit. No agent touches the account.
- **Release.** `go tool task release` adds `telesphorepo-edge_<version>.tar.gz`: the bundled Worker
  (`wrangler deploy --dry-run --outdir`), `package.json`, the provision script and a README. An
  operator without the repo deploys from it.
- **Version match.** The Worker and its archive carry the server version. The server writes its
  version into `edge/v1/config.json`. A Worker of another version passes every request to the
  origin, and the page says "Deploy the Worker of this server version".
- **Change log.** `changes.toml` needs no entry. The four-part bearer token is opaque to clients,
  and the CLI sends no format GETs.

A separate repo, like the clients, would split the Worker from the server that writes its objects.
This spec keeps one repo.

## 2. How the server writes to R2

**Home bucket.** The home bucket is the S3 storage of the server (`storage=s3`) on R2. Blobs keep
their keys `[s3.prefix/]blobs/sha256/<2>/<hex>`. `edge.enabled` needs `storage=s3` and a
`base_url` without a path; else the group `Validate` refuses it. The group `Test` runs
`storage.Probe` on the home bucket and on every regional bucket. The server does not check the
endpoint host, so the e2e fixture can use the local R2 S3 endpoint.

**Regional buckets.** Each chosen region has its own bucket in the same account. The edge module
opens one `storage.Store` per region, with the endpoint, credentials, path style and prefix of the
storage settings and the bucket of the region. So the R2 API token must cover every edge bucket.
The server writes pointers to the regional buckets. The copy Worker writes the blobs.

**Batch delete.** `storage.Store` gets `DeleteMany(ctx, keys)`. The S3 store sends `DeleteObjects`
with up to 1000 keys per call; the FS store deletes one key at a time. A `Remove` batch of 500
paths costs one call per bucket.

**Objects** (all under `[s3.prefix/]edge/v1/`, JSON):

| Key | Content | Buckets |
|---|---|---|
| `config.json` | `{version, enabled, access_ttl, edge_key (Ed25519 public), formats: {name: {shape, prefix, immutable: [globs], mutable: [globs]}}, home: {hint}, regions: [{hint, binding, ready}]}` | home |
| `p/<repo id>/<sha256 hex of path>` | `{repo (id), path, sha256, sha1, md5, size, ct, mod (unix), kind}`, or `{repo, path, pending: true}` while a replacement runs | home; also regional when the path is immutable |
| `a/repo/<name>` | `{id, format, kind, anon, private, guarded, members: [names]}` | home |
| `a/user/<id>` | `{enabled, local, all, read: [repo ids]}` (`all`: a server permission with `AllRepos`; `read`: repos with `repo.download`) | home |
| `a/token/<public id>` | `{id, user, hash (hex SHA-256 of the secret), exp}` | home |
| `a/device/<id>` | `{user, exp}` | home |
| `seen/<isolate>-<minute>.json` | Written by the Worker (section 3) | home |
| `gap/state.json` | Written by the Worker's gap check: the cursor, per region the last pass and the copies it queued, and the Worker version | home |
| `gap/request.json` | Written by the server to ask for a gap pass now | home |
| `copy/<hint>/<hex>.json` | Written by the copy Worker: the multipart upload ID and finished parts of a large copy | home |

The pointer key hashes the path, so a 1024-byte repo path fits the 1024-byte R2 key limit.

**IDs are reused.** `repos.id`, user IDs and device IDs have no `AUTOINCREMENT`, so SQLite can give
an ID of a deleted row to a new row. A repo delete removes every file through `Remove` before it
deletes the repo row, so no pointer of the old repo stays under `p/<id>/`. The Worker also checks
that the pointer's `repo` equals the `id` of the repo object. The edge module deletes `a/user/<id>`
and `a/device/<id>` when the row goes.

**Mutable and immutable paths.** A format declares `format.Spec.Edge{Shape, Immutable, Mutable}`
with `format.Glob` patterns (`*`, `?` and `**/`). A path is immutable only when it matches an
`Immutable` glob and no `Mutable` glob. Every other path is mutable. The Worker ports `format.Glob`
exactly.

| Format | Immutable | Mutable |
|---|---|---|
| maven | `**/*` | `**/maven-metadata.xml*`, `**/*-SNAPSHOT/*` (the existing `mutable` rule) |
| debian | `pool/**/*`, `dists/**/by-hash/**/*` | none |
| docker | `**/_manifests/sha256/*`, `**/_blobs/sha256/*` | none (`_tags/` matches no immutable glob) |
| raw | none (the `overwrite` config can change at any time) | none |

A proxy repo gets a pointer only for a path whose `Classify` class is not `Changing`. So a proxy's
changing files have no pointer at all and always go to the origin, which revalidates them. This is
stricter than the consistency rule.

**The pipeline hook.** `OnCommitted` and `OnRemoved` belong to one format. The kernel adds a
pipeline watcher that runs for every format and every writer: format PUTs, uploads, imports, proxy
fetches, Maven metadata, Debian indexes, Docker tags and manifests. The edge module registers it in
`Init`. The watcher has these calls:

- `Replacing(ctx, repo, paths)`: in `CommitAll` after `admit` and before `ensure`, under the path
  locks. This is the only point where a commit knows which writes replace an existing asset.
- `Committed(ctx, tx, repo, assets)`: in the commit transaction.
- `Removing(ctx, repo, paths)`: in `removeBatch` after the path locks and before the transaction.
- `Removed(ctx, tx, repo, assets)`: in the remove transaction.
- `Touched(ctx, repo, paths)`: after `Pipeline.SetTimes`, which changes `updated_at` (the pointer's
  `mod`) outside the pipeline transaction. `SetChecked` changes only `checked_at`, which no answer
  shows, so it needs no call.

**The outbox.** The table `edge_outbox` has one row per dirty path: `(repo_id, path)` as the key,
`sha256` (empty for "read the current state"), `due_at` and `attempts`. A new row for a path
replaces the old one.

1. **Before a replacement.** `Replacing` writes a dirty row (empty `sha256`) in its own
   transaction. Then it changes the old pointers in every bucket where they can be. For an immutable
   path it puts `{pending: true}` in the home bucket and in every region. For a mutable path it
   deletes the home pointer. A failed write fails the commit. If the commit transaction fails after
   this step, the dirty row makes the outbox restore the pointer of the file that stays.
2. **Before a remove batch.** `Removing` writes dirty rows in its own transaction, then deletes the
   pointer keys of the batch with one `DeleteMany` per bucket. A failed delete fails the batch.
   Deletes are free R2 operations.
3. **In the transaction.** `Committed` upserts one row per stored asset with its `sha256`.
   `Removed` upserts one row per removed asset with an empty `sha256`. A crash after the commit
   loses nothing.
4. **After the transaction.** The job `edge.outbox` takes a due row and then
   `Pipeline.Lock(repo, path)`. Under the lock it reads the asset row again:
   - The row exists, and its `sha256` equals the outbox row's `sha256` or the outbox row's is
     empty: it puts the home pointer, then the regional pointers of an immutable path.
   - The asset row is gone: it deletes the pointer in every bucket.
   - The asset row has another `sha256`: it drops the outbox row, because the newer commit has its
     own row.
   It deletes the outbox row only after every write succeeds. A failed write retries with backoff.

The lock orders each outbox write against every commit and remove of the path. So the outbox never
puts the pointer of a file that a remove deleted. Until a pointer lands, the Worker finds no
pointer, or a pending one, and passes to the origin.

R2 allows one write per second to one key. The outbox spaces writes to one key by one second, and
it waits outside the path lock. A 429 on a write of step 1 or 2 retries for up to 2 s. R2 reads do
not wait for earlier writes, so a 404 does not prove that an earlier put failed. The outbox treats a
put that timed out as unknown: it keeps the row, retries it, and applies the final state once more
one minute after the timed-out attempt.

**Access objects.** Change notices mark the affected keys: `auth.Service` (tokens, users,
permissions, grants), `repos.Service` (create, update, members, delete), `netrules.Service` (IP
rules through `SetRules`) and `publish.Service` (device revoke, device expiry, user disable of
devices). The outbox writes the marked keys. The job `edge.reconcile` runs every 5 minutes. It
computes every access object from the database, compares its hash with `edge_written`, writes the
difference, and deletes the objects whose rows are gone. The reconcile covers a crash between a
change and its notice.

- `private` is `access.Engine.Private(repo)`, the rule that sets `Cache-Control: private` on origin
  reads.
- `guarded` is the new `access.Engine.Guarded(repo)`, built on `ReadRestricter.RestrictsRead`. It
  does not build the access report.

**Backfill.** Turning `edge.enabled` on, or adding a region, starts the job `edge.backfill`. It
walks `assets` by `id > cursor` in pages of 500 and queues the pointers. For a new region it first
deletes every pointer that the bucket holds (list and `DeleteMany`), then puts the pointer of every
immutable path. It saves its cursor per region in `edge_state`.

**Blob copies.** An R2 event notification rule on the home bucket (prefix `[s3.prefix/]blobs/`,
actions `object-create` and `object-delete`) sends to the queue `<home>-edge-copy`. The consumer
acts on the current state of the home bucket, never on the event alone. It HEADs the home key.
Present means copy to each ready or filling region that lacks it. Absent means delete the regional
copies. So duplicate and out-of-order messages are safe: Queues deliver at least once, with no
stated order. A delete event has no size and no ETag, and a lifecycle deletion also sends
`object-delete`; the HEAD handles both.

- **How it copies.** The R2 Workers binding has no copy method. The consumer streams the home
  `get().body` into the regional `put()`. Above 4.9 GiB it uses `createMultipartUpload` and
  `uploadPart`, with parts of equal size: the larger of 100 MiB and the size divided by 10,000,
  rounded up to a whole MiB. A copy that does not finish in one consumer run saves its upload ID and
  finished parts in `copy/<hint>/<hex>.json`, and the retried message resumes it with
  `resumeMultipartUpload`. The consumer deletes the state object when the copy completes. R2 aborts
  an unfinished multipart upload after 7 days.
- **Gap check.** The cron runs every 10 minutes. A run does a pass when `gap/request.json` is newer
  than the last pass, or when the last pass is older than 24 hours; else it ends after one read. A
  pass lists the home `blobs/` and each region in key order (1000 keys per class A list), merges the
  lists, and queues copies and deletes. It saves its cursor in `gap/state.json` and stops before
  5,000 subrequests per run. This ceiling is a choice that keeps a run short; the platform default
  is 10,000 and `limits.subrequests` can raise it. The server writes `gap/request.json` when a
  region is added.

**Region states.** A region is `filling`, `ready` or gone.

1. The admin adds a region (section 9). The region is `filling`. `config.json` lists it with
   `ready: false`. The Worker does not read a filling region; clients near it read the nearest
   ready region or the home bucket.
2. The backfill writes the region's pointers. The gap check copies the blobs; the server asks for
   a pass at once with `gap/request.json`.
3. When the backfill is done, the server lists the home `blobs/` and the regional `blobs/` in key
   order. When no blob is missing, it sets `ready: true`. Commits and copies during the fill are
   already written to the filling region, so no gap opens.
4. A Worker reads a region only when `config.json` lists it as ready and the Worker has a binding
   for it. So a region change works before the redeploy too: the Worker ignores a region it has no
   binding for.
5. The admin removes a region. The server removes it from `config.json` and stops writing to its
   bucket at once. The page tells the admin to deploy the Worker again and to delete the bucket in
   Cloudflare. A bucket that is added again later gets the stale-pointer delete of the backfill.

**Storage location changes.** A change of a key in `storage.LocationKeys` other than
`s3.redirect`, or a storage move of `internal/move` that runs, makes the edge module write
`enabled: false` into the `config.json` of the old home bucket and mark `edge_state` "deploy
needed". The module does not patch the setting, because a `TR_EDGE_ENABLED` override would refuse
the patch. The page then asks for a new deploy.

While edge is suspended, the Worker reads `enabled: false` within `edge.access_ttl` and passes every
request to the origin. The pre-step calls (`Replacing`, `Removing`) keep running against the old
buckets until that TTL has passed, so a file that changes during the move never serves stale from a
Worker with a cached `config.json`. The module then drops the outbox and writes nothing.

When the admin deploys the Worker for the new home and saves the edge settings, the module treats it
as a first start: it deletes the `edge/v1/` prefix in the new home and in every regional bucket,
then runs `edge.backfill` for all pointers. Regional blobs stay, because they are content-addressed.
Regions go through "filling" to "ready" as for a new region. The regional bucket names follow the
new home (`<home bucket>-<hint>`), and `bun run provision` creates the new notification rule and
queue. A move to `storage=fs` turns edge off: `edge.enabled` needs `storage=s3`.

## 3. Access parity

**What the edge answers.** A GET or HEAD on a format route where all of these hold. Anything else
goes to the origin.

1. `config.json` says `enabled`, and its `version` is the Worker's version.
2. The URL parses with the format's shape. `standard` is `/<format>/<repo>/<path>`. `oci` is
   `/v2/<repo>/<image>/manifests/<ref>` and `/v2/<repo>/<image>/blobs/sha256:<hex>`. Every other
   path under a Worker route goes to the origin: `/v2/`, tags lists, referrers, the catalog,
   `/debian/<repo>/key.asc` and uploads. A request with the query parameter `_state` (a Docker
   upload token) goes to the origin. The Worker ignores other query parameters, as the origin's
   read routes do.
3. The repo object exists, has the route's format, and `guarded` is false. The edge cannot record
   a `network.denied` event, and IP lists can hold 100,000 ranges; so any read rule sends the repo
   to the origin.
4. The caller may read the repo, by the table below.
5. A pointer exists and is not pending (section 4 gives the group and proxy rules).

The Worker never sees a route outside the format prefixes. These paths stay on the origin only:
`/docker/token`, `/docker/hosts`, `/cli/`, `/install.sh`, `/install.ps1`, `/api/devices/*`,
`/api/publish/sessions/exchange`, `/api/changes` and the rest of `/api/`.

| Credential | Edge check | Same as origin because |
|---|---|---|
| None | `anon` | `repo.download` for anyone on an anonymous-read repo |
| Read token `trp_`: the Basic password with any username, or `Authorization: Bearer trp_...` | Token object by public ID; constant-time compare (`crypto.subtle.timingSafeEqual`) of the SHA-256 of the secret; `exp` in the future; user object `enabled` and `local`; repo id in `read`, or `all`, or `anon` | `VerifyReadToken` refuses a disabled user, a user that is not local and an expired token; the read-token branch of `bearerPrincipal` does the same |
| Core bearer `trb.`, on the `oci` shape only, with cred anonymous, password, read token or device, no scope with a session, and no upload binding | Ed25519 edge signature; `exp`; a scope for this repo with `Read`; then the credential: anonymous: `anon`; password: user object `enabled`; read token: the read-token row above; device: device object `exp` and user object `enabled` (an SSO user's device is valid) | `verifyBearer` reloads the same rows; the password was checked when the token was issued |
| Core bearer on the `standard` shape | none | goes to the origin; `route.FormatRead` reads no bearer, so the origin treats it as no credential |
| Core bearer of a publish session, or with a session scope, or an upload token; `trs_`; `trx.` (Basic password or Bearer); `trsu_`; any other registered token kind; a Basic password without `trp_`; a `tr_session` cookie; any other `Authorization` | none | goes to the origin |
| A credential that fails any edge check | none | goes to the origin, which answers 401 and counts the failed attempt |

Passwords are argon2id (64 MiB) and UI sessions are server rows, so neither leaves the origin. The
Worker looks for the `tr_session` cookie only; other cookies do not change the answer.

**Group reads with a bearer.** The bearer scope is checked on the group only. Each member is
checked against the rows of the credential behind the token, without scopes. This is the origin's
rule (`Principal.InGroup` and `Engine.GroupMembers`).

**Bearer verification.** The origin's HMAC key never leaves the origin: with it, anyone could mint
a bearer for any user or open publish session. The token endpoint adds a second signature:
`trb.<payload>.<hmac>.<ed25519>`. The Ed25519 key is derived from the master key (HKDF-SHA256, info
`telesphorepo edge v1`, the seed of the key). The public key is in `config.json`, so Cloudflare
holds no secret. Workers WebCrypto supports Ed25519. Three kernel changes:

- `verifyBearer` parses the four-part form. Today it splits at the first `.` after the prefix, so
  a fourth part fails.
- `bearerPayload` adds the read token's public ID next to the row ID `t`.
- One function adds the Ed25519 signature for both `IssueBearer` and `IssueUploadToken`.

**Revocation speed.** The Worker reads access objects and `config.json` through the Cache API with
the TTL `edge.access_ttl` (default 30 s). The Cache API keeps an object per data center, and
`cache.delete` purges only the local data center, so the TTL is the bound. The Cache API does not
work behind Cloudflare Access, so the base URL host must not be behind Cloudflare Access. The server
writes a changed object within a second.

| Change | Reaches the edge | What a stale edge can serve meanwhile |
|---|---|---|
| Read token deleted or expired; user disabled; grant removed; repo made private; device revoked; IP read rule added | ≤ `edge.access_ttl` + write time; ≤ 5 min + TTL after a server crash in that second | Downloads of that repo to that credential, in a data center that cached the object |
| File deleted or replaced | Immediately (the pointer is deleted or pending before the commit) | Nothing |
| Edge turned off | ≤ `edge.access_ttl` | Downloads that the origin would also allow |

`edge.access_ttl` 0 reads the home bucket on every request (slower far away, exact).

**The other rules.**

- *Repo kinds.* The edge only reads, so 405 for writes to proxies and groups stays with the origin.
- *Mirror lock and repo operations.* They refuse `Write`, `Delete` and `Mint` only; reads do not
  change. A commit during a delete answers 409 at the origin. A repo delete removes the pointers
  batch by batch through `Removing`, and then the repo object.
- *Audit.* The server records no download events, so nothing is lost. Guard refusals stay at the
  origin. Edge downloads are absent from the origin's access log. Workers Logs can sample them, and
  the Worker never logs `Authorization`, cookies or query strings.
- *`last_used_at`.* Only a direct read-token check updates `last_used_at` at the origin; a bearer
  does not, and devices update it only on mint and setup. So the Worker collects only the `trp_`
  read tokens that it served directly, per isolate. It writes `seen/<isolate>-<minute>.json` at
  most once a minute (`waitUntil`) when the list is not empty. The job `edge.seen` lists `seen/`
  every minute, applies `last_used_at` and deletes the files.
- *Client IP.* In a same-zone subrequest, Cloudflare sets `CF-Connecting-IP` from `x-real-ip`, and
  a Worker script can change `x-real-ip`. So the Worker sets `x-real-ip` on every forwarded request
  to the incoming `CF-Connecting-IP`, and never forwards a value from the client. The origin's IP
  rules, rate limits and audit then see the real client, with `list:cloudflare` in the trusted
  proxies, as today.
- *Pass-through errors.* The Worker forwards with `fetch()` and catches its errors. On an error it
  answers a 502 problem document. A GET or HEAD also calls `ctx.passThroughOnException()`. A request
  with a body never relies on it: if the origin fetch throws after it read the body, the fallback
  reaches the origin without the body. `passThroughOnException` does not cover a CPU or memory
  overrun (error 1102), so the Worker keeps CPU per request small. On Workers Paid there is no
  request cap: cost grows with traffic, and the edge does not stop.
- *Known difference.* The origin refuses even a valid token from an address that it blocks after
  failed attempts (`authenticate` checks `limits.blocked` first). The block lives in origin memory
  and changes every few seconds. Copying it to the edge costs a write per change against the
  one-write-per-second key limit. A valid 256-bit token is not a guess, and every wrong guess still
  reaches the origin and counts. The edge accepts this difference (Decisions).

## 4. Formats

- **Maven.** Releases: regional pointer and blob. `maven-metadata.xml*` and snapshot files: home
  pointer, regional blob. Checksum files are assets, so they serve like any file.
- **Debian.** `pool/**` and `by-hash/**` from the regions. `Release`, `InRelease` and `Packages*`
  through the home pointer. apt with `Acquire-By-Hash` reads the indexes by hash, so most bytes
  come from the region. `key.asc` and uploads go to the origin.
- **Raw.** Home pointer, regional blob.
- **Docker/OCI.** Manifests by digest and blobs: regional. Manifests by tag: home pointer. The
  answer adds `Docker-Content-Digest` (`sha256:` + pointer `sha256`) and
  `Docker-Distribution-API-Version: registry/2.0`. The edge never redirects. A pull still gets its
  token from the origin (`/v2/` and `/docker/token`), once per pull. A CI identity pull gets an
  anonymous bearer, so the edge serves it only on an anonymous-read repo.
- **Proxy repos.** A cached file whose class is not `Changing` serves from the edge. A miss or a
  `Changing` path goes to the origin, which fetches, commits, and so writes the pointer for the next
  client.
- **Group repos.** The Worker walks the members in order, like `Reader.Resolve`:
  - The whole request goes to the origin when the path is mutable for the format, or when any
    member is `guarded`. Whether a guarded member is readable depends on the client IP, which the
    edge cannot check. Every `Merger` path is mutable: a format keeps its merged paths out of its
    `Immutable` globs or inside its `Mutable` globs.
  - It skips a member the caller may not read, checked against the credential's rows without
    scopes (section 3), and without an audit event, like `Allows`.
  - A hosted member with a pointer serves. A hosted member without one is skipped. A pending
    pointer sends the whole request to the origin.
  - A proxy member with a pointer serves. Without one the request goes to the origin, because the
    origin would fetch at that member before it tries the next one.
  - No member serves: the origin answers (404 or 502).
  - The Worker reads member pointers in parallel, at most 6 at a time.

**Which bucket.** The Worker picks the nearest ready region, or the home bucket, by great-circle
distance from `request.cf.latitude` and `request.cf.longitude` to a fixed point per hint. The
points are estimates of where each hint stores data: `wnam` San Francisco, `enam` Ashburn, `weur`
London, `eeur` Warsaw, `apac` Singapore, `oc` Sydney. The home bucket counts at the point of
`edge.home_hint`. Without coordinates, the Worker uses the home bucket. R2 has no South America
hint, so South America reads `enam` or the next nearest choice.

- An immutable path reads the pointer in the chosen bucket. A miss there goes to the origin; the
  outbox writes the home pointer first and the regional ones within seconds.
- A mutable path reads the home pointer.
- The blob is read in the chosen bucket. A miss reads the home blob, because a copy can lag the
  pointer.

## 5. Headers at the edge

The Worker sets what `assets.Reader.ServeAsset` sets on a streamed answer, from the pointer:

- `Content-Type` (`ct`), `ETag: "<sha256>"`, `Last-Modified` (`mod`), `X-Content-Type-Options:
  nosniff`, `Content-Security-Policy: default-src 'none'; sandbox`, `Content-Disposition:
  attachment` unless the parsed media type is `text/plain`, `Accept-Ranges: bytes`,
  `X-Checksum-SHA256`, and `X-Checksum-SHA1` and `X-Checksum-MD5` when the pointer has them.
- `Cache-Control: private` when the repo object's `private` is true. This copies
  `access.Engine.Private`, which sets the same header on origin reads. On Free, Pro and Business,
  Cloudflare does not cache a response to a request with `Authorization` unless `Cache-Control` has
  `public`, `s-maxage` or `must-revalidate`. On Enterprise with Origin Cache Control off, or with a
  Cache Rule that forces caching, it can. `private` protects those zones.
- `Telesphorepo-Version` from `config.json.version`. The server sends it on every answer, and the
  CLI reads it.
- `If-None-Match` that matches: 304. The Worker ignores `If-Modified-Since` and `If-Match`, as the
  origin's S3 path does. HEAD: the headers and `Content-Length` from the pointer, with no R2 read of
  the blob.
- One range (`bytes=a-b`, `a-`, `-n`) with an absent `If-Range` or an `If-Range` that equals the
  exact ETag: 206 with `Content-Range` from an R2 ranged get. Several ranges or an `If-Range` that
  does not match: the whole file.
- A range that the origin answers with 416 goes to the origin: a range outside the file,
  `bytes=-0`, and any range on an empty file. The origin's 416 is `{"error"}` with
  `application/json` and `no-store`.
- `Telesphorepo-Edge: hit; region=<hint>` marks an edge answer. Tests use it; it names no bucket
  and no secret.

With `s3.redirect` on (the default), the origin answers a GET with a 302 to a presigned URL, with
`Cache-Control: no-store` and no `Last-Modified`; Docker blobs redirect, manifests do not. The edge
streams the blob itself and answers 200. Clients follow the redirect, so the bytes are the same.

## 6. Uploads and the 100 MB limit

The Worker passes every method other than GET and HEAD to the origin, with its body streamed and
`x-real-ip` set (section 3). Cloudflare limits a request body to 100 MB on Free and Pro and to 200
MB on Business, with or without a Worker, so nothing changes for uploads. With
`docker.direct_uploads` on, the upload session `Location`s of Docker pushes point at the DNS-only
host of `docker.upload_url`, and the Worker has no route on that host. Maven, raw and Debian
uploads over the limit use the resumable upload routes in chunks, as today.

## 7. Testing

A new zone `edge` (tag `edge`, `go tool task test edge`) in `e2e/edge_test.go`. No unit tests and
no fakes: the tests run the real Worker and the real server.

**Fixture.** A container built from `edge/test/Containerfile` (Node, bun, the `edge/` sources) runs
`wrangler dev --local --ip 0.0.0.0 --test-scheduled --local-upstream <server>:8080` with a test
`wrangler.jsonc`: a home bucket and two regional buckets (local R2), the queue, and
`local_dev.experimental_s3_credentials` on each bucket. The server under test uses `storage=s3`
with the endpoint `http://edge:8787/cdn-cgi/local/r2/s3`, `s3.path_style=true` and
`s3.bucket=<home>`. Path style puts the bucket after the endpoint path, so the server reaches
`/cdn-cgi/local/r2/s3/<bucket>/<key>`, the local endpoint of each bucket. The server writes the
same local R2 that the Worker reads. `TR_BASE_URL` points at the Worker, because the Docker realm
uses `base_url`. The trusted proxies of the server name the Worker container. Clients (`mvn`,
Gradle, `apt`, `docker`, curl) point at the Worker; the pass-through reaches the server through
`--local-upstream`. Each test starts its own fixture and calls `t.Parallel()` first.

**Spike first** (before the build). A self-terminating script `edge/test/spike.ts` starts a wrangler
dev container, probes, prints the results and stops the container. It answers three questions:

1. Does the server's S3 storage (aws-sdk-go-v2, path style) work against
   `/cdn-cgi/local/r2/s3/<bucket>`: the storage probe, `Put`, `GetRange`, `List`, `DeleteMany` and
   a multipart upload? The script also records which client headers wrangler dev sets
   (`CF-Connecting-IP`, `x-real-ip`).
2. Does local R2 emit event notifications to a local queue?
3. How long does a read of a small object from a far bucket take? This runs on the real account
   only if the maintainer runs it. Until then it is an estimate: about 100 to 300 ms per read from
   another continent, against a few ms from a near bucket.

If question 1 fails, the `edge` zone runs against a real Cloudflare account, the same way as the
`e2e-edge-cloudflare` job: in CI with the secrets, and on the maintainer's machine with their token.
A different S3 server is not a fallback, because the Worker must read the same bucket that the
server writes.

The spike pins the exact Wrangler version in `edge/package.json`. The local S3 key is experimental
(Wrangler 4.115.0 added it), and Miniflare has renamed it upstream, so a version bump reruns the
spike.

**The notification path.** If local R2 emits event notifications, the local zone also tests the
path from a home put to a regional copy. If it does not, the job `e2e-edge-cloudflare` of `ci.yml`
tests it against a real Cloudflare account. The maintainer provides the account and the token as CI
secrets; agents never touch the account. The job provisions a home bucket and one regional bucket
with unique names, the queue and the notification rule, deploys the Worker, puts and deletes blobs
through S3, waits for the regional copies and deletions, and removes everything it made. The local
zone tests the consumer through the real scheduled gap check (`/cdn-cgi/local/scheduled`).

**What the tests prove.**

- *Parity matrix.* For each format, each credential (none, read token by Basic and by Bearer, core
  bearer of each kind on oci, a core bearer on standard, password, session, publish session, device
  proof, bad token), each repo setting (public, private with and without a grant,
  `repositories.all`, read IP rule) and each kind (hosted, proxy, group with a private member, a
  guarded member and a proxy member): the same request to the Worker and to the server directly
  gives the same status, the same body bytes and the same values of an allowlist of headers, which
  includes `Telesphorepo-Version` and `Cache-Control`. The matrix runs with `s3.redirect=false`, so
  the origin streams and the headers compare. One case with `s3.redirect=true` compares the body
  after the redirect. Each case also asserts `Telesphorepo-Edge`, so the matrix proves which
  requests the edge answers and that it does not pass everything.
- *Client IP.* A request with a spoofed `X-Real-IP` still reaches the origin with the client
  address that the Worker got.
- *Revocation* (with `edge.access_ttl=1s`): token delete, user disable, grant removal, repo made
  private, device revoke and an IP rule each turn edge hits into origin answers within 2 s.
- *Freshness.* A file delete and a Maven republish show at the edge at once. A Docker tag move
  serves the new digest. A release replaced in the first member of a group never serves the second
  member's copy.
- *Copies.* A new blob reaches both regions; the Worker reads the home blob before that. Deleting a
  regional copy by hand and running the cron restores it. A blob GC removes the regional copies.
- *Regions.* Adding a region fills it and marks it ready; removing one stops the writes to it.
- *Range, HEAD, 304, 416, If-Range* on both paths.
- *Fail open.* `edge.enabled=false`, a version mismatch, and a malformed pointer that the test
  writes through S3 each give origin answers, not errors.
- *Real clients.* `mvn` and Gradle resolve, `apt-get install` and `docker pull` through the Worker,
  for a public and a private repo.
- *`last_used_at`* moves after a read-token download at the edge.

**Adding the zone.**

- `tools/task/build.go`: `testZones`, the switch and `all`; the help text in `tools/task/main.go`.
- `|| edge` in the build tags of `e2e/harness_test.go`, `e2e/main_test.go` and
  `e2e/clients_test.go`.
- `go vet -tags edge ./e2e/...` in `.github/workflows/ci.yml`, and the zone in the options of
  `.github/workflows/e2e-extended.yml`.
- AGENTS.md: the command table, six tags, `bun run build` and `bunx tsc --noEmit` in `edge/` for a
  subagent, and the "Which zone" rule: run the `edge` zone when a change touches `internal/edge`,
  `internal/assets`, `internal/access`, `internal/auth`, `internal/storage` or `edge/`.

## 8. Costs and limits

Workers Paid: $5 a month with 10 M requests and 30 M CPU ms; then $0.30 per million requests and
$0.02 per million CPU ms; subrequests are not billed. Limits: 30 s CPU per request by default (5 min
max, `limits.cpu_ms`), 128 MB memory per isolate, 10,000 subrequests per invocation by default
(configurable up to 10 M), 6 connections waiting for response headers at once, Cache API 1,000
calls per request.

R2 Standard: $0.015 per GB-month; class A $4.50 per million (PutObject, CopyObject, ListObjects,
multipart and its parts); class B $0.36 per million (GetObject, HeadObject); DeleteObject free;
egress free; free tier 10 GB, 1 M class A and 10 M class B a month. The pricing page names only
DeleteObject as free; this spec counts `DeleteObjects` as free too, which is an estimate. Single
upload 5 GiB (4.995 GiB), multipart 10,000 parts of equal size, one write per second per key (429
above), strong read-after-write and list-after-write consistency.

Queues: 1 M operations a month included, then $0.40 per million; each message costs a write, a read
and a delete per 64 KB, and each retry adds a read; 5,000 messages per second per queue, batches of
100, 15 min wall time per consumer run. Event notifications: 100 rules per bucket, no overlapping
rules.

Operations per edge download: one pointer read (up to one per member for a group), one blob read,
and 0 to 3 access-object reads on a Cache API miss. Per stored file: one home pointer put, plus one
per region for an immutable path; a replacement of an immutable path adds one pending put per
bucket. Per new blob: one queue message and one put per region. The idle cron costs one class B
read per run.

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

**Module.** `internal/edge`, module `edge`, with `Requires: storage, network, publish`, after
`publish` in `modules.go`. It owns the schema (`edge_outbox`, `edge_written`, `edge_state`), the
jobs `edge.outbox`, `edge.backfill`, `edge.reconcile` (5 min) and `edge.seen` (1 min), and the
routes `GET /api/edge/status` and `GET /api/edge/wrangler.jsonc`, each with
`route.UIPerm(edge.manage)`. The routes answer RFC 9457 problem documents, and the module declares
its named problems with `httpx.DeclareProblem`. A format opts in with
`format.Spec.Edge{Shape, Immutable, Mutable}`; a format without it gets no Worker route.

**Settings group `edge`** (permission `edge.manage`, page `/settings/edge`):

| Key | Type | Default | Rule |
|---|---|---|---|
| `edge.enabled` | bool | false | Needs `storage=s3` and a `base_url` without a path |
| `edge.home_hint` | string | `""` | One of `wnam enam weur eeur apac oc`; required when `edge.enabled` is true |
| `edge.regions` | list | `[]` | Entries `<hint>=<bucket>`, hint one of `wnam enam weur eeur apac oc`, each hint once, not `edge.home_hint`, bucket not the home bucket |
| `edge.access_ttl` | duration | `30s` | 0 to 5 min |

`Test` runs `storage.Probe` on the home bucket and on each regional bucket. Permission
`edge.manage` ("Manage edge delivery", subsystem `edge`, second factor). No new audit actions:
`settings.update` covers the changes.

**Page "Edge delivery"** (Settings, Server group):

- **Status.** On or off; the Worker version and when the gap check last ran (from
  `gap/state.json`); a version mismatch; "deploy needed" after a storage or region change; the home
  bucket.
- **Regions.** A table with one row per R2 location hint, six rows. Each row shows the hint and its
  area ("Western North America (wnam)" and so on), a checkbox "Serve from this region", a bucket
  name field (the default is `<home bucket>-<hint>`), and the state: not used, filling (pointers
  written of the total, blobs missing), ready, or waiting for a deploy. The row of
  `edge.home_hint` shows "Home bucket" and has no checkbox. A select above the table sets
  `edge.home_hint`.
  - The admin checks or clears rows and clicks Save. The page writes `edge.home_hint` and
    `edge.regions` through the settings API and runs `Test`. A failed probe shows its error on the
    row and saves nothing.
  - Clearing a row opens a `ConfirmDialog`: "The server stops writing to bucket `<bucket>`. Deploy
    the Worker again, then delete the bucket in Cloudflare."
  - After a save that changes the regions, a banner says: "Run `bun run provision`, then `bun run
    deploy`." Provision creates each new bucket with its location hint.
  - Each row also shows the pointers waiting in the outbox and the last error.
- **Backfill.** The progress per region while it runs.
- **Settings.** `edge.enabled` and `edge.access_ttl`.
- **Deploy.** The generated `wrangler.jsonc` in a `CodeBlock`, the commands `bun run provision` and
  `bun run deploy`, and the token permissions they need.

The access report of a repo adds one line: "Downloads come from the edge" or "Downloads go to the
server: read rules apply".

## Decisions

The maintainer's decisions:

1. Consistency before latency: mutable paths have their pointer in the home bucket only; immutable
   paths have regional pointers and regional blobs.
2. Downloads are warm everywhere from the first request: no design where a region gets a file only
   after a first miss.
3. The admin chooses the regions in the UI. The server has no default region list.
4. Cloudflare only: R2, Queues and Workers.
5. Nothing is in production: core parts change freely, `schema.sql` files change in place, with no
   migrations and no compatibility shims.

The spec author's calls:

6. Workers Cache is not the delivery path: it evicts, so a far region is not always warm.
7. Sippy is not the delivery path: it copies a file only on the first miss.
8. The copy Worker streams `get().body` into `put` or multipart `uploadPart`. It uses no
   cross-bucket `CopyObject`.
9. The server does not check the endpoint host. `edge.enabled` needs `storage=s3`, and the group
   `Test` probes the home bucket and every regional bucket.
10. `storage.Store` gets `DeleteMany` (S3 `DeleteObjects`, up to 1000 keys), so a `Remove` batch
    costs one call per bucket.
11. The outbox worker takes `Pipeline.Lock(repo, path)`, reads the asset row again, and puts only
    when the row exists with the same `sha256`; else it deletes the pointer. A dirty row before
    each pre-step and an outbox row in each commit and remove transaction keep it complete.
12. A removed region: the server stops writing to its bucket, and the page tells the admin to
    redeploy and to delete the bucket.
13. Tests run the real Worker in wrangler dev with local R2 and local Queues. The consumer is tested
    through the real scheduled gap check. The notification path is tested locally if local R2
    emits notifications, else in a CI job against a real Cloudflare account. No stand-in producer,
    no SeaweedFS copy fixture.
14. A spike script answers the three open questions before the build and pins the Wrangler
    version.
15. `edge.access_ttl` defaults to 30 s.
16. A valid read token from an address that the origin blocked for failed attempts still downloads
    at the edge. The failed attempts still reach the origin and count there.
17. Uploads stay on the origin. Docker uploads over the body limit use `docker.direct_uploads`.
18. Download snippets for private repos use a read token, never a password.

## Sources

- R2 data location (hints): https://developers.cloudflare.com/r2/reference/data-location/
- R2 Snowflake regions (São Paulo to `enam`): https://developers.cloudflare.com/r2/reference/partners/snowflake-regions/
- R2 limits: https://developers.cloudflare.com/r2/platform/limits/
- R2 error codes (429, 10058): https://developers.cloudflare.com/r2/api/error-codes/
- R2 consistency: https://developers.cloudflare.com/r2/reference/consistency/
- R2 event notifications: https://developers.cloudflare.com/r2/buckets/event-notifications/
- Wrangler R2 commands: https://developers.cloudflare.com/workers/wrangler/commands/r2/
- R2 Workers API (methods, ranged reads, multipart): https://developers.cloudflare.com/r2/api/workers/workers-api-reference/
- R2 S3 API: https://developers.cloudflare.com/r2/api/s3/api/
- R2 uploads: https://developers.cloudflare.com/r2/objects/upload-objects/
- R2 pricing: https://developers.cloudflare.com/r2/pricing/
- R2 changelog: https://developers.cloudflare.com/changelog/product/r2/
- Queues delivery guarantees: https://developers.cloudflare.com/queues/reference/delivery-guarantees/
- Queues limits: https://developers.cloudflare.com/queues/platform/limits/
- Queues pricing: https://developers.cloudflare.com/workers/platform/pricing/#queues
- Queues local development: https://developers.cloudflare.com/queues/configuration/local-development/
- Workers limits: https://developers.cloudflare.com/workers/platform/limits/
- Workers pricing: https://developers.cloudflare.com/workers/platform/pricing/
- `passThroughOnException`: https://developers.cloudflare.com/workers/runtime-apis/context/#passthroughonexception
- Routes and same-zone `fetch()`: https://developers.cloudflare.com/workers/configuration/routing/routes/
- `CF-Connecting-IP` in Worker subrequests: https://developers.cloudflare.com/fundamentals/reference/http-headers/#cf-connecting-ip-in-worker-subrequests
- Cache API: https://developers.cloudflare.com/workers/runtime-apis/cache/
- Default cache behavior: https://developers.cloudflare.com/cache/concepts/default-cache-behavior/
- Cacheable responses and `Authorization`: https://developers.cloudflare.com/cache/concepts/cache-responses/
- Web Crypto (Ed25519, HKDF, `timingSafeEqual`): https://developers.cloudflare.com/workers/runtime-apis/web-crypto/
- Cron triggers and `--test-scheduled`: https://developers.cloudflare.com/workers/configuration/cron-triggers/
- Wrangler `dev` flags: https://developers.cloudflare.com/workers/wrangler/commands/workers/
- Wrangler 4.115.0 local R2 S3 endpoint: https://releases.sh/release/rel_Ep-sFGOtu356Mx2P2iRwq
- Miniflare 5.20260820.0-alpha key rename: https://newreleases.io/project/github/cloudflare/workers-sdk/release/miniflare@5.20260820.0-alpha
- Config explorer (`dev.experimentalS3Credentials`): https://developers.cloudflare.com/cf/projects/config-explorer/
- Workers Cache (decision 6): https://developers.cloudflare.com/workers/cache/, https://developers.cloudflare.com/workers/cache/limitations/
- Sippy (decision 7): https://developers.cloudflare.com/r2/data-migration/sippy/, https://developers.cloudflare.com/changelog/post/2026-07-24-r2-sippy-azure-s3-compatible-support/
