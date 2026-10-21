// The Finder-style views of Browse: List (the default), Tree (folders that
// expand in place), Columns (Miller columns, for deep trees) and Cover Flow (the versions and builds
// of one artifact, where time is the axis). Every view fills the same area, so a switch never moves
// the layout around it. The chosen view is remembered per user, in this browser, for each place that offers a
// choice ("files", "versions").

import { auth } from "$lib/auth.svelte";
import ListIcon from "@lucide/svelte/icons/list";
import ListTreeIcon from "@lucide/svelte/icons/list-tree";
import ColumnsIcon from "@lucide/svelte/icons/columns-3";
import CoverFlowIcon from "@lucide/svelte/icons/gallery-horizontal-end";
import type { Component } from "svelte";

export type ViewKind = "list" | "tree" | "columns" | "coverflow";

export const viewLook: Record<ViewKind, { label: string; icon: Component<{ class?: string }> }> = {
  list: { label: "List", icon: ListIcon },
  tree: { label: "Tree", icon: ListTreeIcon },
  columns: { label: "Columns", icon: ColumnsIcon },
  coverflow: { label: "Cover Flow", icon: CoverFlowIcon },
};

const storageKey = (scope: string) => `telesphorepo.view.${auth.user?.username ?? "-"}.${scope}`;

function load(scope: string): string | null {
  try {
    return localStorage.getItem(storageKey(scope));
  } catch {
    return null;
  }
}

/**
 * The view chosen for one place. Bind a ViewSwitcher to `value`; a change is stored for the
 * signed-in user (a signed-out visitor shares one choice per browser).
 *
 *   const view = new ViewChoice("files", ["list", "columns"]);
 *   <ViewSwitcher bind:value={view.value} views={view.views} />
 */
export class ViewChoice {
  readonly views: ViewKind[];
  #scope: string;
  #fallback: ViewKind;
  #value = $state<ViewKind | null>(null);

  constructor(scope: string, views: ViewKind[], fallback: ViewKind = views[0]) {
    this.#scope = scope;
    this.views = views;
    this.#fallback = fallback;
  }

  get value(): ViewKind {
    if (this.#value) return this.#value;
    const saved = load(this.#scope) as ViewKind | null;
    return saved && this.views.includes(saved) ? saved : this.#fallback;
  }

  set value(v: ViewKind) {
    this.#value = v;
    try {
      localStorage.setItem(storageKey(this.#scope), v);
    } catch {
      /* storage blocked: the choice lasts for this page */
    }
  }
}
