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

/**
 * A curl and a wget download of one file. For a private repository, both ask for the read token
 * at a hidden prompt, so the token is not in the shell history or in an argument of a process.
 */
export function downloadSnippets(repo: Repo, url: string): Snippet[] {
  // The section heading says what they do, so the blocks need no title.
  if (repo.anonymous_read) return [use("curl", "", `curl -fLO ${url}`), use("wget", "", `wget ${url}`)];
  const note = "Create a read token on Settings › Tokens. Enter it at the prompt.";
  const curl =
    "printf 'Read token: '; read -rs READ_TOKEN; echo\n" +
    `printf 'user = "YOUR_USERNAME:%s"\\n' "$READ_TOKEN" | curl -fLO -K - ${url}`;
  return [use("curl", "", curl, note), use("wget", "", `wget --user YOUR_USERNAME --ask-password ${url}`, note)];
}
