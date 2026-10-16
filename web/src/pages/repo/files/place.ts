// What a folder of the Files tab is: a plain folder, an artifact (it has versions), or a version
// (its files are builds). The format plugin decides (GET /versions and /builds answer null for
// any other folder), so a new format brings its own artifacts. A version is named by its artifact
// folder and ?version= in the address (#/repo/x/files?path=pool/main/u/urn/&version=3.0~SNAPSHOT).
// A format with a folder per version (Maven) also opens the version at that folder: the folder's
// parent is the artifact and its name the version. The requests are made only where they can
// match: such a version folder's name starts with a digit, and an artifact sits at least two
// folders deep.

import { api, type Artifact, type Version } from "$lib/api";
import { baseName, parentOf } from "../browse";
import type { DeleteTarget } from "./FolderList.svelte";

export type Place =
  | { kind: "folder" }
  | { kind: "artifact"; artifact: Artifact }
  | { kind: "version"; artifact: Artifact; version: Version };

/** The place of `folder`; `version` is the ?version= of the address ("" for none). */
export async function placeOf(repo: string, folder: string, version: string): Promise<Place> {
  if (folder === "") return { kind: "folder" };
  if (version) {
    const b = await api.repos.builds(repo, folder, version, { limit: 1 });
    if (b.artifact && b.version) return { kind: "version", artifact: b.artifact, version: b.version };
  }
  const depth = folder.split("/").filter(Boolean).length;
  const maybeVersion = depth >= 3 && /^\d/.test(baseName(folder));
  const [b, v] = await Promise.all([
    maybeVersion ? api.repos.builds(repo, parentOf(folder), baseName(folder), { limit: 1 }) : null,
    depth >= 2 && !maybeVersion ? api.repos.versions(repo, folder, { limit: 1 }) : null,
  ]);
  if (b?.artifact && b.version) return { kind: "version", artifact: b.artifact, version: b.version };
  if (v?.artifact) return { kind: "artifact", artifact: v.artifact };
  // An artifact whose name starts with a digit ("3d-engine").
  if (maybeVersion) {
    const a = (await api.repos.versions(repo, folder, { limit: 1 })).artifact;
    if (a) return { kind: "artifact", artifact: a };
  }
  return { kind: "folder" };
}

/** True where Cover Flow carries meaning: the versions of an artifact, or the builds of a snapshot. */
export const flowsHere = (p: Place | undefined) =>
  p?.kind === "artifact" || (p?.kind === "version" && p.version.kind === "snapshot" && p.version.builds_count > 1);

/**
 * What a delete of a version removes. When every file of its builds lies in the folder named
 * after the version (Maven), the folder, with its checksum and signature files. Else (Debian
 * keeps every version of a package in one folder) the files of all its builds.
 */
export async function versionTarget(repo: string, artifact: Artifact, v: Version): Promise<DeleteTarget> {
  const files: string[] = [];
  let after: string | undefined;
  do {
    const p = await api.repos.builds(repo, artifact.path, v.version, { after, limit: 100 });
    for (const b of p.items) files.push(...b.files.map((f) => f.path));
    after = p.next ?? undefined;
  } while (after);
  const folder = artifact.path + v.version + "/";
  if (files.length && files.every((f) => f.startsWith(folder))) return { path: folder, dir: true };
  return { path: artifact.path, dir: false, files, version: v.version };
}
