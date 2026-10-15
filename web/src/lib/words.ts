// Counts and lists of names in the words that every page uses: the selection bar, the confirm
// dialogs and the toasts of bulk actions.

import { formatCount } from "$lib/format";

/** A count and its noun: count(1, "file", "files") is "1 file", count(12, ...) is "12 files". */
export const count = (n: number, one: string, many: string) => `${formatCount(n)} ${n === 1 ? one : many}`;

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
