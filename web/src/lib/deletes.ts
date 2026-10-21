// A repository delete runs on the server after POST /api/repos/delete answers 202: the
// repository keeps its delete operation, which counts the files removed, until it is gone.

import { toast } from "svelte-sonner";
import { api, ApiError, errorMessage } from "$lib/api";
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
