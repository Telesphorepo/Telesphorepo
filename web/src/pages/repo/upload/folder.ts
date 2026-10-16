// Reads the files of a dropped or chosen folder with their paths inside it. A folder upload
// sends each file with its path as the multipart filename (internal/api/files.go).

import { anyGlob } from "$lib/glob";

/** One file of a folder and its path, e.g. "build/libs/app-1.0.jar" (forward slashes). */
export type FolderFile = { file: File; path: string };

/**
 * The files read so far; `more` is true when the folder holds more than the limit. `skipped`
 * counts the files that `keep` left out; they are never sent.
 */
export type FolderRead = { files: FolderFile[]; more: boolean; skipped: number };

/** Chooses the files of a folder to send by their path; undefined sends every file. */
export type Keep = ((path: string) => boolean) | undefined;

/** The files of a folder that the globs take (the upload's keep of the format descriptor); no globs keeps every file. */
export function keepOf(globs: readonly string[]): Keep {
  return globs.length ? (p) => anyGlob(globs, p) : undefined;
}

const clean = (p: string) => p.replace(/\\/g, "/").replace(/^\/+/, "");

/** The files of an <input type="file" webkitdirectory> that keep takes, up to limit. */
export function filesFromInput(list: FileList, limit: number, keep: Keep = undefined): FolderRead {
  const all = [...list].map((file) => ({ file, path: clean(file.webkitRelativePath || file.name) }));
  const kept = keep ? all.filter((f) => keep(f.path)) : all;
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
    const kept = keep ? all.filter((f) => keep(f.path)) : all;
    return { files: kept.slice(0, limit), more: kept.length > limit, skipped: all.length - kept.length };
  }
  const files: FolderFile[] = [];
  let more = false;
  let skipped = 0;
  const walk = async (e: FileSystemEntry): Promise<void> => {
    if (more) return;
    if (e.isFile) {
      const path = clean(e.fullPath || e.name);
      if (keep && !keep(path)) {
        skipped++;
        return;
      }
      if (files.length >= limit) {
        more = true;
        return;
      }
      files.push({ file: await fileOf(e as FileSystemFileEntry), path });
    } else if (e.isDirectory) {
      for (const c of await children(e as FileSystemDirectoryEntry)) await walk(c);
    }
  };
  for (const e of drop.entries) await walk(e);
  files.sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0));
  return { files, more, skipped };
}
