# Telesphorepo

A small self-hosted package repository for Maven/Gradle artifacts, Debian packages and raw files. It is a
single Go binary with a web UI, and stores files on local disk or in any S3-compatible bucket.

## Run it locally

You need [Go](https://go.dev/dl/), [bun](https://bun.sh) and Node 26. Docker or Podman is needed
only for the tests.

```sh
go tool task dev
```

Open **http://localhost:5173**. The startup banner prints the admin username and password and
some client snippets. A demo repo for each format (`maven`, `debian`, `raw`) is already created. Data lives in
`.telesphorepo-dev/`; delete it to start over. Press Ctrl+C to stop. The dev server does not enforce the second factor.

## Run it in production

```sh
docker run -d -p 8080:8080 -v telesphorepo-data:/data ghcr.io/telesphorepo/telesphorepo:latest
```

The image supports `linux/amd64` and `linux/arm64`. `:latest` is the newest release; `:main` is the newest green build of `main`, and `:sha-<commit>` pins one build. To build the image yourself, run
`go tool task build image`. It builds `telesphorepo:local`.

You can also download a binary for Linux, macOS or Windows (amd64 or arm64) from the
[GitHub Releases](https://github.com/Telesphorepo/Telesphorepo/releases) page. Check the
download against `SHA256SUMS`, unpack it, and run `./telesphorepo serve`.

- **Admin password:** on first start the server prints a generated `admin` password once to
  the container log (`docker logs <container>`).
- **Configuration:** sign in, then set up storage (local disk or S3/R2), the public URL, the
  Debian signing key and the rest on the **Settings** page. Changes apply without a restart, and
  secrets are stored encrypted.
- **Single sign-on:** admins can sign in through an OIDC provider (Keycloak, Entra, Google...).
  Set the issuer, client and admin group under **Settings > Single sign-on**, and register
  `<public URL>/api/auth/oidc/callback` at the provider. Publishers use local accounts with a
  second factor.
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

## Commands you'll use

```sh
go tool task dev | build | test       # develop, build bin/telesphorepo, run the e2e suite
go tool task test import | scale | clients | all # other test zones (import needs a Nexus container)
telesphorepo user add alice [--admin] # prompts for the password
telesphorepo user passwd alice        # reset a password
telesphorepo user reset-2fa alice     # remove the second factors of a user
docker exec -it <container> /telesphorepo user add alice
```

To move the blobs to another store, change the storage on the **Settings** page. The server
copies the existing blobs in the background. To move hosted repos from Nexus 3, add the Nexus
server on the **Nexus** page. The server mirrors the repos you pick, and they stay read only. Click
**Cut over** when you are ready to publish here.

A password or an API token (a read token) only reads. To publish, a client needs a publish
session. See **Publishing**.

Anyone can browse the anonymous-read repos in the web UI. A user with write access can upload
files and delete files or folders there. Both actions use a publish session that the UI asks for
with a second-factor code. Admins see every event, with its IP address, in **Settings > Audit
log**. The server keeps events for 365 days (`audit.retention`).

## Publishing

Every upload needs a publish session. It is a credential for one repo that ends after
`publish.session_ttl` (15 min by default). To publish from a laptop, add a second factor (an
authenticator app or a passkey) under **Settings > Security**. An admin does this at the first
sign-in. Other users do it when they approve their first device. The clients below are in [github.com/Telesphorepo/clients](https://github.com/Telesphorepo/clients). Replace `VERSION` with the latest `v` release there.

**Gradle.** In `settings.gradle.kts`:

```kotlin
plugins { id("com.telesphorepo") version "VERSION" }
telesphorepo { url = "https://packages.example.com" }
```

Run `./gradlew telesphorepoLogin` once. Open the printed link, sign in, and enter the second
factor to approve the device. After that, `./gradlew publish` gets a session by itself.

**Maven.** In `.mvn/extensions.xml`:

```xml
<extensions>
  <extension>
    <groupId>com.telesphorepo</groupId>
    <artifactId>telesphorepo-maven-extension</artifactId>
    <version>VERSION</version>
  </extension>
</extensions>
```

Add `-Dtelesphorepo.url=https://packages.example.com` to `.mvn/maven.config`. Run
`mvn com.telesphorepo:telesphorepo-maven-plugin:VERSION:login` once, approve the device,
and then run `mvn deploy`.

**GitHub Actions.** Give the job `permissions: id-token: write`. An admin adds a trust policy
on the **Trust policies** page under Settings (preset "GitHub Actions"): the repo, the branch or tag, and the
workflow that may publish. The Gradle plugin and the Maven extension then use the GitHub token
with no secret. Without them, post the token to `/api/publish/sessions/oidc`; the **Publish**
page shows the `curl` steps.

**Jenkins.** Install the OpenID Connect Provider plugin and bind an "OpenID Connect id token"
credential to an environment variable, for example `OIDC_TOKEN`. An admin adds a trust policy
with the preset "Jenkins" (issuer `<jenkins url>/oidc`). Set `oidcTokenEnv = "OIDC_TOKEN"` in the
Gradle plugin block, or `-Dtelesphorepo.oidcTokenEnv=OIDC_TOKEN` for Maven.

**By hand.** On the **Publish** page, choose a repo, enter a second-factor code, and copy the
token. Use it as the password with any username:

```sh
curl -f -u publish:trs_TOKEN -T file.tar.gz https://packages.example.com/raw/raw/path/file.tar.gz
```

A user who loses every factor can be reset with `telesphorepo user reset-2fa NAME`.

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
sudo curl -fsSL https://packages.example.com/keys/debian.asc -o /etc/apt/keyrings/telesphorepo.asc
echo "deb [signed-by=/etc/apt/keyrings/telesphorepo.asc] https://packages.example.com/debian/debian stable main" \
  | sudo tee /etc/apt/sources.list.d/telesphorepo.list
curl -u publish:trs_TOKEN -T pkg_1.0_amd64.deb https://packages.example.com/debian/debian/upload/stable/main
```

Raw files:

```sh
curl -f -u publish:trs_TOKEN -T file.tar.gz https://packages.example.com/raw/raw/path/file.tar.gz
```

The repo page in the UI shows the snippets for each repo. Gradle, private apt repos, the API and
everything else: [SPEC.md](SPEC.md).

## License

The server is licensed under AGPL-3.0 (see [LICENSE](LICENSE)). The [clients](https://github.com/Telesphorepo/clients) (Gradle plugin and Maven extension) are in their own repository under the MIT license.
