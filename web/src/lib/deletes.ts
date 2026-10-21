// A repository delete runs on the server after POST /api/repos/delete answers 202: the
// repository keeps its delete operation, which counts the files removed, until it is gone.

import { toast } from "svelte-sonner";
import { api, ApiError } from "$lib/api";
import { count } from "$lib/words";
import { formatCount } from "$lib/format";

const pollEvery = 1000;

/**
 * Waits until every repository of names is gone, one after another, and shows the progress in one
 * loading toast. A delete that fails throws an Error with the operation's message; the repository
 * keeps its failed operation, which its page offers to resume or stop.
 */
export async function waitForDeletes(names: readonly string[]): Promise<void> {
  const id = toast.loading(`Deleting ${count(names.length, "repository", "repositories")}…`);
  try {
    for (const [i, name] of names.entries()) {
      for (;;) {
        let op;
        try {
          op = (await api.repos.get(name)).operation;
        } catch (e) {
          if (e instanceof ApiError && e.status === 404) break;
          throw e;
        }
        if (op?.state === "failed") throw new Error(op.message);
        const of = names.length > 1 ? ` (${formatCount(i + 1)} of ${formatCount(names.length)})` : "";
        const files = op && op.total > 0 ? `: ${formatCount(op.done)} of ${count(op.total, "file", "files")}` : "";
        toast.loading(`Deleting ${name}${of}${files}`, { id });
        await new Promise((r) => setTimeout(r, pollEvery));
      }
    }
  } finally {
    toast.dismiss(id);
  }
}
