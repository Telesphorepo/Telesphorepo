// JSON shapes of the /api routes (internal/api). Times are RFC 3339 strings. Fields that the
// server marks `omitempty` are optional here.

/** A keyset page: pass `next` back as `after` to get the following page; null on the last one. */
export type Page<T> = { items: T[]; next: string | null };
/** Keyset parameters of a list route. */
export type PageParams = { after?: string; limit?: number };

// --- auth and users ---

export type User = {
  id: number;
  username: string;
  is_admin: boolean;
  disabled: boolean;
  kind: "local" | "oidc";
  /** "" when unknown (SSO accounts only). */
  email: string;
  created_at: string;
};

/** GET /api/me and a finished sign-in. second_factor_required: the session reaches only the setup routes. */
export type Me = User & { second_factor_required: boolean };

/** One repository a person can use: level "read" (download) or "write" (download and publish), and why. */
export type UserRepoAccess = {
  repo: string;
  format: string;
  public: boolean;
  level: "read" | "write";
  via: "admin" | "permission" | "public";
};
/**
 * GET /api/me/access and GET /api/users/{username}/access: a keyset page of the repositories a
 * person can use. can_publish is false for a single sign-on or disabled account, whatever its level.
 */
export type UserAccess = Page<UserRepoAccess> & {
  username: string;
  is_admin: boolean;
  disabled: boolean;
  kind: "local" | "oidc";
  can_publish: boolean;
};

/** POST /api/auth/login answers this when the account has a second factor. */
export type SecondFactorStep = { second_factor: true; methods: SecondFactorMethod[] };
export type SecondFactorMethod = "totp" | "passkey" | "recovery";
export type LoginAnswer = Me | SecondFactorStep;

export type AuthConfig = {
  password_login: boolean;
  oidc: boolean;
  admin_password_login: boolean;
  /** True under `telesphorepo dev`: the second factor is optional (an enrolled one is still checked). */
  second_factor_optional: boolean;
};

/** A second-factor proof: one of a TOTP code, a passkey assertion (browser JSON) or a recovery code (sign-in only). */
export type Proof = { totp_code?: string; passkey?: unknown; recovery_code?: string };

export type NewUser = { username: string; password: string; is_admin: boolean };
export type UserPatch = { password?: string; is_admin?: boolean };

/** PATCH /api/repos, PATCH /api/users, PUT /api/permissions, POST /api/permissions/delete: how many items changed. */
export type Changed = { changed: number };

// --- read tokens ---

export type Token = {
  id: number;
  name: string;
  created_at: string;
  expires_at: string | null;
  last_used_at: string | null;
};
/** POST /api/tokens: the secret is in `token` and is shown once. */
export type NewToken = Token & { token: string };

// --- second factor (/api/security) ---

export type TOTPFactor = { id: number; name: string; created_at: string };
export type Passkey = { id: number; name: string; created_at: string; last_used_at: string | null };
export type Factors = {
  totp: TOTPFactor[];
  passkeys: Passkey[];
  /** Unused recovery codes left. */
  recovery_codes: number;
  second_factor: boolean;
};
export type TOTPSetup = { setup_id: string; secret: string; otpauth_url: string };
/** WebAuthn options as the server sends them (binary fields base64url). See $lib/webauthn. */
export type PasskeyOptions = { publicKey: Record<string, unknown> & { challenge: string } };

// --- formats and repos ---

export type RepoConfig = Record<string, unknown>;
export type Format = {
  name: string;
  title: string;
  default_config: RepoConfig;
  settings_group?: string;
  /** The format's icon: an inline SVG in currentColor. */
  icon?: string;
  /** Icons of artifact kinds that differ from the format (`gradle-plugin` in a Maven repo). */
  kind_icons?: Record<string, string>;
  /** The kinds of repository the format takes: "hosted", and "proxy" or "group" when it serves them. */
  kinds: RepoKind[];
};

/** hosted: files published here; proxy: a cache of a remote repository; group: reads several repositories. */
export type RepoKind = "hosted" | "proxy" | "group";

/** One member of a group, in read order. */
export type GroupMember = { name: string; format: string; kind: "hosted" | "proxy"; public: boolean };

