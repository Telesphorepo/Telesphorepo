// Path and name helpers that the Files views share. The requests and their JSON shapes are in
// $lib/api (api.repos.tree, versions, builds, file, snippets, usage).

import type { BuildRecord } from "$lib/api";

/** The link of b with rel, if the provider made one. */
export const buildLink = (b: BuildRecord, rel: string) => b.links.find((l) => l.rel === rel);

const sideExt = /\.(md5|sha1|sha256|sha512|asc|sig)$/;

/** A checksum or signature of another file: a.jar.sha1 → "a.jar"; null for any other name. */
export function sideOf(path: string): string | null {
  const m = path.match(sideExt);
  return m ? path.slice(0, -m[0].length) : null;
}

/** The label of a checksum or signature file next to its file: "SHA-1". */
export function sideLabel(path: string): string {
  const ext = path.slice(path.lastIndexOf(".") + 1);
  return { md5: "MD5", sha1: "SHA-1", sha256: "SHA-256", sha512: "SHA-512", asc: "Signature", sig: "Signature" }[ext] ?? ext;
}

/** The short form of a commit: 7 characters. */
export const shortSha = (sha: string) => sha.slice(0, 7);

/** The parent folder of a repo path ("" for the top). */
export function parentOf(path: string): string {
  const p = path.endsWith("/") ? path.slice(0, -1) : path;
  return p.slice(0, p.lastIndexOf("/") + 1);
}

/** The last segment of a repo path, without the folder slash. */
export const baseName = (path: string) => {
  const p = path.endsWith("/") ? path.slice(0, -1) : path;
  return p.slice(p.lastIndexOf("/") + 1);
};

/** A build's name for people: the format's label ("Build 3"), else its id. */
export const buildLabel = (id: string, label?: string) => label || id;

/** The build's name inside a sentence: the label with a lower-case first letter ("build 3"), or the id as it is. */
export const buildInline = (id: string, label?: string) => (label ? label[0].toLowerCase() + label.slice(1) : id);
