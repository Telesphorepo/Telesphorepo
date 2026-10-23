// The hits of a search, grouped the way people look for an artifact: by repository, then by the
// folder that holds the files (an artifact version folder: "com/contoso/widget/2.1.0/"). The
// folders under one parent fold into one entry for the parent (the artifact: its version
// folders, and its own files such as maven-metadata.xml) when there are two of them, or one and
// files of the parent's own. A checksum or a
// signature of a file ("widget-2.1.0.jar.sha1", "….asc") folds into that file when the file is
// among the hits; alone it stays a file of its own.

import type { SearchHit } from "$lib/api";

/** The endings of the checksum and signature files that sit next to a file. */
const sidecarEnds = [".md5", ".sha1", ".sha256", ".sha512", ".asc", ".sig"];

/** A checksum or signature of a file: its ending without the dot ("sha1", "asc.sha256"). */
export type Sidecar = { label: string; hit: SearchHit };
export type HitFile = { hit: SearchHit; name: string; sidecars: Sidecar[] };
/** A folder that holds hits. `folder` is "" for the top folder, else it ends with "/". */
export type HitFolder = { folder: string; name: string; files: HitFile[]; updated: string };
/**
 * One entry of a repository: a folder with its files, or a parent folder with the folders under
 * it that hold hits (`children`, two or more) and its own files.
 */
export type HitEntry = { key: string; repo: string; folder: string; files: HitFile[]; children: HitFolder[]; fileCount: number; updated: string };
export type HitRepo = { repo: string; entries: HitEntry[]; files: number };
export type HitGroups = { repos: HitRepo[]; files: number; folders: number };

/** The path of the file that `p` is a checksum or signature of, or "" when it is not one. */
function mainOf(p: string): string {
  let m = p;
  for (;;) {
    const end = sidecarEnds.find((e) => m.toLowerCase().endsWith(e));
    if (!end) return m === p ? "" : m;
    m = m.slice(0, -end.length);
  }
}

const folderOf = (p: string) => p.slice(0, p.lastIndexOf("/") + 1);
/** "a/b/c/" → "a/b/"; "a/" → "". */
const parentOf = (folder: string) => folderOf(folder.slice(0, -1));
const nameOf = (folder: string) => folder.slice(parentOf(folder).length, -1);

export function groupHits(hits: readonly SearchHit[]): HitGroups {
  const paths = new Set(hits.map((h) => h.repo + "\u0000" + h.path));
  // Each repository's folders in the order of their first hit.
  const byRepo = new Map<string, Map<string, HitFolder>>();
  const files = new Map<string, HitFile>();
  const later: { main: string; hit: SearchHit }[] = [];

  for (const h of hits) {
    const main = mainOf(h.path);
    if (main && paths.has(h.repo + "\u0000" + main)) {
      later.push({ main, hit: h });
      continue;
    }
    let folders = byRepo.get(h.repo);
    if (!folders) byRepo.set(h.repo, (folders = new Map()));
    const folder = folderOf(h.path);
    let f = folders.get(folder);
    if (!f) folders.set(folder, (f = { folder, name: folder ? nameOf(folder) : "", files: [], updated: h.updated_at }));
    const file: HitFile = { hit: h, name: h.path.slice(folder.length), sidecars: [] };
    f.files.push(file);
    if (h.updated_at > f.updated) f.updated = h.updated_at;
    files.set(h.repo + "\u0000" + h.path, file);
  }
  // The main file has no checksum ending, so it is a file of its own ("x.jar.asc.sha1" folds into "x.jar").
  for (const { main, hit } of later) files.get(hit.repo + "\u0000" + main)?.sidecars.push({ label: hit.path.slice(main.length + 1), hit });
  for (const f of files.values()) f.sidecars.sort((a, b) => a.label.localeCompare(b.label));

  const repos: HitRepo[] = [];
  let folderCount = 0;
  for (const [repo, folders] of byRepo) {
    folderCount += folders.size;
    // A parent takes in the folders under it (that take in none of their own) when it has two
    // of them, or one and files of its own: deepest first, so a nested artifact stays its own entry.
    const children = new Map<string, string[]>();
    for (const folder of folders.keys()) if (folder) children.set(parentOf(folder), [...(children.get(parentOf(folder)) ?? []), folder]);
    const depth = (f: string) => f.split("/").length;
    const groups = new Set<string>();
    for (const p of [...children.keys()].sort((a, b) => depth(b) - depth(a))) {
      const leaves = children.get(p)!.filter((c) => !groups.has(c)).length;
      if (leaves >= 2 || (leaves >= 1 && folders.has(p))) groups.add(p);
    }
    const entries = new Map<string, HitEntry>();
    const entryOf = (folder: string, updated: string) => {
      let e = entries.get(folder);
      if (!e) entries.set(folder, (e = { key: repo + "\u0000" + folder, repo, folder, files: [], children: [], fileCount: 0, updated }));
      return e;
    };
    for (const f of folders.values()) {
      const parent = f.folder ? parentOf(f.folder) : "";
      const own = !f.folder || groups.has(f.folder) || !groups.has(parent);
      const e = entryOf(own ? f.folder : parent, f.updated);
      if (e.folder === f.folder) e.files.push(...f.files);
      else e.children.push(f);
      e.fileCount += f.files.length;
      if (f.updated > e.updated) e.updated = f.updated;
    }
    const list = [...entries.values()];
    repos.push({ repo, entries: list, files: list.reduce((n, e) => n + e.fileCount, 0) });
  }
  return { repos, files: repos.reduce((n, r) => n + r.files, 0), folders: folderCount };
}
