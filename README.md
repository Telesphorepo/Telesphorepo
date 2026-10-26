# Telesphorepo

A small self-hosted package repository for Maven and Gradle artifacts, npm packages, Python
packages, Rust crates, NuGet packages, container images and Helm charts (OCI), Debian packages and
raw files. Each format has hosted repos; every format but raw also takes proxies of a public
registry, and every format but raw and Debian takes groups that put several repos behind one URL.
It is a single Go binary with a web UI, and
stores files on local disk or in any S3-compatible bucket.

## Run it locally

You need [Go](https://go.dev/dl/), [bun](https://bun.sh) and Node 26. Docker or Podman is needed
only for the tests.

```sh
go tool task dev
```

Open **http://localhost:5173**. The startup banner prints the admin username and password and
some client snippets. The first run fills the instance with the same demo data each time, before the
server starts: about 150,000 Maven files, and npm, Python, Rust, NuGet and Debian packages, raw
files and container images, in the public repos `maven`, `npm`, `pypi`, `crates`, `nuget`,
`debian`, `raw` and `docker`, a
private Maven repo `internal`, and five demo users (their passwords are in the banner). To start with
empty repos, run the binary as `telesphorepo-server dev --empty` on a fresh data dir. Data lives in
`.telesphorepo-dev/`; delete it to start over. Press Ctrl+C to stop. The dev server does not enforce the second factor.

## Run it in production

```sh
docker run -d -p 8080:8080 -v telesphorepo-data:/data -v telesphorepo-backups:/backups ghcr.io/telesphorepo/telesphorepo:latest
```

The image supports `linux/amd64` and `linux/arm64`. `:latest` is the newest release; `:main` is the newest green build of `main`, and `:sha-<commit>` pins one build. To build the image yourself, run
`go tool task build image`. It builds `telesphorepo:local`.

You can also download a binary for Linux, macOS or Windows (amd64 or arm64) from the
[GitHub Releases](https://github.com/Telesphorepo/Telesphorepo/releases) page. Check the
download against `SHA256SUMS`, unpack it, and run `./telesphorepo-server serve`.

- **Admin password:** on first start the server prints a generated `admin` password once to
  the container log (`docker logs <container>`).
- **Configuration:** sign in, then set up storage (local disk or S3/R2), the public URL, and
  the rest on the **Settings** page. The server creates a Debian signing key named `default` on the first start. Changes apply without a restart, and
  secrets are stored encrypted.
- **Single sign-on:** people can sign in through an OIDC provider (Keycloak, Entra, Google...).
  Set the issuer and the client under **Settings > Server > Sign-in options**, and register
  `<public URL>/api/auth/oidc/callback` at the provider. To give server permissions from the
  provider, set a claim and map each value of it to permissions on the same page. A person who
  signs in with single sign-on approves a laptop for the CLI with a new sign-in at the
  provider; a local account approves it with a second factor.
- **Passkeys:** a passkey belongs to one host name. Until the public URL (`base_url`) is set, it takes the address in the browser, so set `base_url` to that same address. They need HTTPS (or `localhost`); authenticator-app codes do not.
- **Environment variables:** none are required. Three are optional:

| Variable | Default | |
|---|---|---|
| `TR_DATA_DIR` | `/data` in the image | Database, upload spool, local blobs, master key |
| `TR_LISTEN` | `:8080` | Listen address |
| `TR_MASTER_KEY` | Generated into the data dir | Key that encrypts secret settings (base64, 32 bytes); pass it from a secret manager to keep it off the volume |

Any setting can also be forced from the environment, e.g. `TR_COOKIE_SECURE=false` when the
server is served over plain HTTP. A forced setting is read-only in the UI.

## Back up and restore

The image backs itself up into `/backups` every 24 hours and keeps the newest 7. Mount a volume
there, best on another disk:

```sh
docker run -d -p 8080:8080 -v telesphorepo-data:/data -v telesphorepo-backups:/backups ghcr.io/telesphorepo/telesphorepo:latest
```

The folder, the schedule and the number to keep are under **Settings > Server > Backups**, next to
**Back up now**. A backup holds the database, the master key and, with local disk storage, the
stored files. Files in an S3 bucket stay in the bucket, so back up the bucket too; a key in
`TR_MASTER_KEY` is not written, so keep it with the backups.

To back up from the command line, also while the server runs: `telesphorepo-server backup /backups`.
To restore, stop the server and restore a backup folder into an empty data volume:

```sh
docker run --rm -v telesphorepo-backups:/backups -v telesphorepo-restored:/data \
  ghcr.io/telesphorepo/telesphorepo:latest restore /backups/20261020T201500.123Z
```

Then start the server on `telesphorepo-restored`.
## Behind a reverse proxy

Run Telesphorepo behind a reverse proxy that ends TLS. [Caddy](https://caddyserver.com) is the
simplest. This `Caddyfile` gets a certificate and forwards all requests to the server on port 8080:

```
packages.example.com {
	reverse_proxy localhost:8080
}
```

Caddy sets `X-Forwarded-For`. The server trusts that header only from the proxies in the setting
`trusted_proxies`. By default the list is empty, so the server uses the address of the TCP peer.
The server uses the client address for rate limits, the audit log and the IP rules of repos.

To trust your proxy:

1. Start Caddy and open the server through it once.
2. Open **Settings > Server > Network and proxy**. The page shows "Requests from 10.89.0.5
   carry forwarded headers. Is this your reverse proxy?" with the proxy's address.
3. Click **Trust this address**.

Or set the list yourself, for example `TR_TRUSTED_PROXIES=10.89.0.5`.

- **Docker and Podman:** with port publishing, every outside connection can arrive from a private
  gateway address. Trust the proxy's own address, not `private`, so that a client cannot forge
  its address.
- **Cloudflare:** put `list:cloudflare` in `trusted_proxies`. On the Network and proxy page, tick the network list Cloudflare. The server then trusts Cloudflare's published ranges, which it refreshes daily. If Caddy sits between Cloudflare and the server, list Caddy's address and `list:cloudflare`.
- Set `base_url` in Settings to the public URL. From then on the server answers only for its host,
  `localhost`, `127.0.0.1`, `::1` and the Docker upload host; a request with another `Host` gets
  `421 Misdirected Request` (`/healthz` answers on every host). To serve one repository at the
  root of a domain of its own, see [Repository domains](docs/repo-domains.md).

An admin can limit who may download from or publish to each repo by address. Open the repo settings, then the **Access** tab, and set **From where**. Ranges can be addresses, CIDRs, or network lists. The server has the lists **GitHub Actions** and **Cloudflare**. Admins add their own under **Settings > People and CI > Networks** and paste the addresses in any format. An empty rule means no limit.

## Commands you'll use

```sh
go tool task dev | build | test       # develop, build bin/telesphorepo-server, run the e2e suite
go tool task test import | scale | clients | all # other test zones (import needs a Nexus container)
telesphorepo-server user add alice [--admin] # prompts for the password
telesphorepo-server user passwd alice        # reset a password
telesphorepo-server user reset-2fa alice     # remove the second factors of a user
docker exec -it <container> /telesphorepo-server user add alice
telesphorepo publish | status | update       # the CLI: publish a project, show the state, update the CLI
```

With the blobs on Cloudflare R2, clients far from the server can download from R2 buckets near
them: choose the regions on **Settings > Edge delivery**, then download the edge Worker there and
deploy it with the steps of that page (see `edge/README.md`).

To move the blobs to another store, change the storage on the **Settings** page. The server
copies the existing blobs in the background. To move hosted repos from Nexus 3, add the Nexus
server on the **Nexus** page. The server mirrors the repos you pick, and they stay read only. Click
**Cut over** when you are ready to publish here. To move each project with a coding agent, use the
skill in [`skills/`](skills/README.md).

A password or an API token (a read token) only reads. To publish, a client needs a publish
session. See **Publishing**.

Anyone can browse the anonymous-read repos in the web UI. A user who may publish can upload
files or a whole folder, and a user who may delete can delete files or folders there. An upload
shows a plan of what it stores, and stores nothing until the user confirms. Both actions use a
publish session that the UI asks for with a second-factor code. A person with the Activity
permission sees every event, with its IP address, in **Settings > Server > Activity**. The server
keeps events for 365 days (`audit.retention`).

## Install the CLI

Each server serves the Telesphorepo CLI of its own build. In the web UI, open **Install the CLI**
in the account menu (or from the **Publish** tab of a repo) for the line of your system, the
archives with their SHA-256, and the commands to install one by hand.

Linux and macOS:

```sh
curl -fsSL https://packages.example.com/install.sh | sh
```

The script installs into `~/.local/bin`. When that folder is not on your `PATH`, it asks to add
it to the start-up file of your shell (`~/.zprofile` for zsh on macOS, `~/.bashrc` for bash on
Linux, and so on); open a new terminal afterwards.

Windows (PowerShell):

```powershell
irm https://packages.example.com/install.ps1 | iex
```

It installs into `%LOCALAPPDATA%\Programs\telesphorepo` and adds that folder to your `PATH`.

After the server is updated, run `telesphorepo update` to get its CLI.

## Publishing

Every upload needs a publish session. It is a credential for one repo that ends after
`publish.session_ttl` (15 min by default). The Telesphorepo CLI gets the sessions. To publish from a
laptop, add a second factor (an authenticator app or a passkey) under **Settings > Your account >
Sign-in and security**. An admin does this at the first sign-in.

**Set up a project once.** On the **Publish** tab of a repo, click **Set up a project**, enter the
second factor, and run the line in the project folder:

```sh
curl -fsSL https://packages.example.com/install.sh | sh -s -- setup 'https://packages.example.com/#/setup/trsu_...'
```

On Windows, the tab shows the PowerShell line. The line installs the CLI from the server, enrolls
the computer and writes `telesphorepo.toml` (the server and the repo). Commit that file. The line
works once, for 10 minutes.

**Publish.** In the project folder, run:

```sh
telesphorepo publish
```

The CLI runs the standard publish command of the project with the session token in its
environment. The build files stay as they are.

| Repo | The CLI finds | `telesphorepo publish` runs |
|---|---|---|
| Maven | `pom.xml` | `mvn deploy` (or `./mvnw`) |
| Maven | `build.gradle(.kts)`, `settings.gradle(.kts)` | `gradle publish` (or `./gradlew`) |
| NuGet | `*.csproj`, `*.fsproj`, `*.sln`, `*.slnx` | `dotnet pack`, then pushes each package |
| npm | `package.json` | `npm publish`, `pnpm publish`, `yarn npm publish` or `bun publish`, after the lockfile |
| PyPI | `pyproject.toml`, `setup.py` | `poetry build`, `uv build` or `python -m build`, then uploads each file |
| Cargo | `Cargo.toml` | `cargo publish` |
| Docker | `Chart.yaml` | `helm package`, then `helm push` |
| Raw, Debian | the files you name | uploads them: `telesphorepo publish build/app.zip` |

A teammate who clones the project runs `telesphorepo publish` and approves the computer in the
browser once.

**Docker.** The setup line adds the credential helper of the CLI to `~/.docker/config.json`. Then
Docker, buildx, Podman, Helm and ORAS push with no `docker login`:

```sh
docker push packages.example.com/docker/team/app:1.0
helm push mychart-0.1.0.tgz oci://packages.example.com/docker
```

**CI.** An admin adds a trust policy under **Settings > People and CI > CI pipelines** (start from
"GitHub Actions" or "Jenkins"). The CI job installs the CLI with
`curl -fsSL https://packages.example.com/install.sh | sh` and runs `telesphorepo publish` (Docker:
`docker push`). The CLI uses the job's identity token, so the job stores no secret. GitHub Actions
needs `permissions: id-token: write`. Jenkins needs the OpenID Connect Provider plugin and an "OpenID
Connect id token" credential with the ID `telesphorepo-id-token`, which the Jenkinsfile binds to
`OIDC_TOKEN` in the publish stage. The Publish tab shows the CI file of each repo.

**By hand.** On **Settings > Your account > Tokens**, choose a repo, enter a second-factor code, and copy the
token. Use it as the password with any username. Paste it at the hidden prompt, so it stays out
of the shell history and the process list:

```sh
printf 'Publish token: '; read -rs TOKEN; echo
printf 'user = "publish:%s"\n' "$TOKEN" | curl -fsS -K - -T file.tar.gz https://packages.example.com/raw/raw/path/file.tar.gz
```

A user who loses every factor can be reset with `telesphorepo-server user reset-2fa NAME`.

## Clients

Maven reads from `https://packages.example.com/maven/maven`. Put the URL in `pom.xml`. A private
repo needs matching credentials (server id `telesphorepo`) in `~/.m2/settings.xml`:

```xml
<distributionManagement>
  <repository><id>telesphorepo</id><url>https://packages.example.com/maven/maven</url></repository>
</distributionManagement>
```

apt:

```sh
sudo curl -fsSL https://packages.example.com/debian/debian/key.asc -o /etc/apt/keyrings/telesphorepo.asc
echo "deb [signed-by=/etc/apt/keyrings/telesphorepo.asc] https://packages.example.com/debian/debian stable main" \
  | sudo tee /etc/apt/sources.list.d/telesphorepo.list
```

dotnet reads the source `https://packages.example.com/nuget/nuget/index.json` from `nuget.config`
(the repo page shows the file, with the credentials of a private repo).

Docker, Helm and ORAS use the repo as the first part of the image name. The registry must be at
the root of a host:

```sh
docker pull packages.example.com/docker/team/app:1.0
```

Behind Cloudflare (Free and Pro plans), each image layer must be under 100 MB. Pulls have no
limit. For larger layers, turn on `docker.direct_uploads` and set `docker.upload_url` to a
DNS-only name of the server. This makes the IP address of the server public; open that port in
its firewall for the clients that push. Docker, Podman, skopeo and crane keep the main host: the
server sends their layer uploads to the upload address. ORAS and Helm 4 push to the upload
address by name. A docker repo can also be a proxy of another registry
(Docker Hub, GHCR, Quay) or a group.

The repo page in the UI shows the snippets for each repo. Gradle, private apt repos, the API and
everything else: [SPEC.md](SPEC.md).

## License

The server is licensed under AGPL-3.0 (see [LICENSE](LICENSE)). The Telesphorepo CLI
(`cmd/telesphorepo`) is licensed under the MIT license, in its own LICENSE file.
