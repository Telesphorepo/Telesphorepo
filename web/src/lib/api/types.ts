// JSON shapes of the /api routes (internal/api). Times are RFC 3339 strings. Fields that the
// server marks `omitempty` are optional here.

/** A keyset page: pass `next` back as `after` to get the following page; null on the last one. */
export type Page<T> = { items: T[]; next: string | null };
/** Keyset parameters of a list route. */
export type PageParams = { after?: string; limit?: number };

// --- auth and users ---

/** A permission ID, "<subsystem>.<name>", e.g. "repo.publish" (GET /api/permissions). */
export type PermId = string;

/** One permission of the registry: what a holder can do, and the permissions it needs. */
export type PermissionDef = {
  id: PermId;
  label: string;
  help: string;
  needs: PermId[];
  /** Holding it makes the UI session need a second factor. */
  second_factor: boolean;
  /** A server permission that holds every repository permission on every repository. */
  all_repos: boolean;
};
/** A part of the server that permissions control, with its permissions in registration order. */
export type PermissionSubsystem = {
  id: string;
  scope: "server" | "repo";
  label: string;
  help: string;
  module: string;
  permissions: PermissionDef[];
};
/** GET /api/permissions: the subsystems in module start order. */
export type PermissionRegistry = { subsystems: PermissionSubsystem[] };

export type User = {
  id: number;
  username: string;
  /** The server permissions of the account. An administrator holds all of them. */
  permissions: PermId[];
  disabled: boolean;
  kind: "local" | "oidc";
  /** "" when unknown (SSO accounts only). */
  email: string;
  created_at: string;
  /** Single sign-on sets the server permissions at each sign-in; nobody changes them here. */
  permissions_managed: boolean;
};

/**
 * The session view: GET /api/me, and `session` of a finished sign-in. second_factor_required: the
 * session reaches only the setup routes. repo_permissions: every repository where the caller has a
 * grant, with the whole set of each ({} for a holder of repositories.all, whose all_repos is true and who
 * holds every permission everywhere).
 */
export type Me = User & {
  second_factor_required: boolean;
  all_repos: boolean;
  repo_permissions: Record<string, PermId[]>;
};

/** One repository a person can use: the repository permissions there, and why. */
export type UserRepoAccess = {
  repo: string;
  format: string;
  kind: RepoKind;
  public: boolean;
  perms: PermId[];
  via: "all_repos" | "permission" | "public";
};
/**
 * GET /api/me/access and GET /api/users/{username}/access: a keyset page of the repositories a
 * person can use. can_publish is false only for a disabled account.
 */
export type UserAccess = Page<UserRepoAccess> & {
  username: string;
  permissions: PermId[];
  disabled: boolean;
  kind: "local" | "oidc";
  can_publish: boolean;
};

/** GET /api/people: one person of a picker. */
export type PersonName = { username: string };

/** POST /api/auth/login answers this when the account has a second factor. */
export type SecondFactorStep = { second_factor: true; methods: SecondFactorMethod[] };
export type SecondFactorMethod = "totp" | "passkey" | "recovery";
/** A finished sign-in step: the session view. */
export type SessionAnswer = { session: Me };
export type LoginAnswer = SessionAnswer | SecondFactorStep;

export type AuthConfig = {
  oidc: boolean;
  /** "all": every local account signs in with a password; "publishers": people who manage sign in with single sign-on only. */
  password_login: "all" | "publishers";
  /** True under `telesphorepo-server dev`: the second factor is optional (an enrolled one is still checked). */
  second_factor_optional: boolean;
};

/** A second-factor proof: one of a TOTP code, a passkey assertion (browser JSON) or a recovery code (sign-in only). */
export type Proof = { totp_code?: string; passkey?: unknown; recovery_code?: string };
/**
 * A fresh second factor for a device approval or a manual publish session, sent in the header
 * X-Telesphorepo-Proof: a TOTP code or a passkey assertion of a local account, or the grant of a
 * single sign-on account. A recovery code is for the sign-in only.
 */
export type FreshProof = Proof | { grant: string };

export type NewUser = { username: string; password: string; permissions: PermId[] };
export type UserPatch = { password: string };

/** PATCH /api/repos, PATCH /api/users, PUT /api/grants: how many items changed. */
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

/** A field of a form that a format describes: a repo config option or an upload field. */
export type FormatField = {
  key: string;
  label: string;
  help?: string;
  type: "string" | "bool" | "list" | "enum" | "secret" | "rules";
  options?: string[];
  /** A rules field: the key of the value (one of options) in each row {match, <value_key>}. */
  value_key?: string;
  required?: boolean;
  default?: unknown;
  /** An example value that the empty control shows. */
  placeholder?: string;
  /** What a change of a saved value does; shown before the change is saved. */
  change_note?: string;
};