/** The remote of a proxy. It never holds the credentials. Times are in seconds. */
export type RepoProxy = {
  /** http(s), ends with "/". */
  remote_url: string;
  has_credentials: boolean;
  /** Seconds the server uses a changing file before it asks the remote again. */
  metadata_ttl: number;
  /** Seconds the server remembers a file that the remote does not have. */
  not_found_ttl: number;
  /** The last successful remote request; null before the first. */
  last_ok_at: string | null;
  /** "" when none. */
  last_error: string;
  last_error_at: string | null;
};

/** "admin" for admins; otherwise the caller's level on the repo. */
export type RepoPermission = "admin" | "write" | "read" | "none";

export type Repo = {
  name: string;
  format: string;
  description: string;
  anonymous_read: boolean;
  config: RepoConfig;
  permission: RepoPermission;
  /** Why uploads answer 409 (a storage move, a Nexus mirror); "" when they do not. */
  locked_by: string;
  /** Number of stored files; every file counts (artifacts, metadata, checksums, index files). */
  file_count: number;
  /** Total bytes of the stored files. */
  total_size: number;
  /** When a file was last published; null before the first publish. */
  last_published_at: string | null;
  kind: RepoKind;
  /** A group: how many members it has. */
  member_count?: number;
  /** GET of a group: its members in read order. */
  members?: GroupMember[];
  /** GET of a hosted or proxy repo: the groups it is in, by name. */
  groups?: string[];
  /** A proxy: its remote. */
  proxy?: RepoProxy;
};

/** The remote of a new proxy, or the changes to it. Send username and password together; username "" removes them. */
export type ProxyInput = {
  remote_url?: string;
  username?: string;
  password?: string;
  metadata_ttl?: number;
  not_found_ttl?: number;
};
export type NewRepo = {
  name: string;
  format: string;
  description?: string;
  anonymous_read: boolean;
  config?: RepoConfig;
  /** Default "hosted". */
  kind?: RepoKind;
  /** A group: 1 to 50 repository names, in read order. */
  members?: string[];
  /** A proxy: its remote (remote_url is required). */
  proxy?: ProxyInput & { remote_url: string };
  /** A proxy: create it even when the remote does not answer. */
  force?: boolean;
};
export type RepoPatch = { description?: string; config?: RepoConfig; proxy?: ProxyInput };
/** POST /api/repos/delete: the deleted repositories and how many files went with them. */
export type ReposDeleted = { deleted: string[]; files: number };

export type Permission = { username: string; level: "read" | "write" };

// --- effective access (GET /api/repos/{name}/access) ---

/** A person with access: a grant on the repo ("permission"), or an administrator ("admin", only with ?admins=1). */
export type AccessPerson = { username: string; level: "read" | "write"; via: "permission" | "admin" };
/** Where requests may come from: any network, or only these network lists and address ranges. */
export type AccessNetworks = { any: boolean; lists: { name: string; title: string }[]; cidrs: { cidr: string; note: string }[] };
/** A CI pipeline (CI rule) that lists the repo, with what its conditions allow. Disabled ones are listed too. */
export type AccessPipeline = {
  id: number;
  rule: string;
  system: "github-actions" | "other";
  issuer: string;
  repositories?: string[];
  branches?: string[];
  tags?: string[];
  workflows?: string[];
  subjects?: string[];
  disabled: boolean;
};
/**
 * GET /api/repos/{name}/access (admin). download.people: everyone with a grant (read or write);
 * publish.people: the local accounts with write. admins counts the enabled administrators (for
 * publishing, the local ones). publish.locked_by says why nobody can publish now ("" when not).
 */
export type RepoAccess = {
  repo: string;
  format: string;
  download: { public: boolean; people: AccessPerson[]; admins: number; networks: AccessNetworks; sentence: string };
  publish: {
    people: AccessPerson[];
    admins: number;
    ci: AccessPipeline[];
    networks: AccessNetworks;
    locked_by: string;
    sentence: string;
  };
};

export type Snippet = { title: string; lang: string; code: string };
export type Usage = { base_url: string; snippets: Snippet[] };
/** GET /api/repos/{name}/status: format specific, {} when the format reports nothing. */
export type RepoStatus = Record<string, unknown>;

