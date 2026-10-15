// Download snippets for one file whose format has no artifact snippets: the apt setup and install
// of a .deb, and a curl or wget download of any file. A Maven artifact's snippets come from the
// format plugin (GET /api/repos/{name}/snippets).

import type { Repo } from "$lib/api";
import type { Snippet } from "./browse";

/** Package name, version and architecture of a .deb in the pool (pool/<component>/.../name_version_arch.deb). */
export function debFile(path: string): { name: string; version: string; arch: string; component: string } | null {
  const segs = path.split("/");
  const file = segs[segs.length - 1];
  const m = file.match(/^([^_]+)_([^_]+)_([^_]+)\.deb$/);
  if (!m || segs[0] !== "pool" || segs.length < 3) return null;
  return { name: m[1], version: m[2], arch: m[3], component: segs[1] };
}

const use = (client: string, title: string, code: string, note?: string): Snippet => ({
  client,
  purpose: "use",
  context: "artifact",
  title,
  lang: "sh",
  code,
  note,
});

/** The apt setup and install commands for a .deb in a Debian repo. */
export function aptSnippets(repo: Repo, base: string, deb: NonNullable<ReturnType<typeof debFile>>, dist: string): Snippet[] {
  const url = `${base}/debian/${repo.name}`;
  const keyring = `/etc/apt/keyrings/${repo.name}.asc`;
  const setup = [
    "sudo install -d -m 0755 /etc/apt/keyrings",
    `sudo curl -fsSL ${repo.anonymous_read ? "" : "-u YOUR_USERNAME:YOUR_READ_TOKEN "}${url}/key.asc -o ${keyring}`,
    `echo "deb [signed-by=${keyring}] ${url} ${dist} ${deb.component}" \\`,
    `  | sudo tee /etc/apt/sources.list.d/${repo.name}.list`,
  ];
  if (!repo.anonymous_read) {
    setup.push(
      `echo "machine ${url} login YOUR_USERNAME password YOUR_READ_TOKEN" \\`,
      `  | sudo tee /etc/apt/auth.conf.d/${repo.name}.conf`,
      `sudo chmod 600 /etc/apt/auth.conf.d/${repo.name}.conf`,
    );
  }
  return [
    use("apt", "Add the repository (once)", setup.join("\n")),
    use("apt", "Install the package", `sudo apt update\nsudo apt install ${deb.name}`),
  ];
}

/** A curl and a wget download of one file. */
export function downloadSnippets(repo: Repo, url: string): Snippet[] {
  const note = repo.anonymous_read ? undefined : "Create a read token on Settings › Tokens.";
  const curlAuth = repo.anonymous_read ? "" : "-u YOUR_USERNAME:YOUR_READ_TOKEN ";
  const wgetAuth = repo.anonymous_read ? "" : "--user YOUR_USERNAME --password YOUR_READ_TOKEN ";
  // The section heading says what they do, so the blocks need no title.
  return [use("curl", "", `curl -fLO ${curlAuth}${url}`, note), use("wget", "", `wget ${wgetAuth}${url}`, note)];
}
