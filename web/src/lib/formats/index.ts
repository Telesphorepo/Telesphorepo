// Format UI registry (spec §5.4). A format may add a custom repo-config editor here; every other
// part of the UI is generic and reads /api/formats, /status and /usage. Formats without an entry
// use the JSON editor.

import type { Component } from "svelte";
import type { RepoConfig } from "$lib/api";
import Debian from "./debian.svelte";

/** An editor edits one repo config object: onChange gets the new object, or null while it is invalid. */
export type EditorProps = { value: RepoConfig; onChange: (v: RepoConfig | null) => void };

export const editors: Record<string, Component<EditorProps>> = {
  debian: Debian,
};

/**
 * The title (the same as the plugin's, shown until /api/formats loads) and a one-line description of a built-in format. The icon of every format comes
 * from its plugin: render it with FormatIcon (or FormatBadge for the icon and the title).
 */
export type FormatLook = { title?: string; description?: string };

const looks: Record<string, FormatLook> = {
  maven: { title: "Maven / Gradle", description: "Java libraries and Gradle plugins, for Maven and Gradle builds." },
  debian: { title: "Debian (apt)", description: "Packages that apt installs on Debian and Ubuntu." },
  raw: { title: "Raw files", description: "Any files, downloaded by their path." },
};

/** The look of a format. A format without an entry has no title or description of its own. */
export function formatLook(format: string): FormatLook {
  return looks[format] ?? {};
}