export type Asset = {
  path: string;
  size: number;
  sha256: string;
  content_type: string;
  kind: string;
  updated_at: string;
};

/** One child of a folder in GET /api/repos/{name}/tree. Folders have only name, type and path. */
export type TreeEntry = {
  name: string;
  type: "dir" | "file";
  /** The full repo path; a folder's ends with "/". */
  path: string;
  size?: number;
  sha256?: string;
  updated_at?: string;
  /** Username of the uploader; never an IP address. */
  uploaded_by?: string;
};

/** POST /api/repos/{name}/files/delete: files by path, and folders by prefix (ending in "/"). */
export type FilesDelete = { paths?: string[]; prefixes?: string[] };
/** POST /api/repos/{name}/files/delete: how many files the server removed. */
export type DeleteResult = { deleted: number };

/** One stored file of a manual upload. Maven answers also list the checksum and metadata files. */
export type Uploaded = { path: string; status: "created" | "replaced" | "unchanged"; size: number; sha256: string };
/** POST /api/repos/{name}/upload. */
export type UploadAnswer = { files: Uploaded[] };
/** Bytes of an upload body sent so far. */
export type UploadProgress = { sent: number; total: number };

/**
 * One artifact or package that POST /api/repos/{name}/upload/scan found: kind is "maven",
 * "debian" or "file"; files are relative paths of the upload; problems say why an upload would
 * refuse it (empty: it is fine).
 */
export type ScanFound = { kind: string; title: string; files: string[]; problems: string[] };
/** POST /api/repos/{name}/upload/scan: nothing is stored. ignored are the paths no item takes. */
export type ScanAnswer = { found: ScanFound[]; ignored: string[] };

// --- network rules ---

/** One entry of a repo's IP rule: a CIDR ("203.0.113.0/24") or the name of an IP list. */
export type IPRule = { kind: "cidr" | "list"; value: string; note: string };
/** GET/PUT /api/repos/{name}/ip-rules. An empty list leaves its scope open to every address. */
export type IPRules = { read: IPRule[]; publish: IPRule[] };

/** What refers to an IP list: repo rules (by scope) and the trusted_proxies setting. */
export type IPListUsage = { repos: { repo: string; scope: "read" | "publish" }[]; trusted_proxies: boolean };
/**
 * A named set of address ranges (GET /api/ip-lists). A preset is fetched by the server (source is
 * its address; fetched_at is null before the first good fetch); a custom list is edited by admins.
 * ranges is only in GET /api/ip-lists/{name}.
 */
export type IPList = {
  name: string;
  kind: "preset" | "custom";
  title: string;
  description: string;
  count: number;
  ranges?: string[];
  source: string;
  fetched_at: string | null;
  error: string;
  created_at: string;
  used_by: IPListUsage;
};
export type NewIPList = { name: string; title?: string; description?: string; ranges?: string[] };

/**
 * POST /api/ip-ranges/parse: the ranges in pasted text (addresses as /32 or /128, CIDRs masked,
 * duplicates removed), the lines that hold none (1-based), and how many duplicates were dropped.
 */
export type ParsedRanges = {
  ranges: string[];
  invalid: { line: number; text: string; reason: string }[];
  duplicates: number;
};
export type IPListPatch = { name?: string; title?: string; description?: string; ranges?: string[] };

/** A key that signs Debian package lists. error says why the server cannot use it. */
export type SigningKey = {
  name: string;
  fingerprint: string;
  public_key: string;
  created_at: string;
  error?: string;
  /** Names of the repositories that sign with this key. */
  repos: string[];
};
/** {name} creates an Ed25519 key; armored (and its passphrase) imports a private key. */
export type NewSigningKey = { name: string; armored?: string; passphrase?: string };
/** GET /api/network/proxy-candidates: an untrusted peer that sent a forwarded header (header names which). */
export type ProxyCandidate = { address: string; last_seen: string; count: number; header: string };

export type SearchHit = {
  repo: string;
  path: string;
  size: number;
  sha256: string;
  kind: string;
  updated_at: string;
};

