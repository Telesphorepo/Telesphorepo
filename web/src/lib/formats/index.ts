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
