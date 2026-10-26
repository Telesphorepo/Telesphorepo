# Repository domains

A repository domain serves the files of one repository at the root of a domain of its own:
`https://updater.example.com/v1/latest.json` reads `v1/latest.json` of the repository, the same
file as `https://repo.example.com/raw/releases/v1/latest.json`. Use it for an address you hand
out once and never want to change, such as the update URL built into an application.

A domain only reads. GET and HEAD work; any other method gets `405` with `Allow: GET, HEAD`, so
publish at the base URL as before. `/` is `404`, and no page of the web UI is served on a domain.
Every format but Docker can have a domain; a Docker client always reads from `/v2/`.

## Set one up

1. Point the domain at the server: give it the same A and AAAA records as the base URL host.
2. Add the domain to the site block of your reverse proxy, so it gets a certificate and reaches
   the server. With Caddy, list it next to the base URL host:

   ```caddy
   repo.example.com, updater.plex.us.org, plex-updater.com {
   	reverse_proxy localhost:8080
   }
   ```

3. Open **Settings › General** and add a line to **Repository domains** for each domain, as
   `<domain>=<repository>`, for example `updater.plex.us.org=plex-updates`. The setting refuses a
   repository that does not exist, a Docker repository and an address of the server itself.

The server takes the domain at once. A domain whose repository is renamed keeps working through
the redirect of the old name. A domain whose repository is deleted answers `404`.

## Aliases of the base URL

A domain that should open the whole server, such as an old name or a second top-level domain, is
no repository domain. Redirect it to the base URL in the reverse proxy:

```caddy
telesphorepo.net, telesphorepo.org {
	redir https://telesphorepo.com{uri} permanent
}
```

Once the base URL is set, the server answers only for its host, `localhost`, `127.0.0.1`, `::1`
and the Docker upload host. A request to any other host that is no repository domain gets
`421 Misdirected Request`.

## Private repositories

A domain keeps the access of its repository. A public repository reads without credentials. A
private one answers `401` with a Basic challenge, and reads with:

- HTTP Basic with a password, or with a read token as the password:
  `curl -u alice:trp_... https://updater.example.com/v1/latest.json`
- a read token as a Bearer token: `Authorization: Bearer trp_...`
- a device of the CLI.

Create a read token for the application under **Account › Tokens**, signed in as an account that
may download from the repository. A signed-in browser session does not count on a
domain: the browser keeps its cookie for the base URL host alone, and the server ignores one on a
domain.

## Edge delivery

With edge delivery on, the Worker serves the domain too: its configuration has a route
`<domain>/*` for each domain. With a Cloudflare API token in **Settings › Edge delivery** and
**Deploy on its own** on, the server deploys the new routes itself 10 seconds after you add or
remove a domain (or select **Deploy now**), and deletes the route of a removed domain. Without a
token, download the Worker again (or the `wrangler.jsonc`) and run `bun run deploy`. Until the
route is deployed, the server serves the domain itself. A Worker of a release without repository domains
sends every request to the server while a domain is listed, and the page says why.

A Worker route must be on a zone of the Cloudflare account of the Worker. A domain whose zone is in
another Cloudflare account, or not on Cloudflare, gets no edge: take its route out of
`wrangler.jsonc` before you deploy, and the server serves that domain itself. The deploy from the
server stops at such a domain and names it and the account of its zone; deploy from a computer
then.
