// Client snippets for one file of a repo: the Maven and Gradle dependency of a Maven artifact,
// the apt setup for a .deb, and a curl download for everything else.

import type { Repo } from "$lib/api";

export type ClientSnippet = { title: string; code: string };

export type MavenCoords = {
  groupId: string;
  artifactId: string;
  version: string;
  classifier: string;
  ext: string;
};

// Checksums, signatures and Gradle module files are not artifacts a build depends on.
const notArtifact = /\.(md5|sha1|sha256|sha512|asc|module)$/;

/**
 * The coordinates of a Maven repo path such as com/acme/app/1.0.0/app-1.0.0-sources.jar; null
 * when the path is not an artifact of the standard layout (metadata, checksums, other files).
 */
export function mavenCoords(path: string): MavenCoords | null {
  const segs = path.split("/");
  if (segs.length < 4) return null;
  const file = segs[segs.length - 1];
  const version = segs[segs.length - 2];
  const artifactId = segs[segs.length - 3];
  const groupId = segs.slice(0, -3).join(".");
  if (notArtifact.test(file) || !file.startsWith(artifactId + "-")) return null;

  let rest = file.slice(artifactId.length + 1);
  if (rest.startsWith(version)) {
    rest = rest.slice(version.length);
  } else if (version.endsWith("-SNAPSHOT")) {
    // A timestamped snapshot: app-1.0-20260101.120000-3.jar in folder 1.0-SNAPSHOT.
    const m = rest.match(new RegExp("^" + escapeRE(version.slice(0, -"SNAPSHOT".length)) + "\\d{8}\\.\\d{6}-\\d+"));
    if (!m) return null;
    rest = rest.slice(m[0].length);
  } else {
    return null;
  }

  const dot = rest.lastIndexOf(".");
  if (dot < 0) return null;
  const ext = rest.slice(dot + 1);
  const head = rest.slice(0, dot);
  if (head !== "" && !head.startsWith("-")) return null;
  return { groupId, artifactId, version, classifier: head.slice(1), ext };
}

const escapeRE = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

/** The Maven <dependency> and the Gradle (Kotlin DSL) dependency of an artifact. */
export function mavenSnippets(c: MavenCoords): ClientSnippet[] {
  // A pom stands for its artifact; other types than jar need <type> and @ext.
  const type = c.ext === "jar" || c.ext === "pom" ? "" : c.ext;
  const xml = [
    "<dependency>",
    `  <groupId>${c.groupId}</groupId>`,
    `  <artifactId>${c.artifactId}</artifactId>`,
    `  <version>${c.version}</version>`,
    ...(c.classifier ? [`  <classifier>${c.classifier}</classifier>`] : []),
    ...(type ? [`  <type>${type}</type>`] : []),
    "</dependency>",
  ].join("\n");
  const gradle =
    `implementation("${c.groupId}:${c.artifactId}:${c.version}` +
    (c.classifier ? `:${c.classifier}` : "") +
    (type ? `@${type}` : "") +
    `")`;
  return [
    { title: "Maven (pom.xml)", code: xml },
    { title: "Gradle (build.gradle.kts)", code: gradle },
  ];
}

/** Package name, version and architecture of a .deb file name (name_version_arch.deb). */
export function debFile(path: string): { name: string; version: string; arch: string; component: string } | null {
  const segs = path.split("/");
  const file = segs[segs.length - 1];
  const m = file.match(/^([^_]+)_([^_]+)_([^_]+)\.deb$/);
  if (!m || segs[0] !== "pool" || segs.length < 3) return null;
  return { name: m[1], version: m[2], arch: m[3], component: segs[1] };
}

/** The apt setup and install commands for a .deb in a Debian repo. */
export function aptSnippets(repo: Repo, base: string, deb: NonNullable<ReturnType<typeof debFile>>, dist: string): ClientSnippet[] {
  const url = `${base}/debian/${repo.name}`;
  const keyring = "/etc/apt/keyrings/telesphorepo.asc";
  const setup = [
    "sudo install -d -m 0755 /etc/apt/keyrings",
    `sudo curl -fsSL ${base}/keys/debian.asc -o ${keyring}`,
    `echo "deb [signed-by=${keyring}] ${url} ${dist} ${deb.component}" \\`,
    `  | sudo tee /etc/apt/sources.list.d/${repo.name}.list`,
  ];
  if (!repo.anonymous_read) {
    setup.push(
      `echo "machine ${url} login USER password DOWNLOAD_TOKEN" \\`,
      `  | sudo tee /etc/apt/auth.conf.d/${repo.name}.conf`,
      `sudo chmod 600 /etc/apt/auth.conf.d/${repo.name}.conf`,
    );
  }
  return [
    { title: "Add the repository (once)", code: setup.join("\n") },
    { title: "Install the package", code: `sudo apt update\nsudo apt install ${deb.name}` },
  ];
}

/** A curl download of one file. */
export function curlSnippet(repo: Repo, downloadURL: string): ClientSnippet {
  const auth = repo.anonymous_read ? "" : "-u USER:DOWNLOAD_TOKEN ";
  return { title: "Download with curl", code: `curl -fL ${auth}-O ${downloadURL}` };
}
