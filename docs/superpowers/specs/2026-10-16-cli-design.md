# One publishing client: design spec

Status: for approval. Tool facts were checked on 2026-10-16 against the docs listed at the end.
A fact marked **(unverified)** comes from memory. The e2e test of its format confirms it before
the format ships.

## Goal

One Go program, `telesphorepo`, publishes to every format, from a developer computer and from CI.
A person learns two commands:

- `setup`, once per project, from a line that they copy from the repo page;
- `publish`, every time, in the project folder.

The CLI finds the project from a marker file and its lockfile. It runs the standard publish
command of that ecosystem, with the token in the env. It never reads build logic and never
re-implements a tool. Docker and Cargo publish with their native command through a credential
helper. The Gradle plugin, the Maven extension and the separate Docker credential helper binary go
away.

## 1. Decisions

| Question | Decision | Reason |
|---|---|---|
| How many publish commands | **One: `telesphorepo publish`**, for every format with a publish step of its own (section 4) | One habit across all projects. The docs of every format say the same thing |
| How the repo is known | **`telesphorepo.toml`, committed in the project.** `setup` writes it | The repo choice belongs to the project, not to one computer. Teammates and CI read the same file |
| How a project is set up | **One copied line** with a single-use setup code from the repo page (section 2) | The code enrolls the computer and names the repo in one step. No browser approval, no repo prompt |
| How the token gets to the tool | The CLI runs the tool as a child process. It passes the token in the env, or in a temp config file that refers to the env. Gradle gets an init script and Maven gets `-DaltDeploymentRepository` and a temp settings file. The person types only `telesphorepo publish` | No build file edit, so Groovy DSL, Kotlin DSL and project layout do not matter |
| Docker and Cargo | **The native command** through a credential helper of the same binary. `setup` asks y/N and then edits the user's tool config | `docker push` and `cargo publish` have a credential hook. A second command for them would only duplicate it |
| Subcommands of the server binary, or a separate binary | **Separate binary.** The client is `cmd/telesphorepo`. The server binary becomes `telesphorepo-server` (`cmd/telesphorepo-server`) | The server binary is 33 MB (measured: `bin/telesphorepo.exe`, windows/amd64). Estimate for the client: 7 to 9 MB. The short name goes to the program that people type |
| Where the client code lives | This repo: `cmd/telesphorepo` and packages under it. It imports no server package | One release, one e2e suite, one version for server and client |
| Gradle plugin, Maven extension, clients repo | **Retire all.** Stop releases, mark the published artifacts as deprecated in their README, archive `Telesphorepo/clients` | Maven Central does not delete artifacts |
| Publisher client registry | **Delete it.** One client enrolls devices | The approval page names "Telesphorepo CLI" |
| License of the CLI | **MIT** for `cmd/telesphorepo` and its packages, in their own LICENSE file. The server stays AGPL-3.0 | Every developer and CI job installs the CLI; companies install MIT code without review |
| Where the CLI downloads come from | **The server embeds the CLI archives** of its own build (section 8) | Version lockstep with the server and installs with no internet |
| Which CLI version is current | **The server of the current project.** The CLI syncs to it, as an NTP client syncs to its stratum (section 9) | The server serves the CLI of its own build, so its version is the right version |

New Go module for the client: `github.com/BurntSushi/toml` (read `telesphorepo.toml` and the
Cargo config).

## 2. Setup

**The copied line.** The Publish tab of a repo has a **Set up a project** button. The button asks
for the second factor, then shows one line for the OS of the browser, and the other OS on a
toggle:

```sh
curl -fsSL https://repo.example.com/install.sh | sh -s -- setup 'https://repo.example.com/#/setup/trsu_...'
```

```powershell
& ([scriptblock]::Create((irm https://repo.example.com/install.ps1))) setup 'https://repo.example.com/#/setup/trsu_...'
```

The install script installs or replaces the CLI (section 7) and then runs
`telesphorepo setup <link>`. The CLI reads the server from the link and the code from the
fragment. The code is in the fragment, so a browser that opens the link never sends it to the
server. The UI page `#/setup/<code>` says: run this line in a terminal in the project folder.