/** How the web UI takes uploads of a format (POST /api/repos/{name}/upload/plan and /upload). */
export type FormatUpload = {
  /** The drop zone takes chosen files, and a dropped or chosen folder. */
  single: boolean;
  folder: boolean;
  /** The file types of the file picker, e.g. ".deb" (empty: any file). */
  accept: string[];
  hint: string;
  /** Globs of the files of a folder that the browser sends (empty: every file). */
  keep: string[];
  /** The words for one item of a plan and for several. */
  one: string;
  many: string;
  /** What the server does with the files besides storing them. */
  note?: string;
  /** Why a plan leaves files out. */
  ignored?: string;
  /** What an upload needs when the plan has no item. */
  empty?: string;
  fields: FormatField[];
  /** The files whose first bytes a plan reads, and how many bytes. */
  reads: { glob: string; max_bytes: number }[];
};

/** A fact about a delete of the files that match the glob: the delete plan counts them. */
export type DeleteNote = { match: string; one: string; many: string; text: string };

/**
 * GET /api/formats: the UI descriptor of a format. Every format choice of the UI reads it, so no
 * shared UI code names a format.
 */
export type Format = {
  name: string;
  title: string;
  description: string;
  /** The format's icon: an inline SVG in currentColor. */
  icon?: string;
  /** Icons of artifact kinds that differ from the format (`gradle-plugin` in a Maven repo). */
  kind_icons?: Record<string, string>;
  /** The kinds of repository the format takes: "hosted", and "proxy" or "group" when it serves them. */
  kinds: RepoKind[];
  config: { default: RepoConfig; fields: FormatField[] };
  /** The module fields of its repositories for each kind it takes, e.g. {hosted: ["signing_key"], proxy: []} (Repo.fields). */
  repo_fields: Partial<Record<RepoKind, string[]>>;
  /** The repository settings tabs of other modules that it shows, e.g. "signing". */
  repo_tabs: string[];
  /** null: no uploads through the web UI. */
  upload: FormatUpload | null;
  delete_notes: DeleteNote[];
  /** The format's part of the details of a matching file (the first match): a part of its UI module. */
  file_details: { match: string; part: string }[];
  browse: { versions: boolean; artifact_snippets: boolean };
  /** Well-known remotes of a proxy. */
  remote_presets: { label: string; url: string }[];
  /** One sentence about the files of a proxy. */
  proxy_note?: string;
  /** The path under a hosted repo where the format serves its public signing key. */
  public_key?: string;
  labels: {
    /** Artifact kind -> its words, e.g. "bom" -> "Bill of materials". */
    artifact_kinds: Record<string, string>;
    /** Artifact kind -> the word for its title, e.g. "Coordinates", "Plugin ID". */
    artifact_name: Record<string, string>;
    /** Snippet tool -> its label, e.g. "gradle-kotlin" -> "Gradle (Kotlin)". */
    tools: Record<string, string>;
    /** The heading of the artifact snippets; "" for "Use it in a build". */
    install_heading?: string;
    /**
     * The words for the files of a build, the first match wins: extension "*" is any extension,
     * classifier "*" any classifier and "" none; label may hold {classifier} and {extension}.
     */
    build_files: { extension: string; classifier: string; label: string }[];
  };
  /** The Nexus formats whose files this format imports, e.g. "maven2". */
  import_sources: string[];
  /** The build tools that publish to the format. */
  tools: { id: string; label: string }[];
  /** The publish category: "cli" (telesphorepo publish in the project folder) or "helper" (the native command of the tool). */
  publish: "cli" | "helper";
};
/** hosted: files published here; proxy: a cache of a remote repository; group: reads several repositories. */
export type RepoKind = "hosted" | "proxy" | "group";

/** One member of a group, in read order. */
/** A member of a group; `permissions` are the caller's permissions on it. */
export type GroupMember = { name: string; format: string; kind: "hosted" | "proxy"; public: boolean; permissions?: string[] };

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

/** An operation on a repository: while it runs or holds, writes, deletes and publish sessions answer 409. */
export type RepoOperation = {
  id: number;
  /** "delete", "mirror", ... */
  type: string;
  /** The module that began it. */
  owner: string;
  state: "running" | "held" | "failed";
  done: number;
  total: number;
  /** The text writes answer with, and the line the UI shows. */
  message: string;
  /** The error of a failed operation; "" otherwise. */
  error: string;
};

