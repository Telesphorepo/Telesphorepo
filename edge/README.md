# Telesphorepo edge Worker

The Cloudflare Worker of Telesphorepo's edge delivery. Clients far from your server download from
Cloudflare R2 buckets near them. The Worker answers a download only when it can prove that your
server would give the same answer, and it passes every other request to your server.

You need:

- a Telesphorepo server with `storage = s3` on Cloudflare R2 and a base URL without a path,
- the base URL host on a Cloudflare zone, not behind Cloudflare Access,
- [bun](https://bun.sh) and Node 22 or newer,
- a Cloudflare login with access to Workers, R2 and Queues of the account of the R2 buckets.

## Deploy

1. In the web UI, open **Settings > Edge delivery**. Choose the home region and the regions to
   serve from, and save.
2. In the Deploy section, download the edge Worker. The zip holds the Worker of your server's
   version, built and ready (`dist/index.js`), with the `wrangler.jsonc` of your settings.
3. Unpack it, and in the `telesphorepo-edge` folder run:

   ```sh
   bun install            # wrangler, at the version the Worker is built with
   bunx wrangler login    # once: sign in to Cloudflare in the browser
   bun run provision
   bun run deploy
   ```

`provision` creates each regional bucket with its location hint, the copy queue and the event
notification rule of the home bucket. It skips what exists, so you can run it again. It asks
nothing: when your login reaches several Cloudflare accounts, set `CLOUDFLARE_ACCOUNT_ID` to the
account of the buckets (`CLOUDFLARE_ACCOUNT_ID=<id> bun run provision`, and the same for
deploy). In CI, set `CLOUDFLARE_API_TOKEN` in place of the login, with Workers Scripts edit,
Workers Routes edit, R2 edit and Queues edit.

Then turn edge delivery on in the web UI. The switch turns on once the Worker has reported its
version: the Edge delivery page checks the Worker every 30 seconds until then, and you can select
**Check now** to check at once. The check reaches the Worker through Cloudflare at
`/.well-known/telesphorepo-edge/check` on your host, a route of the `wrangler.jsonc`. When it says
that the request reached the server, the Worker is not deployed on that host or route: deploy it
again. Regions fill while edge delivery is on: the server writes their pointers, and the Worker
copies their files at each gap check (every 10 minutes, or Check now).

After you change the regions, download the Worker again and run provision and deploy. When Settings
› Overview says the Worker is outdated, download it again and deploy it: until then it sends every
request to your server. Other server releases need no deploy. After you remove a region, deploy again, then delete its bucket in
Cloudflare.

## Test the regions

The **Speed test** on the Edge delivery page downloads a file through the Worker from the bucket
you pick (a ready region, the home bucket, or your server with no Worker) and times it in your
browser, so you can compare the regions from where you sit. Open the page at your base URL: the
browser downloads from the address the page runs on. The page asks your server for a pass that
works for 10 minutes and sends it with the download in `Telesphorepo-Edge-Pass`, with the bucket in
`Telesphorepo-Edge-Region`. The Worker takes the choice only after its usual checks of the
download, so a pass never lets anyone download more than they could without it. The answer says
which bucket served it (`Telesphorepo-Edge: hit; region=<hint>`) and what became of the choice
(`Telesphorepo-Edge-Override`). An outdated Worker sends every download to your server, so deploy it
again first.

### From a checkout of the repository

Open **Deploy from a checkout of the repository** in the Deploy section, save its
`wrangler.jsonc` in this folder, and run the same four commands here. That `wrangler.jsonc`
points at `src/index.ts`, and deploy bundles the Worker of your server's version from it. Check
out the commit of your server's version first.

## Develop

```sh
bun install
bunx tsc --noEmit    # type check
bun run build        # bundle into dist/ (wrangler deploy --dry-run)
```

`go tool task build` bundles the Worker into the server (`internal/edgedist`), which hands out the
download; `go tool task release` also packs it as `telesphorepo-edge_<version>.tar.gz`.
