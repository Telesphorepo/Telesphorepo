---
name: telesphorepo-migrate
description: Move a software project from a Sonatype Nexus server to a Telesphorepo server, together with the person you work for. Use it when someone asks to switch a project's downloads or publishing from Nexus to Telesphorepo, to replace Nexus addresses in build files, package manager config, lockfiles, Dockerfiles, Helm values, Kubernetes manifests or CI, or to set up `telesphorepo publish` and its CI steps. Covers Gradle and Maven, npm, pnpm, Yarn and Bun, Python (pip, uv, Poetry), Cargo, NuGet, Docker images and Helm charts, Debian (apt) and raw files.
---

# Move a project from Nexus to Telesphorepo

You move one project (one git repository) from Nexus to Telesphorepo. You do the work in the
files. The person you work for does only what needs a person: approving this computer, making a
read token, making repositories, and the CI pipeline (trust policy) on the server. Each time you
need such a step, say exactly what to click and type, then stop and wait until the person says it
is done. Never guess that a step is done.

Rules for the whole task:

- Never write a password, a read token or a publish token into a file, a commit, a command line or
  the chat. Read each secret from an environment variable or a CI secret. Never ask the person to
  paste a token to you.
- Change files outside the project (for example `~/.m2/settings.xml`, `~/.npmrc`,
  `~/.docker/config.json`) only after you show the change and the person says yes.
- Keep every checksum and hash. The files in Telesphorepo are the same bytes as in Nexus.
- Do not commit or push unless the person asks. At the end, list what you changed.

## 1. Ask the person

Ask these in one message, then wait for the answers:

1. The Telesphorepo address, for example `https://repo.example.com`.
2. The Nexus address, for example `https://nexus.example.com`, and for Docker each Nexus Docker
   host or port and the repository behind it (for example `nexus.example.com:8082` is
   `docker-hosted`).
3. The scope:
   - **Downloads only**: change the download addresses, and leave publishing on Nexus.
   - **Full switch-over**: the downloads, then publishing with `telesphorepo publish` and CI, and a
     rehearsal on a test repository.
4. The repository names, Nexus name -> Telesphorepo name. Tell the person where to read them:
   the **Repositories** page of Telesphorepo lists every repository with its format.
5. Which of those repositories are private (only signed-in people can download). The Repositories
   page shows a lock after a private one.

## 2. Find the ecosystems and the Nexus addresses

Search every file of the git repository, including hidden folders, for:

- the Nexus host name (and its IP address or other names, if the person gave any),
- `/repository/`, the path that every Nexus address has,
- each Nexus Docker host and port.

