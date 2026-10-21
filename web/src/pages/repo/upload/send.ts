// The large files of an upload go through the resumable upload routes in chunks, so no request
// body passes the 100 MB limit of a proxy such as Cloudflare. The upload request then names them
// in its part "uploads"; the server finishes each one with its size.

import { api, ApiError, type FinishedUpload } from "$lib/api";
import type { FolderFile } from "./folder";

/** The most bytes of files that one upload request carries. */
export const requestLimit = 90 * 1000 * 1000;
/** The bytes of one chunk of a resumable upload. */
export const chunkBytes = 16 * 1024 * 1024;
/** How often a chunk is sent before the upload fails. */
const chunkTries = 4;

/** The files that go through resumable uploads: the largest first, until the rest fit in one request. */
export function resumableOf(files: FolderFile[]): Set<FolderFile> {
  let rest = files.reduce((n, f) => n + f.file.size, 0);
  const out = new Set<FolderFile>();
  for (const f of [...files].sort((a, b) => b.file.size - a.file.size)) {
    if (rest <= requestLimit) break;
    out.add(f);
    rest -= f.file.size;
  }
  return out;
}

const aborted = () => new DOMException("The upload was cancelled.", "AbortError");

/**
 * Sends f through a resumable upload of repo in chunks and answers it for the part "uploads".
 * onSent gets the bytes of f the server holds and those of the chunk in flight. A chunk that fails
 * is sent again from the offset that the server holds; a 416 names it. A failure or an abort
 * deletes the upload.
 */
export async function sendResumable(
  repo: string,
  f: FolderFile,
  token: string,
  signal: AbortSignal,
  onSent: (sent: number) => void,
): Promise<FinishedUpload> {
  if (signal.aborted) throw aborted();
  const { id } = await api.repos.startUpload(repo, token);
  try {
    let offset = 0;
    let tries = 0;
    while (offset < f.file.size) {
      if (signal.aborted) throw aborted();
      const chunk = f.file.slice(offset, Math.min(offset + chunkBytes, f.file.size));
      const from = offset;
      const r = api.repos.appendUpload(repo, id, chunk, from, token, (n) => onSent(from + n));
      const stop = () => r.abort();
      signal.addEventListener("abort", stop);
      try {
        offset = (await r.done).received;
        tries = 0;
      } catch (e) {
        if (signal.aborted || !(e instanceof ApiError)) throw e;
        const received = (e.problem as { received?: unknown } | undefined)?.received;
        if (e.status === 416 && typeof received === "number") offset = received;
        else if (e.status !== 0 && e.status < 500) throw e;
        if (++tries >= chunkTries) throw e;
      } finally {
        signal.removeEventListener("abort", stop);
      }
      onSent(offset);
    }
    return { path: f.path, id, size: f.file.size };
  } catch (e) {
    void api.repos.cancelUpload(repo, id, token).catch(() => {});
    throw e;
  }
}
