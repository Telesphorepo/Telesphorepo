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
export type UserPatch = { password?: string; is_admin?: boolean; disabled?: boolean };

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
export type Format = { name: string; title: string; default_config: RepoConfig; settings_group?: string };

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
};
export type NewRepo = { name: string; format: string; description?: string; anonymous_read: boolean; config?: RepoConfig };
export type RepoPatch = { description?: string; anonymous_read?: boolean; config?: RepoConfig };

export type Permission = { username: string; level: "read" | "write" };

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

/** DELETE /api/repos/{name}/files: how many files the server removed. */
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

/** One entry of a repo's IP allow-list: a CIDR ("203.0.113.0/24") or a preset name. */
export type IPRule = { kind: "cidr" | "preset"; value: string; note: string };
/** GET/PUT /api/repos/{name}/ip-rules. An empty list leaves its scope open to every address. */
export type IPRules = { read: IPRule[]; publish: IPRule[] };
/** A fetched IP range preset (GET /api/network/presets). fetched_at is null before the first good fetch. */
export type NetworkPreset = { name: string; title: string; count: number; fetched_at: string | null; error: string };
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

export type SettingType = "string" | "bool" | "int" | "duration" | "list";
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
  source: SettingSource;
  /** Forced by the environment variable `env`; read-only in the UI. */
  locked: boolean;
  /** Not changeable through PATCH /api/settings (the signing key has its own route). */
  read_only?: boolean;
};
export type SettingsGroup = { name: string; label: string; settings: Setting[] };
export type Settings = { groups: SettingsGroup[] };
/** PATCH body: key to value. null resets a setting; "" for a secret leaves it unchanged. */
export type SettingsPatch = Record<string, unknown>;
/** 202 answer of a PATCH that changes the storage location while assets exist. */
export type SettingsMoveStarted = { move: Move };
export type TestResult = { ok: boolean; error?: string; fields?: Record<string, string> };
export type SigningKey = { public_key: string; fingerprint: string };

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
  cutover: boolean;
  started_at: string;
  finished_at: string | null;
  error: string;
  imported: number;
  skipped: number;
  ignored: number;
  failed: number;
  repos?: NexusSyncRepo[];
};
export type NexusSyncRepo = {
  source_repo: string;
  source_format: string;
  target: string;
  state: "pending" | "running" | "done" | "failed" | "cancelled";
  error: string;
  imported: number;
  skipped: number;
  ignored: number;
  failed: number;
};
export type NexusSource = {
  id: number;
  name: string;
  base_url: string;
  state: "mirroring" | "cut_over";
  created_at: string;
  /** Error of the last ended sync; "" when it succeeded. */
  last_error: string;
  last_sync: NexusSync | null;
  cutover_sync_id: number;
  cutover_failed: number;
};
export type NexusSourceRepo = {
  name: string;
  format: string;
  /** "" for a mapped repo that Nexus did not list. */
  type: string;
  supported: boolean;
  reason: string;
  target_format: string;
  target: string;
};
export type NexusSourceDetail = NexusSource & {
  repos: NexusSourceRepo[];
  /** Why the list holds only the mapped repos; "" when Nexus listed its repos. */
  nexus_error: string;
};
export type NewNexusSource = { name: string; base_url: string; username: string; password: string };
export type NexusSourcePatch = { base_url?: string; username?: string; password?: string };
export type NexusMapping = { source_repo: string; target: string; create: boolean };
export type NexusFailure = { source_repo: string; path: string; reason: string };

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
