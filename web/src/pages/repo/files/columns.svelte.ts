// One column of the Columns view, or one open folder of the Tree view: the children of one folder. A plain folder lists its tree
// in the server's order (folders, then files; version folders newest first), checksum and signature files left out. A version
// folder (its parent is the artifact, its name the version) lists the files of its latest build
// and how many earlier builds it has, so a snapshot with many builds stays one short column.

import { api, errorMessage, type Build, type TreeEntry, type Version } from "$lib/api";
import { Keyset } from "$lib/load.svelte";
import { baseName, parentOf } from "../browse";

/** `repo` is the repository that serves a file of a group. */
export type Entry = { name: string; path: string; type: "dir" | "file"; size?: number; repo?: string };

/** An earlier build of a version: its files are known with it, so it opens without a request. */
export type EarlierBuild = { key: string; id: string; label?: string; published_at: string | null; files: Entry[] };

export class Column {
  readonly folder: string;
  readonly #repo: string;
  #builds = $state<Keyset<Build> | null>(null);
  /** The version this folder is, once the builds request answered; null for a plain folder. */
  version = $state<Version | null>(null);
  #build = $state<Entry[] | null>(null);
  #tree = $state<Keyset<TreeEntry> | null>(null);
  #probing = $state(false);
  #error = $state("");

  constructor(repo: string, folder: string) {
    this.folder = folder;
    this.#repo = repo;
    const depth = folder.split("/").filter(Boolean).length;
    if (depth >= 3 && /^\d/.test(baseName(folder))) {
      this.#probing = true;
      api.repos
        .builds(repo, parentOf(folder), baseName(folder), { limit: 1 })
        .then((b) => {
          if (b.version && b.items[0]) {
            this.version = b.version;
            this.#build = b.items[0].files.map((f) => ({ name: f.name, path: f.path, type: "file" as const, size: f.size, repo: b.version?.repo }));
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

  /** Loads the earlier builds of this version (once); they show where the "N earlier builds" row is. */
  loadEarlier = () => {
    if (this.#builds || !this.version) return;
    const v = this.version;
    this.#builds = new Keyset((p) => api.repos.builds(this.#repo, parentOf(this.folder), v.version, p), 50);
  };
  get earlierLoading() {
    return this.#builds?.loading ?? false;
  }
  get earlierError() {
    return this.#builds?.error ?? "";
  }
  get earlierHasMore() {
    return this.#builds?.hasMore ?? false;
  }
  get earlierLoadingMore() {
    return this.#builds?.loadingMore ?? false;
  }
  moreEarlier = () => this.#builds?.more();
  /** The earlier builds loaded so far, newest first (the latest build's files are the entries). */
  get earlierBuilds(): EarlierBuild[] {
    const repo = this.version?.repo;
    return (this.#builds?.items ?? []).slice(1).map((b) => ({
      key: `build:${this.folder}:${b.id}`,
      id: b.id,
      label: b.label,
      published_at: b.published_at,
      files: b.files.map((f) => ({ name: f.name, path: f.path, type: "file" as const, size: f.size, repo })),
    }));
  }

  get entries(): Entry[] {
    if (this.#build) return this.#build;
    const items = this.#tree?.items ?? [];
    return items
      .filter((e) => !e.side_of)
      .map((e) => ({ name: e.name.replace(/\/$/, ""), path: e.path, type: e.type, size: e.type === "file" ? e.size : undefined, repo: e.repo }));
  }
}
