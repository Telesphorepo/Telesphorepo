# Telesphorepo edge Worker

The Cloudflare Worker of Telesphorepo's edge delivery. Clients far from your server download from
Cloudflare R2 buckets near them. The Worker answers a download only when it can prove that your
server would give the same answer, and it passes every other request to your server.

You need:

- a Telesphorepo server with `storage = s3` on Cloudflare R2 and a base URL without a path,
- the base URL host on a Cloudflare zone of the account of the R2 buckets, not behind Cloudflare
  Access, and each repository domain (Settings › General) on a zone of that account too: a route
  must be on a zone of the Worker's account, so take the route of a domain elsewhere out of the
  `wrangler.jsonc`, and the server serves that domain itself,
- for a deploy from the server, a Cloudflare API token; for a deploy from your computer, [bun](https://bun.sh),
  Node 22 or newer and a Cloudflare login with access to Workers, R2 and Queues of the account of
  the R2 buckets.

## Deploy from the server

The server can deploy the Worker itself, with no computer in between:

1. In the Cloudflare dashboard, open the account of the R2 buckets, go to **Manage account ›
   Account API tokens**, select **Create Token**, then **Custom**, and add two permission
   policies:
   - **Entire Account:** Developer Platform › Workers: **Admin**, Developer Platform › Workers R2
     Storage: **Edit**, Developer Platform › Queues: **Edit**
   - **All Domains** (or the domains of the base URL host and of each repository domain):
     Developer Platform › Workers Routes: **Edit**, DNS & Zones › Zone: **Read**

   Workers needs Admin, not Editor: Editor cannot create a Worker, and the first deploy creates
   it. Cloudflare's "Edit Cloudflare Workers" template lacks Queues, so start from Custom.
2. In **Settings › Edge delivery**, choose the home region and the regions to serve from, set the
   token in the Deploy section, and save.
3. Select **Deploy now**.

The server reads the account from the S3 endpoint of its storage settings
(`https://<account ID>.r2.cloudflarestorage.com`). It creates each missing regional bucket, the
copy queue and the notification rule of the home bucket, uploads the Worker of its own version,
makes it the consumer of the queue, sets its cron and its routes, turns off its workers.dev
address, and checks it. Each step skips what is already right. It never takes over a route or a
queue of another Worker: it stops and names it. With **Deploy on its own** on, the server deploys
again 10 seconds after a change of the regions, the repository domains or the storage location,
after an upgrade, and when the deployed Worker is outdated. A failure shows on the page with its
step, Cloudflare's error code and message, and what to do; the server tries again after 1 minute,
then longer, up to an hour.

## Deploy from your computer

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
deploy). In CI, set `CLOUDFLARE_API_TOKEN` in place of the login, a token with the permissions of
**Deploy from the server**.

Then turn edge delivery on in the web UI. The switch turns on once the Worker has reported its
version: the Edge delivery page checks the Worker every 30 seconds until then, and you can select
**Check now** to check at once. The check reaches the Worker through Cloudflare at
`/.well-known/telesphorepo-edge/check` on your host, a route of the `wrangler.jsonc`. When it says
that the request reached the server, the Worker is not deployed on that host or route: deploy it
again. Regions fill while edge delivery is on: the server writes their pointers, and the Worker
copies their files at each gap check (every 10 minutes, or Check now).

After you change the regions, download the Worker again and run provision and deploy. After you
add or remove a repository domain, download it again and deploy. When Settings
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
