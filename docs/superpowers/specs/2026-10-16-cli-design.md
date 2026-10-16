# One publishing client: design spec

Status: for approval. Tool facts were checked on 2026-10-16 against the docs listed at the end.
A fact marked **(unverified)** comes from memory; the e2e test of its format confirms it before
the format ships.

## Goal

One Go program, `telesphorepo`, publishes to every format from a developer computer and from CI,
with the same command. It owns the device key and mints short-lived sessions. Where a tool has a
user-level credential hook, `login` installs the hook, so the native command (`docker push`,
`cargo publish`, `./gradlew publish`) works unchanged with no project edit. Where a tool has no
hook, `telesphorepo publish` or `telesphorepo exec` gives the token to the tool in its env. The
Gradle plugin, the Maven extension and the separate Docker credential helper go away.

## 1. Decisions

| Question | Decision | Reason |
|---|---|---|
| Subcommands of the server binary, or a separate binary | **Separate binary.** The client is `cmd/telesphorepo`. The server binary becomes `telesphorepo-server` (`cmd/telesphorepo-server`) | The server binary is 33 MB (measured: `bin/telesphorepo.exe`, windows/amd64). It embeds the UI, SQLite, the S3 SDK and OIDC. The client needs the stdlib, `golang.org/x/sys` (Windows ACL), `golang.org/x/mod` (Go module zips) and a TOML reader. Estimate: 7 to 9 MB. Many developers and CI jobs install the client; few people install the server, and most of them run the image. The short name goes to the program that people type |
| Where the client code lives | This repo: `cmd/telesphorepo` and packages under it. It imports no server package | One release, one e2e suite, one version for server and client. The rule "imports no server package" keeps the protocol explicit, as the helper does now |
| Gradle plugin and Maven extension | **Retire both**, and the whole clients repo | Gradle: a global init script that `login` writes gives plain `./gradlew publish` the credentials (section 3); the plugin adds nothing that a build file cannot declare. Maven: `telesphorepo publish` and `exec` with `${env}` in a settings file. One client in one language to maintain. A Maven hook for plain `mvn deploy` needs Java code; see open question 2 |
| Docker helper | A mode of the same binary, chosen by the program name `docker-credential-telesphorepo` | Docker runs `docker-credential-<suffix>` from `PATH`. The installer makes a second name for the same file (section 5). `credHelpers` keeps the value `telesphorepo` |
| Git credential helper | None | The server hosts no git repositories. No format reads through git |
| Publisher client registry | **Delete it.** One client enrolls devices | `internal/clients`, `Extender.PublisherClient`, the `client` field of an enrollment and the `client` column of `devices` go. The approval page names "Telesphorepo CLI" |

New Go modules for the client, with reasons: `golang.org/x/mod` (Go team; module zip creation and
checks, `go.mod` parsing, path escaping, semver: section 3, Go) and `github.com/BurntSushi/toml`
(read `Cargo.toml`, `.cargo/config.toml`, `pyproject.toml` and the project file).

## 2. Commands

Positional arguments for the obvious thing. A flag only for an optional knob. A TTY prompt where a
person must choose. Every command has an example-led `--help`.

| Command | Does |
|---|---|
| `telesphorepo login SERVER [--name NAME]` | Enrolls this computer: the device flow of the helper now (Ed25519 key, `POST /api/devices/enroll`, the link and code, the browser on a TTY, the poll with `slow_down`). It saves `~/.telesphorepo/<server>/device.json` and `hosts.json` (`GET /docker/hosts`). Then it installs the user-level hooks of section 3 (Docker `credHelpers` always; Cargo, Gradle and Go when the tool is on `PATH` or its user folder exists) and prints each file that it changed. A device that the server still accepts stays. Default name: the host name |
| `telesphorepo logout [SERVER]` | Revokes the device on the server (new route, section 4), removes the hooks of that server, and deletes `~/.telesphorepo/<server>/`. Without SERVER and with one enrolled server, that server |
| `telesphorepo status` | For each enrolled server: the device ID, whether the server accepts it (`POST /api/publish/challenge`), the Docker hosts, and each hook with its file. For the current folder: the project kinds found, the server and repo that `publish` will use, and the CI system found. Prints no secret |
| `telesphorepo token [--read] REPO_URL` | The primitive that every hook calls. Prints to stdout a publish session token for the repo of the URL (`--read`: a device read proof, section 4). It refuses when stdout is a terminal. One build gets one session per repo: the CLI keeps a token exchange nonce per calling process in `exchange.json`, as the helper does now, and the server returns the open session of that nonce and repo. Under `publish` or `exec` (also in CI) it prints the session token of the env for that repo and calls no server |
| `telesphorepo goauth [URL]` | The `GOAUTH` command (section 3, Go). Go runs it; it is not in the overview |
| `telesphorepo init [REPO] [--ci none\|github\|jenkins-pipeline\|jenkins-freestyle]` | Writes `telesphorepo.toml` (server and repo) and, on request, the CI file. Asks for missing values on a TTY. Shows each change first; a change to a file that exists needs `y` |
| `telesphorepo publish [REPO]` | Finds the project, mints a session, runs the native tool with the token in its env, closes the session. Section 3 |
| `telesphorepo upload REPO[/PATH] FILE...` | Uploads files to a format without a project (raw, Debian). Raw: PATH is the folder in the repo. Debian: PATH is `<dist>/<component>`. Large files use the resumable upload routes |
| `telesphorepo exec [REPO] -- CMD [ARGS...]` | Mints a session for REPO, runs CMD with the env of section 3 for the repo's format, closes the session, and exits with the exit code of CMD |
| `docker-credential-telesphorepo get\|store\|erase\|list` | The helper protocol now, unchanged. One addition: with no device for the host and `TELESPHOREPO_TOKEN` in the env for that server (set by `exec`), `get` answers that session token |
| `telesphorepo cargo-credential --cargo-plugin` | The Cargo credential provider protocol (section 3, Cargo). Cargo runs it; it is not in the overview |
| `telesphorepo version`, `help [command]` | |