// --- settings ---

export type SettingType = "string" | "bool" | "size" | "duration" | "list";
export type SettingSource = "db" | "env" | "default";
/** A secret's value is never sent: only whether it is set. */
export type SecretValue = { set: boolean };

export type Setting = {
  key: string;
  env: string;
  group: string;
  label: string;
  help?: string;
  type: SettingType;
  options?: string[];
  secret: boolean;
  value: unknown;
  default: unknown;
  /** A size or duration in words ("512 MiB", "90 days"), with the default's. */
  display?: string;
  default_display?: string;
  source: SettingSource;
  /** Forced by the environment variable `env`; read-only in the UI. */
  locked: boolean;
};
export type SettingsGroup = { name: string; label: string; settings: Setting[] };
export type Settings = { groups: SettingsGroup[] };
/** PATCH body: key to value. null resets a setting; "" for a secret leaves it unchanged. */
export type SettingsPatch = Record<string, unknown>;
/** 202 answer of a PATCH that changes the storage location while assets exist. */
export type SettingsMoveStarted = { move: Move };
export type TestResult = { ok: boolean; error?: string; fields?: Record<string, string> };

// --- storage moves ---

export type MoveState = "copying" | "done" | "failed" | "cancelled";
export type Move = {
  id: number;
  state: MoveState;
  total: number;
  copied: number;
  skipped: number;
  failed: number;
  error: string;
  started_at: string;
  finished_at: string | null;
  target: { storage: string; endpoint?: string; bucket?: string; prefix?: string; fs_root?: string };
};
export type MoveFailure = { asset_id: number; key: string; reason: string };

// --- Nexus ---

export type NexusSync = {
  id: number;
  source_id: number;
  state: "running" | "done" | "failed" | "cancelled";
  trigger: "save" | "manual" | "schedule" | "cutover";
  /** The user who started it; "" for the scheduler or a deleted user. */
  started_by: string;
  started_at: string;
  finished_at: string | null;
  error: string;
  checked: number;
  imported: number;
  skipped: number;
  ignored: number;
  failed: number;
  /** Bytes copied. */
  bytes: number;
  /** Hosted repositories that the sync copies. */
  repo_count: number;
  /** Of those, the ended ones. */
  repos_done: number;
  /** The repository that copies now; "" when none. */
  current_repo: string;
  repos?: NexusSyncRepo[];
};
export type NexusSyncRepo = {
  source_repo: string;
  source_format: string;
  target: string;
  state: "pending" | "running" | "done" | "failed" | "cancelled";
  error: string;
  /** Files read from Nexus: imported + skipped + ignored + failed. */
  checked: number;
  imported: number;
  skipped: number;
  ignored: number;
  failed: number;
  /** Bytes copied. */
  bytes: number;
  /** When the runner finished the last page. */
  active_at: string | null;
  /** `checked` of the newest earlier finished walk of this repository; 0 when none. */
  previous_checked: number;
};
export type NexusStatus = "no_repos" | "syncing" | "up_to_date" | "failed" | "cut_over";
export type NexusSource = {
  id: number;
  name: string;
  base_url: string;
  state: "mirroring" | "cut_over";
  created_at: string;
  /** Error of the last ended sync; "" when it succeeded. */
  last_error: string;
  /** The newest sync (running or ended), without repos. */
  last_sync: NexusSync | null;
  cutover_sync_id: number;
  cutover_failed: number;
  status: NexusStatus;
  /** null: none due (no repositories, switched over, or a sync runs now). */
  next_sync_at: string | null;
  /** 0 when no sync runs. */
  running_sync_id: number;
  /** Mapped repositories (hosted, proxy and group). */
  repo_count: number;
  /** Seconds: the live nexus.sync_interval setting. */
  sync_interval: number;
};
export type NexusSourceRepo = {
  name: string;
  /** Nexus format id: "maven2", "apt", "raw", "npm", ... */
  format: string;
  /** "" for a mapped repo that Nexus no longer lists. */
  type: "hosted" | "proxy" | "group" | "";
  supported: boolean;
  /** Why it is not supported; "" when supported. */
  reason: string;
  offline: boolean;
  /** What Nexus does: anyone can download it. */
  public: boolean;
  /** The format here: "maven", "debian", "raw"; "" when none. */
  target_format: string;
  /** The name of the repository here; "" when not mapped. */
  target: string;
  /** Proxy only: the remote of the Nexus proxy. */
  remote_url: string;
  /** Proxy only: the remote username that Nexus gives (never the password). */
  remote_username: string;
  /** Group only: the Nexus member names in Nexus order ([] when unknown). */
  members: string[];
  /** Group only: false when the Nexus account cannot read the members. */
  members_known: boolean;
  /** Group only, mapped: the members of the group here, as Nexus names, in order. */
  target_members: string[];
  /** Apt only: Nexus gives the signing key pair of this repository over its API. */
  nexus_key: boolean;
  /** Apt only: the Nexus key has a passphrase, which Nexus does not give. */
  nexus_key_encrypted: boolean;
};
export type NexusSourceDetail = NexusSource & {
  /** Supported first, then by name. */
  repos: NexusSourceRepo[];
  /** Why the list holds only the mapped repos; "" when Nexus listed its repos. */
  nexus_error: string;
};
export type NewNexusSource = { name: string; base_url: string; username: string; password: string };
export type NexusSourcePatch = { base_url?: string; username?: string; password?: string };
export type NexusMapping = {
  source_repo: string;
  /** The name here. */
  target: string;
  /** Create the target when it does not exist. */
  create: boolean;
  /** Used only when the save creates the target. */
  anonymous_read: boolean;
  /** Apt hosted only, when the save creates the target. Default "new". */
  signing_key?: "new" | "nexus";
  /** The passphrase of the Nexus key; "" when it has none. */
  signing_passphrase?: string;
  /** Proxy only, when the save creates the target. */
  remote_username?: string;
  remote_password?: string;
  /** Group only, when members_known is false: the Nexus member names picked by hand. */
  members?: string[];
};
export type NexusFailure = { source_repo: string; path: string; reason: string };
export type NexusLogLine = { id: number; at: string; source_repo: string; level: "info" | "warn" | "error"; message: string };

