// Download snippets for one file: a curl or wget download. The snippets of a Maven artifact and of a
// .deb come from the format plugin (GET /api/repos/{name}/snippets).

import type { Repo } from "$lib/api";
import type { Snippet } from "./browse";

const use = (client: string, title: string, code: string, note?: string): Snippet => ({
  client,
  purpose: "use",
  context: "artifact",
  title,
  lang: "sh",
  code,
  note,
});

/** A curl and a wget download of one file. */
export function downloadSnippets(repo: Repo, url: string): Snippet[] {
  const note = repo.anonymous_read ? undefined : "Create a read token on Settings › Tokens.";
  const curlAuth = repo.anonymous_read ? "" : "-u YOUR_USERNAME:YOUR_READ_TOKEN ";
  const wgetAuth = repo.anonymous_read ? "" : "--user YOUR_USERNAME --password YOUR_READ_TOKEN ";
  // The section heading says what they do, so the blocks need no title.
  return [use("curl", "", `curl -fLO ${curlAuth}${url}`, note), use("wget", "", `wget ${wgetAuth}${url}`, note)];
}
