// Reads the files of a dropped or chosen folder with their paths inside it. A folder upload
// sends each file with its path as the multipart filename (internal/api/files.go).

import { anyGlob, globMatch } from "$lib/glob";

/** One file of a folder and its path, e.g. "build/libs/app-1.0.jar" (forward slashes). */
export type FolderFile = { file: File; path: string };

/**
 * The files read so far; `more` is true when the folder holds more than the limit. `skipped`
 * counts the files that `keep` left out; they are never sent.
 */
export type FolderRead = { files: FolderFile[]; more: boolean; skipped: number };

/** Chooses the files of a folder to send by their path; undefined sends every file. */
export type Keep = { takes: (path: string) => boolean; under: (dir: string) => boolean } | undefined;

/**
 * The files of a folder that the globs take (the upload's keep of the format descriptor); no globs keeps every file.
 * `under(dir)` is false only when no path below the folder "dir" can match a glob.
 */
export function keepOf(globs: readonly string[]): Keep {
  if (!globs.length) return undefined;
  return { takes: (p) => anyGlob(globs, p), under: (dir) => globs.some((g) => globUnder(g, dir)) };
}

/**
 * Whether a path below the folder can match the glob. A segment with "**" can match any number of
 * folders, so it ends the comparison with true; the last segment of a glob is a file name.
 */
function globUnder(glob: string, dir: string): boolean {
  const gs = glob.split("/");
  const ds = dir.split("/");
  for (let i = 0; i < ds.length; i++) {
    if (i >= gs.length - 1) return false;
    if (gs[i].includes("**")) return true;
    if (!globMatch(gs[i], ds[i])) return false;
  }
  return true;
}

const clean = (p: string) => p.replace(/\\/g, "/").replace(/^\/+/, "");

/** Folders a walk never enters: dot-folders (.git) and node_modules. */
const skippedFolder = (name: string) => name.startsWith(".") || name === "node_modules";

/** How many folders are read at once. */
const READS = 8;

/** The files of an <input type="file" webkitdirectory> that keep takes, up to limit. */
export function filesFromInput(list: FileList, limit: number, keep: Keep = undefined): FolderRead {
  const all = [...list].map((file) => ({ file, path: clean(file.webkitRelativePath || file.name) }));
  const kept = all.filter((f) => {
    const parts = f.path.split("/");
    // parts[0] is the chosen folder itself, the last part the file.
    if (parts.slice(1, -1).some(skippedFolder)) return false;
    return !keep || keep.takes(f.path);
  });
  return { files: kept.slice(0, limit), more: kept.length > limit, skipped: all.length - kept.length };
}

/** The entries and files of one drop. The entries stay readable after the drop handler. */
export type FolderDrop = { entries: FileSystemEntry[]; files: File[] };

/**
 * The entries of a drop. Call it inside the drop handler: the browser empties the DataTransfer
 * once the handler returns.
 */
export function dropEntries(dt: DataTransfer): FolderDrop {
  const entries: FileSystemEntry[] = [];
  for (let i = 0; i < dt.items.length; i++) {
    const item = dt.items[i];
    if (item.kind !== "file") continue;
    const e = item.webkitGetAsEntry?.();
    if (e) entries.push(e);
  }
  return { entries, files: [...dt.files] };
}

/** Whether a drop holds a folder. Call it inside the drop handler, like dropEntries. */
export function dropHasFolder(dt: DataTransfer): boolean {
  for (let i = 0; i < dt.items.length; i++) {
    const item = dt.items[i];
    if (item.kind === "file" && item.webkitGetAsEntry?.()?.isDirectory) return true;
  }
  return false;
}

const fileOf = (e: FileSystemFileEntry) => new Promise<File>((resolve, reject) => e.file(resolve, reject));

/** All children of a folder: readEntries gives them in batches until an empty one. */
async function children(dir: FileSystemDirectoryEntry): Promise<FileSystemEntry[]> {
  const reader = dir.createReader();
  const out: FileSystemEntry[] = [];
  for (;;) {
    const batch = await new Promise<FileSystemEntry[]>((resolve, reject) => reader.readEntries(resolve, reject));
    if (batch.length === 0) return out;
    out.push(...batch);
  }
}

/**
 * The files under dropped entries (folders are walked) that keep takes, up to limit. Without
 * entry support it falls back to the plain dropped files.
 */
export async function filesFromEntries(
  drop: FolderDrop,
  limit: number,
  keep: Keep = undefined,
): Promise<FolderRead> {
  if (drop.entries.length === 0) {
    const all = drop.files.map((file) => ({ file, path: clean(file.name) }));
    const kept = keep ? all.filter((f) => keep.takes(f.path)) : all;
    return { files: kept.slice(0, limit), more: kept.length > limit, skipped: all.length - kept.length };
  }
  const found: { entry: FileSystemFileEntry; path: string }[] = [];
  let more = false;
  let skipped = 0;
  const waiting: (() => void)[] = [];
  let reading = 0;
  // Reads at most READS folders at once.
  const read = async (dir: FileSystemDirectoryEntry) => {
    while (reading >= READS) await new Promise<void>((resolve) => waiting.push(resolve));
    reading++;
    try {
      return await children(dir);
    } finally {
      reading--;
      waiting.shift()?.();
    }
  };
  // Walks the siblings side by side; each file and folder is looked at once.
  const visit = async (e: FileSystemEntry, root: boolean): Promise<void> => {
    if (more) return;
    const path = clean(e.fullPath || e.name);
    if (e.isFile) {
      if (keep && !keep.takes(path)) {
        skipped++;
        return;
      }
      if (found.length >= limit) {
        more = true;
        return;
      }
      found.push({ entry: e as FileSystemFileEntry, path });
    } else if (e.isDirectory) {
      if (!root && skippedFolder(e.name)) return;
      if (keep && !keep.under(path)) return;
      await Promise.all((await read(e as FileSystemDirectoryEntry)).map((c) => visit(c, false)));
    }
  };
  await Promise.all(drop.entries.map((e) => visit(e, true)));
  found.sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0));
  const files = await Promise.all(found.map(async ({ entry, path }) => ({ file: await fileOf(entry), path })));
  return { files, more, skipped };
}