**The setup code.** `trsu_` and 32 random bytes (base64url). The server stores its SHA-256 as an
`auth_challenges` row of kind `setup_code`, with the user and the repo, for 10 minutes. The first
use deletes it. A copy in shell history is then worthless. Creating a code needs a UI session, a
fresh second factor and `repo.publish` on the repo, the same bar as a device approval.

**What `setup` does:**

1. **Device.** If this computer has no device that the server accepts, the CLI makes an Ed25519
   key and calls `POST /api/devices/setup` with `{code, name, public_key}`. The code takes the
   place of the browser approval. With a usable device, the CLI calls the same route with the
   device proof instead of a new key, and the server only checks the code. The answer is
   `{device_id, user, repo, format, kind}`.
2. **Confirmation.** The CLI prints `publishing to releases (maven) as sam`.
3. **Project or hook,** by the category of the format (section 4):
   - *Publish category:* the CLI finds the project (section 3). If no marker matches, it stops and
     names the markers of that format. Then it writes `telesphorepo.toml` in the project root.
     If the file exists with another value, it shows the change and asks y/N.
   - *Helper category:* the CLI writes no project file. It shows the hook (section 4), asks
     "add the credential helper to `~/.docker/config.json`? [y/N]", edits the file on yes, and
     prints the native command, such as `docker push repo.example.com/releases/app:1.0`. On no,
     it prints the line to add.
4. **Git check.** For `telesphorepo.toml`, the CLI runs `git check-ignore -v telesphorepo.toml`.
   If git ignores the file, the CLI says that CI and teammates will not see it, and names the
   `.gitignore` line that matches.
5. **CI file.** On a TTY, the CLI asks for a CI file: none, GitHub Actions, Jenkinsfile or Jenkins
   freestyle. It shows each change first; a change to a file that exists needs `y`. The server
   gives the file (`GET /api/repos/{name}/ci`, which also accepts a publish session of that
   repo).

**The project file:**

```toml
server = "https://repo.example.com"
repo = "releases"
```

A Docker or Cargo project has no project file. A project with several marker kinds adds
`kind = "<kind>"` (section 3).

**A teammate.** A person who clones a project that has `telesphorepo.toml` runs
`telesphorepo publish`. With no device for that server, the CLI runs the device flow inline (the
link and the code, the browser on a TTY, the poll) and then continues. They do not need a setup
code.

## 3. Publish

`telesphorepo publish [FILE...]`:

1. **Project file.** The CLI walks up from the current folder to the nearest `telesphorepo.toml`,
   as git finds `.git`. Each project of a monorepo can have its own. With no file, the CLI stops:
   on a computer it names the Publish tab of the server; in CI it says to commit the file.
2. **Credential.** On a computer: the device proof. In CI: the CI token (section 6). With it, the
   CLI mints one session for the repo. The mint answer includes the repo's `format`.
3. **Project.** The CLI looks for the markers of that format in the folder of the project file. A
   repo of the helper category stops here with the native command and the hook.
4. **Run.** The CLI runs the command of the row, with the token as the row says. It passes the
   exit code through. It closes the session with `DELETE /api/publish/sessions/{id}` at exit, also
   on Ctrl-C.

FILE arguments are for formats without a project: raw (`telesphorepo publish dist/app.zip`, with
`path = "<folder>"` in the project file) and Debian (`telesphorepo publish build/x.deb`, with
`dist` and `component` in the project file). Large files use the resumable upload routes.

**Detection.** A marker picks the ecosystem. A lockfile or a wrapper picks the tool. "Lands with
the format" means that the server format does not exist yet. That row ships with the format and
with its e2e test.

