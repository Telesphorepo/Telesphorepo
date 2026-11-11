# Telesphorepo

A self-hosted package repository: Maven and Gradle, npm, PyPI, Cargo, NuGet, Docker and Helm,
Debian, Arch, RPM and plain files. One Go binary with a web UI. Files go on local disk or in any
S3-compatible bucket.

## Run it

```sh
docker run -d --name telesphorepo -p 8080:8080 \
  -v telesphorepo-data:/data -v telesphorepo-backups:/backups \
  ghcr.io/telesphorepo/telesphorepo:latest
```

Get the admin password it printed on first start:

```sh
docker logs telesphorepo
```

Open http://localhost:8080, sign in as `admin`, and set everything else up on the **Settings**
page: storage, the public URL, single sign-on, backups. Settings apply without a restart.

Image tags: `latest` is the newest release, `main` the newest build of `main`. Binaries for Linux,
macOS and Windows are on the [Releases](https://github.com/Telesphorepo/Telesphorepo/releases)
page; run `./telesphorepo-server serve`.

No environment variables are required. The optional ones:

| Variable | Default | What it is |
|---|---|---|
| `TR_DATA_DIR` | `/data` | Where the database and local files live |
| `TR_LISTEN` | `:8080` | Listen address |
| `TR_MASTER_KEY` | generated in the data folder | Key that encrypts secret settings (base64, 32 bytes) |

Any setting can also be forced from the environment, for example `TR_COOKIE_SECURE=false` on
plain HTTP.

### Behind a reverse proxy

Put a TLS proxy in front. With [Caddy](https://caddyserver.com), the whole `Caddyfile` is:

```
packages.example.com {
	reverse_proxy localhost:8080
}
```

Then open **Settings > Server > Network and proxy** through the proxy once and click **Trust this
address**, so the server sees real client addresses. Behind Cloudflare, also tick the Cloudflare
network list there.

### Users

```sh
docker exec -it telesphorepo /telesphorepo-server user add alice           # asks for the password
docker exec -it telesphorepo /telesphorepo-server user add alice --admin
docker exec -it telesphorepo /telesphorepo-server user passwd alice
docker exec -it telesphorepo /telesphorepo-server user reset-2fa alice     # lost authenticator
```

### Backups

The server backs up into `/backups` every day and keeps the last 7 (change it in **Settings >
Server > Backups**). A backup holds the database, the master key and, on local disk, the files. On
S3, back up the bucket too.

Back up now:

```sh
docker exec telesphorepo /telesphorepo-server backup /backups
```

Restore into an empty volume, then start the server on it:

```sh
docker stop telesphorepo
docker run --rm -v telesphorepo-backups:/backups -v telesphorepo-restored:/data \
  ghcr.io/telesphorepo/telesphorepo:latest restore /backups/20261020T201500.123Z
```

## Publish and download

Each repo page in the UI shows the snippets for its clients (Maven, npm, pip, apt, dnf, Docker...).

To publish, install the CLI from your server:

```sh
curl -fsSL https://packages.example.com/install.sh | sh          # Linux and macOS
```

```powershell
irm https://packages.example.com/install.ps1 | iex               # Windows
```

On a repo's **Publish** tab, click **Set up a project** and run the line it shows in your project
folder. Commit the `telesphorepo.toml` it writes. From then on:

```sh
telesphorepo publish
```

It runs your project's own publish command (`mvn deploy`, `npm publish`, `cargo publish`,
`dotnet pack`, `helm push`...). Docker pushes work without `docker login`. For CI, an admin adds a
trust policy under **Settings > People and CI > CI pipelines**; the job then runs the same
`telesphorepo publish` with no stored secret.

Coming from Nexus 3? Add it on the **Nexus** page: the server mirrors your repos until you cut
over. See [`skills/`](skills/README.md) to move projects with a coding agent.

## Develop

You need [Go](https://go.dev/dl/), [bun](https://bun.sh) and Node 26 (`.node-version`). The tests
also need Docker or Podman.

```sh
go tool task doctor    # check what is installed
go tool task dev       # server on :8080, UI with live reload on http://localhost:5173
```

The first start fills the dev server with demo repos and users; the banner prints their
passwords. Data lives in `.telesphorepo-dev/`; delete it to start over.

```sh
go tool task build                     # bin/telesphorepo-server with the UI built in
go tool task build image               # the container image telesphorepo:local
go tool task gen                       # regenerate the SQL code after changing queries/*.sql
go tool task migration db add_thing    # create the next migration file
go tool task test                      # core e2e tests
go tool task test core clients         # several zones at once
go tool task test -run TestMaven       # some tests only
```

The e2e tests have a time budget and runs are spaced at least 5 minutes apart: finish a change,
then test it. [AGENTS.md](AGENTS.md) has the working rules, and [SPEC.md](SPEC.md) explains how
the server works.

## License

The server is AGPL-3.0 ([LICENSE](LICENSE)). The CLI (`cmd/telesphorepo`) is MIT, in its own
LICENSE file.
