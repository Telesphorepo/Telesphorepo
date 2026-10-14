# Telesphorepo

A small self-hosted package repository for Maven/Gradle artifacts and Debian packages. One Go binary
with an embedded web UI; metadata in SQLite, files on local disk or any S3-compatible bucket (Cloudflare R2, SeaweedFS, ...).

## Run it locally

```sh
bun run dev
```

That is all. It checks your toolchain, installs web dependencies, then starts:

- the API/backend on http://localhost:8080 (rebuilds and restarts when Go files change)
- the web UI with hot reload on **http://localhost:5173** (open this one)

On first run it creates an `admin` user and demo repos `maven` and `debian`. The username and a
random password are printed in the banner when it starts (data lives in `.telesphorepo-dev/`).
Nothing to configure. Ctrl+C stops everything.

Other commands (`npm run <script>` works too):

```sh
bun run build            # UI + bin/telesphorepo (bin\telesphorepo.exe on Windows), UI embedded
bun run build image      # also build the container image telesphorepo:local
bun run preview          # build, then run the production binary in dev mode on :8080
bun run doctor           # show what was detected (Go, bun/npm, container engine) and what is missing
```

## Prerequisites

- Go (version in `go.mod`)
- [bun](https://bun.sh) recommended; npm works as a fallback
- Docker or Podman, only for the tests and for building the image

Works on Windows, macOS and Linux. No bash or make needed.

## Production

Run the container image (data volume `/data`, port 8080, must be writable by uid 65532):

```sh
docker run -d -p 8080:8080 -v telesphorepo-data:/data \
  -e TR_BASE_URL=https://packages.example.com \
  telesphorepo:local
```

or the binary: `bin/telesphorepo serve`. Behind plain HTTP (no TLS) also set `TR_COOKIE_SECURE=false`.
On first start with no users, an `admin` user is created with a random password printed once to
stderr; set `TR_BOOTSTRAP_ADMIN_USER` and `TR_BOOTSTRAP_ADMIN_PASSWORD` to choose your own.
The Debian signing key is generated at `$TR_DATA_DIR/signing.asc` on first start; keep the data volume.

| Variable | Default | Meaning |
|---|---|---|
| `TR_LISTEN` | `:8080` | listen address |
| `TR_DATA_DIR` | `./data` (image: `/data`) | SQLite db, upload spool, local blobs, signing key |
| `TR_BASE_URL` | | public URL, shown in UI hints |
| `TR_STORAGE` | `fs` | `fs` or `s3` |
| `TR_S3_ENDPOINT` | | e.g. `https://<acct>.r2.cloudflarestorage.com` |
| `TR_S3_BUCKET` | | required for `s3` |
| `TR_S3_ACCESS_KEY_ID`, `TR_S3_SECRET_ACCESS_KEY` | | required for `s3` |
| `TR_S3_REGION` | `auto` | |
| `TR_S3_PREFIX` | | optional key prefix |
| `TR_S3_PATH_STYLE` | `false` | needed for SeaweedFS |
| `TR_S3_REDIRECT` | `true` | downloads redirect to a presigned URL; `false` streams through the server |
| `TR_COOKIE_SECURE` | `true` | set `false` when serving over plain HTTP |
| `TR_TRUSTED_PROXIES` | | CIDRs whose `X-Forwarded-For` is trusted |
| `TR_GPG_KEY_FILE` | `$TR_DATA_DIR/signing.asc` | Debian signing key (must exist if set) |
| `TR_MAX_UPLOAD_BYTES` | `2147483648` | max upload size |
| `TR_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

Full list and details: [design spec](docs/superpowers/specs/2026-10-14-telesphorepo-mvp-design.md) section 3.

## Users and tokens

```sh
telesphorepo user add alice          # prompts for the password twice
telesphorepo user add bob --admin
telesphorepo user passwd alice       # reset a password
echo 's3cret-pass' | telesphorepo user add ci   # piped: reads one line
```

In a container: `docker exec -it <container> /telesphorepo user add alice`.

For CI, create an API token in the web UI (shown once). Use it as the password with any username.
Package endpoints use HTTP Basic auth only.

## Client setup

Replace `packages.example.com` with your host (locally `localhost:8080`), `USER`/`TOKEN` with a user and a token.

### Maven

`~/.m2/settings.xml`:

```xml
<settings>
  <servers>
    <server><id>telesphorepo</id><username>USER</username><password>TOKEN</password></server>
  </servers>
</settings>
```

`pom.xml`:

```xml
<distributionManagement>
  <repository>
    <id>telesphorepo</id>
    <url>https://packages.example.com/maven/maven</url>
  </repository>
  <snapshotRepository>
    <id>telesphorepo</id>
    <url>https://packages.example.com/maven/maven</url>
  </snapshotRepository>
</distributionManagement>
```

Then `mvn deploy`. Releases are immutable; re-deploying different content returns 409. To resolve,
add the same URL under `<repositories>`. Maven 3.8.1+ blocks `http://` repos on non-localhost hosts
(the `maven-default-http-blocker` mirror); use HTTPS.

### Gradle

`build.gradle.kts`:

```kotlin
plugins { `maven-publish` }

publishing {
    repositories {
        maven {
            url = uri("https://packages.example.com/maven/maven")
            // only for plain http, e.g. http://localhost:8080
            isAllowInsecureProtocol = true
            credentials {
                username = providers.gradleProperty("trUser").get()
                password = providers.gradleProperty("trToken").get()
            }
        }
    }
}
```

To consume plugins, `settings.gradle.kts`:

```kotlin
pluginManagement {
    repositories {
        maven {
            url = uri("https://packages.example.com/maven/maven")
            isAllowInsecureProtocol = true   // remove when using https
            credentials { username = "USER"; password = "TOKEN" }
        }
        gradlePluginPortal()
    }
}
```

Put `trUser`/`trToken` in `~/.gradle/gradle.properties`, then `gradle publish`.

### Debian / apt

```sh
sudo curl -fsSL https://packages.example.com/keys/debian.asc -o /etc/apt/keyrings/telesphorepo.asc

sudo tee /etc/apt/sources.list.d/telesphorepo.sources <<'EOF'
Types: deb
URIs: https://packages.example.com/debian/debian
Suites: stable
Components: main
Signed-By: /etc/apt/keyrings/telesphorepo.asc
EOF

# only if the repo is private
sudo tee /etc/apt/auth.conf.d/telesphorepo.conf <<'EOF'
machine packages.example.com login USER password TOKEN
EOF
sudo chmod 600 /etc/apt/auth.conf.d/telesphorepo.conf

sudo apt update && sudo apt install mypackage
```

### Upload a .deb with curl

```sh
curl -u USER:TOKEN -T mypackage_1.0_amd64.deb \
  https://packages.example.com/debian/debian/upload/stable/main
```

Indexes rebuild in the background a few seconds after upload.

## Tests

```sh
bun run test                    # end-to-end suite
bun run test -run TestMaven     # extra args pass through to go test
```

The suite builds the real image and runs real Maven, Gradle and apt clients in containers, so it needs
Docker or Podman running. The engine is auto-detected (Docker first, then Podman, including
`podman machine` on macOS/Windows). Force one with `TR_ENGINE`:

```sh
TR_ENGINE=podman bun run test          # macOS / Linux
$env:TR_ENGINE = "podman"; bun run test   # PowerShell
```

S3 storage is tested against a throwaway SeaweedFS container that starts automatically. No R2 bucket
or account is needed. To also test against a real bucket, set (PowerShell: `$env:NAME = "value"`):

```sh
export TR_E2E_R2_ENDPOINT=https://<acct>.r2.cloudflarestorage.com
export TR_E2E_R2_BUCKET=my-test-bucket
export TR_E2E_R2_ACCESS_KEY_ID=...
export TR_E2E_R2_SECRET_ACCESS_KEY=...
```

Skip rebuilding the image when it is unchanged: `E2E_SKIP_BUILD=1 bun run test`
(PowerShell: `$env:E2E_SKIP_BUILD = "1"`). `E2E_IMAGE` picks a different server image and
`E2E_SEAWEEDFS_IMAGE` a different SeaweedFS image.