| Format | Marker | Tool choice | `publish` runs | Token handed over as |
|---|---|---|---|---|
| Maven | `pom.xml` | `mvnw`/`mvnw.cmd`, else `mvn` | `mvn deploy -gs <tmp>/settings.xml -DaltDeploymentRepository=telesphorepo::<url>` | `<tmp>/settings.xml` has `<server><id>telesphorepo</id><username>telesphorepo</username><password>${env.TELESPHOREPO_TOKEN}</password></server>`. `-gs` replaces only the installation settings, so the user's `~/.m2/settings.xml` stays in force and merges as the dominant file. `id::url` needs maven-deploy-plugin 3 **(unverified)**. `-gs` on Maven 4 **(unverified)** |
| Maven (Gradle) | `settings.gradle(.kts)` or `build.gradle(.kts)` | `gradlew`/`gradlew.bat`, else `gradle` | `gradle publish --init-script <tmp>/telesphorepo.init.gradle.kts` | The init script adds a repository `telesphorepo` (URL and credentials from the env) to `publishing.repositories` of each project that applies `maven-publish`. It touches no other repository. No project applies `maven-publish`: the CLI stops and shows the plugin line |
| npm (lands with the format) | `package.json` | `pnpm-lock.yaml` → pnpm, `yarn.lock` → yarn, `bun.lock` → bun, else npm | `pnpm publish` / `yarn npm publish` / `bun publish` / `npm publish` | `npm_config_registry=<url>/npm/<repo>/` and `npm_config_globalconfig=<tmp>/npmrc` with `//<host>/<base>/npm/<repo>/:_authToken=${TELESPHOREPO_TOKEN}`. yarn: `YARN_NPM_PUBLISH_REGISTRY` and `YARN_NPM_AUTH_TOKEN` **(unverified)**. bun: `NPM_CONFIG_REGISTRY` and `NPM_CONFIG_TOKEN` **(unverified)** |
| PyPI (lands with the format) | `pyproject.toml`, `setup.py` | `uv.lock` → uv, `poetry.lock` → poetry, `pdm.lock` → pdm, else uv on `PATH`, else `python -m build` + twine | `uv build --out-dir <tmp>` + `uv publish <tmp>/*` / `poetry publish --build -r telesphorepo` / `pdm publish -r <url>` / `python -m build --outdir <tmp>` + `twine upload <tmp>/*` | uv: `UV_PUBLISH_URL`, `UV_PUBLISH_USERNAME`, `UV_PUBLISH_PASSWORD`. poetry: `POETRY_REPOSITORIES_TELESPHOREPO_URL`, `POETRY_HTTP_BASIC_TELESPHOREPO_USERNAME` and `_PASSWORD` **(unverified)**. pdm: `PDM_PUBLISH_USERNAME`, `PDM_PUBLISH_PASSWORD` **(unverified)**. twine: `TWINE_REPOSITORY_URL`, `TWINE_USERNAME`, `TWINE_PASSWORD`, `TWINE_NON_INTERACTIVE=1`. PEP 517 makes the build the same for any backend, so venvs and Conda envs do not matter |
| RubyGems (lands with the format) | `*.gemspec` (one) | gem | `gem build <name>.gemspec --output <tmp>/x.gem`, then `gem push --host <url> <tmp>/x.gem` | `GEM_HOST_API_KEY` |
| Docker (Helm charts) | `Chart.yaml` | helm | `helm package . -d <tmp>`, then `helm push <tmp>/<chart>.tgz oci://<host>/<repo> --registry-config <tmp>/config.json` | The temp registry config names `credHelpers: {"<host>": "telesphorepo"}`, and the helper answers the session token from the env |
| raw | none | the CLI | upload of FILE... | The CLI sends the token itself |
| Debian | none | the CLI | upload of FILE... | The CLI sends the token itself |

A Docker repo with a `Dockerfile` and no `Chart.yaml` is the helper category: `publish` prints
`docker push` and the hook. When a format has more than one marker kind in the folder, the CLI
asks on a TTY. Without a TTY, it stops and lists them, and `kind = "<kind>"` in the project file
picks one.

**Common rules for every row:**

- The token goes only into the env of the child process, as `TELESPHOREPO_TOKEN` and the tool's
  own variables. Config files that the CLI writes hold a `${env}` reference, never the token.
  They go in a private temp folder (`0700`, owner-only ACL on Windows) that the CLI removes at
  exit.
- The CLI finds a tool with `exec.LookPath`, which also finds `.exe` and `.cmd` on Windows. A
  wrapper in the project comes first.
- The CLI prints the command that it runs, without the token, before it runs it.

## 4. The helper category