**Inline enrollment.** `publish`, `exec` and `init` outside CI with no device that the server
accepts run the login flow inline and then continue, as the Java clients do now. In CI they never
enroll; they fail with what the job must add (the messages of `Environment.noToken` now).

**Which server and repo.** The CLI reads what the project already has, in this order:

1. The REPO argument. The server then comes from steps 2 to 4, or from the one enrolled server.
2. A registry URL in the project config that points at a Telesphorepo server:
   `publishConfig.registry` in `package.json`; `publish = ["<name>"]` in `Cargo.toml` with
   `[registries.<name>] index` in `.cargo/config.toml`; the `url` of
   `<distributionManagement>` in `pom.xml`; `metadata["allowed_push_host"]` in the gemspec (a
   string literal only); `publish-url` of a `[[tool.uv.index]]` in `pyproject.toml`. The CLI
   splits the URL into server base and repo: the server base is an enrolled server URL that
   prefixes it, else the CLI matches `/<format>/<repo>` against `GET /api/formats` of the host.
3. `telesphorepo.toml` in the project root (`init` writes it):
   ```toml
   server = "https://repo.example.com"
   repo = "releases"
   ```
4. On a TTY: a question for the repo, and an offer to save `telesphorepo.toml`.

Else the CLI stops and names the three ways. Gradle, Go, Helm and Conda projects have no registry
field, so they use the argument or `telesphorepo.toml`.

**Which project.** `publish` looks in the current folder for `package.json`, `pyproject.toml`,
`setup.cfg` or `setup.py`, `Cargo.toml`, `go.mod`, `*.gemspec`, `Chart.yaml`, `recipe.yaml`
(rattler-build) or `meta.yaml` (conda-build), `pom.xml`, and `settings.gradle(.kts)` or
`build.gradle(.kts)`. A `Dockerfile` is not a publish project: `docker push` uses the helper. When
several kinds match, the CLI asks on a TTY. Without a TTY it stops and lists them; the
`kind = "<kind>"` key in `telesphorepo.toml` picks one. The format of the repo (from the mint
answer, section 4) must accept the project kind, else the CLI stops before the build.

## 3. Formats: what `publish` runs and how the token gets there

**Hooks and mechanisms.** "Verified" means that the source was read for this spec. "Not verified"
rows are confirmed by the e2e test of the tool before the format ships.

