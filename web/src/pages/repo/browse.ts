// The Browse routes of a repository that $lib/api does not have yet: versions, builds, one file's
// details, per-artifact snippets, and the structured snippets of /usage. JSON shapes follow
// internal/format and internal/provenance. Plus the small helpers the Files views share.

import { api, request, type Page, type PageParams, type TreeEntry } from "$lib/api";

const repo = (name: string) => `/api/repos/${encodeURIComponent(name)}`;

/** The GitHub Actions build that published a file (null or absent: none). */
export type Provenance = {
  repository: string;
  repository_url: string;
  sha: string;
  commit_url: string;
  ref: string;
  ref_name: string;
  ref_type: "branch" | "tag" | "";
  run_id: string;
  run_attempt: string;
  run_url: string;
  workflow: string;
  workflow_path: string;
  workflow_ref: string;
  actor: string;
};

export type SnippetClient = "gradle-kotlin" | "gradle-groovy" | "maven" | "apt" | "curl" | "wget" | "github-actions" | "jenkins";

/** A structured snippet from the format plugin. Snippets of one client come in display order. */
export type Snippet = {
  client: SnippetClient | string;
  purpose: "use" | "publish";
  context: "repo" | "artifact";
  via?: "computer" | "ci" | "hand";
  title: string;
  lang: string;
  code: string;
  note?: string;
};

export type Usage = { base_url: string; snippets: Snippet[] };

export type ArtifactKind = "library" | "bom" | "parent" | "gradle-plugin" | "package";
/** An artifact; path is its folder, with a trailing slash. */
export type Artifact = { path: string; title: string; kind: ArtifactKind | string };

/** A version of an artifact. The artifact folder and the version string name it. */
export type Version = {
  version: string;
  kind: "release" | "snapshot";
  latest_build?: string;
  builds_count: number;
  published_at: string;
  provenance: Provenance | null;
};

export type BuildFile = { name: string; path: string; size: number; sha256: string; classifier: string; extension: string };
export type Build = { build: string; files: BuildFile[]; published_at: string; provenance: Provenance | null };

export type Versions = Page<Version> & { artifact: Artifact | null };
export type Builds = Page<Build> & { artifact: Artifact | null; version: Version | null };

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
  provenance: Provenance | null;
};

export type VersionMode = "pinned" | "latest";
export type ArtifactSnippets = {
  base_url: string;
  artifact: Artifact | null;
  version: string;
  version_mode: VersionMode;
  snippets: Snippet[];
};

const page = (p: PageParams = {}) => ({ after: p.after, limit: p.limit });

/** A child of a folder (GET /tree), with the build that published it. */
export type TreeNode = TreeEntry & { provenance?: Provenance };

export const browse = {
  /** The direct children of a folder ("" is the top). */
  tree: (name: string, path: string, p?: PageParams) => api.repos.tree(name, path, p) as Promise<Page<TreeNode>>,
  usage: (name: string) => request<Usage>("GET", repo(name) + "/usage"),
  /** The versions of an artifact folder, newest first; artifact is null for any other folder. */
  versions: (name: string, path: string, p?: PageParams) =>
    request<Versions>("GET", repo(name) + "/versions", { query: { path, ...page(p) } }),
  /** The builds of one version of an artifact folder, newest first; version is null when the artifact has no such version. */
  builds: (name: string, path: string, version: string, p?: PageParams) =>
    request<Builds>("GET", repo(name) + "/builds", { query: { path, version, ...page(p) } }),
  /** One file with its details; 404 when it does not exist. */
  file: (name: string, path: string) => request<FileInfo>("GET", repo(name) + "/file", { query: { path } }),
  /** The snippets that use one artifact: an artifact folder (with a version, or "" for none), a version folder, or a file. */
  snippets: (name: string, path: string, version: string, mode: VersionMode) =>
    request<ArtifactSnippets>("GET", repo(name) + "/snippets", { query: { path, version, version_mode: mode } }),
};

// --- helpers ---

const sideExt = /\.(md5|sha1|sha256|sha512|asc|sig)$/;

/** A checksum or signature of another file: a.jar.sha1 → "a.jar"; null for any other name. */
export function sideOf(path: string): string | null {
  const m = path.match(sideExt);
  return m ? path.slice(0, -m[0].length) : null;
}

/** The label of a checksum or signature file next to its file: "SHA-1". */
export function sideLabel(path: string): string {
  const ext = path.slice(path.lastIndexOf(".") + 1);
  return { md5: "MD5", sha1: "SHA-1", sha256: "SHA-256", sha512: "SHA-512", asc: "Signature", sig: "Signature" }[ext] ?? ext;
}

/** The short form of a commit: 7 characters. */
export const shortSha = (sha: string) => sha.slice(0, 7);

/** Compares two version strings by their numeric and text parts: "1.10.0" > "1.9.2". */
export function compareVersions(a: string, b: string): number {
  const pa = a.split(/[.\-+_]/);
  const pb = b.split(/[.\-+_]/);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const x = pa[i] ?? "";
    const y = pb[i] ?? "";
    if (x === y) continue;
    const nx = /^\d+$/.test(x);
    const ny = /^\d+$/.test(y);
    if (nx && ny) return Number(x) - Number(y);
    // A missing part: 1.0 < 1.0.1, but 1.0 > 1.0-SNAPSHOT (a text part marks a pre-release).
    if (x === "") return ny ? -1 : 1;
    if (y === "") return nx ? 1 : -1;
    if (nx !== ny) return nx ? 1 : -1;
    return x < y ? -1 : 1;
  }
  return 0;
}

/** The parent folder of a repo path ("" for the top). */
export function parentOf(path: string): string {
  const p = path.endsWith("/") ? path.slice(0, -1) : path;
  return p.slice(0, p.lastIndexOf("/") + 1);
}

/** The last segment of a repo path, without the folder slash. */
export const baseName = (path: string) => {
  const p = path.endsWith("/") ? path.slice(0, -1) : path;
  return p.slice(p.lastIndexOf("/") + 1);
};

/** "20261003.101010-3" → "#3, 3 Oct 2026 10:10" parts: the build number and its UTC time. */
export function buildParts(id: string): { n: string; at: Date | null } {
  const m = id.match(/^(\d{4})(\d{2})(\d{2})\.(\d{2})(\d{2})(\d{2})-(\d+)$/);
  if (!m) return { n: "", at: null };
  return { n: m[7], at: new Date(Date.UTC(+m[1], +m[2] - 1, +m[3], +m[4], +m[5], +m[6])) };
}

/** A build's name for people: "Build 3" for a timestamped snapshot build, else its id. */
export const buildLabel = (id: string) => {
  const { n } = buildParts(id);
  return n ? `Build ${n}` : id === "SNAPSHOT" ? "Build without a timestamp" : id;
};

/** The build's name inside a sentence: "build 3", or the id as it is (a Debian version keeps its case). */
export const buildInline = (id: string) => {
  const label = buildLabel(id);
  return label === id ? id : label.toLowerCase();
};

/** How each snippet client is named on its tab. */
export const clientLabel: Record<string, string> = {
  "gradle-kotlin": "Gradle (Kotlin)",
  "gradle-groovy": "Gradle (Groovy)",
  maven: "Maven",
  apt: "apt",
  curl: "curl",
  wget: "wget",
  "github-actions": "GitHub Actions",
  jenkins: "Jenkins",
};

/** The words for an artifact kind. */
export const artifactKindLabel: Record<string, string> = {
  library: "Library",
  bom: "Bill of materials",
  parent: "Parent POM",
  "gradle-plugin": "Gradle plugin",
  package: "Debian package",
};
