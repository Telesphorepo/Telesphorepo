// What a folder of the Files tab is: a plain folder, an artifact (its subfolders are versions),
// or a version (its files are builds). The format plugin decides (GET /versions and /builds
// answer null for any other folder), so a new format brings its own artifacts. The two requests
// are made only where they can match: a version folder's name starts with a digit, and an
// artifact sits at least two folders deep.

import { browse, baseName, type Artifact, type Version } from "../browse";

export type Place =
  | { kind: "folder" }
  | { kind: "artifact"; artifact: Artifact }
  | { kind: "version"; artifact: Artifact; version: Version };

export async function placeOf(repo: string, folder: string): Promise<Place> {
  if (folder === "") return { kind: "folder" };
  const depth = folder.split("/").filter(Boolean).length;
  const maybeVersion = depth >= 3 && /^\d/.test(baseName(folder));
  const [b, v] = await Promise.all([
    maybeVersion ? browse.builds(repo, folder, { limit: 1 }) : null,
    depth >= 2 && !maybeVersion ? browse.versions(repo, folder, { limit: 1 }) : null,
  ]);
  if (b?.artifact && b.version) return { kind: "version", artifact: b.artifact, version: b.version };
  if (v?.artifact) return { kind: "artifact", artifact: v.artifact };
  // An artifact whose name starts with a digit ("3d-engine").
  if (maybeVersion) {
    const a = (await browse.versions(repo, folder, { limit: 1 })).artifact;
    if (a) return { kind: "artifact", artifact: a };
  }
  return { kind: "folder" };
}

/** True where Cover Flow carries meaning: the versions of an artifact, or the builds of a snapshot. */
export const flowsHere = (p: Place | undefined) =>
  p?.kind === "artifact" || (p?.kind === "version" && p.version.kind === "snapshot" && p.version.builds_count > 1);