| Tool | Mechanism | `login` installs | Source | Verified |
|---|---|---|---|---|
| Docker, buildx, Podman, skopeo, crane | Credential helper `docker-credential-telesphorepo` | `credHelpers` in `$DOCKER_CONFIG/config.json` | Helper now; `TestDockerCredentialHelper` | Yes (e2e now) |
| Helm 4, ORAS | They read the Docker config and its `credHelpers` | Nothing more | `TestDockerClients` (Helm 4 and ORAS push with the helper) | Yes (e2e now) |
| Cargo | Credential provider protocol, `operation: publish` and `read` | `"telesphorepo cargo-credential"` appended to `[registry] global-credential-providers` in `$CARGO_HOME/config.toml`, after `cargo:token` (later entries win; the CLI answers `url-not-supported` for other hosts) | https://doc.rust-lang.org/cargo/reference/credential-provider-protocol.html, https://doc.rust-lang.org/cargo/reference/registry-authentication.html | Yes |
| Gradle | Global init script in `$GRADLE_USER_HOME/init.d/` (every `*.init.gradle(.kts)` there runs for every build) | `telesphorepo.init.gradle.kts` (below) | https://docs.gradle.org/current/userguide/init_scripts.html | Yes (discovery). Configuration cache behaviour: not verified |
| Go (reads) | `GOAUTH` custom command (Go 1.24+): Go runs it before the first HTTPS fetch and again with the URL after a 4xx; it prints URL prefixes, a blank line, header lines, a blank line | `go env -w GOAUTH="netrc;telesphorepo goauth"` when `GOAUTH` is unset; else `login` prints the line to add | `go help goauth` (`src/cmd/go/internal/help/helpdoc.go`), https://go.dev/ref/mod | Yes |
| Maven 3.9 | `bin/mvn` sources `/usr/local/etc/mavenrc`, `/etc/mavenrc` and `~/.mavenrc` unless `MAVEN_SKIP_RC` is set; `mvn.cmd` calls `%USERPROFILE%\mavenrc_pre.cmd` and `.bat`; `mvnw` execs the downloaded `bin/mvn`. A hook there could put a Java extension on `-Dmaven.ext.class.path` | Nothing (open question) | `apache-maven/src/bin/mvn` and `mvn.cmd` (branch `maven-3.9.x`), `maven-wrapper` `only-mvnw` | Yes |
| Maven 4 | User-level core extensions in `~/.m2/extensions.xml` (MNG-7772, fixed for 4.0.0) | Nothing (open question) | https://issues.apache.org/jira/browse/MNG-7772 (search result, page not read) | No |
| pnpm | `tokenHelper` in the user `.npmrc` only: an absolute path with no arguments, scoped per registry key; it prints the token | Nothing: the helper gets no argument, so it cannot tell the repo without one wrapper script per repo. pnpm uses `publish` and `exec` | https://pnpm.io/npmrc | Yes |
| npm | No helper (npm/cli#8141 is open). `${VAR}` in npmrc and `globalconfig` from the env | Nothing; `publish` and `exec` | https://docs.npmjs.com/cli/v11/configuring-npm/npmrc, https://github.com/npm/cli/issues/8141 | Yes |
| uv publish | `UV_PUBLISH_URL`, `UV_PUBLISH_USERNAME`, `UV_PUBLISH_PASSWORD`. `--keyring-provider subprocess` needs an executable named `keyring` on `PATH`, which would take over keyring for every index, so the CLI does not use it | Nothing; `publish` and `exec` | https://docs.astral.sh/uv/reference/environment/ | Yes (env); keyring: search results only |
| twine | `TWINE_REPOSITORY_URL`, `TWINE_USERNAME`, `TWINE_PASSWORD`, `TWINE_NON_INTERACTIVE` | Nothing; `publish` and `exec` | https://twine.readthedocs.io/en/stable/ | Yes |
| RubyGems | `GEM_HOST_API_KEY` for `gem push`; `--host` | Nothing; `publish` and `exec` | https://guides.rubygems.org/environment-variables | Search summary of the guide |
| Conda (rattler-build, pixi) | `RATTLER_AUTH_FILE` is a JSON file with the token, so it puts the token on disk; no uploader targets a plain channel | Nothing; the CLI uploads | https://rattler-build.prefix.dev/latest/reference/cli/rattler-build/upload/ and the authentication page (search results) | Upload targets: yes. Auth file: search results |
| git | Credential helper | Nothing: the server hosts no git repositories | | |
| NuGet | Cross-platform plugin protocol, found by the name `nuget-plugin-*` | Only when a NuGet format comes; its spec decides | | No |

**Gradle init script.** For each repository whose URL starts with the base URL of an enrolled
server, the script sets the credentials (user `telesphorepo`):

- A `PublishToMavenRepository` task of such a repository gets the password in `doFirst` from
  `providers.exec { telesphorepo token <repo URL> }`, so the CLI runs only when the task runs.
- A resolution repository of such a URL gets `telesphorepo token --read <repo URL>` through a
  lazy provider, only when Gradle resolves from it.
- The script touches no other repository and adds no repository, plugin or publication. The
  build declares `maven-publish`, its publication and `publishing.repositories.maven { url =
  uri("<base>/maven/<repo>") }` as any Gradle build does; the repo publish tab shows these lines.

A value that Gradle reads at configuration time goes into the configuration cache entry (encrypted
on disk, the plaintext inside it; Gradle configuration cache requirements page). The publish
credentials are read at execution time. Whether a lazy provider in `doFirst` stays out of the entry
is not verified: `TestCLIGradle` greps `.gradle/configuration-cache` for `trs_` and `trx.`, and the
script falls back to forcing the configuration cache off for publish tasks if it finds one.

Common rules for every row:

- The CLI mints one session per repo (device proof, or CI token), and closes each session with
  `DELETE /api/publish/sessions/{id}` at exit, also on Ctrl-C.
- The token goes only into the env of the child process. Config files that the CLI writes hold a
  `${env}` reference, never the token. They go in a private temp folder (`0700`, owner-only ACL on
  Windows) that the CLI removes at exit.
- The CLI uses the project wrapper when there is one (`./gradlew`, `./mvnw`), else the tool on
  `PATH`.
- `exec` sets the same env as `publish` for the repo's format, and always `TELESPHOREPO_URL`,
  `TELESPHOREPO_REPO` and `TELESPHOREPO_TOKEN`.

| Format | `publish` runs | Token handed over as | Source |
|---|---|---|---|
| Maven | `mvn deploy -gs <tmp>/settings.xml`, plus `-DaltDeploymentRepository=telesphorepo::<url>` when the pom has no `<distributionManagement>` on the server | `<tmp>/settings.xml` has one `<server>` per server repo id of the pom (or `telesphorepo`), with `<username>telesphorepo</username><password>${env.TELESPHOREPO_TOKEN_<n>}</password>`. A release and a snapshot repo that differ get two sessions. `-gs` replaces only the installation settings; `~/.m2/settings.xml` stays and merges as the dominant file | Maven settings reference: "`${env.HOME}` etc. for environment variables"; global and user files merge, user dominant. `id::url` needs maven-deploy-plugin 3 **(unverified)**. `-gs` on Maven 4 **(unverified)** |
| Gradle | `./gradlew publish`. After `login`, plain `./gradlew publish` does the same. In CI (no `login`), `publish` adds `--init-script <tmp>/telesphorepo.init.gradle.kts`, the same script | The init script calls `telesphorepo token`, which answers the session of the env under `publish` and `exec`. `exec` also sets `ORG_GRADLE_PROJECT_telesphorepoUsername` and `ORG_GRADLE_PROJECT_telesphorepoPassword` for a build that uses `credentials(PasswordCredentials::class)` on a repository named `telesphorepo` | Gradle init scripts; Gradle "Supported repository protocols": `<name>Username`/`<name>Password` from Gradle properties, including env |
| npm | `npm publish` | Env `npm_config_globalconfig=<tmp>/npmrc` and `npm_config_registry=<url>/npm/<repo>/`. The file holds `//<host>/<base>/npm/<repo>/:_authToken=${TELESPHOREPO_TOKEN}`. The project and user `.npmrc` stay in force | npm docs: `${VARIABLE_NAME}` replacement in npmrc; auth keys scoped to a registry URL; `globalconfig` settable in the environment |
| PyPI | `uv build --out-dir <tmp>/dist` and `uv publish <tmp>/dist/*`; without uv, `python -m build --outdir <tmp>/dist` and `twine upload <tmp>/dist/*` | uv: `UV_PUBLISH_URL`, `UV_PUBLISH_USERNAME=telesphorepo`, `UV_PUBLISH_PASSWORD`. twine: `TWINE_REPOSITORY_URL`, `TWINE_USERNAME`, `TWINE_PASSWORD`, `TWINE_NON_INTERACTIVE=1`. The fresh out dir stops a stale file in `dist/` from going up | uv environment reference; twine docs |
| RubyGems | `gem build <name>.gemspec --output <tmp>/x.gem`, then `gem push --host <url>/gems/<repo> <tmp>/x.gem` | `GEM_HOST_API_KEY`. Nothing goes to `~/.gem/credentials` | RubyGems environment variables guide |
| Cargo | `cargo publish --registry <name>` | `CARGO_REGISTRIES_<NAME>_TOKEN` (name upper case, `-` as `_`) and `CARGO_REGISTRY_GLOBAL_CREDENTIAL_PROVIDERS=cargo:token`, so the env token works also when the user lists other providers. No registry in `.cargo/config.toml`: the CLI adds `CARGO_REGISTRIES_TELESPHOREPO_INDEX=sparse+<url>/cargo/<repo>/index/` and uses `--registry telesphorepo` | Cargo registry authentication: `cargo:token` "checks the `CARGO_REGISTRIES_<NAME>_TOKEN` environment variable. If this credential provider is not listed, then the `*_TOKEN` environment variables will not work" |
| Cargo (provider mode) | Plain `cargo publish` and `cargo build` after `login` (the global provider) | `get` with `operation: publish`: the session of `telesphorepo token` for the repo of `index-url`, answered as `{"Ok": {"kind": "get", "token": "trs_...", "cache": "expires", "expiration": <session end>, "operation_independent": false}}`. `operation: read`: the device read proof, `cache: expires` at the nonce end. A URL of another server: `url-not-supported` | Cargo credential provider protocol |
| Go (reads) | `go get` and `go mod download` after `login` | `telesphorepo goauth` prints `https://<host><base>/go/` for each enrolled server, a blank line, `Authorization: Basic <telesphorepo:device read proof>`, and a blank line | `go help goauth` |
| Conda | `rattler-build build --output-dir <tmp>` (`recipe.yaml`) or `conda build --output-folder <tmp>` (`meta.yaml`); then the CLI uploads each `<subdir>/*.conda` and `*.tar.bz2` | The CLI sends the token itself (`Authorization: Bearer`). No uploader of the conda ecosystem targets a plain channel server: `anaconda upload` talks to the anaconda.org API, and `rattler-build upload` targets quetz, artifactory, prefix, anaconda, cloudsmith and s3 | rattler-build upload docs |
| Go modules | The CLI only. It needs a semver tag at `HEAD` and a clean tree. It builds the zip with `golang.org/x/mod/zip.CreateFromVCS` (prefix `module@version/`, no `vendor`, no nested modules, 500 MiB limits), checks the major version suffix, and uploads it | The CLI sends the token itself | Go modules reference: GOPROXY protocol, zip constraints |
| Helm | `helm package . -d <tmp>`, then `helm push <tmp>/<chart>.tgz oci://<registry host>/<repo> --registry-config <tmp>/config.json` | The registry config names `credHelpers: {"<host>": "telesphorepo"}`; the helper answers the device proof, or `TELESPHOREPO_TOKEN` in CI. OCI only; no classic chart repo | helm push docs (`--registry-config`); Helm 4 with the helper is in `TestDockerClients` |
| Docker | Not `publish`. `docker push` through the helper on a computer; `telesphorepo exec REPO -- docker push IMAGE` in CI | `exec` sets `DOCKER_CONFIG` to a temp folder with `credHelpers` for the registry hosts and the `credHelpers` and `auths` of the user's config, when the user's config has no entry for the host | Docker credential helper protocol (helper now) |
| Raw | `telesphorepo upload REPO/PATH FILE...` | The CLI sends the token itself (Basic, user `publish`) | Existing route |
| Debian | `telesphorepo upload REPO/DIST/COMPONENT x.deb...` | Same | Existing route |

**Minimal server contract of the formats that are not built.** Each format gets its own spec for
its internals (indexes, metadata, proxy, group). This spec fixes only the write side: each write
route is a `route.FormatWrite`, takes a publish session token of its repo, refuses a password and a
read token, and answers 405 on a proxy or a group. A read accepts a read token, a password and a
publish session token of the repo.

| Format (`name`) | Write route | Credential form the tool sends |
|---|---|---|
| npm (`npm`) | `PUT /npm/{repo}/{package}` (npm publish document with the tarball) | `Authorization: Bearer <token>` |
| PyPI (`pypi`) | `POST /pypi/{repo}/` with `:action=file_upload` (legacy upload API) | Basic, any user |
| RubyGems (`gems`) | `POST /gems/{repo}/api/v1/gems` (the gem as body) | `Authorization: <token>` (no scheme) |
| Cargo (`cargo`) | `PUT /cargo/{repo}/api/v1/crates/new`; sparse index at `/cargo/{repo}/index/` with `config.json` `{dl, api, "auth-required": true}` | `Authorization: <token>` (no scheme). Reads also take a `trx.` device proof, checked by `Minter.Check` as the Docker token endpoint does |
| Conda (`conda`) | `PUT /conda/{repo}/{subdir}/{file}`; the server checks that `info/index.json` names that subdir, and a `Rebuild` writes `repodata.json` per subdir | Bearer or Basic |
| Go (`go`) | `PUT /go/{repo}/{escaped module}/@v/{version}.zip`; the server checks the zip with `golang.org/x/mod/zip`, writes `.mod` from its `go.mod` and `.info` with the upload time, and refuses a version that exists | Bearer or Basic |

## 4. Server changes outside the formats

- **Binary.** `cmd/telesphorepo` moves to `cmd/telesphorepo-server`. The Dockerfile entrypoint,
  `go tool task build`, `preview` and `dev`, the README and the CLI section of SPEC.md follow.
- **Mint answer.** `POST /api/publish/sessions`, `/manual` and `/oidc` also answer `format` (the
  repo's format), so the CLI checks the project kind before it builds.
- **Session from a token exchange nonce.** `POST /api/publish/sessions/exchange` with `{proof:
  "trx.<device id>.<nonce>.<signature>", repo}` calls the `Minter` that `/docker/token` uses: one
  session per repo per nonce, and the open session again on a second call. This is what
  `telesphorepo token` calls, so a hook that runs once per repo or once per request gets one
  session per build.
- **Device read proof on format reads.** A format read (GET and HEAD) accepts the `trx.` proof as
  the Basic password or as the Bearer value, checked by `Minter.Check`, with the grants of the
  device's user (`auth.CredDevice`, as the Docker bearer carries it now). It never writes. Cargo
  sends it as a raw `Authorization` value. The reads of the Gradle hook, the Cargo provider and
  `GOAUTH` use it, so a developer computer needs no read token.
- **Device logout.** `DELETE /api/devices/self` with `{device_id, nonce, signature}`: a single-use
  nonce of `POST /api/publish/challenge`, signed as `"telesphorepo revoke v1\n<nonce>\n<device id>"`.
  It revokes the device and ends its sessions, and records `device.revoke`.
- **Enrollment.** `POST /api/devices/enroll` takes `{name, public_key}`. The device file drops
  `client`; its other fields stay:
  `{"server", "device_id", "private_key", "public_key", "created_at"}`. One device per computer
  and server, at `~/.telesphorepo/<server>/device.json`, with `hosts.json` and `exchange.json`
  next to it. The `docker/` subfolder goes away.
- **CI file route.** `GET /api/repos/{name}/ci` also accepts a publish session token of that repo,
  so `init` gets the CI file with the session it mints (and so proves `repo.publish`).
- **Downloads and install scripts.** Section 5.

**CI.** The CLI finds a CI token in this order: GitHub Actions (`ACTIONS_ID_TOKEN_REQUEST_URL` and
`_TOKEN`, audience = the canonical server URL), then `OIDC_TOKEN` (the Jenkins binding of every
Jenkinsfile; any other CI that exports an identity token under that name). With a CI token it
posts to `/api/publish/sessions/oidc`; it never uses a device in CI. CI is detected as the Java
clients do now (`CI`, `GITHUB_ACTIONS`, `JENKINS_URL`, `JENKINS_HOME`).

What changes in `internal/ci/workflow.go` and the tools:

- Every format's CI publish becomes `telesphorepo publish REPO` (Docker: `telesphorepo exec REPO --
  docker push IMAGE`). `PublishSteps.Publish` holds that line; `Toolchain` and `Build` stay per
  tool (for example `actions/setup-java` and `./gradlew build`).
- `GitHubWorkflow` adds one step before the publish: `curl -fsSL <base>/install.sh | sh`. It keeps
  `permissions: id-token: write` and drops the shell prelude that fetches and masks the token:
  `PublishSteps.Token` goes away, because the CLI asks GitHub for the token itself.
- `Jenkinsfile` keeps `withCredentials` for `OIDC_TOKEN` and runs `curl -fsSL <base>/install.sh |
  sh` and `telesphorepo publish REPO` inside it. A Windows agent runs `install.ps1` and the same
  command.
- `format.CurlTool`, `TokenCurl` in CI files, the Gradle `oidcTokenEnv` and the Maven
  `-Dtelesphorepo.oidcTokenEnv=OIDC_TOKEN` go away. Docker CI no longer runs `docker login` with
  the token.

**`/setup.sh` and `/setup.ps1`.** They become `/install.sh` and `/install.ps1` (section 5). The
project edits move into `telesphorepo init`. The setup frame, `format.SetupFragment` and the
`setup_gradle.*` and `setup_maven.*` fragments go away. A Gradle build declares its publishing
repository itself, and a Maven pom needs no change. `init` writes only `telesphorepo.toml` and the
CI file. The repo publish tab shows
`telesphorepo init REPO` and `telesphorepo publish` for every format.

## 5. Security

- **No token in argv.** No command takes a token as an argument. The CLI passes tokens to children
  only through the env, and to the server only in headers and bodies. Config files that it writes
  hold `${env}` references.
- **No token in logs.** The CLI never prints a CI token or a session token. In GitHub Actions it
  prints `::add-mask::` for each token before the child starts, so a tool that echoes it is masked.
  Errors name the session ID, never the token.
- **No token on disk.** The only secret file is `device.json`: mode 600 in a 700 folder, an ACL
  with the owner as the only entry on Windows, and a file that others can read stops the CLI.
  `exchange.json` holds nonces; a nonce mints nothing without the device signature. Temp folders
  hold no secret and go at exit. `--no-configuration-cache` keeps Gradle from caching the token;
  `GEM_HOST_API_KEY`, the Cargo env token, the uv and twine env and the npm env file write no
  credential file. The e2e tests prove it (section 6).
- **Server identity.** The rules of the helper now: canonical URL (scheme, lower-case ASCII host,
  effective port, base path), requests only to that URL, no redirect followed, and the Docker
  helper answers only for the server host and the hosts of `hosts.json`. A registry URL from the
  project config is used only when it matches an enrolled server, or, in CI, the server of
  `telesphorepo.toml`.
- **`token` output.** `telesphorepo token` writes the token only to a stdout that is not a
  terminal, so the token goes to the hook that captured it and not to a screen or a scrollback.
  The hook files that `login` writes (Docker config, Cargo config, Gradle init script, Go env)
  hold commands and URLs, never a token.
- **Short sessions per repo.** One session per repo, at most `publish.session_ttl`. `publish` and
  `exec` close their sessions at exit. A session that a hook got through `token` has no end
  signal, so it ends at the TTL. A child that runs longer than the TTL fails its late uploads; the
  error says so.
- **Exec env.** The child gets the session token of one repo. The CLI removes `OIDC_TOKEN` from the
  child env after it used the token, so a build step cannot mint for other repos with it. Any
  process of the same user can read the child env (for example `/proc/<pid>/environ`); this is the
  same exposure as any env credential and ends with the session.

## 6. Release and install

- **Release task.** `go tool task release` cross-compiles `telesphorepo` (the CLI) and
  `telesphorepo-server` for linux, darwin and windows on amd64 and arm64, as now. `SHA256SUMS`
  lists every archive. The CLI archive holds `telesphorepo` and the CLI license.
- **The server serves the CLI.** The build compiles the six CLI archives first and embeds them in
  the server (`go:embed`, about 20 MB, estimate). `GET /cli/telesphorepo_<os>_<arch>.<ext>` and
  `GET /cli/SHA256SUMS` are public. The CLI version is the server version, and an air-gapped site
  needs no internet. A build without the archives (`go build ./...`) answers 404 with "this build
  has no CLI downloads". `go tool task dev` builds the host target only.
- **Install one-liners** (`/setup.sh` code reused for the frame and the base URL check):
  ```sh
  curl -fsSL https://repo.example.com/install.sh | sh
  ```
  ```powershell
  irm https://repo.example.com/install.ps1 | iex
  ```
  The server writes its base URL and the SHA-256 of each archive into the script. The script picks
  the archive for the OS and CPU, checks the hash, and installs into `~/.local/bin` (Linux, macOS)
  or `%LOCALAPPDATA%\Programs\telesphorepo` (Windows; it adds the folder to the user `PATH`). In
  GitHub Actions it also appends the folder to `$GITHUB_PATH`. A second run replaces the binary.
- **Helper name.** The installer makes `docker-credential-telesphorepo` next to the CLI: a symlink
  on Linux and macOS, a hard link on Windows (no privilege needed), a copy when a link fails. `login`
  checks that the name resolves on `PATH` to the same file and makes the link when it can.

## 7. E2E tests (clients zone, tag `clients`)

The clients zone builds the CLI from this repo; it no longer clones a clients repo. Each test runs
the real tool in a container against the real server, on both stores. After each publish, a test
greps the container file system (outside `/proc`) for `trs_` and for the CI token, and fails on a
match.

| Test | Tools | Proves |
|---|---|---|
| `TestCLILogin` | CLI in a plain container | `login` prints a code; approval through the API; `device.json` schema and modes; `status`; `logout` revokes on the server and deletes the folder; a refused device enrolls again |
| `TestCLIDocker` (replaces `TestDockerCredentialHelper`) | Docker in Docker, Helm 4, ORAS | The link name works; push and pull with no `docker login`; the helper cases of now; `exec` with a CI token pushes from a config with no device |
| `TestCLIMaven` | `maven:3.9` and Maven 4 images | `publish` with no pom change (`altDeploymentRepository`) and with `<distributionManagement>`; release and snapshot repos; `~/.m2/settings.xml` mirrors stay in force; `mvn` resolves the result |
| `TestCLIGradle` | `gradle` image, Kotlin and Groovy builds | After `login`, plain `./gradlew publish` publishes a library and a Gradle plugin; a build with the configuration cache on publishes and its cache folder holds no `trs_` or `trx.`; a private repo resolves through the read proof; a repository of another host gets no credentials; `logout` removes the init script; `publish` in CI passes the script itself |
| `TestCLICargo` | `rust` image | After `login`, plain `cargo publish` through the provider and `cargo build` of a dependent crate; `publish` in CI through `CARGO_REGISTRIES_<NAME>_TOKEN`. Runs when the Cargo format lands |
| `TestCLIGoAuth` | `golang` image | After `login`, `go get` of a module in a private Go repo through `GOAUTH`. Runs when the Go format lands |
| `TestCLICI` | CLI with `jwebserver` serving a Keycloak token at `ACTIONS_ID_TOKEN_REQUEST_URL`; CLI with `OIDC_TOKEN` | GitHub and Jenkins paths mint OIDC sessions with build records; a job without `id-token: write` fails with the message; the child env has no `OIDC_TOKEN` |
| `TestCLIUpload` | CLI, curl, apt | Raw and Debian uploads; apt installs the package |
| `TestCLIInit` | CLI | `telesphorepo.toml` and each CI file; a second run changes nothing |
| `TestInstall` | `debian`, `alpine`, `mcr.microsoft.com/powershell` | `install.sh` and `install.ps1` install a binary that runs, make the helper name, and refuse a wrong hash |

Each new format adds its row when its server side lands: `node` (`npm publish`, `npm install`),
`python` with uv and twine (`pip install`), `ruby` (`gem push`, `gem install`), `golang`
(`publish`, then `go get` with `GOPROXY`), `rattler-build` and `conda-build` (`conda install` from
the channel), each also through `exec`.

## 8. Deleted

- `cmd/docker-credential-telesphorepo` as a separate command (its code moves into the CLI) and its
  release archive.
- The clients repo (`Telesphorepo/clients`): the Gradle plugin, the Maven extension, plugin and
  client library, the shared Java core, its release workflow and `release.sh`. Archive it.
- `internal/clients`, `Extender.PublisherClient`, `Host.Clients`, the `client` field and column of
  enrollments and devices, `client_title` and `client_help` of the device views.
- In `internal/maven`: `clientsVersion`, `setup.go`, the setup fragments and the Gradle plugin and
  Maven extension snippets. In `internal/publish`: `setup.sh`, `setup.ps1` and the setup routes.
  `format.SetupFragment`, `format.CurlTool`, `ci.PublishSteps.Token` and the curl publish scripts.
- In `e2e`: `TestClients`, `TestSetupPowerShell`, `TestSetupScripts`, `TestDockerCredentialHelper`,
  `BuildHelper`, and the env vars `E2E_CLIENTS_DIR` and `E2E_CLIENTS_REF`.
- In AGENTS.md and SPEC.md: the clients repo rows and sections, replaced by the CLI.

## Open questions

1. **License of the CLI.** The helper is AGPL-3.0 with the server; the Java clients were MIT so
   that companies install them without review. The CLI is what every developer installs. MIT for
   `cmd/telesphorepo` (its own LICENSE file), or AGPL with the rest?
2. **Maven hook for plain `mvn deploy`.** Without it, Maven publishes through `telesphorepo
   publish` or `exec`. With it, `login` writes a marked block in `~/.mavenrc` and
   `%USERPROFILE%\mavenrc_pre.cmd` that adds `-Dmaven.ext.class.path=<jar>` to `MAVEN_OPTS`, and
   the jar is a small Java extension (an `AuthenticationSelector` that runs `telesphorepo token`
   for repos on the server). Cost: a Java build in this repo's release, a jar embedded in the CLI,
   an edit of a shell rc file of the user, and no effect in an IDE that embeds Maven or with
   `MAVEN_SKIP_RC`. Recommendation: no hook now; `publish` covers the computer and CI.
3. **CLI downloads in the server.** Embedding the six archives adds about 20 MB (estimate) to the
   server binary and image, and gives version lockstep and air-gapped installs. The other choice is
   install scripts that fetch from GitHub Releases.

## Sources

- Gradle init scripts: https://docs.gradle.org/current/userguide/init_scripts.html
- Gradle configuration cache requirements: https://docs.gradle.org/current/userguide/configuration_cache_requirements.html
- pnpm `tokenHelper`: https://pnpm.io/npmrc
- npm token helper request: https://github.com/npm/cli/issues/8141
- `go help goauth`: https://github.com/golang/go/blob/master/src/cmd/go/internal/help/helpdoc.go
- Maven 3.9 launchers: https://github.com/apache/maven/blob/maven-3.9.x/apache-maven/src/bin/mvn and `mvn.cmd`
- Maven wrapper: https://github.com/apache/maven-wrapper/blob/master/maven-wrapper-distribution/src/resources/only-mvnw

- Maven settings reference: https://maven.apache.org/settings.html
- Gradle repository credentials: https://docs.gradle.org/current/userguide/supported_repository_protocols.html
- npm config files: https://docs.npmjs.com/cli/v11/configuring-npm/npmrc
- uv environment variables: https://docs.astral.sh/uv/reference/environment/
- twine: https://twine.readthedocs.io/en/stable/
- RubyGems environment variables: https://guides.rubygems.org/environment-variables
- Cargo credential provider protocol: https://doc.rust-lang.org/cargo/reference/credential-provider-protocol.html
- Cargo registry authentication: https://doc.rust-lang.org/cargo/reference/registry-authentication.html
- rattler-build upload: https://rattler-build.prefix.dev/latest/reference/cli/rattler-build/upload/
- Go modules reference: https://go.dev/ref/mod
- helm push: https://helm.sh/docs/helm/helm_push/