| Tool | Hook | Config file that `setup` edits on yes | Source |
|---|---|---|---|
| Docker, buildx, Podman, Helm 4, ORAS | `docker-credential-telesphorepo`, the same binary under a second name | `credHelpers.<host>: "telesphorepo"` in `$DOCKER_CONFIG/config.json` (default `~/.docker/config.json`), for the server host and the hosts of `hosts.json` | The helper now; `TestDockerCredentialHelper`, `TestDockerClients` |
| Cargo (lands with the format) | Credential provider protocol, `operation: publish` and `read` | `"telesphorepo cargo-credential"` appended to `[registry] global-credential-providers` in `$CARGO_HOME/config.toml` (default `~/.cargo/config.toml`), and the registry entry `[registries.<repo>] index = "sparse+<url>/cargo/<repo>/index/"` | https://doc.rust-lang.org/cargo/reference/credential-provider-protocol.html |

- Docker `get` with a device answers a session that it mints through the exchange nonce, as the
  helper does now. One build gets one session per repo.
- Cargo `get` with `operation: publish` answers a session for the repo of `index-url`, with
  `cache: expires` at the session end. `operation: read` answers the device read proof. A URL of
  another server answers `url-not-supported`.
- In CI, with no device, both helpers mint from the CI token of section 6 themselves. So a CI job
  runs plain `docker push` or `cargo publish` after the install step.
- `logout` removes the entries that `setup` added and keeps all other entries.

## 5. Commands

| Command | Does |
|---|---|
| `telesphorepo setup LINK` | Section 2 |
| `telesphorepo publish [FILE...]` | Section 3 |
| `telesphorepo login SERVER` | The device flow alone, for a computer that only reads, or for a person who wants to enroll first. Saves `~/.telesphorepo/<server>/device.json` and `hosts.json` |
| `telesphorepo logout [SERVER]` | Revokes the device on the server, removes the hook entries of that server, and deletes `~/.telesphorepo/<server>/` |
| `telesphorepo status` | Each enrolled server: the device, whether the server accepts it, the hooks. The current folder: the project file, the format, the tool that `publish` would run. Prints no secret |
| `telesphorepo update [SERVER]` | Installs the CLI of the server: the server of the current project, else SERVER, else the one enrolled server. Section 9 |
| `telesphorepo version`, `help [command]` | |
| `docker-credential-telesphorepo get\|store\|erase\|list` | The Docker helper protocol. Docker runs it |
| `telesphorepo cargo-credential --cargo-plugin` | The Cargo provider protocol. Cargo runs it. Not in the overview |

Every command has an example-led `--help`. No command takes a token as an argument.

## 6. Server changes

- **Binary.** `cmd/telesphorepo` moves to `cmd/telesphorepo-server`. The Dockerfile entrypoint,
  `go tool task build`, `preview` and `dev`, the README and the CLI section of SPEC.md follow.
- **Setup codes.** `POST /api/publish/setup-codes` with `{repo}`: a UI session, a fresh second
  factor and `repo.publish` on the repo (`route.RepoRule`). It answers `{code, link, expires_at}`
  and records `setup_code.create`. `POST /api/devices/setup` (public) takes `{code, name,
  public_key}` or `{code, device_id, nonce, signature}`. It deletes the code, enrolls or checks
  the device, and records `device.setup`. A wrong, used or expired code answers 401
  `this setup line is used or expired: copy a new one from <base_url>/#/repo/<name>`, without the
  repo name when the code is unknown. `auth_challenges.kind` gets the value `setup_code`.
- **Mint answer.** `POST /api/publish/sessions` and `/oidc` also answer `format` and `kind`.
- **Session from a token exchange nonce.** `POST /api/publish/sessions/exchange` with `{proof:
  "trx.<device id>.<nonce>.<signature>", repo}` calls the `Minter` that `/docker/token` uses: one
  session per repo per nonce, and the open session again on a second call. The helpers use it.
- **Device read proof on format reads.** A format read (GET and HEAD) accepts the `trx.` proof as
  the Basic password or as the Bearer value, with the grants of the device's user. It never
  writes. The Cargo provider uses it.