// --- publishing ---

export type EnrollmentRequest = { name: string; kind: "gradle" | "maven"; public_key: string };
export type Enrollment = {
  device_code: string;
  user_code: string;
  verification_url: string;
  expires_in: number;
  interval: number;
};
export type PendingEnrollment = { user_code: string; name: string; kind: "gradle" | "maven"; expires_at: string };

export type Device = {
  id: number;
  user_id: number;
  /** Set in the admin list (every user's devices). */
  username?: string;
  name: string;
  kind: "gradle" | "maven";
  created_at: string;
  expires_at: string;
  last_used_at: string | null;
  revoked_at: string | null;
  state: string;
};

/** A minted publish session. `token` is shown once. */
export type PublishSession = { id: number; token: string; repo: string; expires_at: string };

export type SessionInfo = {
  id: number;
  repo: string;
  via: "device" | "oidc" | "manual";
  subject: string;
  username?: string;
  device_id: number | null;
  device_name?: string;
  trust_id: number | null;
  trust_name?: string;
  created_at: string;
  expires_at: string;
  closed_at: string | null;
  state: string;
};

export type Trust = {
  id: number;
  name: string;
  issuer: string;
  audience: string;
  /** Claim name to allowed values; each is exact or a `*` glob. */
  claims: Record<string, string[]>;
  jwks: string;
  repos: string[];
  created_at: string;
  disabled: boolean;
};
/** Create needs name, issuer, claims and repos; update changes only the fields that are set. */
export type TrustInput = {
  name?: string;
  issuer?: string;
  audience?: string;
  claims?: Record<string, string[]>;
  jwks?: string;
  repos?: string[];
  disabled?: boolean;
};

// --- audit log ---

export type AuditEvent = {
  id: number;
  at: string;
  /** Username, session or subject that acted. */
  actor: string;
  action: string;
  repo: string;
  target: string;
  /** Extra facts of the event (JSON object). Never holds secret values. */
  detail: Record<string, unknown> | null;
  ip: string;
};
export type AuditFilter = { action?: string; repo?: string };
