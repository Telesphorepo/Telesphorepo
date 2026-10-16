# Telesphorepo

A small self-hosted package repository for Maven/Gradle artifacts, Debian packages, container images and Helm charts (OCI), and raw files. It is a
single Go binary with a web UI, and stores files on local disk or in any S3-compatible bucket.

## Run it locally

You need [Go](https://go.dev/dl/), [bun](https://bun.sh) and Node 26. Docker or Podman is needed
only for the tests.

```sh
go tool task dev
```

Open **http://localhost:5173**. The startup banner prints the admin username and password and
some client snippets. The first run fills the instance with the same demo data each time, before the
server starts: about 150,000 Maven files, Debian packages, raw files and container images in the public repos `maven`, `debian`, `raw` and `docker`, a
private Maven repo `internal`, and five demo users (their passwords are in the banner). To start with
empty repos, run the binary as `telesphorepo-server dev --empty` on a fresh data dir. Data lives in
`.telesphorepo-dev/`; delete it to start over. Press Ctrl+C to stop. The dev server does not enforce the second factor.

## Run it in production

```sh
docker run -d -p 8080:8080 -v telesphorepo-data:/data ghcr.io/telesphorepo/telesphorepo:latest
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
  signs in with single sign-on approves a laptop for Gradle or Maven with a new sign-in at the
  provider; a local account approves it with a second factor.
- **Passkeys:** they need the public URL (`base_url`) in Settings. Authenticator-app codes do not.
- **Environment variables:** none are required. Three are optional:

| Variable | Default | |
|---|---|---|
| `TR_DATA_DIR` | `/data` in the image | Database, upload spool, local blobs, master key |
| `TR_LISTEN` | `:8080` | Listen address |
| `TR_MASTER_KEY` | Generated into the data dir | Key that encrypts secret settings (base64, 32 bytes); pass it from a secret manager to keep it off the volume |

Any setting can also be forced from the environment, e.g. `TR_COOKIE_SECURE=false` when the
server is served over plain HTTP. A forced setting is read-only in the UI. Back up the data
volume.

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
- Set `base_url` in Settings to the public URL.

An admin can limit who may download from or publish to each repo by address. Open the repo settings, then the **Access** tab, and set **From where**. Ranges can be addresses, CIDRs, or network lists. The server has the lists **GitHub Actions** and **Cloudflare**. Admins add their own under **Settings > People and CI > Networks** and paste the addresses in any format. An empty rule means no limit.

## Commands you'll use

```sh
go tool task dev | build | test       # develop, build bin/telesphorepo-server, run the e2e suite
go tool task test import | scale | clients | all # other test zones (import needs a Nexus container)
telesphorepo-server user add alice [--admin] # prompts for the password
telesphorepo-server user passwd alice        # reset a password
telesphorepo-server user reset-2fa alice     # remove the second factors of a user
docker exec -it <container> /telesphorepo-server user add alice
```

To move the blobs to another store, change the storage on the **Settings** page. The server
copies the existing blobs in the background. To move hosted repos from Nexus 3, add the Nexus
server on the **Nexus** page. The server mirrors the repos you pick, and they stay read only. Click
**Cut over** when you are ready to publish here.

A password or an API token (a read token) only reads. To publish, a client needs a publish
session. See **Publishing**.

Anyone can browse the anonymous-read repos in the web UI. A user who may publish can upload
files or a whole folder, and a user who may delete can delete files or folders there. An upload
shows a plan of what it stores, and stores nothing until the user confirms. Both actions use a
publish session that the UI asks for with a second-factor code. A person with the Activity
permission sees every event, with its IP address, in **Settings > Server > Activity**. The server
keeps events for 365 days (`audit.retention`).

## Publishing

Every upload needs a publish session. It is a credential for one repo that ends after
`publish.session_ttl` (15 min by default). To publish from a laptop, add a second factor (an
authenticator app or a passkey) under **Settings > Your account > Sign-in and security**. An admin does this at the first
sign-in. Other users do it when they approve their first device. The clients are in [github.com/Telesphorepo/clients](https://github.com/Telesphorepo/clients): the Gradle plugin is on the Gradle Plugin Portal and the Maven extension is on Maven Central. The **Publish to this repository** tab of each repo shows every snippet, with the repo's address filled in.

**One command.** In a Gradle or Maven project, run the setup script. It adds the plugin or the
extension and the repository to the build files, and it can write the CI file. It shows each
change first and asks before it edits a file:

```sh
curl -fsSL https://packages.example.com/setup.sh | sh -s -- releases
```

On Windows, use `setup.ps1` with `irm` in PowerShell. The **Publish to this repository** tab shows the
command for your repository. The sections below show the same change by hand.

**Gradle.** In `settings.gradle.kts`, at the top:

```kotlin
plugins { id("com.telesphorepo.publish") version "0.1.0" }
telesphorepo {
    url = "https://packages.example.com"
    repository = "releases"
}
```

The plugin publishes each Java project to `releases`, so the build file needs only a group and a
version. Run `./gradlew publish`. The first time on a computer, it opens a link in the browser.
Sign in and enter the second factor to approve the computer. The publish then continues. Later
publishes get a session by themselves.

**Maven.** In `.mvn/extensions.xml`:

```xml
<extensions>
  <extension>
    <groupId>com.telesphorepo</groupId>
    <artifactId>telesphorepo-maven-extension</artifactId>
    <version>0.1.0</version>
  </extension>
</extensions>
```

Add `-Dtelesphorepo.url=https://packages.example.com` to `.mvn/maven.config` and the repo to
`<distributionManagement>`. Run `mvn deploy`. The first time on a computer, approve the computer
in the browser as for Gradle.

**GitHub Actions.** Give the job `permissions: id-token: write`. An admin adds a trust policy
under **Settings > People and CI > CI pipelines** (start from "GitHub Actions"): the repo, the branch or tag, and the
workflow that may publish. The Gradle plugin and the Maven extension then use the GitHub token
with no secret. For Debian and Raw repos, the Publish tab shows a workflow that posts the token
to `/api/publish/sessions/oidc` with `curl`.

**Jenkins.** Install the OpenID Connect Provider plugin and add an "OpenID Connect id token"
credential with the ID `telesphorepo-id-token`. An admin adds a trust policy starting from
"Jenkins" (issuer `<jenkins url>/oidc`). The Jenkinsfile binds the credential to `OIDC_TOKEN`
only in the publish stage, because the server takes a token for 10 minutes only. Set
`oidcTokenEnv = "OIDC_TOKEN"` in the Gradle plugin block, or
`-Dtelesphorepo.oidcTokenEnv=OIDC_TOKEN` for Maven.

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
printf 'Publish token: '; read -rs TOKEN; echo
printf 'user = "publish:%s"\n' "$TOKEN" | curl -fsS -K - -T pkg_1.0_amd64.deb https://packages.example.com/debian/debian/upload/stable/main
```

Raw files:

```sh
printf 'Publish token: '; read -rs TOKEN; echo
printf 'user = "publish:%s"\n' "$TOKEN" | curl -fsS -K - -T file.tar.gz https://packages.example.com/raw/raw/path/file.tar.gz
```

Docker, Helm and ORAS use the repo as the first part of the image name. The registry must be at
the root of a host. To push from a computer, get `docker-credential-telesphorepo` for your system
from the [GitHub Releases](https://github.com/Telesphorepo/Telesphorepo/releases) page, put it on
your `PATH`, and enroll the computer once. Approve the code in the browser, as for Gradle. After
that, Docker, buildx, Podman, Helm and ORAS push with no `docker login`:

```sh
docker-credential-telesphorepo login https://packages.example.com
docker push packages.example.com/docker/team/app:1.0
helm push mychart-0.1.0.tgz oci://packages.example.com/docker
docker pull packages.example.com/docker/team/app:1.0
```

The helper keeps the device key in `~/.telesphorepo/<server>/docker/device.json` and adds itself
to `credHelpers` in `~/.docker/config.json`. `docker logout` deletes the key from the computer;
revoke the device under **Settings > Your account > Devices**. Without the helper, paste a publish
token at a hidden prompt:

```sh
printf 'Publish token: '; read -rs TOKEN; echo
printf '%s' "$TOKEN" | docker login packages.example.com -u publish --password-stdin
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

The server and the Docker credential helper are licensed under AGPL-3.0 (see [LICENSE](LICENSE)). The [clients](https://github.com/Telesphorepo/clients) (Gradle plugin and Maven extension) are in their own repository under the MIT license.