- **Device logout.** `DELETE /api/devices/self` with `{device_id, nonce, signature}`, signed as
  `"telesphorepo revoke v1\n<nonce>\n<device id>"`. It revokes the device, ends its sessions and
  records `device.revoke`.
- **Enrollment.** `POST /api/devices/enroll` takes `{name, public_key}`. The device file is
  `{"server", "device_id", "private_key", "public_key", "created_at"}`, one per computer and
  server, at `~/.telesphorepo/<server>/device.json`, with `hosts.json` and `exchange.json` next
  to it.
- **CI file route.** `GET /api/repos/{name}/ci` also accepts a publish session of that repo.
- **Publish tab.** For every format of the publish category, the tab shows the setup button and
  `telesphorepo publish`. For the helper category, it shows the setup button and the native
  command. The format descriptor gives the category (`format.Spec.Publish`: `cli` or `helper`).
- **Install scripts.** `/setup.sh` and `/setup.ps1` become `/install.sh` and `/install.ps1`
  (section 7). The setup frame, `format.SetupFragment` and the `setup_gradle.*` and `setup_maven.*`
  fragments go away.

**CI.** The CLI and the helpers find a CI token in this order: GitHub Actions
(`ACTIONS_ID_TOKEN_REQUEST_URL` and `_TOKEN`, audience = the canonical server URL), then
`OIDC_TOKEN` (the Jenkins binding of every Jenkinsfile, and any CI that exports an identity token
under that name). With a CI token, they post to `/api/publish/sessions/oidc` and never use a
device. CI is detected by `CI`, `GITHUB_ACTIONS`, `JENKINS_URL` and `JENKINS_HOME`.

Changes in `internal/ci/workflow.go` and the tools:

- Every format's CI publish becomes the install step and `telesphorepo publish`. For Docker, it
  is the install step and `docker push IMAGE`. `PublishSteps.Publish` holds that line;
  `Toolchain` and `Build` stay per tool.
- `GitHubWorkflow` adds `curl -fsSL <base>/install.sh | sh` before the publish. It keeps
  `permissions: id-token: write` and drops the shell prelude that fetches and masks the token
  (`PublishSteps.Token` goes away).
- `Jenkinsfile` keeps `withCredentials` for `OIDC_TOKEN` and runs the install and the publish
  inside it. A Windows agent runs `install.ps1`.
- `format.CurlTool`, `TokenCurl`, the Gradle `oidcTokenEnv` and the Maven
  `-Dtelesphorepo.oidcTokenEnv=OIDC_TOKEN` go away. Docker CI no longer runs `docker login`.

## 7. Security

- **No token in argv.** The setup code is the one secret in a command line. It works once, for
  10 minutes, and enrolls only a device of the user who made it.
- **No token in logs.** The CLI never prints a CI token, a session token or a setup code. In
  GitHub Actions it prints `::add-mask::` for each token before the child starts. Errors name the
  session ID.
- **No token on disk.** The only secret file is `device.json`: mode 600 in a 700 folder, an ACL
  with the owner as the only entry on Windows. A file that others can read stops the CLI. Temp
  folders hold no secret and go at exit. The tool config files that `setup` edits hold commands
  and URLs, never a token.
- **Server identity.** Canonical URL (scheme, lower-case ASCII host, effective port, base path),
  requests only to that URL, no redirect followed. The Docker helper answers only for the server
  host and the hosts of `hosts.json`. The server of `telesphorepo.toml` must be an enrolled server
  on a computer, or the server of the CI token's trust policy in CI.
- **Short sessions per repo.** One session per repo, at most `publish.session_ttl`. `publish`
  closes its session at exit. A helper session ends at the TTL. A child that runs longer than the
  TTL fails its late uploads, and the error says so.
- **Child env.** The child gets the session token of one repo. The CLI removes `OIDC_TOKEN` from
  the child env, so a build step cannot mint for other repos with it.

## 8. Release, install and test platforms

- **Release task.** `go tool task release` cross-compiles `telesphorepo` and
  `telesphorepo-server` for linux, darwin and windows on amd64 and arm64. `SHA256SUMS` lists every
  archive.