Group the hits by ecosystem with the table below. Read the section of each ecosystem you found in
[Ecosystems](#ecosystems) before you change its files. Tell the person what you found: the
ecosystems, the files, and each Nexus repository the project uses that is not in their list of
names. Ask for the missing names before you go on.

| You find | Ecosystem |
|---|---|
| `pom.xml`, `build.gradle(.kts)`, `settings.gradle(.kts)`, `settings.xml` | Gradle and Maven |
| `package.json`, `.npmrc`, `.yarnrc.yml`, `bunfig.toml` | npm, pnpm, Yarn, Bun |
| `pyproject.toml`, `requirements*.txt`, `setup.py`, `pip.conf`, `uv.lock`, `poetry.lock` | Python |
| `Cargo.toml`, `.cargo/config.toml` | Cargo |
| `*.csproj`, `*.fsproj`, `*.sln`, `*.slnx`, `nuget.config` | NuGet |
| `Dockerfile`, compose files, Kubernetes manifests, `Chart.yaml`, `values*.yaml` | Docker and Helm |
| apt sources, `curl` or `wget` of Nexus files | Debian and raw |

## 3. Install the CLI and approve this computer

Do this for the full switch-over. For downloads only, do it only when the project pulls Docker
images or Helm charts from a private repository on this computer (see Docker and Helm).

1. Run `telesphorepo version`. If the command is missing, install the CLI that the server serves:

   ```sh
   curl -fsSL https://repo.example.com/install.sh | sh          # Linux and macOS
   ```

   ```powershell
   irm https://repo.example.com/install.ps1 | iex               # Windows PowerShell
   ```

   It installs into `~/.local/bin` (Windows: `%LOCALAPPDATA%\Programs\telesphorepo`, added to the
   user `PATH`), with `docker-credential-telesphorepo` next to it. Use the full path when the folder
   is not on the `PATH` of your shell yet.

2. Run `telesphorepo login https://repo.example.com`. The CLI prints `Open <link> and approve code
   <code>`, tries to open the browser, and waits until the person approves. If your shell tool
   shows output only when a command ends, start the command in the background and read its output,
   or ask the person to run it in their own terminal.

3. Tell the person, with the link and the code from the output:

   > I need you to approve this computer. Open <link> and sign in. Check that the page shows the
   > code <code>, then click **Approve device** and confirm with your second step (an
   > authenticator code or a passkey). With single sign-on, you sign in again at your provider
   > instead. Tell me when the page says it is approved.

4. Wait for the CLI line `This computer is device N of https://repo.example.com`. The code works
   for a few minutes; if it ran out, run `telesphorepo login` again.

After this, the computer can publish to every repository where the person's account has
**Publish**. The server checks each repository on each publish. No setup line is needed for each
project. `telesphorepo status` shows the device, whether the server accepts it, the Docker
credential helper entries, and the command that `telesphorepo publish` runs in this folder. It
prints no secret.

## 4. Private repositories: the read token

Downloads from a private repository need a read token. A password or a read token only reads; it
never publishes. Ask for the token only when a repository the project downloads from is private:

> I need a read token, because <repo> is private. In Telesphorepo, open **Settings** ›
> **Tokens**, click **New read token**, type a name such as `<project> builds`, then click
> **Create token**. Copy it now: it shows once. Do not paste it to me. Set it, with your
> username, in these environment variables: <names>. Set them in the terminal you start me from,
> or in your shell's secret store, and add the same names as CI secrets of the project. Tell me
> when they are set.

A single sign-on account cannot make a read token. Then ask the person for an account with a
password (a service account) from the server admin.

Use the environment variable names of the ecosystem's section. Where the tool lets you choose,
use `TELESPHOREPO_READ_USER` and `TELESPHOREPO_READ_TOKEN`. Never use `TELESPHOREPO_TOKEN`: the CLI
sets that name to the publish token when it runs a publish.

## 5. Rewrite the download addresses

For each ecosystem, change every download address to its Telesphorepo address, as its section
says. A Nexus address has the form `https://nexus.example.com/repository/<nexus name>/...`.

Change these files too: Dockerfiles, compose files, Helm values, Kubernetes manifests, CI files
(`.github/workflows/`, `Jenkinsfile`, `.gitlab-ci.yml` and others), shell and PowerShell scripts,
Ansible, Terraform or cloud-init files, devcontainer files, and docs and READMEs.

For downloads only, leave publishing as it is: publish addresses, publish credentials and publish
steps stay on Nexus until the full switch-over.

Then check the result:

1. Search again for the Nexus host, `/repository/` and the Nexus Docker hosts. Explain each hit
   that is left.
2. Run a clean build with an empty cache, as the ecosystem's section says, and check in its output
   that every download comes from the Telesphorepo host. With a private repository, the read token
   variables must be set in your shell; if they are not, ask the person to run the build and send
   you the output.

## 6. Full switch-over: publishing

Telesphorepo's copies of Nexus (the mirrored repositories) refuse uploads until the person switches
that repository over on the server. Each repository switches over on its own, so this project's
repository can switch over while the others keep copying from Nexus. Until then you rehearse
publishing on its rehearsal repository, and the project moves to the real repository after the
switch-over.

1. Ask the person early, so they can do it while you work:

   > I need a rehearsal repository for publishing. In Telesphorepo, open **Settings** › **Server**
   > › **Nexus migration**, open the Nexus server, and in the row of `<real name>` open the
   > actions menu and click **Rehearse publishing**. It makes `<real name>-rehearsal` with the same
   > format and settings, and everyone who can publish to `<real name>` can publish to it. Then, for
   > CI, open **Settings** › **People and CI** › **CI pipelines** (a server admin can do this) and
   > add a CI pipeline for this project, or edit the one it has: <values>. Under the repositories it
   > can publish to, choose `<real name>-rehearsal` (it comes first), and also `<real name>` for
   > after the switch-over. Tell me when it is done.

   The values of the CI pipeline:

   ```text
   GitHub Actions:  where the job runs = GitHub Actions, GitHub repository = <org>/<repo>,
                    workflow file = publish.yml (or the workflow you change), branches = main
   Jenkins:         where the job runs = Jenkins, Jenkins address = <https://jenkins...>,
                    jobs = the full job name, such as team/app
   ```

2. Remove the Nexus publishing from the project and its CI, as the ecosystem's section says: the
   publish addresses, the publish credentials and the publish steps. Keep the build steps that make
   the artifacts. List the CI secrets that are no longer used, so the person can delete them.

3. Write `telesphorepo.toml` in the project folder (the folder of the build file). It holds no
   secret, and the project commits it:

   ```toml
   server = "https://repo.example.com"
   repo = "maven-releases-rehearsal"
   ```

   Optional keys: `kind` when the folder has several project kinds (`maven`, `gradle`, `npm`,
   `python`, `cargo`, `dotnet`, `helm`), `path` for a raw repository (the folder in the repository
   that `telesphorepo publish FILE` uploads into), and `dist` and `component` for a Debian
   repository (default `stable` and `main`). Check `git check-ignore telesphorepo.toml`: if git
   ignores it, CI does not see it; tell the person.

   Docker images need no `telesphorepo.toml`; a Helm chart does.

4. Add the CI steps, as in [CI](#ci) and the ecosystem's section.

5. Publish from this computer: run `telesphorepo status`, check the command it names, then run
   `telesphorepo publish` in the project folder. The CLI mints a publish session for the
   repository of `telesphorepo.toml`, runs the project's own publish command with the token in its
   environment, and closes the session. The build files stay as they are.

   If it fails with a 403 or "no permission", the person's account lacks **Publish** on the
   rehearsal repository (Rehearse publishing gives it only to the people who could publish to
   `<real name>` then). Ask the person to fix it, then run it again.

   To publish the same version again, ask the person to empty the rehearsal: in the same actions
   menu on the Nexus server's page, **Empty it** deletes every file and keeps its settings, its CI
   pipelines and who can publish.

6. Check the published package: install or download it from the rehearsal repository in a new
   temporary folder outside the project, with an empty cache, as the ecosystem's section says.
   Remove the temporary folder afterwards.

7. To rehearse CI too, the person pushes the change to a branch that the CI pipeline lists, and
   tells you the result of the run. Do not push yourself unless the person asks.

8. Leave a short note for the switch-over day, in your report (and in the pull request text, if
   you write one):

   > Switch over `<real name>` on the Nexus server's page (its actions menu, **Switch over**; the
   > other repositories keep copying). Then set `repo` in `telesphorepo.toml` to `<real name>`, check
   > that the CI pipeline lists `<real name>`, publish once, and delete `<real name>-rehearsal`
   > (the same menu, **Delete it**).

## CI

The CLI uses the identity token of the CI job, so the job stores no secret for publishing. Mirror
the files that the server writes (they are on the **Publish to this repository** tab of each
repository):

GitHub Actions, `.github/workflows/publish.yml`, or the same steps in the existing workflow:

```yaml
on:
  push:
    branches: ["main"]

permissions:
  id-token: write
  contents: read

jobs:
  publish:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      # the toolchain steps of the ecosystem
      # the build step of the ecosystem, if it has one
      - run: curl -fsSL https://repo.example.com/install.sh | sh
      - run: telesphorepo publish
```

In an existing workflow, add `id-token: write` to the `permissions` of the job or the file, and
keep its other permissions. The install step puts the CLI on the `PATH` of the later steps.

Jenkins, `Jenkinsfile`: keep the build stage, and add a publish stage after it. The identity
token is good for 10 minutes, so only this stage gets it:

```groovy
    stage('Publish to Telesphorepo') {
      steps {
        withCredentials([string(credentialsId: 'telesphorepo-id-token', variable: 'OIDC_TOKEN')]) {
          script {
            if (isUnix()) {
              sh '''
                curl -fsSL https://repo.example.com/install.sh | sh
                export PATH="$HOME/.local/bin:$PATH"
                telesphorepo publish
              '''
            } else {
              powershell '''
                & ([scriptblock]::Create((irm https://repo.example.com/install.ps1)))
                telesphorepo publish
              '''
            }
          }
        }
      }
    }
```

Tell the person what Jenkins needs once: the plugin **OpenID Connect Provider**, and a credential
of the kind **OpenID Connect id token** with the ID `telesphorepo-id-token` and the audience
`https://repo.example.com` (the address in `telesphorepo.toml`). A freestyle job binds that
credential to the variable `OIDC_TOKEN` under **Environment** › **Use secret text(s) or file(s)**,
then runs the build, the install line, `export PATH="$HOME/.local/bin:$PATH"` and
`telesphorepo publish` in one shell step.

If the project is in a subfolder of the git repository, put `cd <subfolder>` before
`telesphorepo publish`. If a CI job downloads from a private repository, give its build step the
read token variables from CI secrets.

## Ecosystems

### Gradle and Maven

Files: `pom.xml`, `.mvn/maven.config`, any `settings.xml` in the project, `build.gradle(.kts)`,
`settings.gradle(.kts)` (`pluginManagement`, `dependencyResolutionManagement`),
`gradle.properties`, `gradle/*.gradle(.kts)`, `buildSrc/`, `build-logic/`, init scripts, and
`gradle/wrapper/gradle-wrapper.properties` when Nexus serves the Gradle distribution. Outside the
project: `~/.m2/settings.xml`, `~/.gradle/gradle.properties`, `~/.gradle/init.d/`.

Address: `https://nexus.example.com/repository/<nexus name>/` becomes
`https://repo.example.com/maven/<telesphorepo name>`. The files keep their paths, so only the
prefix changes. A `<mirror>` with `<mirrorOf>*</mirrorOf>` points at a Telesphorepo group. A
`distributionUrl` on a Nexus raw repository becomes `https://repo.example.com/raw/<name>/<path>`;
keep `distributionSha256Sum`.

Lockfiles: Gradle lockfiles and `gradle/verification-metadata.xml` hold no addresses. Keep them.

Private: Gradle, in the `maven { }` block, `name = "<gradleName>"` and
`credentials(PasswordCredentials::class)` (Groovy: `credentials(PasswordCredentials)`). The name is
the repository name as a Gradle identifier (`my-libs` is `myLibs`). Gradle then reads the
environment variables `ORG_GRADLE_PROJECT_<gradleName>Username` and
`ORG_GRADLE_PROJECT_<gradleName>Password`. Maven: a `<server>` whose `<id>` is the `<id>` of the
repository (or mirror), with `<username>${env.TELESPHOREPO_READ_USER}</username>` and
`<password>${env.TELESPHOREPO_READ_TOKEN}</password>`, in `~/.m2/settings.xml` and in a settings
file of the project that CI passes with `-s`.

Gotchas: a hosted repository holds only its own packages; keep `mavenCentral()` or use a group
with a proxy of Maven Central. A Maven group merges the versions of all its members. Maven refuses
`http://` addresses other than `localhost`; Gradle needs `isAllowInsecureProtocol = true` for one.

Check: `./gradlew build --refresh-dependencies`, or
`mvn -U -Dmaven.repo.local=<new empty folder> verify`.

Publishing: remove `<distributionManagement>` entries and `<server>` credentials for Nexus, and
Nexus staging plugins (`nexus-staging-maven-plugin`, `io.github.gradle-nexus.publish-plugin`) when
they target this Nexus. In Gradle, remove the Nexus repository from `publishing { repositories {
} }`: `gradle publish` publishes to every repository listed there. Keep the `maven-publish` plugin,
the publications, `group` and `version`; a Gradle project needs them. `telesphorepo publish` runs
`mvn deploy` (or `./mvnw`) with the repository as the target, or `gradle publish` (or `./gradlew`)
with an init script that adds the repository to each project that applies `maven-publish`.

CI: GitHub Actions toolchain `actions/setup-java@v6` with `distribution: temurin` and
`java-version: 21` (use the project's Java version); no build step. Jenkins build stage:
`./gradlew build` (Windows `.\gradlew.bat build`), or `mvn -B verify`.

Verify: in a new temporary folder, a minimal Gradle or Maven build that depends on the published
`group:artifact:version` from `https://repo.example.com/maven/<rehearsal>`, built with an empty
cache.

### npm, pnpm, Yarn, Bun

Files: `.npmrc` (project and `~/.npmrc`), `.yarnrc.yml`, `.yarnrc` (Yarn 1), `bunfig.toml`,
`package.json` (`publishConfig`, scripts), lockfiles, Dockerfiles (`npm config set registry`,
build arguments), CI (`registry-url` of `actions/setup-node`).

Address: `https://nexus.example.com/repository/<nexus name>/` becomes
`https://repo.example.com/npm/<telesphorepo name>/`, with the trailing slash. Scoped registries
(`@acme:registry=`) change the same way. Remove the Nexus `_auth`, `always-auth` and `email`
lines.

Yarn 2 and later: `npmRegistryServer` and `npmScopes.<scope>.npmRegistryServer`. On a hosted
repository, Yarn 4 needs `npmMinimalAgeGate: 0`, or it installs a version only once it is a week
old. On a group or proxy, list your own packages in `npmPreapprovedPackages` instead. A plain
`http://` server needs its host in `unsafeHttpWhitelist`. Bun reads `.npmrc`; `bunfig.toml` can
set `[install] registry` and scopes.

Lockfiles: in `package-lock.json`, Yarn 1 `yarn.lock`, `pnpm-lock.yaml` and `bun.lock`, change
the prefix `https://nexus.example.com/repository/<nexus name>/` to
`https://repo.example.com/npm/<telesphorepo name>/`. The tarballs keep their paths
(`<name>/-/<file>.tgz`) and their `integrity`. A Yarn 2+ `yarn.lock` holds no address. Then run
the frozen install of the tool: `npm ci`, `pnpm install --frozen-lockfile`,
`yarn install --immutable` or `bun install --frozen-lockfile`.

Private: `.npmrc` gets `//repo.example.com/npm/<name>/:_authToken=${NPM_TOKEN}`; Yarn gets
`npmAlwaysAuth: true` and `npmAuthToken: "${NPM_TOKEN}"`. The person sets `NPM_TOKEN` to the read
token, and CI gives it to the install step from a secret.

Gotchas: in an npm group, the first member that has a package name owns it, and the group serves
every version of that name from that member only (a Nexus group merges them). If the project
needs versions of one name from two members, tell the person. Telesphorepo does not serve
`npm audit`; point an audit step at `--registry=https://registry.npmjs.org/` or tell the person.

Publishing: remove `publishConfig.registry` and Nexus publish auth lines, and the `npm publish`
steps of CI with their secrets. `telesphorepo publish` picks the tool by the lockfile:
`pnpm publish` (pnpm-lock.yaml), `yarn npm publish` (yarn.lock), `bun publish` (bun.lock), else
`npm publish`. In a workspace, ask which packages publish, and put a `telesphorepo.toml` in each
of their folders.

CI: GitHub Actions toolchain and build step, by tool:

```text
npm   actions/setup-node@v5 (node-version: lts/*)                  run: npm ci
pnpm  pnpm/action-setup@v4, then actions/setup-node@v5             run: pnpm install --frozen-lockfile
Yarn  actions/setup-node@v5                                        run: corepack enable && yarn install --immutable
Bun   oven-sh/setup-bun@v2                                         run: bun install --frozen-lockfile
```

Jenkins build stage: the same install command.

Verify: in a new temporary folder, `npm init -y`, an `.npmrc` with the rehearsal registry, then
`npm install <package>@<version>`.

### Python

Files: `pip.conf` or `pip.ini` (project, `~/.config/pip/pip.conf`, `/etc/pip.conf`),
`requirements*.txt` and constraints files (`--index-url`, `--extra-index-url`, `--trusted-host`),
`pyproject.toml` (`[[tool.uv.index]]`, `[[tool.poetry.source]]`, `[tool.pdm]`), `uv.toml`,
`poetry.toml`, `setup.cfg`, `tox.ini`, `noxfile.py`, Dockerfiles and CI (`PIP_INDEX_URL`,
`PIP_EXTRA_INDEX_URL`, `UV_INDEX_URL`, `UV_DEFAULT_INDEX`), `.pypirc` (publishing).

Address: `https://nexus.example.com/repository/<nexus name>/simple/` (with or without `simple/`)
becomes `https://repo.example.com/pypi/<telesphorepo name>/simple/`. On a plain `http://` server,
pip needs `trusted-host = <host>` and uv `allow-insecure-host`.

Lockfiles: `uv.lock` and `poetry.lock` hold file addresses whose paths differ between Nexus and
Telesphorepo, so run the lock command instead of editing: `uv lock`, or `poetry lock` (Poetry 1:
`poetry lock --no-update`). Then check `git diff`: the same versions and the same hashes, other
addresses only. In requirements files with `--hash`, change only the index lines.

Private: pip reads credentials from the address, so put the whole address in the environment:
`PIP_INDEX_URL=https://<user>:<token>@repo.example.com/pypi/<name>/simple/`, and keep the
address without credentials in the files. uv: `UV_INDEX_<NAME>_USERNAME` and
`UV_INDEX_<NAME>_PASSWORD` (the index name in upper case, other characters as `_`). Poetry:
`POETRY_HTTP_BASIC_<SOURCE>_USERNAME` and `POETRY_HTTP_BASIC_<SOURCE>_PASSWORD`.

Gotchas: a hosted repository holds only your own packages; their dependencies need a group with a
PyPI proxy, or PyPI itself. In a PyPI group, the first member that has a project name owns it,
with every file of that project.

Publishing: remove `.pypirc` use, `twine upload`, `uv publish` and `publish-url`, `poetry publish
-r` and the Poetry repository config for Nexus, and their CI secrets. `telesphorepo publish` runs
`poetry build` (with `[tool.poetry]`), `uv build` (with `uv.lock`) or `python -m build`, then
uploads every wheel and sdist. `telesphorepo publish dist/*.whl` uploads files you built.

CI: GitHub Actions toolchain: uv `astral-sh/setup-uv@v7`; Poetry `actions/setup-python@v6`
(`python-version: 3.x`) and `run: pipx install poetry`; pip `actions/setup-python@v6` and
`run: python -m pip install build`. Jenkins: uv and Poetry need no build stage, but the Jenkins
agent needs the tool installed; pip `python3 -m pip install --user build`.

Verify: in a new temporary virtual environment, `pip install --index-url
https://repo.example.com/pypi/<rehearsal>/simple/ --extra-index-url https://pypi.org/simple/
<package>==<version>`.

### Cargo

Files: `.cargo/config.toml` (project and `~/.cargo/config.toml`), `Cargo.toml` (`registry =` of
dependencies, `publish =`), `Cargo.lock`, Dockerfiles, CI.

Address: the index becomes `sparse+https://repo.example.com/cargo/<telesphorepo name>/index/`.
Keep the registry names that `Cargo.toml` uses, and change only `index` in
`[registries.<name>]`. A proxy or group of crates.io also takes `[source.crates-io]
replace-with = "<name>"`.

Lockfiles: in `Cargo.lock`, change each `source = "sparse+<Nexus index>"` (or `registry+`) to the
Telesphorepo index, exactly as in the config. Keep every `checksum`. Then run
`cargo fetch --locked`.

Private: on the registry, `credential-provider = "cargo:token"`; the token comes from
`CARGO_REGISTRIES_<NAME>_TOKEN` (the registry name in upper case, `-` as `_`). This needs Cargo
1.74 or later.

Gotchas: in a Cargo group, the first member that has a crate name owns it.

Publishing: set `publish = ["telesphorepo"]` in `Cargo.toml` where it names the Nexus registry:
the CLI publishes with `--registry telesphorepo`, and Cargo refuses a registry that the list does not name. Do not
remove the key, or a plain `cargo publish` goes to crates.io. Remove `cargo publish --registry
<nexus>` steps and Nexus tokens. `telesphorepo publish` runs `cargo publish` to the repository.

CI: GitHub Actions toolchain `dtolnay/rust-toolchain@stable`; Jenkins build stage `cargo test`.

Verify: in a new temporary folder, `cargo new verify`, a `.cargo/config.toml` with the rehearsal
registry, `cargo add <crate>@<version> --registry <name>`, then `cargo build`.

### NuGet

Files: `nuget.config` or `NuGet.Config` (the solution folder and its parents, and the user's),
`Directory.Build.props` and `.targets` (`RestoreSources`, `RestoreAdditionalProjectSources`),
`*.csproj` and `*.fsproj`, Dockerfiles and CI (`dotnet nuget add source`, `--source`,
`source-url` of `actions/setup-dotnet`).

Address: `https://nexus.example.com/repository/<nexus name>/index.json` (or the v2 address
without `index.json`) becomes `https://repo.example.com/nuget/<telesphorepo name>/index.json`.
Keep the source keys, so `packageSourceMapping` and the credentials still match. A source on plain
`http://` needs `allowInsecureConnections="true"`.

Lockfiles: `packages.lock.json` holds no address. Keep it.

Private: `<packageSourceCredentials>` under the source key (a key that starts with a digit is
written `_x003N_...`), with `Username` `%NUGET_USERNAME%` and `ClearTextPassword` `%NUGET_TOKEN%`.

Gotchas: in a NuGet group, the first member that has a package id owns it.

Publishing: remove `dotnet nuget push` or `nuget push` steps to Nexus, `<apikeys>`, and the API key
secrets. `telesphorepo publish` runs `dotnet pack -c Release` and pushes each package;
`telesphorepo publish <file>.nupkg` pushes packages you built.

CI: GitHub Actions toolchain `actions/setup-dotnet@v5` with `dotnet-version: 10.0.x` (use the
project's SDK); Jenkins build stage `dotnet build -c Release`.

Verify: in a new temporary folder, `dotnet new console`, a `nuget.config` with the rehearsal
source, then `dotnet add package <id> --version <version>`.

### Docker and Helm

Files: `Dockerfile*` (`FROM`, `COPY --from`), compose files (`image:`), Kubernetes manifests and
Kustomize (`image:`, `images:`, `imagePullSecrets`), Helm `Chart.yaml` (`dependencies`),
`Chart.lock`, `values*.yaml` (`image.registry`, `image.repository`), helmfile, skaffold, Tiltfile,
devcontainer files, CI (`docker login`, `docker pull`, `helm registry login`, `helm repo add`).

Address: Telesphorepo serves the registry at the root of its host, and the repository name is the
first part of the image name. `nexus.example.com:8082/team/app:1.0` (a Nexus Docker port or host)
becomes `repo.example.com/<telesphorepo name>/team/app:1.0`. Through a proxy of Docker Hub, an
official image is `repo.example.com/<proxy name>/nginx:1.27` (or `library/nginx`). The manifests
are the same, so each `@sha256:` digest stays valid. A plain `http://` server needs its host in
`insecure-registries` of `/etc/docker/daemon.json`.

Helm: Telesphorepo keeps charts as OCI artifacts in Docker repositories. A chart dependency
`repository: https://nexus.../repository/<helm repo>/` becomes `oci://repo.example.com/<name>`,
and `helm repo add` lines for it go. Nexus Helm repositories are not copied by the Nexus import, so
ask the person which Docker repository holds the charts. Then run `helm dependency update` to
renew `Chart.lock`.

Private: on a computer with the CLI device (step 3), add the credential helper of the CLI to the
Docker config file (`~/.docker/config.json`, or `$DOCKER_CONFIG/config.json`), keeping every
other key: `"credHelpers": {"repo.example.com": "telesphorepo"}` (with `:port` when the server
has a port). Docker, buildx, Podman, Helm and ORAS then pull what the person can read and push
with no `docker login`. `telesphorepo login` does not add this entry: show the change and add it
after the person says yes. `telesphorepo status` shows the entry. Elsewhere (CI, servers,
Kubernetes), log in with the read token from a secret: `printf '%s' "$TELESPHOREPO_READ_TOKEN" |
docker login repo.example.com -u "$TELESPHOREPO_READ_USER" --password-stdin`. For Kubernetes, the
person makes the pull secret (`kubectl create secret docker-registry ...`); the manifests name only
the secret.

Publishing: remove `docker login` to Nexus, Nexus push credentials, and `helm push` or `curl`
uploads to Nexus. Images publish with `docker push repo.example.com/<repo>/<image>:<tag>` through
the credential helper. A chart folder (with `Chart.yaml`) gets a `telesphorepo.toml`, and
`telesphorepo publish` runs `helm package`, then `helm push` to `oci://repo.example.com/<repo>`.

CI for images, GitHub Actions (mirror it exactly; change `app` to the image name):

```yaml
      - uses: actions/checkout@v7
      - run: docker build -t "repo.example.com/<repo>/app:$GITHUB_SHA" .
      - run: curl -fsSL https://repo.example.com/install.sh | sh
      - name: Publish to <repo>
        run: |
          export DOCKER_CONFIG="$(mktemp -d)"
          printf '%s\n' '{"credHelpers":{"repo.example.com":"telesphorepo"}}' > "$DOCKER_CONFIG/config.json"
          docker push "repo.example.com/<repo>/app:$GITHUB_SHA"
        env:
          TELESPHOREPO_SERVER: "https://repo.example.com"
```

Jenkins: the build stage runs `docker build -t "repo.example.com/<repo>/app:${GIT_COMMIT:-$BUILD_NUMBER}" .`;
the publish stage wraps the `withCredentials` block in
`withEnv(['TELESPHOREPO_SERVER=https://repo.example.com'])` and runs, after the install line and the
`PATH` line, the three lines of the GitHub step with `${GIT_COMMIT:-$BUILD_NUMBER}` as the tag. With
its identity token, a job reads only what anonymous people read, so to build from a private base
image, log in with the read token before the build. For a Helm chart, CI installs Helm (for example
`azure/setup-helm@v4`), then the CLI, and runs `telesphorepo publish`.

Verify: remove the local image, then `docker pull repo.example.com/<rehearsal>/<image>:<tag>`; or
`helm pull oci://repo.example.com/<rehearsal>/<chart> --version <version>` in a new temporary
folder.

### Debian and raw

Files: apt sources (`/etc/apt/sources.list.d/*.list` and `*.sources`, and the Dockerfiles,
Ansible, cloud-init or scripts that write them), keyrings, `curl` and `wget` of Nexus files in
scripts, Dockerfiles and CI, docs.

apt address: `deb https://nexus.example.com/repository/<nexus name>/ <dist> main` becomes
`deb [signed-by=/etc/apt/keyrings/<name>.asc] https://repo.example.com/debian/<name> <dist> main`.
The import keeps the distribution of the Nexus repository and puts every package in the component
`main`. Ask the person which signing key the repository got at the import: with the Nexus key,
clients keep their trust; with another key, they install it from
`https://repo.example.com/debian/<name>/key.asc` into `/etc/apt/keyrings/<name>.asc`.

Raw address: `https://nexus.example.com/repository/<nexus name>/<path>` becomes
`https://repo.example.com/raw/<name>/<path>`. The files keep their paths.

Private: apt reads credentials only from `/etc/apt/auth.conf.d/`. Give the person these lines to
run; they read the token at a hidden prompt:

```sh
printf 'Read token: '; read -rs READ_TOKEN; echo
sudo install -m 600 /dev/null /etc/apt/auth.conf.d/<name>.conf
printf 'machine %s login <user> password %s\n' https://repo.example.com/debian/<name> "$READ_TOKEN" \
  | sudo tee -a /etc/apt/auth.conf.d/<name>.conf > /dev/null
```

For `curl`, pass the credentials on stdin, never as an argument:
`printf 'user = "%s:%s"\n' "$TELESPHOREPO_READ_USER" "$TELESPHOREPO_READ_TOKEN" | curl -fsSL -K - -o <file> <address>`.

Publishing: remove `curl -u` uploads to Nexus and their passwords. `telesphorepo publish <file>`
uploads files: for raw into the folder `path` of `telesphorepo.toml`, for Debian each `.deb` into
`dist` and `component`.

CI: no toolchain; the publish step is `telesphorepo publish <files>`.

Verify: raw, download the file with `curl` and compare its SHA-256; Debian, in a throwaway
`debian` container, add the rehearsal source and key, then `apt-get update` and
`apt-get install <package>`.

## Report

End with:

- every file you changed, grouped by ecosystem,
- each Nexus address left, with the reason,
- each place you were not sure about,
- the CI secrets that are no longer used,
- for the full switch-over, the note for the switch-over day from step 6.
