// Counts and lists of names in the words that every page uses: the selection bar, the confirm
// dialogs and the toasts of bulk actions, and the kind of a repository.

import { formatCount } from "$lib/format";

/** A count and its noun: count(1, "file", "files") is "1 file", count(12, ...) is "12 files". */
export const count = (n: number, one: string, many: string) => `${formatCount(n)} ${n === 1 ? one : many}`;

/**
 * The kind of a repository in plain words: "Hosted", "Proxy of a remote repository", "Group of 3
 * repositories". The remote of a proxy shows only to the holders of Edit settings (its Settings).
 */
export function kindPhrase(r: { kind: string; member_count?: number }): string {
  if (r.kind === "group") return `Group of ${count(r.member_count ?? 0, "repository", "repositories")}`;
  if (r.kind === "proxy") return "Proxy of a remote repository";
  return "Hosted";
}

/**
 * A server version as people read it. A release is its tag ("v1.4.0"); a build of main
 * ("main-bebe7ef", the image CI publishes on each push) is Canary with its commit; a local build
 * ("dev") is Development.
 */
export function versionName(v: string): { name: string; commit?: string } {
  const main = /^main-([0-9a-f]{7,40})$/.exec(v);
  if (main) return { name: "Canary", commit: main[1] };
  if (v === "dev") return { name: "Development" };
  return { name: v };
}

/** The text of the selection bar: "1 selected", "12 selected". */
export const selectedText = (n: number) => `${formatCount(n)} selected`;

/**
 * Names for a confirm dialog: all of them up to `max`, else the first `max` and how many more.
 * "oasis, plex and scissors"; "oasis, plex, scissors, enginehub, worldedit and 7 more".
 */
export function someNames(names: readonly string[], max = 5): string {
  if (names.length > max) return `${names.slice(0, max).join(", ")} and ${formatCount(names.length - max)} more`;
  if (names.length <= 1) return names.join("");
  return `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
}