- **The server serves the CLI.** The build compiles the six CLI archives first and embeds them
  (`go:embed`, about 20 MB, estimate). `GET /cli/telesphorepo_<os>_<arch>.<ext>` and
  `GET /cli/SHA256SUMS` are public. A build without the archives answers 404 with "this build has
  no CLI downloads". `go tool task dev` builds the host target only.
- **Install scripts.** The server writes its base URL and the SHA-256 of each archive into
  `install.sh` and `install.ps1`. The script picks the archive for the OS and CPU, checks the
  hash, and installs into `~/.local/bin` (Linux, macOS) or `%LOCALAPPDATA%\Programs\telesphorepo`
  (Windows; it adds the folder to the user `PATH`). It makes `docker-credential-telesphorepo` next
  to the CLI: a symlink on Linux and macOS, a hard link on Windows, a copy when a link fails. In
  GitHub Actions it appends the folder to `$GITHUB_PATH`. Arguments after the script name go to
  the CLI.
- **OS layer.** The CLI keeps its files in `~/.telesphorepo` on every OS. The tool config paths
  come from the tool's env var with its documented default. The OS-specific code is the
  owner-only ACL, the parent process lookup and opening the browser; it moves from the helper.
- **Platforms in tests.** Linux containers run every e2e test. Every change vets and builds the
  CLI for `GOOS=darwin` and `GOOS=windows`. The maintainer's Windows computer runs the core zone
  natively. A GitHub Actions job on `macos-latest` runs only on release tags: it starts the
  darwin server binary, installs the CLI with `install.sh`, and runs setup and publish for a
  Maven and a raw project. Both repos are public, so the standard runner is free. Agents never
  start that job.

## 9. Version check and update

**Two numbers.** `version` is the release (`main.version`, stamped by `go tool task build` and
`release`). `protocol` is an integer that changes only when the CLI–server API breaks. The CLI
imports no server package, so each side keeps its own `protocol` constant. `TestCLIVersion` checks
that the two are equal.

**The exchange.** Every request of the CLI and of its helper modes sends
`User-Agent: telesphorepo-cli/<version> (<os>/<arch>)`. Every response of the server to such a
request carries `Telesphorepo-CLI: <version>; protocol=<n>`, the CLI of its own build. The CLI makes
no request for the check.

| State | On a TTY | Without a TTY (CI, a hook) |
|---|---|---|
| Same version | Nothing | Nothing |
| Other version, same protocol | After the command ends: `the server has CLI v0.4.0, you have v0.3.1. Update now? [Y/n]`, at most once a day per server (`~/.telesphorepo/<server>/update.json`) | One notice line on stderr, at most once a day per server |
| The CLI protocol is older | The server answers 426 `update the CLI: telesphorepo update`. The CLI asks Y/n, updates and runs the command again | The 426 message with the install line; exit 1 |
| The CLI protocol is newer | The CLI stops: `this server is older than your CLI; run telesphorepo update to install the server's version` | The same message; exit 1 |

The Docker and Cargo helpers never ask. They print the notice on stderr only. A CLI with version
`dev` and a server with version `dev` skip the check.

**`telesphorepo update`:**

1. It downloads `GET /cli/telesphorepo_<os>_<arch>.<ext>` and `GET /cli/SHA256SUMS` from the server
   and checks the hash.
2. It writes the new binary in the folder of the running binary, then renames it into place. On
   Windows it first renames the running file to `telesphorepo.exe.old`; every start deletes a
   `.old` file that is left.
3. It makes the `docker-credential-telesphorepo` name again, because a Windows hard link keeps the
   old file.
4. When the folder is not writable, it names the folder and prints the install line of the server.

The update can install an older version: the target is the server's CLI, not the newest one.

**Several servers.** One binary follows the server of the current project. Another enrolled server
on another version still works while the protocols are equal, and gives the message of the table
when they are not.

## 10. E2E tests (clients zone, tag `clients`)

The clients zone builds the CLI from this repo. Each test runs the real tool in a container
against the real server, on both stores. After each publish, a test greps the container file
system (outside `/proc`) for `trs_`, `trsu_` and the CI token, and fails on a match.