export type Repo = {
  name: string;
  format: string;
  description: string;
  anonymous_read: boolean;
  /** GET of one repository only; a list leaves it out. */
  config?: RepoConfig;
  /** The values of the module fields of the repository, e.g. {signing_key: "default"}. */
  fields: Record<string, unknown>;
  /** The caller's repository permissions on it; [] for a visitor of a public repository. */
  permissions: PermId[];
  /** The active operation (a repo delete, a Nexus mirror): writes and deletes answer 409 with its message. null for none. */
  operation: RepoOperation | null;
  /** Number of stored files; every file counts (artifacts, metadata, checksums, index files). */
  file_count: number;
  /** Total bytes of the stored files. */
  total_size: number;
  /** When a file was last published; null before the first publish. */
  last_published_at: string | null;
  /** A group: true while the server has not counted the files it serves to the caller yet (the counters are 0). */
  counts_pending?: boolean;
  /** GET /api/repos only: the downloads of the last 30 days, written once a minute. */
  downloads?: number;
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

/** What GET /api/repos keeps: a name prefix, a format, a kind, the repositories where the caller holds one of perm. */
export type RepoFilter = { q?: string; format?: string; kind?: RepoKind; perm?: PermId[] };

/** A bounded count: `more` when it stopped at 10,000. */
export type Count = { count: number; more: boolean };

/** GET /api/overview. */
export type Overview = {
  /** Every repository for a holder of All repositories (`all`), else the ones where the caller holds more than download. */
  repos: { all: boolean; count: number; files: number; size: number };
  /** The largest of those repositories (up to 8) that store something. */
  largest: Repo[];
  /** The public repositories that are not groups. */
  public_repos: number;
  /** A holder of Manage people: the accounts. */
  people?: Count;
};

/** An old name of a repository that still answers for it (GET /api/repos/{name}/redirects). */
export type RepoRedirect = {
  name: string;
  created_at: string;
  /** The last request through the name; null: never used. */
  last_used_at: string | null;
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
  /** Module field values (Format.repo_fields), e.g. {signing_key: "team"}; a field without one gets its default. */
  fields?: Record<string, unknown>;
};
export type RepoPatch = { description?: string; config?: RepoConfig; proxy?: ProxyInput; fields?: Record<string, unknown> };
/** POST /api/repos/delete: the repositories whose delete started, each with its running delete operation. */
export type ReposDeleted = { deleting: { name: string; operation: RepoOperation }[] };

/** GET /api/repos/{name}/grants: a person's whole permission set on the repository. */
export type Permission = { username: string; perms: PermId[] };

// --- effective access (GET /api/repos/{name}/access) ---

/**
 * A person with access and their whole permission set: a grant on the repo ("permission"), or a
 * holder of All repositories ("all_repos", only with ?admins=1).
 */
export type AccessPerson = { username: string; perms: PermId[]; via: "permission" | "all_repos" };
/** Where requests may come from: any network, or only these network lists and address ranges. */
export type AccessNetworks = { any: boolean; lists: { name: string; title: string }[]; cidrs: { cidr: string; note: string }[] };
/** What one guard of the access engine says about an operation; the network guard's detail is AccessNetworks. */
export type GuardReport = { restricted: boolean; summary: string; detail: Record<string, unknown> };
/** The guard reports of an operation, by guard name. */
export type AccessGuards = Record<string, GuardReport>;
/** A CI pipeline (CI rule) that lists the repo, with what its conditions allow. Disabled ones are listed too. */
export type AccessPipeline = {
  id: number;
  rule: string;
  /** The CI provider that reads its policy ("github-actions", "jenkins"), or "other". */
  system: string;
  issuer: string;
  repositories?: string[];
  branches?: string[];
  tags?: string[];
  workflows?: string[];
  subjects?: string[];
  disabled: boolean;
};
/**
 * GET /api/repos/{name}/access (Manage access, Edit settings or CI pipelines). people: each person
 * with a grant, once, with the whole set. download.people: the grants with download;
 * publish.people: the grants with publish. admins counts the enabled holders of All repositories.
 * publish.blocked says why nobody can publish now (null when people and pipelines can). The guards of
 * download and publish say what else limits them, by guard name (e.g. "network").
 */
export type RepoAccess = {
  repo: string;
  format: string;
  people: AccessPerson[];
  kind: RepoKind;
  download: { public: boolean; people: AccessPerson[]; admins: number; guards: AccessGuards; sentence: string; /** Extra sentences from modules, e.g. "Downloads come from the edge." */ notes: string[] };
  publish: {
    people: AccessPerson[];
    admins: number;
    ci: AccessPipeline[];
    guards: AccessGuards;
    blocked: { code: "read_only_kind" | "operation"; message: string } | null;
    sentence: string;
  };
};

/** A structured snippet (format plugin or core): one copy-paste step. Snippets of one tool come in display order. */
export type Snippet = {
  /** Stable and unique in one answer, e.g. "gradle-kotlin.settings". */
  id: string;
  /** The tool it is for, e.g. "gradle-kotlin", "maven", "apt", "curl", "github-actions", "jenkins". */
  tool: string;
  purpose: "use" | "publish";
  context: "repo" | "artifact";
  via?: "computer" | "ci" | "hand";
  /** The file it belongs in, e.g. ".github/workflows/publish.yml"; absent for a command. */
  file?: string;
  title: string;
  lang: string;
  code: string;
  /** A CI file that a project usually has already (a GitHub Actions workflow, a Jenkinsfile): what the existing file needs, as diff lines, each after its mark ("+" added, " " context). */
  diff?: string;
  note?: string;
};
/** The label of a snippet tool, as the answer names it. */
export type ToolLabel = { id: string; label: string };

/** GET /api/repos/{name}/usage: the public base URL, the labels of the tools, the snippets. */
export type Usage = { base_url: string; tools: ToolLabel[]; snippets: Snippet[] };
/** GET /api/repos/{name}/status: the units of the repo that wait for a rebuild of its indexes. */
export type RepoStatus = { pending: number };
/** The CI system of GET /api/repos/{name}/ci. */
export type CISystem = "github-actions" | "jenkins";
/** The options of GET /api/repos/{name}/ci: the build tool (default: the format's first) and the trigger. */
export type CIOptions = { system: CISystem; tool?: string; branches?: string[]; tags?: string[] };

/** A link of a CI build record, made by its CI provider. */
export type BuildLink = { rel: "repository" | "commit" | "run" | "workflow" | string; label: string; url: string };

/** The CI build that published a file (null: none), as its CI provider read it. */
export type BuildRecord = {
  id: number;
  provider: string;
  provider_title: string;
  repository: string;
  revision: string;
  ref: string;
  ref_name: string;
  ref_type: "branch" | "tag" | "";
  run: string;
  workflow: string;
  actor: string;
  links: BuildLink[];
};

/**
 * An artifact; path is its folder, with a trailing slash. title is its coordinates or package name,
 * name its short name for a heading, kind the format's word for it (labels.artifact_kinds).
 */
export type Artifact = { path: string; title: string; name: string; kind: string };

/** A version of an artifact. The artifact folder and the version string name it. */
export type Version = {
  version: string;
  kind: "release" | "snapshot";
  /** The folder that holds every file of the version and no file of another (Maven: the version folder); absent when the version shares its folder (Debian). */
  folder?: string;
  latest_build?: string;
  /** The format's name of the latest build for people ("Build 3"); empty: show latest_build. */
  latest_label?: string;
  builds_count: number;
  /** null when the version has no date (a proxy's version whose remote document names none). */
  published_at: string | null;
  build: BuildRecord | null;
  /** A group: the member that serves the version. */
  repo?: string;
  /** A proxy's version that its remote lists and whose files the proxy has not cached yet. */
  remote?: boolean;
};

/** A file of a build. repo: in a group, the member that serves it. remote: a proxy's file not cached yet (size 0 and sha256 empty when the remote document names neither). */
export type BuildFile = { name: string; path: string; size: number; sha256: string; classifier: string; extension: string; repo?: string; remote?: boolean };
/** A build of a version; label is the format's name for it for people, empty to show the id. */
export type Build = { id: string; label?: string; files: BuildFile[]; published_at: string | null; build: BuildRecord | null };

/** GET /api/repos/{name}/versions: artifact is null for a folder that is not an artifact. */
export type Versions = Page<Version> & { artifact: Artifact | null };
/** GET /api/repos/{name}/builds: artifact and version are null when the artifact has no such version. */
export type Builds = Page<Build> & { artifact: Artifact | null; version: Version | null };

/** GET /api/repos/{name}/file: one file with its details. */
export type FileInfo = {
  name: string;
  path: string;
  size: number;
  sha256: string;
  content_type: string;
  kind: string;
  created_at: string;
  updated_at: string;
  uploaded_by: string;
  build: BuildRecord | null;
  /** A group: the member that serves the file. */
  repo?: string;
};

export type VersionMode = "pinned" | "latest";
/** GET /api/repos/{name}/snippets: the snippets that use one artifact. */
export type ArtifactSnippets = {
  base_url: string;
  artifact: Artifact | null;
  version: string;
  version_mode: VersionMode;
  /** The snippets take one version only: the UI offers no choice of version. */
  fixed_version: boolean;
  tools: ToolLabel[];
  snippets: Snippet[];
};

export type Asset = {
  path: string;
  size: number;
  sha256: string;
  content_type: string;
  kind: string;
  updated_at: string;
  /** A group: the member that serves the file. */
  repo?: string;
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
  /** The CI build that published the file; absent for none. */
  build?: BuildRecord | null;
  /** A file in the listing of a group: the member that serves it. */
  repo?: string;
};

/** POST /api/repos/{name}/files/delete and /files/delete/plan: files by path, and folders by prefix (ending in "/"). */
export type FilesDelete = { paths?: string[]; prefixes?: string[] };
/** POST /api/repos/{name}/files/delete: how many files the server removed. */
export type DeleteResult = { deleted: number };
/**
 * POST /api/repos/{name}/files/delete/plan: how many files a delete removes (more: the folders hold
 * more than the plan counted), and for each delete note of the format the count of files it matches.
 */
export type DeletePlan = { files: number; more: boolean; notes: { text: string; count: number; one: string; many: string }[] };

/** One stored file of a manual upload; generated marks the checksum and metadata files that the server wrote. */
export type Uploaded = { path: string; status: "created" | "replaced" | "unchanged"; size: number; sha256: string; generated?: boolean };
/** POST /api/repos/{name}/upload. */
export type UploadAnswer = { files: Uploaded[] };
/** Bytes of an upload body sent so far. */
export type UploadProgress = { sent: number; total: number };

/** One file of an upload plan: its path in the upload and the repo path it gets ("" while the plan cannot tell). */
export type PlanTarget = { path: string; target: string };
/** One artifact or package of an upload plan; problems say why the upload refuses it (empty: it can go). */
export type PlanItem = { title: string; files: PlanTarget[]; facts?: Record<string, string>; problems: string[] };
/** A field that applies to an upload: its value, read from the files (read: fixed) or typed; problem refuses the upload. */
export type PlanField = FormatField & { value: string; read?: boolean; problem?: string };
/**
 * POST /api/repos/{name}/upload/plan: what an upload stores. ignored are the paths no item takes.
 * heads names the first bytes the server holds for a later plan ("" when it holds none).
 */
export type UploadPlan = { items: PlanItem[]; ignored: string[]; fields: PlanField[]; heads?: string };
/**
 * The request of an upload plan: the typed fields, the open folder, and the files with their heads.
 * With heads (of an earlier plan), the heads of the paths in held are not sent: the server has them.
 */
export type PlanRequest = {
  fields: Record<string, string>;
  folder: string;
  files: { path: string; file: File }[];
  heads?: string;
  held?: Set<string>;
};
/** POST /api/repos/{name}/uploads: a resumable upload. */
export type UploadSession = { id: string; received: number; expires_at: string };
/** A file that went through a resumable upload, for the part "uploads" of POST /api/repos/{name}/upload. */
export type FinishedUpload = { path: string; id: string; size: number };
// --- network rules ---

/** One entry of a repo's IP rule: a CIDR ("203.0.113.0/24") or the name of an IP list. */
export type IPRule = { kind: "cidr" | "list"; value: string; note: string };
/** GET/PUT /api/repos/{name}/ip-rules. An empty list leaves its scope open to every address. */
export type IPRules = { read: IPRule[]; publish: IPRule[] };

/** What refers to an IP list: repo rules (by scope) and the trusted proxies. */
export type IPListUsage = { repos: { repo: string; scope: "read" | "publish" }[]; trusted_proxies: boolean };
/**
 * A named set of address ranges (GET /api/ip-lists). A preset is fetched by the server (source is
 * its address; fetched_at is null before the first good fetch); a custom list is edited by holders of Network.
 * ranges is only in GET /api/ip-lists/{name}.
 */
export type IPList = {
  id: number;
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

/** A key that signs the indexes of repositories. error says why the server cannot use it. */
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
/** The matches of one repository; `more`: at least `matches`, the count stopped there. */
export type SearchRepo = { name: string; format: string; matches: number; more: boolean };
/** A page of GET /api/search: a repository's hits together; the first page also has `repos`, in the same order. */
export type SearchPage = Page<SearchHit> & { repos?: SearchRepo[] };

// --- settings ---

/** An image (the server logo) reports only whether it is set, like a secret; its own route changes it. */
export type SettingType = "string" | "bool" | "int" | "size" | "duration" | "list" | "map" | "image";
/** GET /api/identity: the name of the server, the URL of its logo, and whether either is set. */
export type Identity = { name: string; logo: string | null; customized: boolean };
export type SettingSource = "stored" | "env" | "default";
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
/**
 * A settings group of a module: perm reads and edits every setting of it, page is the UI path
 * that edits it ("" for Settings › General).
 */
export type SettingsGroup = { id: string; label: string; module: string; perm: PermId; page: string; settings: Setting[] };
export type Settings = { groups: SettingsGroup[] };
/** PATCH body: key to value. null resets a setting; "" for a secret leaves it unchanged. */
export type SettingsPatch = Record<string, unknown>;
/** 202 answer of a PATCH that changes the storage location while assets exist. */
export type SettingsMoveStarted = { move: Move };
export type TestResult = { ok: boolean; error?: string; fields?: Record<string, string> };

// --- edge delivery ---

/** The six R2 location hints. */
export type EdgeHint = "wnam" | "enam" | "weur" | "eeur" | "apac" | "oc";
/** not_used: no bucket; filling: pointers and blobs are copied; ready: the Worker reads it; waiting: saved, the Worker needs a deploy. */
export type EdgeRegionState = "not_used" | "filling" | "ready" | "waiting";
export type EdgeRegionStatus = {
  hint: EdgeHint;
  /** The area of the hint, e.g. "Western Europe". */
  area: string;
  bucket: string;
  state: EdgeRegionState;
  /** Backfill progress while the region is filling. */
  pointers_written: number;
  pointers_total: number;
  /** Every pointer of the region is written; the server then checks its files. */
  pointers_done: boolean;
  /** Blobs of the home bucket that the region lacks; null before the count is known. */
  blobs_missing: number | null;
  /** When the count of missing files last changed; null before a count. */
  blobs_missing_since: string | null;
  /** The end of the last whole count of missing files; null before one. */
  blobs_checked_at: string | null;
  /** The copies and deletes that the Worker's last finished copy pass queued for the region; null before one. */
  copies_queued: number | null;
  /** Pointers waiting in the outbox for this bucket. */
  outbox: number;
  last_error: string;
};
/** GET /api/edge/status */
export type EdgeStatus = {
  enabled: boolean;
  /** on, draining (stopped; the Workers' cached configuration still says on) or off. */
  mode: "on" | "draining" | "off";
  /** Why the settings cannot run edge delivery while it is switched on; "" when they can. */
  unfit: string;
  server_version: string;
  /** The build the Worker reported at its last gap check; null before the first check. */
  worker_version: string | null;
  /** Why the deployed Worker sends every request to the server (it lacks a feature this server needs); "" when it serves. */
  worker_outdated: string;
  gap_checked_at: string | null;
  /** Seconds: the interval of the Worker's gap check (its cron). */
  gap_check_every: number;
  /** Seconds: the interval of the server's count of the files of a filling region. */
  blob_check_every: number;
  /** The start of the Worker's copy pass that runs now; null between passes. */
  gap_pass_started: string | null;
  /** A storage or region change, or an outdated Worker, needs a new deploy. */
  deploy_needed: boolean;
  /** The storage change that asks for the deploy ("storage change", "storage move"); "" otherwise. */
  deploy_reason: string;
  /** Why this build has no edge Worker to download; "" when it has. */
  worker_missing: string;
  /** Why a check of the Worker cannot run (POST /api/edge/check); "" when it can. */
  check_unfit: string;
  /** What the last check of the Worker found; null before one. */
  last_check: EdgeCheck | null;
  /** outbox: every row of the outbox (pointers and access objects); pointers: its pointer rows. */
  home: { hint: EdgeHint | ""; bucket: string; outbox: number; pointers: number; last_error: string };
  /** The backfill of every pointer after edge delivery starts. */
  backfill: { running: boolean; queued: number; total: number };
  /** One entry per chosen region. */
  regions: EdgeRegionStatus[];
};
/**
 * POST /api/edge/check: worker (the Worker answered and ran its gap check), refused (the Worker
 * refused the check), server (the request reached the server, not the Worker), other (something
 * else answered), unreachable (no answer), error (the check request was not written).
 */
export type EdgeCheck = {
  at: string;
  outcome: "worker" | "refused" | "server" | "other" | "unreachable" | "error";
  /** The Worker of the server's version answered and ran its gap check. */
  ok: boolean;
  message: string;
  worker_version?: string;
};
/** A file that the speed test suggests: an immutable file of a public repository; url is its download path. */
export type EdgeSpeedFile = { repo: string; format: string; path: string; size: number; url: string };
/** GET /api/edge/speedtest: base_url is the scheme and host of the base URL ("" when unset); unfit says why no pass can be issued. */
export type EdgeSpeedFiles = { base_url: string; unfit: string; files: EdgeSpeedFile[] };
/** POST /api/edge/speedtest: the pass that lets this browser pick the bucket of a download. */
export type EdgeSpeedPass = { token: string; expires_at: string };

// --- backups ---

/** The step of a running backup. */
export type BackupProgress = { step: "files" | "database" | "cleanup"; blobs: number; copied: number };
/** The backup job of the server process. */
export type BackupJob = {
  running: boolean;
  started_at: string | null;
  progress: BackupProgress | null;
  /** The end and the error ("" for none) of the last run since the server started. */
  last_at: string | null;
  last_error: string;
};
/** One folder of the backup folder. An incomplete one did not finish; the next run removes it. */
export type Backup = {
  name: string;
  created_at: string;
  complete: boolean;
  version: string;
  storage: string;
  /** The database and the files it uses: what a restore brings back. */
  size: number;
  /** The database file: all that the backup holds when its files stay in the S3 bucket. */
  database_size: number;
  files: number;
  blobs_included: boolean;
  master_key_included: boolean;
};
/** GET /api/backup. */
export type BackupOverview = {
  job: BackupJob;
  /** When the next scheduled backup is due; null while backups are off. */
  next_at: string | null;
  /** Newest first. */
  backups: Backup[];
  /** Why the folder could not be read; "" for none. */
  error: string;
};
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
  /** When an admin put the ended move away; GET /api/storage/moves/current then answers 404. */
  dismissed_at: string | null;
  target: { storage: string; endpoint?: string; bucket?: string; prefix?: string; fs_root?: string };
  /**
   * The storage settings the move switches to, by setting key, in the form of GET /api/settings:
   * the secret access key is {set}, never the secret.
   */
  target_settings: Record<string, unknown>;
  /**
   * The copier's last save of the counters; null when no copier of the server process runs the
   * move. It saves at most every `save_every` seconds, when a file is done.
   */
  progress_at: string | null;
  save_every: number;
};
/** POST /api/storage/moves/{id}/retry: the new move, or null when the settings saved at once. */
export type MoveRetried = { move: Move | null };
export type MoveFailure = { blob_id: number; key: string; reason: string };

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
  /** The merge whose final sync this is (trigger cutover), 0 for none; its repos at the start. */
  merge_id: number;
  merge_from: string;
  merge_into: string;
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
  /** Mapped hosted repositories that still copy from Nexus. */
  copying: number;
  /** Mapped hosted repositories that switched over: uploads go to them here. */
  switched_over: number;
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
  /** What the target format asks before a save creates a target of this repository. */
  questions: ImportQuestion[];
  /** What the target format read from the repository's settings, e.g. {key_in_source: true}. */
  facts: Record<string, unknown>;
  /** A mapped hosted repository that switched over; null while it copies from Nexus. */
  switch_over: NexusSwitchOver | null;
  /** The rehearsal repositories of the repository here. */
  rehearsals: string[];
  /** The repositories that projects move to from the repository here (Move it project by project). */
  moving: string[];
  /** A merge into the repository here that runs or failed; 0 for none. */
  merge_id: number;
};
/**
 * A path that both repositories hold with other bytes, where the format keeps a file fixed (a
 * release, a Docker tag), or a label of the format (an npm dist-tag) with other values. `path` is
 * the key of its choice ("label:<key>" for a label).
 */
