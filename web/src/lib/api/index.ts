// Typed functions for every /api route. Pages import `api` and call e.g. api.repos.list().
// Routes and JSON shapes follow internal/api; see SPEC.md for the rules behind them.

import { request, requestOrNull, requestWithProgress } from "./client";
import type * as T from "./types";

export * from "./client";
export type * from "./types";

const enc = encodeURIComponent;
const repo = (name: string) => `/api/repos/${enc(name)}`;
const page = (p: T.PageParams = {}) => ({ after: p.after, limit: p.limit });

export const api = {
  auth: {
    config: () => request<T.AuthConfig>("GET", "/api/auth/config"),
    /** Step 1. A user with a second factor gets {second_factor, methods}; anyone else is signed in. */
    login: (username: string, password: string) =>
      request<T.LoginAnswer>("POST", "/api/auth/login", { body: { username, password } }),
    /** Passkey options for the pending sign-in (step 2). */
    loginPasskeyOptions: () => request<T.PasskeyOptions>("POST", "/api/auth/login/passkey"),
    /** Step 2: a TOTP code, a passkey assertion or a recovery code. */
    loginSecond: (proof: T.Proof) => request<T.Me>("POST", "/api/auth/login/second", { body: proof }),
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
    list: (p?: T.PageParams) => request<T.Page<T.User>>("GET", "/api/users", { query: page(p) }),
    /** Admin: one account; 404 when no account has the name. */
    get: (username: string) => request<T.User>("GET", `/api/users/${enc(username)}`),
    /** Admin: the repositories one person can use, by repository name. */
    access: (username: string, p?: T.PageParams) =>
      request<T.UserAccess>("GET", `/api/users/${enc(username)}/access`, { query: page(p) }),
    create: (u: T.NewUser) => request<T.User>("POST", "/api/users", { body: u }),
    /** Users change their own password; admins change anyone's password and is_admin. */
    update: (username: string, patch: T.UserPatch) =>
      request<T.User>("PATCH", `/api/users/${enc(username)}`, { body: patch }),
    /** Admin: disables or enables 1 to 1000 people. A disabled person's sessions end. 400 when the list holds the caller and disabled is true. */
    setDisabled: (usernames: string[], disabled: boolean) =>
      request<T.Changed>("PATCH", "/api/users", { body: { usernames, disabled } }),
  },

  /** Admin: the level of each pair of person and repository. */
  permissions: {
    /** Sets the level for every pair of user and repository. */
    set: (users: string[], repos: string[], level: "read" | "write") =>
      request<T.Changed>("PUT", "/api/permissions", { body: { users, repos, level } }),
    /** Removes the level of every pair of user and repository. */
    delete: (users: string[], repos: string[]) =>
      request<T.Changed>("POST", "/api/permissions/delete", { body: { users, repos } }),
  },

  formats: {
    list: () => request<T.Format[]>("GET", "/api/formats"),
  },

  repos: {
    /** Every repo the caller can read (anonymous: the anonymous-read ones). Not paged. */
    list: () => request<T.Repo[]>("GET", "/api/repos"),
    get: (name: string) => request<T.Repo>("GET", repo(name)),
    create: (r: T.NewRepo) => request<T.Repo>("POST", "/api/repos", { body: r }),
    update: (name: string, patch: T.RepoPatch) => request<T.Repo>("PATCH", repo(name), { body: patch }),
    /** Admin: makes 1 to 1000 repositories public (true) or private (false). */
    setPublic: (names: string[], anonymous_read: boolean) =>
      request<T.Changed>("PATCH", "/api/repos", { body: { names, anonymous_read } }),
    /**
     * Admin: removes 1 to 1000 repositories, their rows and their blobs. 409 when one is locked
     * (nothing is deleted); a failure part way is an ApiError, and the repositories before it are gone.
     */
    delete: (names: string[]) => request<T.ReposDeleted>("POST", "/api/repos/delete", { body: { names } }),
    status: (name: string) => request<T.RepoStatus>("GET", repo(name) + "/status"),
    usage: (name: string) => request<T.Usage>("GET", repo(name) + "/usage"),
    /** The direct children of a folder ("" is the root), folders first. */
    tree: (name: string, path = "", p?: T.PageParams) =>
      request<T.Page<T.TreeEntry>>("GET", repo(name) + "/tree", { query: { path, ...page(p) } }),
    /** Assets under a path prefix, by path. */
    assets: (name: string, prefix = "", p?: T.PageParams) =>
      request<T.Page<T.Asset>>("GET", repo(name) + "/assets", { query: { prefix, ...page(p) } }),
    /**
     * Deletes files (`paths`) and folders (`prefixes`, each ending in "/"). Needs a publish
     * session token for the repo, or an admin UI session (then leave `token` out).
     */
    deleteFiles: (name: string, target: T.FilesDelete, token?: string) =>
      request<T.DeleteResult>("POST", repo(name) + "/files/delete", { body: target, token }),
    /**
     * Manual upload (multipart: the format's fields first, then `file` parts). Needs a publish
     * session token. Reports the progress of the body; abort() cancels it.
     */
    upload: (name: string, form: FormData, token: string, onProgress: (p: T.UploadProgress) => void) => {
      const r = requestWithProgress<T.UploadAnswer | undefined>(repo(name) + "/upload", form, token, (sent, total) =>
        onProgress({ sent, total }),
      );
      return { done: r.done.then((a) => ({ files: Array.isArray(a?.files) ? a.files : [] })), abort: r.abort };
    },
    /**
     * Checks the files of a dropped folder and stores nothing. The body is like upload's: each
     * `file` part's filename is its path inside the folder. Needs a publish session token.
     */
    scanUpload: (name: string, form: FormData, token: string, onProgress: (p: T.UploadProgress) => void) => {
      const r = requestWithProgress<T.ScanAnswer | undefined>(repo(name) + "/upload/scan", form, token, (sent, total) =>
        onProgress({ sent, total }),
      );
      return {
        done: r.done.then((a) => ({
          found: Array.isArray(a?.found) ? a.found : [],
          ignored: Array.isArray(a?.ignored) ? a.ignored : [],
        })),
        abort: r.abort,
      };
    },
    /** Admin: the repo's IP allow-lists for downloads (read) and publishing. */
    ipRules: {
      get: (name: string) => request<T.IPRules>("GET", repo(name) + "/ip-rules"),
      /** Replaces both lists; answers them as stored (CIDRs masked, addresses as /32 or /128). */
      set: (name: string, rules: T.IPRules) => request<T.IPRules>("PUT", repo(name) + "/ip-rules", { body: rules }),
    },
    permissions: {
      list: (name: string) => request<T.Permission[]>("GET", repo(name) + "/permissions"),
    },
    /** Admin: who can download from and publish to the repo, and from where. admins lists the administrators too. */
    access: (name: string, opts: { admins?: boolean } = {}) =>
      request<T.RepoAccess>("GET", repo(name) + "/access", { query: { admins: opts.admins ? "1" : undefined } }),
    /** The URL a browser downloads a file from (the format's own route serves it). */
    fileURL: (format: string, name: string, path: string) =>
      `/${enc(format)}/${enc(name)}/${path.split("/").map(enc).join("/")}`,
  },


  /** Substring search over paths (q needs 3+ characters), optionally in one repo. */
  search: (q: string, opts: { repo?: string } & T.PageParams = {}) =>
    request<T.Page<T.SearchHit>>("GET", "/api/search", { query: { q, repo: opts.repo, ...page(opts) } }),

  settings: {
    get: () => request<T.Settings>("GET", "/api/settings"),
    /**
     * Saves settings. Answers the new settings, or {move} (202) when the change starts a storage
     * move. A validation error is an ApiError with `fields`.
     */
    patch: (patch: T.SettingsPatch) => request<T.Settings | T.SettingsMoveStarted>("PATCH", "/api/settings", { body: patch }),
    testStorage: (patch: T.SettingsPatch) => request<T.TestResult>("POST", "/api/settings/storage/test", { body: patch }),
    testOIDC: (patch: T.SettingsPatch) => request<T.TestResult>("POST", "/api/settings/oidc/test", { body: patch }),
  },

  /** Admin: named address ranges that repo rules and trusted proxies refer to. */
  ipLists: {
    /** Every list without its ranges: presets first, then by name. */
    list: () => request<T.IPList[]>("GET", "/api/ip-lists"),
    /** One list with its ranges. */
    get: (name: string) => request<T.IPList>("GET", `/api/ip-lists/${enc(name)}`),
    create: (l: T.NewIPList) => request<T.IPList>("POST", "/api/ip-lists", { body: l }),
    /** Changes the fields that are set; a new name also changes the rules that refer to the list. */
    update: (name: string, l: T.IPListPatch) => request<T.IPList>("PUT", `/api/ip-lists/${enc(name)}`, { body: l }),
    /** 409 while a repo rule or the trusted proxies use the list. */
    delete: (name: string) => request("DELETE", `/api/ip-lists/${enc(name)}`),
    /** Fetches a preset now; a failed fetch is an ApiError (502) and the last good ranges stay. */
    refresh: (name: string) => request<T.IPList>("POST", `/api/ip-lists/${enc(name)}/refresh`),
  },

  /** Admin: pulls the addresses and ranges out of pasted text. Stores nothing. */
  ipRanges: {
    parse: (text: string, signal?: AbortSignal) => request<T.ParsedRanges>("POST", "/api/ip-ranges/parse", { body: { text }, signal }),
  },

  /** Admin: keys that sign the package lists of Debian repositories. */
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

  moves: {
    /** The newest move, running or ended; null when there has been none. */
    current: () => requestOrNull<T.Move>("GET", "/api/storage/moves/current"),
    get: (id: number) => request<T.Move>("GET", `/api/storage/moves/${id}`),
    failures: (id: number, p?: T.PageParams) =>
      request<T.Page<T.MoveFailure>>("GET", `/api/storage/moves/${id}/failures`, { query: page(p) }),
    cancel: (id: number) => request<T.Move>("DELETE", `/api/storage/moves/${id}`),
  },

  nexus: {
    list: (p?: T.PageParams) => request<T.Page<T.NexusSource>>("GET", "/api/nexus/sources", { query: page(p) }),
    create: (s: T.NewNexusSource) => request<T.NexusSource>("POST", "/api/nexus/sources", { body: s }),
    get: (id: number) => request<T.NexusSourceDetail>("GET", `/api/nexus/sources/${id}`),
    update: (id: number, patch: T.NexusSourcePatch) =>
      request<T.NexusSource>("PATCH", `/api/nexus/sources/${id}`, { body: patch }),
    delete: (id: number) => request("DELETE", `/api/nexus/sources/${id}`),
    setRepos: (id: number, mappings: T.NexusMapping[]) =>
      request<T.NexusSourceDetail>("PUT", `/api/nexus/sources/${id}/repos`, { body: mappings }),
    sync: (id: number) => request<T.NexusSync>("POST", `/api/nexus/sources/${id}/sync`),
    cutOver: (id: number) => request<T.NexusSync>("POST", `/api/nexus/sources/${id}/cutover`),
    syncs: (id: number, p?: T.PageParams) =>
      request<T.Page<T.NexusSync>>("GET", `/api/nexus/sources/${id}/syncs`, { query: page(p) }),
    getSync: (id: number) => request<T.NexusSync>("GET", `/api/nexus/syncs/${id}`),
    syncFailures: (id: number, p?: T.PageParams) =>
      request<T.Page<T.NexusFailure>>("GET", `/api/nexus/syncs/${id}/failures`, { query: page(p) }),
    cancelSync: (id: number) => request<T.NexusSync>("DELETE", `/api/nexus/syncs/${id}`),
  },

  devices: {
    /** Device flow, client side (the Gradle plugin and Maven extension call it; no auth). */
    enroll: (req: T.EnrollmentRequest) => request<T.Enrollment>("POST", "/api/devices/enroll", { body: req }),
    poll: (device_code: string) =>
      request<{ device_id: number }>("POST", "/api/devices/enroll/poll", { body: { device_code } }),
    /** The enrollment waiting for approval; 404 when the code is unknown or expired. */
    pending: (user_code: string) =>
      request<T.PendingEnrollment>("GET", "/api/devices/enroll", { query: { user_code } }),
    approve: (user_code: string, proof: T.Proof) =>
      request<T.Device>("POST", "/api/devices/approve", { body: { user_code, ...proof } }),
    /** The caller's devices, or every device (with username) for an admin; newest first. */
    list: (p?: T.PageParams) => request<T.Page<T.Device>>("GET", "/api/devices", { query: page(p) }),
    revoke: (id: number) => request("DELETE", `/api/devices/${id}`),
  },

  publish: {
    /** Minting from a device or a CI token (client side; no auth). */
    challenge: (device_id: number) => request<{ nonce: string }>("POST", "/api/publish/challenge", { body: { device_id } }),
    deviceSession: (b: { device_id: number; repo: string; nonce: string; signature: string }) =>
      request<T.PublishSession>("POST", "/api/publish/sessions", { body: b }),
    oidcSession: (token: string, repo: string) =>
      request<T.PublishSession>("POST", "/api/publish/sessions/oidc", { body: { token, repo } }),
    /** A manual publish session for one repo after a fresh second factor. */
    manualSession: (repo: string, proof: T.Proof) =>
      request<T.PublishSession>("POST", "/api/publish/sessions/manual", { body: { repo, ...proof } }),
    /** The caller's sessions, newest first. */
    sessions: (p?: T.PageParams) => request<T.Page<T.SessionInfo>>("GET", "/api/publish/sessions", { query: page(p) }),
    /** Ends a session; authenticates with its own token. */
    closeSession: (id: number, token: string) => request("DELETE", `/api/publish/sessions/${id}`, { token }),
    /** Admin: every session, newest first. */
    log: (p?: T.PageParams) => request<T.Page<T.SessionInfo>>("GET", "/api/publish/log", { query: page(p) }),
    trusts: {
      list: (p?: T.PageParams) => request<T.Page<T.Trust>>("GET", "/api/publish/trusts", { query: page(p) }),
      get: (id: number) => request<T.Trust>("GET", `/api/publish/trusts/${id}`),
      create: (t: T.TrustInput) => request<T.Trust>("POST", "/api/publish/trusts", { body: t }),
      update: (id: number, t: T.TrustInput) => request<T.Trust>("PATCH", `/api/publish/trusts/${id}`, { body: t }),
      delete: (id: number) => request("DELETE", `/api/publish/trusts/${id}`),
    },
  },

  /** Admin: the audit log, newest first. */
  audit: (filter: T.AuditFilter & T.PageParams = {}) =>
    request<T.Page<T.AuditEvent>>("GET", "/api/audit", {
      query: { action: filter.action, repo: filter.repo, ...page(filter) },
    }),
};
