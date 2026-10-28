// Typed functions for every /api route. Pages import `api` and call e.g. api.repos.list().
// Routes and JSON shapes follow internal/api; see SPEC.md for the rules behind them.

import { request, requestOrNull, requestText, requestWithProgress, sendWithProgress } from "./client";
import type * as T from "./types";
import { globMatch } from "$lib/glob";

export * from "./client";
export type * from "./types";

const enc = encodeURIComponent;
const repo = (name: string) => `/api/repos/${enc(name)}`;
const page = (p: T.PageParams = {}) => ({ after: p.after, limit: p.limit });

/** The X-Telesphorepo-Proof header of a fresh second factor: "totp <code>", "passkey <base64url JSON>" or "grant <grant>". */
function proofHeader(p: T.FreshProof): string {
  if ("grant" in p) return "grant " + p.grant;
  if (p.totp_code) return "totp " + p.totp_code.replace(/\s/g, "");
  if (p.passkey === undefined) return "";
  const bytes = new TextEncoder().encode(JSON.stringify(p.passkey));
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return "passkey " + btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

export const api = {
  auth: {
    config: () => request<T.AuthConfig>("GET", "/api/auth/config"),
    /** Step 1. A user with a second factor gets {second_factor, methods}; anyone else is signed in. */
    login: (username: string, password: string) =>
      request<T.LoginAnswer>("POST", "/api/auth/login", { body: { username, password } }),
    /** Passkey options for the pending sign-in (step 2). */
    loginPasskeyOptions: () => request<T.PasskeyOptions>("POST", "/api/auth/login/passkey"),
    /** Step 2: a TOTP code, a passkey assertion or a recovery code. */
    loginSecond: (proof: T.Proof) => request<T.SessionAnswer>("POST", "/api/auth/login/second", { body: proof }),
    logout: () => request("POST", "/api/auth/logout"),
    /** Full-page navigation target of "Sign in with SSO". */
    oidcLoginURL: "/api/auth/oidc/login",
  },

  /** The caller; 401 when signed out. */
  me: () => request<T.Me>("GET", "/api/me"),
  /** The repositories the caller can use, by name. */
  myAccess: (p?: T.PageParams) => request<T.UserAccess>("GET", "/api/me/access", { query: page(p) }),

  security: {
    get: () => request<T.Factors>("GET", "/api/security"),
    beginTOTP: (name: string) => request<T.TOTPSetup>("POST", "/api/security/totp/begin", { body: { name } }),
    confirmTOTP: (setup_id: string, code: string) =>
      request<T.TOTPFactor>("POST", "/api/security/totp/confirm", { body: { setup_id, code } }),
    deleteTOTP: (id: number) => request("DELETE", `/api/security/totp/${id}`),
    beginPasskey: () => request<T.PasskeyOptions>("POST", "/api/security/passkeys/begin"),
    finishPasskey: (name: string, credential: unknown) =>
      request<T.Passkey>("POST", "/api/security/passkeys/finish", { body: { name, credential } }),
    /** Options for a fresh passkey check before a sensitive action. */
    assertPasskeyOptions: () => request<T.PasskeyOptions>("POST", "/api/security/passkeys/assert"),
    deletePasskey: (id: number) => request("DELETE", `/api/security/passkeys/${id}`),
    /** Replaces all recovery codes; the codes are shown once. */
    recoveryCodes: () => request<{ codes: string[] }>("POST", "/api/security/recovery-codes"),
  },

  tokens: {
    list: () => request<T.Token[]>("GET", "/api/tokens"),
    create: (name: string, expires_in_days?: number) =>
      request<T.NewToken>("POST", "/api/tokens", { body: { name, expires_in_days } }),
    delete: (id: number) => request("DELETE", `/api/tokens/${id}`),
  },

  users: {
    /** People: every account, or the ones whose username starts with q. */
    list: (p?: T.PageParams, q?: string) => request<T.Page<T.User>>("GET", "/api/users", { query: { q, ...page(p) } }),
    /** People: one account; 404 when no account has the name. */
    get: (username: string) => request<T.User>("GET", `/api/users/${enc(username)}`),
    /** People: the usernames of the enabled administrators (every server permission). */
    administrators: async () => (await request<{ usernames: string[] }>("GET", "/api/administrators")).usernames,
    /** People: the repositories one person can use, by repository name. */
    access: (username: string, p?: T.PageParams) =>
      request<T.UserAccess>("GET", `/api/users/${enc(username)}/access`, { query: page(p) }),
    /** people.manage: a new local account with server permissions that the caller holds. */
    create: (u: T.NewUser) => request<T.User>("POST", "/api/users", { body: u }),
    /** Changes a password: your own, or with People that of a person whose permissions you all hold. */
    update: (username: string, patch: T.UserPatch) =>
      request<T.User>("PATCH", `/api/users/${enc(username)}`, { body: patch }),
    /**
     * people.manage: replaces the server permissions of a person. The caller adds and removes only
     * permissions they hold (403); 409 when no enabled person would keep people.manage.
     */
    setPermissions: (username: string, permissions: T.PermId[]) =>
      request<T.User>("PUT", `/api/users/${enc(username)}/permissions`, { body: { permissions } }),
    /** People: disables or enables 1 to 1000 people. A disabled person's sessions end. 400 when the list holds the caller and disabled is true. */
    setDisabled: (usernames: string[], disabled: boolean) =>
      request<T.Changed>("PATCH", "/api/users", { body: { usernames, disabled } }),
  },

  /** People whose username starts with q, for a person picker (at most 20 a page). Needs People, or Manage access on a repository. */
  people: {
    search: (q: string, p?: T.PageParams) => request<T.Page<T.PersonName>>("GET", "/api/people", { query: { q, ...page(p) } }),
  },

  /** The permission registry: every subsystem with its permissions (no credential needed). */
  permissions: () => request<T.PermissionRegistry>("GET", "/api/permissions"),

  /** The version of the server and the released entries of its change log newer than since (every one without it). No credential needed. */
  changes: (since?: string) => request<T.Changes>("GET", "/api/changes", { query: { since } }),

  /** The repository permissions of each pair of person and repository. */
  grants: {
    /**
     * Sets the whole permission set of every pair of user and repository; [] removes the pair.
     * Needs repo.access on every repository, and the caller adds or removes only permissions
     * they hold there (403).
     */
    set: (users: string[], repos: string[], perms: T.PermId[]) =>
      request<T.Changed>("PUT", "/api/grants", { body: { users, repos, perms } }),
    /**
     * Several sets at once, in one transaction: each group sets its own permissions on every pair
     * of its users and repositories. The checks of `set` apply to every group; a refusal changes
     * nothing. At most 1000 pairs in all, and each pair in one group only.
     */
    setGroups: (groups: T.GrantGroup[]) => request<T.Changed>("PUT", "/api/grants", { body: { groups } }),
  },

  formats: {
    list: () => request<T.Format[]>("GET", "/api/formats"),
  },

  identity: {
    get: () => request<T.Identity>("GET", "/api/identity"),
    /** General settings: replaces the logo with a PNG, JPEG or WebP file. Answers the identity. */
    putLogo: (file: Blob) => request<T.Identity>("PUT", "/api/identity/logo", { raw: file }),
    /** General settings: removes the logo. Answers the identity. */
    deleteLogo: () => request<T.Identity>("DELETE", "/api/identity/logo"),
  },

  repos: {
    /**
     * A keyset page by name of the repositories the caller can see (a visitor: the public ones),
     * narrowed on the server: `q` a name prefix, `format`, `kind`, `perm` (the caller holds one of
     * them). Items have no `config`; GET of one repository has it.
     */
    list: (f: T.RepoFilter = {}, p?: T.PageParams) =>
      request<T.Page<T.Repo>>("GET", "/api/repos", { query: { q: f.q, format: f.format, kind: f.kind, perm: f.perm?.join(","), ...page(p) } }),
    /** The limit repositories (at most 50) published last that the caller sees, where they hold one of perm. */
    recent: (limit: number, perm?: T.PermId[]) =>
      request<T.Page<T.Repo>>("GET", "/api/repos", { query: { sort: "published", limit, perm: perm?.join(",") } }),
    /** The repositories of names that the caller sees (at most 200), by name. */
    named: async (names: string[]) =>
      names.length === 0
        ? []
        : (await request<T.Page<T.Repo>>("GET", "/api/repos?" + names.map((n) => "name=" + enc(n)).join("&"))).items,
    get: (name: string) => request<T.Repo>("GET", repo(name)),
    create: (r: T.NewRepo) => request<T.Repo>("POST", "/api/repos", { body: r }),
    update: (name: string, patch: T.RepoPatch) => request<T.Repo>("PATCH", repo(name), { body: patch }),
    /** Edit settings: replaces the members of a group and their order (1 to 50 names). Answers the group. */
    setMembers: (name: string, members: string[]) =>
      request<T.Repo>("PUT", repo(name) + "/members", { body: { members } }),
    /** Edit settings: checks the remote of a proxy now. A remote that does not answer throws an ApiError with the reason. */
    testProxy: (name: string) => request<{ ok: boolean }>("POST", repo(name) + "/proxy/test"),
    /** Edit settings on each: makes 1 to 1000 repositories public (true) or private (false). */
    setPublic: (names: string[], anonymous_read: boolean) =>
      request<T.Changed>("PATCH", "/api/repos", { body: { names, anonymous_read } }),
    /**
     * All repositories: starts the delete of 1 to 1000 repositories and their files (202). 409 when one has an
     * operation (nothing is deleted). Each delete runs on the server: the repository keeps its delete operation
     * with the progress until it is gone (404); a failed one keeps a failed operation that can be resumed or cleared.
     */
    delete: (names: string[]) => request<T.ReposDeleted>("POST", "/api/repos/delete", { body: { names } }),
    /** All repositories: runs the failed operation of the repository again (a failed delete goes on deleting the repository on the server). */
    resumeOperation: (name: string) => request<{ ok: boolean }>("POST", repo(name) + "/operation/resume"),
    /** All repositories: ends the failed operation of the repository; the repository keeps the files that are left. */
    clearOperation: (name: string) => request<void>("DELETE", repo(name) + "/operation"),
    /** All repositories: gives the repository a new name; the old name stays as a redirect. 409 when the name is taken. */
    rename: (name: string, to: string) => request<T.Repo>("POST", repo(name) + "/rename", { body: { name: to } }),
    /** The other names that answer for the repository, by name. */
    redirects: (name: string) => request<T.RepoRedirect[]>("GET", repo(name) + "/redirects"),
    /** All repositories: makes another name answer for the repository. 409 when a repository or a redirect holds it. */
    addRedirect: (name: string, redirect: string) =>
      request<T.RepoRedirect>("POST", repo(name) + "/redirects", { body: { name: redirect } }),
    /** All repositories: removes a redirect; the old name answers 404 and is free again. */
    removeRedirect: (name: string, redirect: string) =>
      request<void>("DELETE", repo(name) + "/redirects/" + encodeURIComponent(redirect)),
    status: (name: string) => request<T.RepoStatus>("GET", repo(name) + "/status"),
    /** The snippets of the repository: how to use it in a build and how to publish to it, with the labels of their tools. */
    usage: (name: string) => request<T.Usage>("GET", repo(name) + "/usage"),
    /** The CI file that publishes to a hosted repository from system, with the tool and the trigger. */
    ci: (name: string, o: T.CIOptions) =>
      request<T.Snippet>("GET", repo(name) + "/ci", {
        query: { system: o.system, tool: o.tool, branches: o.branches?.join(","), tags: o.tags?.join(",") },
      }),
    /** The direct children of a folder ("" is the root), folders first, each file with the build that published it. */
    tree: (name: string, path = "", p?: T.PageParams) =>
      request<T.Page<T.TreeEntry>>("GET", repo(name) + "/tree", { query: { path, ...page(p) } }),
    /** The versions of an artifact folder, newest first; artifact is null for any other folder. */
    versions: (name: string, path: string, p?: T.PageParams) =>
      request<T.Versions>("GET", repo(name) + "/versions", { query: { path, ...page(p) } }),
    /** The builds of one version of an artifact folder, newest first; version is null when the artifact has no such version. */
    builds: (name: string, path: string, version: string, p?: T.PageParams) =>
      request<T.Builds>("GET", repo(name) + "/builds", { query: { path, version, ...page(p) } }),
    /** One build of a version, by its id: items holds it, or nothing when the version has no such build. */
    build: (name: string, path: string, version: string, id: string) =>
      request<T.Builds>("GET", repo(name) + "/builds", { query: { path, version, build: id } }),
    /** One file with its details; 404 when it does not exist. */
    file: (name: string, path: string) => request<T.FileInfo>("GET", repo(name) + "/file", { query: { path } }),
    /** The snippets that use one artifact: an artifact folder (with a version, or "" for none), a version folder, or a file. */
    snippets: (name: string, path: string, version: string, mode: T.VersionMode) =>
      request<T.ArtifactSnippets>("GET", repo(name) + "/snippets", { query: { path, version, version_mode: mode } }),
    /** Assets under a path prefix, by path. */
    assets: (name: string, prefix = "", p?: T.PageParams) =>
      request<T.Page<T.Asset>>("GET", repo(name) + "/assets", { query: { prefix, ...page(p) } }),
    /**
     * Deletes files (`paths`) and folders (`prefixes`, each ending in "/"). Needs a publish
     * session token for the repo, or a UI session with Delete files (then leave `token` out).
     */
    deleteFiles: (name: string, target: T.FilesDelete, token?: string) =>
      request<T.DeleteResult>("POST", repo(name) + "/files/delete", { body: target, token }),
    /** The state of a delete of files that answered 202. */
    deleteStatus: (name: string, id: string) => request<T.DeleteStatus>("GET", repo(name) + "/files/delete/" + encodeURIComponent(id)),
    /** What a delete of files and folders removes, with the delete notes of the format. Deletes nothing. */
    deletePlan: (name: string, target: T.FilesDelete) => request<T.DeletePlan>("POST", repo(name) + "/files/delete/plan", { body: target }),
    /**
     * What an upload of files would store (the format's plan), and stores nothing. The body holds the
     * list of files and the first bytes of each file that a read rule of the format matches. Needs
     * a publish session token. Reports the progress of the body; abort() cancels it.
     */
    uploadPlan: (
      name: string,
      req: T.PlanRequest,
      reads: T.FormatUpload["reads"],
      token: string,
      onProgress: (p: T.UploadProgress) => void,
    ) => {
      const form = new FormData();
      const list = req.files.map((f) => ({ path: f.path, size: f.file.size }));
      const plan = { fields: req.fields, folder: req.folder, files: list, heads: req.heads };
      form.append("plan", new Blob([JSON.stringify(plan)], { type: "application/json" }));
      for (const f of req.files) {
        if (req.heads && req.held?.has(f.path)) continue;
        const rule = reads.find((r) => globMatch(r.glob, f.path));
        if (rule) form.append("head", f.file.slice(0, rule.max_bytes), f.path);
      }
      const r = requestWithProgress<T.UploadPlan>(repo(name) + "/upload/plan", form, token, (sent, total) => onProgress({ sent, total }));
      return { done: r.done, abort: r.abort };
    },
    /**
     * Manual upload (multipart: the format's fields first, then `file` parts, each with its path in
     * the upload as its filename). Needs a publish session token. Reports the progress of the body;
     * abort() cancels it.
     */
    upload: (name: string, form: FormData, token: string, onProgress: (p: T.UploadProgress) => void) => {
      const r = requestWithProgress<T.UploadAnswer | undefined>(repo(name) + "/upload", form, token, (sent, total) =>
        onProgress({ sent, total }),
      );
      return { done: r.done.then((a) => ({ files: Array.isArray(a?.files) ? a.files : [] })), abort: r.abort };
    },
    /** Starts a resumable upload with a publish session token; its chunks go with appendUpload. */
    startUpload: (name: string, token: string) => request<T.UploadSession>("POST", repo(name) + "/uploads", { token }),
    /**
     * Appends chunk at offset to the resumable upload id: answers the bytes the server holds. A
     * wrong offset is an ApiError 416 whose problem holds received. Reports the progress of the chunk.
     */
    appendUpload: (name: string, id: string, chunk: Blob, offset: number, token: string, onProgress: (sent: number) => void) => {
      const range = `bytes ${offset}-${offset + chunk.size - 1}/*`;
      const r = sendWithProgress<{ received: number }>("PATCH", repo(name) + "/uploads/" + enc(id), chunk, token, { "Content-Range": range }, (sent) =>
        onProgress(sent),
      );
      return { done: r.done, abort: r.abort };
    },
    /** Deletes the resumable upload id and its bytes. */
    cancelUpload: (name: string, id: string, token: string) => request("DELETE", repo(name) + "/uploads/" + enc(id), { token }),
    /** Edit settings: the repo's IP allow-lists for downloads (read) and publishing. */
    ipRules: {
      get: (name: string) => request<T.IPRules>("GET", repo(name) + "/ip-rules"),
      /** Replaces both lists; answers them as stored (CIDRs masked, addresses as /32 or /128). */
      set: (name: string, rules: T.IPRules) => request<T.IPRules>("PUT", repo(name) + "/ip-rules", { body: rules }),
    },
    /** repo.access: each person with a grant on the repository, with their whole set. */
    grants: (name: string) => request<T.Permission[]>("GET", repo(name) + "/grants"),
    /**
     * Manage access, Edit settings or CI pipelines: who can download from and publish to the repo,
     * and from where. admins lists the holders of All repositories too.
     */
    access: (name: string, opts: { admins?: boolean } = {}) =>
      request<T.RepoAccess>("GET", repo(name) + "/access", { query: { admins: opts.admins ? "1" : undefined } }),
    /** The URL a browser downloads a file from (the format's own route serves it). */
    fileURL: (format: string, name: string, path: string) =>
      `/${enc(format)}/${enc(name)}/${path.split("/").map(enc).join("/")}`,
  },


  /** Substring search over paths (q needs 3+ characters), optionally in one repo: a repository's hits together, by path. */
  search: (q: string, opts: { repo?: string } & T.PageParams = {}) =>
    request<T.SearchPage>("GET", "/api/search", { query: { q, repo: opts.repo, ...page(opts) } }),

  settings: {
    get: () => request<T.Settings>("GET", "/api/settings"),
    /**
     * Saves settings. Answers the new settings, or {move} (202) when the change starts a storage
     * move. A validation error is an ApiError with `fields`.
     */
    patch: (patch: T.SettingsPatch) => request<T.Settings | T.SettingsMoveStarted>("PATCH", "/api/settings", { body: patch }),
    /** Checks the values of group (merged over the current ones, not saved), e.g. the probe of a store. */
    test: (group: string, patch: T.SettingsPatch) => request<T.TestResult>("POST", `/api/settings/${enc(group)}/test`, { body: patch }),
  },

  /** Network: named address ranges that repo rules and trusted proxies refer to. */
  ipLists: {
    /** Every list without its ranges: presets first, then by name. */
    list: () => request<T.IPList[]>("GET", "/api/ip-lists"),
    /** One list with its ranges. */
    get: (name: string) => request<T.IPList>("GET", `/api/ip-lists/${enc(name)}`),
    create: (l: T.NewIPList) => request<T.IPList>("POST", "/api/ip-lists", { body: l }),
    /** Changes the fields that are set. Rules and trusted proxies refer to the list by its id, so a new name keeps them. */
    update: (name: string, l: T.IPListPatch) => request<T.IPList>("PUT", `/api/ip-lists/${enc(name)}`, { body: l }),
    /** 409 while a repo rule or the trusted proxies use the list. */
    delete: (name: string) => request("DELETE", `/api/ip-lists/${enc(name)}`),
    /** Fetches a preset now; a failed fetch is an ApiError (502) and the last good ranges stay. */
    refresh: (name: string) => request<T.IPList>("POST", `/api/ip-lists/${enc(name)}/refresh`),
  },

  /** The IP lists that the trusted proxies include (the literal entries are the setting trusted_proxies). */
  trustedProxies: {
    get: () => request<T.TrustedProxyLists>("GET", "/api/network/trusted-proxies"),
    /** Replaces the lists, by id. */
    set: (lists: number[]) => request<T.TrustedProxyLists>("PUT", "/api/network/trusted-proxies", { body: { lists } }),
  },

  /** Pulls the addresses and ranges out of pasted text. Stores nothing. */
  ipRanges: {
    parse: (text: string, signal?: AbortSignal) => request<T.ParsedRanges>("POST", "/api/ip-ranges/parse", { body: { text }, signal }),
  },

  /** Signing keys: keys that sign the indexes of repositories. */
  signingKeys: {
    list: () => request<T.SigningKey[]>("GET", "/api/signing-keys"),
    get: (name: string) => request<T.SigningKey>("GET", `/api/signing-keys/${enc(name)}`),
    /** {name} creates a new key; {name, armored, passphrase?} imports one. */
    create: (k: T.NewSigningKey) => request<T.SigningKey>("POST", "/api/signing-keys", { body: k }),
    /** 409 while a repository uses the key. */
    delete: (name: string) => request("DELETE", `/api/signing-keys/${enc(name)}`),
    /** The address of the armored public key; the browser downloads it as <name>.asc. */
    publicURL: (name: string) => `/api/signing-keys/${enc(name)}/public.asc`,
  },

  network: {
    /** Untrusted peers that sent forwarded headers: likely reverse proxies, most recent first. */
    proxyCandidates: () => request<T.ProxyCandidate[]>("GET", "/api/network/proxy-candidates"),
  },

  backups: {
    /** The backups in the backup folder and the state of the backup job. */
    get: () => request<T.BackupOverview>("GET", "/api/backup"),
    /** Starts a backup now; 409 while one runs, 400 without a backup folder. */
    run: () => request<T.BackupJob>("POST", "/api/backup/run"),
  },
  moves: {
    /** The newest move, running or ended; null when there has been none. */
    current: () => requestOrNull<T.Move>("GET", "/api/storage/moves/current"),
    get: (id: number) => request<T.Move>("GET", `/api/storage/moves/${id}`),
    failures: (id: number, p?: T.PageParams) =>
      request<T.Page<T.MoveFailure>>("GET", `/api/storage/moves/${id}/failures`, { query: page(p) }),
    cancel: (id: number) => request<T.Move>("DELETE", `/api/storage/moves/${id}`),
    /**
     * Tries an ended move again: changes (storage settings; a key left out keeps the target's
     * value, a secret left out keeps the target's secret) go on top of its target, and the result
     * saves like a PATCH of the settings. A validation error is an ApiError with `fields`.
     */
    retry: (id: number, changes: T.SettingsPatch = {}) =>
      request<T.MoveRetried>("POST", `/api/storage/moves/${id}/retry`, { body: changes }),
    /** Checks the target that a retry with changes would move to, like the settings test. Saves nothing. */
    test: (id: number, changes: T.SettingsPatch) => request<T.TestResult>("POST", `/api/storage/moves/${id}/test`, { body: changes }),
    /** Puts an ended move away: the page then shows no move. 409 while it copies. */
    dismiss: (id: number) => request<T.Move>("POST", `/api/storage/moves/${id}/dismiss`),
  },

  /** Edge delivery: the state of the Worker and the regional buckets, and its generated config. */
  edge: {
    status: () => request<T.EdgeStatus>("GET", "/api/edge/status"),
    /** Makes the deployed Worker run its gap check now; 429 while a check runs or within 10 s of the last one. */
    check: () => request<T.EdgeCheck>("POST", "/api/edge/check"),
    /** Starts a deploy of the Worker to Cloudflare with the token of the settings; 409 while one runs or when the settings cannot deploy. */
    deploy: () => request<T.EdgeDeploy>("POST", "/api/edge/deploy"),
    /** Files to download in the speed test, and the base URL the test runs on. */
    speedFiles: () => request<T.EdgeSpeedFiles>("GET", "/api/edge/speedtest"),
    /** A pass of the speed test: for 10 minutes the Worker reads a download from the bucket that this browser names. */
    speedPass: () => request<T.EdgeSpeedPass>("POST", "/api/edge/speedtest"),
    /** The wrangler.jsonc that the server builds from its settings, for a checkout of edge/. */
    wranglerConfig: () => requestText("/api/edge/wrangler.jsonc"),
    /** The address of the file, for a download link. */
    wranglerURL: "/api/edge/wrangler.jsonc",
    /** The address of the edge Worker folder (a zip with the wrangler.jsonc in it), for a download link. */
    workerURL: "/api/edge/worker.zip",
  },

  nexus: {
    list: (p?: T.PageParams) => request<T.Page<T.NexusSource>>("GET", "/api/nexus/sources", { query: page(p) }),
    create: (s: T.NewNexusSource) => request<T.NexusSource>("POST", "/api/nexus/sources", { body: s }),
    get: (id: number) => request<T.NexusSourceDetail>("GET", `/api/nexus/sources/${id}`),
    update: (id: number, patch: T.NexusSourcePatch) =>
      request<T.NexusSource>("PATCH", `/api/nexus/sources/${id}`, { body: patch }),
    delete: (id: number) => request("DELETE", `/api/nexus/sources/${id}`),
    /** Saves the repositories to copy and starts a sync of them (`running_sync_id`). 409 while a sync runs. */
    setRepos: (id: number, repos: T.NexusMapping[]) =>
      request<T.NexusSourceDetail>("PUT", `/api/nexus/sources/${id}/repos`, { body: { repos } }),
    sync: (id: number) => request<T.NexusSync>("POST", `/api/nexus/sources/${id}/sync`),
    cutOver: (id: number) => request<T.NexusSync>("POST", `/api/nexus/sources/${id}/cutover`),
    /** Switches over the repository that Nexus repository `repo` copies into: its final sync. */
    switchOver: (id: number, repo: string) =>
      request<T.NexusSync>("POST", `/api/nexus/sources/${id}/repos/${encodeURIComponent(repo)}/switch-over`),
    /** Creates the rehearsal repository of the repository that Nexus repository `repo` copies into. */
    rehearse: (id: number, repo: string) =>
      request<{ name: string }>("POST", `/api/nexus/sources/${id}/repos/${encodeURIComponent(repo)}/rehearsal`),
    /** Creates the repository that projects move to from the one that Nexus repository `repo` copies into, and with `group` the group <name>-all of both. */
    move: (id: number, repo: string, body: { name: string; group: boolean }) =>
      request<{ name: string; group: string }>("POST", `/api/nexus/sources/${id}/repos/${encodeURIComponent(repo)}/move`, { body }),
    /** What a merge of `from` into `into` would do (`into` empty: the repository `from` moves into). It reads every file of `from`. */
    mergePlan: (from: string, into?: string) => request<T.NexusMergePlan>("GET", "/api/nexus/merges/plan", { query: { from, into } }),
    /** Starts a merge; its operation holds both repositories until it ends. */
    startMerge: (body: T.NexusMergeInput) => request<T.NexusMerge>("POST", "/api/nexus/merges", { body }),
    merge: (id: number) => request<T.NexusMerge>("GET", `/api/nexus/merges/${id}`),
    /** Starts to delete every file of a rehearsal repository; its operation shows the progress. */
    emptyRehearsal: (name: string) =>
      request<{ name: string; files: number }>("POST", `/api/nexus/rehearsals/${encodeURIComponent(name)}/empty`),
    syncs: (id: number, p?: T.PageParams) =>
      request<T.Page<T.NexusSync>>("GET", `/api/nexus/sources/${id}/syncs`, { query: page(p) }),
    getSync: (id: number) => request<T.NexusSync>("GET", `/api/nexus/syncs/${id}`),
    syncFailures: (id: number, p?: T.PageParams) =>
      request<T.Page<T.NexusFailure>>("GET", `/api/nexus/syncs/${id}/failures`, { query: page(p) }),
    /** The log of a sync, oldest first; `repo` keeps the lines of one Nexus repository. */
    syncLog: (id: number, p?: T.PageParams, repo?: string) =>
      request<T.Page<T.NexusLogLine>>("GET", `/api/nexus/syncs/${id}/log`, { query: { ...page(p), repo } }),
    cancelSync: (id: number) => request<T.NexusSync>("DELETE", `/api/nexus/syncs/${id}`),
  },

  devices: {
    /** The enrollment waiting for approval; 404 when the code is unknown or expired. */
    pending: (user_code: string) =>
      request<T.PendingEnrollment>("GET", "/api/devices/enroll", { query: { user_code } }),
    /** A local account sends a fresh second factor; a single sign-on account sends the grant of ssoApproveURL. */
    approve: (user_code: string, proof: T.FreshProof) =>
      request<T.Device>("POST", "/api/devices/approve", { body: { user_code }, proof: proofHeader(proof) }),
    /** Full-page navigation: a new single sign-on that returns to /device?code= with a grant, or with an error. */
    ssoApproveURL: (user_code: string) => `/api/auth/oidc/device?code=${enc(user_code)}`,
    /** The caller's devices, or every device (with username) for a holder of People; newest first. */
    list: (p?: T.PageParams) => request<T.Page<T.Device>>("GET", "/api/devices", { query: page(p) }),
    revoke: (id: number) => request("DELETE", `/api/devices/${id}`),
  },

  cli: {
    /** The CLI archives of the server's build, from /cli/SHA256SUMS (lines "<sha256>  <name>"). */
    archives: async (): Promise<T.CLIArchive[]> => {
      const text = await requestText("/cli/SHA256SUMS");
      return text
        .split("\n")
        .map((l) => l.trim().match(/^([0-9a-f]{64})\s+\*?(\S+)$/))
        .filter((m): m is RegExpMatchArray => !!m)
        .map(([, sha256, name]) => {
          const [, os = "", arch = ""] = name.match(/^telesphorepo_([a-z]+)_([a-z0-9]+)\./) ?? [];
          return { name, sha256, os, arch, url: `/cli/${enc(name)}` };
        });
    },
  },

  publish: {
    /** Minting from a device or a CI token (client side; no auth). */
    challenge: (device_id: number) => request<{ nonce: string; expires_at: string }>("POST", "/api/publish/challenge", { body: { device_id } }),
    deviceSession: (b: { device_id: number; repo: string; nonce: string; signature: string }) =>
      request<T.PublishSession>("POST", "/api/publish/sessions", { body: b }),
    oidcSession: (token: string, repo: string) =>
      request<T.PublishSession>("POST", "/api/publish/sessions/oidc", { body: { token, repo } }),
    /** A manual publish session for one repo after a fresh second factor. */
    manualSession: (repo: string, proof: T.FreshProof) =>
      request<T.PublishSession>("POST", "/api/publish/sessions/manual", { body: { repo }, proof: proofHeader(proof) }),
    /** A setup code for one repo after a fresh second factor. */
    setupCode: (repo: string, proof: T.FreshProof) =>
      request<T.SetupCode>("POST", "/api/publish/setup-codes", { body: { repo }, proof: proofHeader(proof) }),
    /** Full-page navigation target of an SSO account: a new sign-in that returns to the repo page with ?setup_grant=. */
    ssoSetupURL: (repo: string) => `/api/auth/oidc/setup?repo=${enc(repo)}`,
    /** The caller's sessions, newest first. */
    sessions: (p?: T.PageParams) => request<T.Page<T.SessionInfo>>("GET", "/api/publish/sessions", { query: page(p) }),
    /** Ends a session; authenticates with its own token. */
    closeSession: (id: number, token: string) => request("DELETE", `/api/publish/sessions/${id}`, { token }),
    /** Activity: every session, newest first. */
    log: (p?: T.PageParams) => request<T.Page<T.SessionInfo>>("GET", "/api/publish/log", { query: page(p) }),
    trusts: {
      /** The CI pipelines the caller manages, or the ones whose name starts with q. */
      list: (p?: T.PageParams, q?: string) => request<T.Page<T.Trust>>("GET", "/api/publish/trusts", { query: { q, ...page(p) } }),
      /** How many CI pipelines the caller manages (a count of every one stops at 10,000: more). */
      count: () => request<T.Count>("GET", "/api/publish/trusts/count"),
      get: (id: number) => request<T.Trust>("GET", `/api/publish/trusts/${id}`),
      create: (t: T.TrustInput) => request<T.Trust>("POST", "/api/publish/trusts", { body: t }),
      update: (id: number, t: T.TrustInput) => request<T.Trust>("PATCH", `/api/publish/trusts/${id}`, { body: t }),
      delete: (id: number) => request("DELETE", `/api/publish/trusts/${id}`),
    },
  },

  /** activity.read: the actions of the audit log with their labels. */
  auditActions: () => request<T.AuditAction[]>("GET", "/api/audit/actions"),

  /** activity.read: the audit log, newest first. */
  audit: (filter: T.AuditFilter & T.PageParams = {}) =>
    request<T.Page<T.AuditEvent>>("GET", "/api/audit", {
      query: {
        action: filter.action,
        repo: filter.repo,
        actor: filter.actor,
        since: filter.since,
        until: filter.until,
        ...page(filter),
      },
    }),

  /** activity.read: the people of the Activity filter whose username starts with q (at most 20 a page). */
  auditPeople: (q: string, p?: T.PageParams) =>
    request<T.Page<T.PersonName>>("GET", "/api/audit/people", { query: { q, ...page(p) } }),

  /** The figures of the Settings overview, each a counter or a bounded count. */
  overview: () => request<T.Overview>("GET", "/api/overview"),
  /** activity.read: the downloads of the last 30 days (today included) and the top repositories. */
  stats: () => request<T.DownloadStats>("GET", "/api/stats"),
};
