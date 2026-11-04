# Signed download links

A person copies a direct URL to one file of a private repository and pastes it into a tool that
takes only a plain URL (a game panel that downloads a server jar). The link lives a short time.

## Shape

- New module `links` (`internal/links`): its settings group, its audit action, its route, and the
  verifier of the new credential kind. No table, no migration, no query.
- `POST /api/repos/{name}/links` `{path, ttl?}` (ttl in seconds). Policy: a UI session and
  `repo.download` on the repo (`route.Policy{Accept: CredSession, Repo: Read/repo.download}`).
  400 for a bad path or ttl, 404 when the file is not in the repo (a group: in a member the
  person reads), 409 for a format without a file URL under `/<format>/{repo}/` (Docker).
  Answers `{url, expires_at}`.
- The URL is the file's format URL, the one the web UI's Download button uses, plus a query:
  `<base_url>/<format>/<repo>/<path>?texp=<unix>&tsig=<user id>.<repo id>.<mac>`. The file name
  stays the last path segment. No format reads `texp` or `tsig`.
- `mac` = base64url(HMAC-SHA256(key, "telesphorepo link v1\n" repo id "\n" user id "\n" texp
  "\n" path)), where path is the server path `/<format>/<repo>/<file path>` (decoded).

## Credential

- `auth.CredLink`, a new credential kind. `route.FormatRead` accepts it; the router allows it
  only on GET and HEAD routes with a repo read and no server permission (like a device).
- `Identify` reads it (`Read.Link`) when the request has no `Authorization` header and has
  `tsig`: the module's verifier checks expiry, the MAC (constant time) over the request's
  `URL.Path`, then loads the user with `principalByID` (disabled or gone: refused). A failure
  is 401 and counts against the client address like a bad device proof.
- The principal carries `Link.RepoID`; the repo step of the route refuses another repo (a repo
  deleted and created again under the same name). The access engine then decides with the
  user's current grants plus the repo's allowed networks for the downloader's address, as for
  any read. A link never writes: a write route reads no link.

## Secret and settings (group `links`, `settings.general`, Settings › General)

- `links.ttl` (`1h`), `links.max_ttl` (`7d`, at most 30 days; `links.ttl` at most it).
- `links.secret` (secret, `""`). The first link generates 32 random bytes and saves them like a
  PATCH (`settings.update`, keys only). Rotation: an admin enters a new value (at least 32
  characters) or resets it; every link ends, and the next link generates a fresh one. Read live
  from the snapshot per request.

## Logs, audit, edge

- `file.link.create` (subsystem `repo`): repo, target the path, `detail.expires_at`. Never the
  signature. The server logs paths only, never query strings (checked: `route.Fail`, the docker
  errors, the access log).
- Edge: a link to a private repo is anonymous to the Worker, so it already goes to the server. A
  group with a public member could be answered from that member, so the Worker passes every
  request with `tsig` to the server. Links stay server-only.
- Web: only `api.repos.link(name, path, ttl?)` and its type.
