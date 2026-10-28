// A repository delete runs on the server after POST /api/repos/delete answers 202: the
// repository keeps its delete operation, which counts the files removed, until it is gone. A
// delete of files that takes more than 2 s answers 202 too, and goes on in the background
// (POST /api/repos/{name}/files/delete, then GET .../files/delete/{id}).

import { toast } from "svelte-sonner";
import { api, ApiError, errorMessage, type FilesDelete } from "$lib/api";
import { count } from "$lib/words";
import { formatCount, formatSpan } from "$lib/format";
import { pause } from "$lib/live.svelte";

const pollEvery = 1000;
/** The longest wait between two tries after failed requests. */
const maxBackoff = 30_000;

/**
 * Waits until every repository of names is gone, one after another, and shows the progress in one
 * loading toast. It asks every second, pauses while the tab is hidden or the browser is offline,
 * and after a failed request (the network, or the server answers 5xx) tries again, longer apart,
 * and says since when the toast is not updated. A delete that fails throws an Error with the
 * operation's message; the repository keeps its failed operation, which its page offers to resume
 * or stop.
 */
export async function waitForDeletes(names: readonly string[]): Promise<void> {
  const id = toast.loading(`Deleting ${count(names.length, "repository", "repositories")}…`);
  let updated = Date.now();
  let failures = 0;
  try {
    for (const [i, name] of names.entries()) {
      for (;;) {
        let op;
        try {
          op = (await api.repos.get(name)).operation;
          updated = Date.now();
          failures = 0;
        } catch (e) {
          if (e instanceof ApiError && e.status === 404) break;
          if (e instanceof ApiError && e.status < 500) throw e;
          failures++;
          const wait = Math.min(pollEvery * 2 ** failures, maxBackoff);
          toast.loading(`Deleting ${name}: not updated for ${formatSpan(Date.now() - updated)}, trying again in ${formatSpan(wait)}`, {
            id,
            description: errorMessage(e),
          });
          await pause(wait);
          continue;
        }
        if (op?.state === "failed") throw new Error(op.message);
        const of = names.length > 1 ? ` (${formatCount(i + 1)} of ${formatCount(names.length)})` : "";
        const files = op && op.total > 0 ? `: ${formatCount(op.done)} of ${count(op.total, "file", "files")}` : "";
        toast.loading(`Deleting ${name}${of}${files}`, { id, description: undefined });
        await pause(pollEvery);
      }
    }
  } finally {
    toast.dismiss(id);
  }
}

/**
 * Deletes the files and folders of target in repo, 1000 items a request (the most the server
 * takes), with the count of deleted files in one loading toast, and returns how many it deleted. A
 * request that answers 202 is asked about every second, as waitForDeletes does. When the server
 * restarted during a delete it throws, saying to delete again.
 */
export async function deleteFiles(repo: string, target: FilesDelete): Promise<number> {
  const paths = target.paths ?? [];
  const prefixes = target.prefixes ?? [];
  const id = toast.loading("Deleting…");
  const batch = 1000;
  let n = 0;
  try {
    for (let i = 0; i < paths.length + prefixes.length; i += batch) {
      const ps = paths.slice(i, i + batch);
      const xs = prefixes.slice(Math.max(0, i - paths.length), Math.max(0, i + batch - paths.length));
      const res = await api.repos.deleteFiles(repo, { paths: ps.length ? ps : undefined, prefixes: xs.length ? xs : undefined });
      if (!res.id) {
        n += res.deleted;
        continue;
      }
      let updated = Date.now();
      let failures = 0;
      for (;;) {
        let st;
        try {
          st = await api.repos.deleteStatus(repo, res.id);
          updated = Date.now();
          failures = 0;
        } catch (e) {
          if (e instanceof ApiError && e.status === 404)
            throw new Error(`The server restarted during the delete, after ${count(n, "file", "files")}. Delete again to remove the files that are left.`);
          if (e instanceof ApiError && e.status < 500) throw e;
          failures++;
          const wait = Math.min(pollEvery * 2 ** failures, maxBackoff);
          toast.loading(`Deleting: not updated for ${formatSpan(Date.now() - updated)}, trying again in ${formatSpan(wait)}`, { id, description: errorMessage(e) });
          await pause(wait);
          continue;
        }
        if (st.state === "failed") throw new Error(`The delete stopped after ${count(n + st.deleted, "file", "files")}: ${st.error}`);
        if (st.state === "done") {
          n += st.deleted;
          break;
        }
        toast.loading(`Deleting: ${count(n + st.deleted, "file", "files")} so far`, { id, description: undefined });
        await pause(pollEvery);
      }
    }
  } finally {
    toast.dismiss(id);
  }
  return n;
}
