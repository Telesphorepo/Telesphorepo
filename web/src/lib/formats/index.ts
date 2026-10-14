// Format UI registry (spec §5.4). A format may add a custom repo-config editor here; every other
// part of the UI is generic and reads /api/formats, /status and /usage. Formats without an entry
// use the JSON editor.

import type { Component } from "svelte";
import CoffeeIcon from "@lucide/svelte/icons/coffee";
import PackageIcon from "@lucide/svelte/icons/package";
import FilesIcon from "@lucide/svelte/icons/files";
import BoxIcon from "@lucide/svelte/icons/box";
import type { RepoConfig } from "$lib/api";
import Debian from "./debian.svelte";

/** An editor edits one repo config object: onChange gets the new object, or null while it is invalid. */
export type EditorProps = { value: RepoConfig; onChange: (v: RepoConfig | null) => void };

export const editors: Record<string, Component<EditorProps>> = {
  debian: Debian,
};

/** How a format looks everywhere in the UI: an icon and a hue for its tint. */
export type FormatLook = { icon: Component<{ class?: string }>; hue: number };

const looks: Record<string, FormatLook> = {
  maven: { icon: CoffeeIcon, hue: 25 },
  debian: { icon: PackageIcon, hue: 340 },
  raw: { icon: FilesIcon, hue: 220 },
};

/** The look of a format. A format without an entry gets a box icon and a hue from its name. */
export function formatLook(format: string): FormatLook {
  return looks[format] ?? { icon: BoxIcon, hue: [...format].reduce((h, c) => (h * 31 + c.charCodeAt(0)) % 360, 7) };
}