# Edge delivery

This note records the design for fast downloads far from the server. It is not built yet. The
order of work is: more formats, then edge delivery.

## Decisions

- **One origin.** One Telesphorepo server is the origin. A warm standby copies the SQLite
  database to the bucket. There is no multi-master cluster.
- **Cloudflare only.** Edge delivery uses Cloudflare R2, Queues and Workers (the Workers Paid
  plan). Other vendors can come later as contributions. The bucket layout below is the contract.
- **Client URLs never change.** The Worker runs on path routes of the main hostname, for example
  `repo.example.com/maven/*`, `/debian/*` and `/v2/*`. The server builds the routes
  from the URL prefixes of its formats. The admin UI and `/api` have no route.
- **No client-side part.** Clients use plain HTTP. There is no bundle or manifest download.

## Bucket layout

The server writes to the home R2 bucket:

- Content-addressed blobs.
- `edge/` pointer files: a path maps to a digest, a size, a content type and the regions that
  hold the blob.
- Small access objects, one per token hash and one per repo. Many small objects keep the
  Worker inside its CPU limit.

## Copies to the regions

There is one R2 bucket per chosen region, for example North America, South America, Europe and
Oceania. The location hint of a bucket is fixed when the bucket is created. R2 has no bucket
replication, so a Worker copies the blobs:

1. R2 event notifications (object create and delete, with a prefix filter) go to a Queue.
2. A Worker reads the Queue. It copies each new blob to the regional buckets, with multipart
   uploads for large blobs. It removes a copy after the server deletes the blob.
3. A scheduled Worker runs a nightly gap check in chunks and keeps a cursor between runs.

## Requests

- The Worker answers GET and HEAD from the nearest regional bucket. Until the copy of a new blob
  is done, it reads the home bucket.
- The Worker passes every other request to the origin with `fetch(request)`, and also every
  request it cannot answer. A cache miss of a proxy repo goes to the origin.
- Past the free limits, the routes fail open to the origin.

## Uploads through Cloudflare

Cloudflare limits a proxied request body to 100 MB on the Free and Pro plans, with or without a
Worker. The core has resumable chunked upload sessions, so a client that uploads in chunks
keeps each chunk under the limit. Every common OCI client sends a layer in one request, so each
layer must be under 100 MB. The `docker.direct_uploads` setting lifts the limit: a push starts on
the proxied hostname, and the server answers the upload start with an upload URL on
`docker.upload_url`, a DNS-only record of the origin. This makes the origin IP public. Downloads
stay on the proxied hostname.
