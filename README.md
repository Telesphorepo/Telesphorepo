# Telesphorepo

A small self-hosted package repository for Maven/Gradle artifacts and Debian packages. It is a
single Go binary with a web UI, and stores files on local disk or in any S3-compatible bucket.

## Run it locally

You need [Go](https://go.dev/dl/), [bun](https://bun.sh) and Node 26. Docker or Podman is needed
only for the tests.

```sh
go tool task dev
```

Open **http://localhost:5173**. The startup banner prints the admin username and password and
some client snippets. The `maven` and `debian` demo repos are already created. Data lives in
`.telesphorepo-dev/`; delete it to start over. Press Ctrl+C to stop.

## Run it in production

```sh
go tool task build image        # builds telesphorepo:local
docker run -d -p 8080:8080 -v telesphorepo-data:/data telesphorepo:local
```

- **Admin password:** on first start the server prints a generated `admin` password once to
  the container log (`docker logs <container>`).
- **Configuration:** sign in, then set up storage (local disk or S3/R2), the public URL, the
  Debian signing key and the rest on the **Settings** page. Changes apply without a restart, and
  secrets are stored encrypted.
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
telesphorepo user add alice [--admin] # prompts for the password
telesphorepo user passwd alice        # reset a password
docker exec -it <container> /telesphorepo user add alice
```

To use the repo from CI, create an API token in the UI. Use the token as the password, with any
username.

## Clients

Maven, in `pom.xml`, with matching credentials (server id `telesphorepo`) in
`~/.m2/settings.xml`:

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
curl -u USER:TOKEN -T pkg_1.0_amd64.deb https://packages.example.com/debian/debian/upload/stable/main
```

Gradle, private apt repos, the API and everything else: [SPEC.md](SPEC.md).