| Test | Tools | Proves |
|---|---|---|
| `TestCLISetup` | CLI, git | The setup line from `POST /api/publish/setup-codes` enrolls and writes `telesphorepo.toml`; a second use and an expired code fail with the message; an ignored file gets the warning; a CI file on request; a helper repo writes no project file, and y/N edits or keeps `config.json` |
| `TestCLILogin` | CLI | `publish` with no device runs the device flow inline; `status`; `logout` revokes on the server, removes the hook entries and deletes the folder |
| `TestCLIDocker` (replaces `TestDockerCredentialHelper`) | Docker in Docker, Helm 4, ORAS | Push and pull with no `docker login` after setup; `publish` of a chart; plain `docker push` in CI with a CI token and no device |
| `TestCLIMaven` | `maven:3.9`, Maven 4 | `publish` with no pom change; the user's mirrors stay in force; `mvn` resolves the result |
| `TestCLIGradle` | `gradle`, Kotlin and Groovy builds | `publish` of a library and of a Gradle plugin; a repository of another host gets no credentials; a build with no `maven-publish` gets the message |
| `TestCLICI` | CLI with `jwebserver` serving a Keycloak token at `ACTIONS_ID_TOKEN_REQUEST_URL`; CLI with `OIDC_TOKEN` | GitHub and Jenkins paths mint OIDC sessions with build records; the child env has no `OIDC_TOKEN` |
| `TestCLIUpload` | CLI, curl, apt | Raw and Debian `publish FILE`; apt installs the package |
| `TestCLIVersion` | CLI built with two versions and two protocols | The header on each answer; the notice and the daily limit; 426 for an old protocol; the stop for a new protocol; `update` replaces the binary and the helper name and checks the hash; the protocol constants of CLI and server are equal |
| `TestInstall` | `debian`, `alpine`, `mcr.microsoft.com/powershell` | `install.sh` and `install.ps1` install a binary that runs, make the helper name, pass arguments to the CLI, and refuse a wrong hash |

Each new format adds its row when its server side lands: npm with each of npm, pnpm, yarn and bun;
PyPI with uv, poetry, pdm and twine; RubyGems; Cargo through the provider.

## 11. Deleted

- `cmd/docker-credential-telesphorepo` as a separate command (its code moves into the CLI) and its
  release archive.
- The clients repo (`Telesphorepo/clients`): archived.
- `internal/clients`, `Extender.PublisherClient`, `Host.Clients`, the `client` field and column of
  enrollments and devices, `client_title` and `client_help` of the device views.
- In `internal/maven`: `clientsVersion`, `setup.go`, the setup fragments and the Gradle plugin and
  Maven extension snippets. In `internal/publish`: `setup.go`, `setup.sh`, `setup.ps1` and the
  setup routes. `format.SetupFragment`, `format.CurlTool`, `ci.PublishSteps.Token` and the curl
  publish scripts.
- In `e2e`: `TestClients`, `TestSetupPowerShell`, `TestSetupScripts`,
  `TestDockerCredentialHelper`, `BuildHelper`, and the env vars `E2E_CLIENTS_DIR` and
  `E2E_CLIENTS_REF`.
- In AGENTS.md and SPEC.md: the clients repo rows and sections, replaced by the CLI.

## Sources

- Maven settings reference: https://maven.apache.org/settings.html
- Gradle init scripts: https://docs.gradle.org/current/userguide/init_scripts.html
- Gradle repository credentials: https://docs.gradle.org/current/userguide/supported_repository_protocols.html
- npm config files: https://docs.npmjs.com/cli/v11/configuring-npm/npmrc
- pnpm npmrc: https://pnpm.io/npmrc
- uv environment variables: https://docs.astral.sh/uv/reference/environment/
- twine: https://twine.readthedocs.io/en/stable/
- RubyGems environment variables: https://guides.rubygems.org/environment-variables
- Cargo credential provider protocol: https://doc.rust-lang.org/cargo/reference/credential-provider-protocol.html
- Cargo registry authentication: https://doc.rust-lang.org/cargo/reference/registry-authentication.html
- helm push: https://helm.sh/docs/helm/helm_push/
