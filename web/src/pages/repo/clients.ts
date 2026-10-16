// Download snippets for one file of any format: a curl or a wget download. The snippets that use
// an artifact in a build come from the format (GET /api/repos/{name}/snippets).

import type { Repo, Snippet, ToolLabel } from "$lib/api";

const use = (tool: string, code: string, note?: string): Snippet => ({
  id: tool + ".download",
  tool,
  purpose: "use",
  context: "artifact",
  // The section heading says what they do, so the blocks need no title.
  title: "",
  lang: "sh",
  code,
  note,
});

/** The labels of the download tools. */
export const downloadTools: ToolLabel[] = [
  { id: "curl", label: "curl" },
  { id: "wget", label: "wget" },
];

/**
 * A curl and a wget download of one file. For a private repository, both ask for the read token
 * at a hidden prompt, so the token is not in the shell history or in an argument of a process.
 */
export function downloadSnippets(repo: Repo, url: string): Snippet[] {
  if (repo.anonymous_read) return [use("curl", `curl -fLO ${url}`), use("wget", `wget ${url}`)];
  const note = "Create a read token on Settings › Tokens. Enter it at the prompt.";
  const curl =
    "printf 'Read token: '; read -rs READ_TOKEN; echo\n" +
    `printf 'user = "YOUR_USERNAME:%s"\\n' "$READ_TOKEN" | curl -fLO -K - ${url}`;
  return [use("curl", curl, note), use("wget", `wget --user YOUR_USERNAME --ask-password ${url}`, note)];
}
