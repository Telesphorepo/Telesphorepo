# Telesphorepo edge Worker

The Cloudflare Worker of Telesphorepo's edge delivery. Clients far from your server download from
Cloudflare R2 buckets near them. The Worker answers a download only when it can prove that your
server would give the same answer, and it passes every other request to your server.

You need:

- a Telesphorepo server with `storage = s3` on Cloudflare R2 and a base URL without a path,
- the base URL host on a Cloudflare zone, not behind Cloudflare Access,
- [bun](https://bun.sh) and Node 26 or newer,
- a Cloudflare API token with Workers Scripts edit, Workers Routes edit, R2 edit and Queues edit.

## Deploy

1. In the web UI, open **Settings > Edge delivery**. Choose the home region and the regions to
   serve from, turn edge delivery on, and save.
2. Download `wrangler.jsonc` from the same page and save it in this folder. When you deploy from
   the release archive `telesphorepo-edge_<version>.tar.gz`, set `"main": "dist/index.js"` and
   add `"no_bundle": true` in it.
3. Run:

   ```sh
   bun install
   CLOUDFLARE_API_TOKEN=... bun run provision
   bun run deploy
   ```

`provision` creates each regional bucket with its location hint, the copy queue and the event
notification rule of the home bucket. It skips what exists, so you can run it again. Run it
again after you change the regions, then deploy. Deploy the Worker of the same version for each
server release: a Worker of another version sends every request to your server.

After you remove a region, deploy again, then delete its bucket in Cloudflare.

## Develop

```sh
bun install
bunx tsc --noEmit    # type check
bun run build        # bundle into dist/ (wrangler deploy --dry-run)
```

`test/` holds the fixture of the `edge` e2e zone (`go tool task test edge` at the repository
root): the Worker in `wrangler dev` with local R2 and Queues in front of a real server. Wrangler is
pinned to an exact version because the local S3 endpoint of the tests is experimental; rerun the
`edge` zone before you change it.
