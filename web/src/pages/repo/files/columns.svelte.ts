// One column of the Columns view, or one open folder of the Tree view: the children of one folder. A plain folder lists its tree
// (folders first, checksum and signature files left out, version folders newest first). A version
// folder lists the files of its latest build and how many earlier builds it has, so a snapshot
// with many builds stays one short column.

import { api, errorMessage, type TreeEntry } from "$lib/api";
import { Keyset } from "$lib/load.svelte";
import { baseName, browse, compareVersions, sideOf, type Version } from "../browse";

export type Entry = { name: string; path: string; type: "dir" | "file"; size?: number };

export class Column {
  readonly folder: string;
  /** The version this folder is, once the builds request answered; null for a plain folder. */
  version = $state<Version | null>(null);
  #build = $state<Entry[] | null>(null);
  #tree = $state<Keyset<TreeEntry> | null>(null);
  #probing = $state(false);
  #error = $state("");

  constructor(repo: string, folder: string) {
    this.folder = folder;
    const depth = folder.split("/").filter(Boolean).length;
    if (depth >= 3 && /^\d/.test(baseName(folder))) {
      this.#probing = true;
      browse
        .builds(repo, folder, { limit: 1 })
        .then((b) => {
          if (b.version && b.items[0]) {
            this.version = b.version;
            this.#build = b.items[0].files.map((f) => ({ name: f.name, path: f.path, type: "file" as const, size: f.size }));
          } else {
            this.#tree = new Keyset((p) => api.repos.tree(repo, folder, p), 100);
          }
        })
        .catch((e) => (this.#error = errorMessage(e)))
        .finally(() => (this.#probing = false));
    } else {
      this.#tree = new Keyset((p) => api.repos.tree(repo, folder, p), 100);
    }
  }

  get loading() {
    return this.#probing || (this.#tree?.loading ?? false);
  }
  get error() {
    return this.#error || this.#tree?.error || "";
  }
  get hasMore() {
    return this.#tree?.hasMore ?? false;
  }
  get loadingMore() {
    return this.#tree?.loadingMore ?? false;
  }
  more = () => this.#tree?.more();
  reload = () => this.#tree?.reload();

  /** The builds of this version that are not shown (snapshots only). */
  get earlier() {
    return this.version && this.version.builds_count > 1 ? this.version.builds_count - 1 : 0;
  }

  get entries(): Entry[] {
    if (this.#build) return this.#build;
    const items = this.#tree?.items ?? [];
    const paths = new Set(items.map((e) => e.path));
    const shown = items
      .filter((e) => {
        const of = e.type === "file" ? sideOf(e.path) : null;
        return !of || !paths.has(of);
      })
      .map((e) => ({ name: e.name.replace(/\/$/, ""), path: e.path, type: e.type, size: e.type === "file" ? e.size : undefined }));
    const dirs = shown.filter((e) => e.type === "dir");
    const files = shown.filter((e) => e.type === "file");
    // The versions of an artifact: newest first.
    if (dirs.length && dirs.every((d) => /^\d/.test(d.name))) dirs.sort((a, b) => compareVersions(b.name, a.name));
    return [...dirs, ...files];
  }
}
