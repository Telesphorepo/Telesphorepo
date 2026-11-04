// Copy link of a file: a public file's own URL; a private file's short-lived signed link
// (POST /api/repos/{name}/links), which downloads without a sign-in until it expires.

import { toast } from "svelte-sonner";
import { api, errorMessage, type Repo } from "$lib/api";
import { formatDateTime } from "$lib/format";

export async function copyFileLink(repo: Repo, path: string, base: string) {
  if (repo.anonymous_read) {
    const url = base + api.repos.fileURL(repo.format, repo.name, path);
    try {
      await navigator.clipboard.writeText(url);
      toast.success("Link copied.");
    } catch {
      toast.error("Copy failed.");
    }
    return;
  }
  // The clipboard write starts in the click (Safari refuses one after an await), with the link
  // the server signs as its content.
  const link = api.repos.link(repo.name, path);
  try {
    if (typeof ClipboardItem !== "undefined") {
      await navigator.clipboard.write([new ClipboardItem({ "text/plain": link.then((l) => new Blob([l.url], { type: "text/plain" })) })]);
    } else {
      await navigator.clipboard.writeText((await link).url);
    }
    const l = await link;
    toast.success("Link copied.", { description: `Anyone with it can download this file until ${formatDateTime(l.expires_at)}.` });
  } catch (e) {
    toast.error(errorMessage(await link.then(() => e, (err) => err)));
  }
}
