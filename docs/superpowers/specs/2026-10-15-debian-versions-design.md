# Versions and build timeline for Debian packages

## Goal

A Debian package shows its versions and the timeline of its snapshot builds, the same way a
Maven artifact does. A package `urn` with the versions `3.0~SNAPSHOT.20261009160251` …
`3.0~SNAPSHOT.20261014211855` shows one version, `3.0~SNAPSHOT`, with 9 builds on a timeline,
newest first, and not 9 separate versions.

## What is wrong now

`format.Versioned` works on folders: Maven keeps each version in its own folder, and
`Builds(path)` takes that folder. Debian keeps every version of a package in one pool folder
(`pool/main/u/urn/`), so the Debian plugin cannot implement the interface, and a Debian
package has no Versions view and no timeline.

## Core change: a version is (artifact, version), not a folder

The project has no users, so the interface changes in place:

```go
type Versioned interface {
	Versions(ctx context.Context, repo *repos.Repo, artifactPath, after string, limit int) (*Artifact, []Version, string, error)
	Builds(ctx context.Context, repo *repos.Repo, artifactPath, version, after string, limit int) (*Artifact, *Version, []Build, string, error)
}
```

- `Version.Path` goes away. A version is named by its artifact folder and its version string.
- The builds route becomes `GET /api/repos/{name}/builds?path=<artifact folder>&version=<v>&after=&limit=`.
- Maven derives its version folder as `artifactPath + version + "/"`, as it does now inside
  `version()`.
- The UI names a version place as the artifact folder plus `version` in the address
  (`#/repo/x/files?path=pool/main/u/urn/&version=3.0~SNAPSHOT`). For Maven, opening the
  version folder in the tree also opens that place (the folder path maps to the artifact folder
  and the folder name), so links to Maven version folders keep their meaning.
  `pages/repo/files/place.ts`, `columns.svelte.ts`, `browse.ts`, `VersionsView`, `BuildsView`,
  `ArtifactDetails` and the delete dialog move to the new shape.

## Debian versions

The Debian plugin implements `Versioned` from `deb_packages`, not from folder listings.

- An artifact folder is `pool/<component>/<prefix>/<package>/` where the repo has packages of
  that name in that component. `Artifact.Title` is the package name, `Kind` is "package".
- A **snapshot version** is a Debian version of the form `<base>~SNAPSHOT.<stamp>` or
  `<base>~SNAPSHOT<sep><stamp>`, where `<stamp>` is 8 to 14 digits (`yyyyMMdd[HHmmss]`) and
  `<sep>` is one of `.`, `+`, `~`, `-`. Its version is `<base>~SNAPSHOT`; each stamp is a build.
  A version without that form is a release with one build.
- The **files of a build** are its `.deb` files, one per architecture. `BuildFile.Classifier` is
  the architecture, `Extension` is `deb`.
- Order: versions by Debian version order (`dpkg --compare-versions` rules, which the plugin
  implements in Go); builds by stamp, newest first. A snapshot version sorts at its base
  version (`3.0~SNAPSHOT` before `3.0`, as Debian's `~` rule says).
- `PublishedAt` is the newest `updated_at` of the build's files; `Provenance` comes from the
  build's publish session, as for Maven.
- Queries: add `CREATE INDEX deb_packages_package ON deb_packages(repo_id, component, package)`
  to `internal/debian/migrations/0001_init.sql` in place, and the queries in
  `internal/debian/queries`. Run `go tool task gen`.

## UI

- The Debian Files tab gets the Versions view at a package folder and the build timeline at a
  snapshot version, with the same components as Maven (`VersionsView`, `BuildsView`).
- The builds list shows the architecture of each file in place of the Maven classifier.
- The usage snippet of a Debian version pins it: `apt-get install urn=3.0~SNAPSHOT.20261014211855`
  for a build, and the plain `apt-get install urn` for the version (the newest).

## Tests (e2e)

`e2e/debian_versions_test.go`, on every storage backend:

- Publish `urn` as `0.1~SNAPSHOT.20261003234857`, `3.0~SNAPSHOT.20261009160251`,
  `3.0~SNAPSHOT.20261014211855` and `3.0` (two architectures for one build). The versions
  route lists `3.0`, `3.0~SNAPSHOT` (2 builds) and `0.1~SNAPSHOT` (1 build), in that order.
- The builds route for `3.0~SNAPSHOT` lists the two stamps newest first, and the two-arch build
  has two files with their architectures.
- A folder that is not a package answers `artifact: null`.
- The Maven versions and builds tests (`e2e/versions_test.go`) move to the new route shape.

## Docs

`SPEC.md`: the new route shape, the Debian snapshot rule, the index. `docs/design/rationale.md`:
add Debian to the "versions and builds" entry.