export type NexusMergeConflict = {
  kind: "file" | "label";
  /** A label for people, e.g. "lib: dist-tag beta"; "" for a file. */
  title: string;
  value: string;
  target_value: string;
  path: string;
  size: number;
  sha256: string;
  updated_at: string;
  target_size: number;
  target_sha256: string;
  target_updated_at: string;
};
/** What a merge of `from` into `into` would do, from the files of both now (GET /api/nexus/merges/plan). */
export type NexusMergePlan = {
  from: string;
  into: string;
  format: string;
  /** The Nexus server that copies into `into`. */
  source_id: number;
  source_name: string;
  /** `into` still copies from Nexus: the merge runs a final sync first and switches it over after. */
  copying: boolean;
  files: number;
  copy: number;
  skip: number;
  rebuild: number;
  /** Labels of the format (npm dist-tags) that `into` takes from `from`, conflicts aside. */
  labels: number;
  /** The first 1000 conflicts by path; `conflict_count` counts them all. */
  conflicts: NexusMergeConflict[];
  conflict_count: number;
  /** Files the format cannot take from another repository: a merge with any does not start. */
  blocked: { path: string; reason: string }[];
  blocked_count: number;
  people: string[];
  pipelines: string[];
  groups: string[];
  from_redirects: string[];
  into_redirects: string[];
  /** Why the merge cannot start now; "" when it can. */
  busy: string;
  /** A merge into `into` that runs or failed; 0 for none. */
  merge_id: number;
};
export type NexusMergeChoice = "keep" | "take";
export type NexusMergeInput = {
  from: string;
  into: string;
  keep: "target" | "source";
  choices: Record<string, NexusMergeChoice>;
  otherwise: NexusMergeChoice | "";
};
/** A merge (GET /api/nexus/merges/{id}). */
export type NexusMerge = {
  id: number;
  /** The names of the two repositories when the merge started. */
  from: string;
  into: string;
  /** The merged repository's name now; "" when it is gone. */
  name: string;
  /** The name that answers for the merged repository after the merge. */
  redirect: string;
  keep: "target" | "source";
  format: string;
  source_id: number;
  source_name: string;
  state: "running" | "failed" | "done" | "stopped";
  step: "sync" | "copy" | "access" | "switch" | "retire" | "rename" | "done";
  error: string;
  /** The final sync from Nexus; null when the target copied no more and needed none. */
  sync_id: number | null;
  files: number;
  done: number;
  copied: number;
  skipped: number;
  rebuilt: number;
  kept: number;
  taken: number;
  groups: string[];
  started_by: string;
  started_at: string;
  finished_at: string | null;
};
/** The switch-over of one repository: its final sync and the files that sync did not copy. */
export type NexusSwitchOver = { sync_id: number; failed: number; switched_at: string };
/** A question of the target format of an import (format.Importer Plan). */
export type ImportQuestion = {
  key: string;
  label: string;
  help?: string;
  type: "choice" | "secret";
  options?: { value: string; label: string }[];
  required: boolean;
  default?: string;
  /** Asked only while the answer to the question `key` is `value`; otherwise not shown and not sent. */
  show_when?: { key: string; value: string };
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
  /** The answers to the questions of the target format, by key; used when the save creates the target. */
  answers?: Record<string, string>;
  /** Proxy only, when the save creates the target. */
  remote_username?: string;
  remote_password?: string;
  /** Group only, when members_known is false: the Nexus member names picked by hand. */
  members?: string[];
};
export type NexusFailure = { source_repo: string; path: string; reason: string };
export type NexusLogLine = { id: number; at: string; source_repo: string; level: "info" | "warn" | "error"; message: string };

