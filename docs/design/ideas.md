# Ideas and decisions

Owner's ideas (2026-10-14) with an assessment, and the owner's decision.

## Snapshot builds collapsed by default

- **Idea:** a Maven `-SNAPSHOT` version can hold many timestamped builds. Show only the latest build
  by default.
- **Decision (owner):** do it.
- **Assessment:** it is progressive disclosure, not hiding: one row "1.4.0-SNAPSHOT · latest
  build 2 hours ago", with "12 earlier builds" one click away. Most people resolve only the latest
  snapshot; Nexus lists every timestamped file, the clutter the heuristic evaluation counted
  (12 of 15 files in a version folder were noise).

## Build provenance: link each build to its commit

- **Idea:** attribute a git commit to each build, Jenkins-style, with links to the CI run and the
  commit.
- **Feasibility:**
  - **CI (no extra setup):** a publish session minted from a GitHub Actions OIDC token already
    carries `repository`, `sha`, `ref`, `run_id`, `workflow_ref`; the server verifies the token, so
    it can record them and link to the commit and the run. Jenkins' OIDC token carries the job and
    build URL.
  - **Laptop:** the Gradle plugin and the Maven extension send the local commit and a dirty flag
    when they open a publish session.
  - **Storage:** per publish session; every file uploaded in that session inherits it.
- **Decision (owner):** GitHub Actions builds only. Local builds are not attributed: a local commit may be unpushed or the tree dirty, so the SHA may not exist anywhere, and a wrong link is worse than none.
- **Why it matters:** this is provenance (npm and PyPI show it); no self-hosted artifact repository
  does it well. It extends the ticket idea: each ticket is stamped with where it came from.

## Finder-style views

- **List** (default): zebra stripes, dense single-line rows.
- **Columns** (Miller columns): the best fit for deep Maven trees; every level stays visible,
  arrow keys move through it.
- **Icons:** skip. Artifacts have no thumbnails; icons add nothing over the list.
- **Cover Flow for versions and builds of one artifact:** a horizontal timeline of felt tickets
  (version, date, commit, status). Use it only where time is the natural axis, so it carries
  meaning and does not become a gimmick.
- The chosen view is remembered per user.
- **Decision (owner):** build List, Tree (expandable), Columns and Cover Flow; no Icons view. Switching views must not change the layout: Cover Flow uses the same area as the other views.
