// What a folder delete removes, read before the confirm dialog opens. Lists the assets under the
// folder page by page (one indexed seek each) and stops after `pages`, so a huge folder costs a
// few requests; the dialog then says "more than N files" and the server's answer gives the count.

import { api } from "$lib/api";

export type FolderCount = {
  count: number;
  /** More files exist than were counted. */
  more: boolean;
  /** A .deb is among the counted files. */
  debs: boolean;
  /** A pom lies directly in the folder: it is a Maven version folder. */
  versionFolder: boolean;
};

export async function countFolder(repo: string, prefix: string, pages = 5): Promise<FolderCount> {
  const out: FolderCount = { count: 0, more: false, debs: false, versionFolder: false };
  let after: string | undefined;
  for (let i = 0; i < pages; i++) {
    const p = await api.repos.assets(repo, prefix, { after, limit: 200 });
    for (const a of p.items) {
      out.count++;
      if (a.path.endsWith(".deb")) out.debs = true;
      const rest = a.path.slice(prefix.length);
      if (!rest.includes("/") && rest.endsWith(".pom")) out.versionFolder = true;
    }
    if (!p.next) return out;
    after = p.next;
  }
  out.more = true;
  return out;
}