// --- publishing ---

export type PendingEnrollment = {
  user_code: string;
  name: string;
  expires_at: string;
};

export type Device = {
  id: number;
  user_id: number;
  /** Set in the list of a holder of People (every user's devices). */
  username?: string;
  name: string;
  created_at: string;
  expires_at: string;
  last_used_at: string | null;
  revoked_at: string | null;
  state: string;
};

/** A setup code for one repo: link is the address to give to "telesphorepo setup". It is valid until expires_at and works once. */
export type SetupCode = { code: string; link: string; expires_at: string };

/** A CLI archive of the server's build (GET /cli/SHA256SUMS): os "linux", "darwin" or "windows"; arch "amd64" or "arm64". */
export type CLIArchive = { name: string; sha256: string; os: string; arch: string; url: string };

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
  /** The person who acted; null when the server or a CI job acted. */
  actor_user_id: number | null;
  action: string;
  repo: string;
  target: string;
  /** Extra facts of the event (JSON object). Never holds secret values. */
  detail: Record<string, unknown> | null;
  ip: string;
};
/**
 * GET /api/audit filters: action, repo (a name), actor (a username: the person who acted), since
 * and until (RFC 3339: the events at or after since and before until).
 */
export type AuditFilter = { action?: string; repo?: string; actor?: string; since?: string; until?: string };
/** GET /api/stats: the downloads of each day of the window (UTC days, oldest first) and the top repositories. */
export type DownloadStats = {
  days: { day: string; downloads: number; bytes: number }[];
  repos: { name: string; format: string; downloads: number; bytes: number }[];
  downloads: number;
  bytes: number;
};
/** GET /api/audit/actions: an action of the audit log with its label and permission subsystem. */
export type AuditAction = { id: string; label: string; subsystem: string };

/** One released entry of the change log: what changed in a scope, and which side (needs) must be new. */
export type ChangeEntry = { id?: string; version: string; scope: string; needs: string; summary: string };
/** GET /api/changes: the version of the server and the entries of its change log. */
export type Changes = { version: string; changes: ChangeEntry[] };
/** GET and PUT /api/network/trusted-proxies: the IP lists that the trusted proxies include. */
export type TrustedProxyLists = {
  lists: { id: number; name: string }[];
  /** The lists that TR_TRUSTED_PROXIES names; they change only with the environment. */
  env_lists: string[];
};
