// Request state for pages. Every request shows loading, error and data states, so pages hold
// one of these per request instead of their own flags.

import { untrack } from "svelte";
import { api, errorMessage, type Page, type PageParams } from "$lib/api";

/**
 * One request and its state. Create it in a component's script; it loads at once.
 *
 *   const repo = new Resource(() => api.repos.get(params.name));
 *   {#if repo.loading && !repo.data} <Loading /> {:else if repo.error} <ErrorState error={repo.error} onRetry={repo.reload} /> ...
 *
 * `loading` is true while any request runs; test `loading && !data` so a reload keeps the current
 * content on screen (principle 11).
 *
 * To reload when inputs change, call reload() in an $effect that reads them.
 */
export class Resource<T> {
  data = $state<T | undefined>(undefined);
  error = $state("");
  loading = $state(true);
  #seq = 0;
  #fn: () => Promise<T>;

  constructor(fn: () => Promise<T>, opts: { lazy?: boolean } = {}) {
    this.#fn = fn;
    if (!opts.lazy) this.reload();
    else this.loading = false;
  }

  reload = async () => {
    const seq = ++this.#seq;
    this.loading = true;
    this.error = "";
    try {
      const d = await this.#fn();
      if (seq === this.#seq) this.data = d;
    } catch (e) {
      if (seq === this.#seq) this.error = errorMessage(e);
    } finally {
      if (seq === this.#seq) this.loading = false;
    }
  };

  /** reload(), then throws the error of a failed request: the `run` of a Live (`lib/live.svelte.ts`). */
  fetch = async () => {
    await this.reload();
    if (this.error) throw new Error(this.error);
  };
}

/**
 * What <DataTable> reads: the rows and the request state. A Keyset is one; listOf() makes one
 * from a Resource of an array.
 */
export type TableSource<T> = {
  readonly items: readonly T[];
  readonly loading: boolean;
  readonly error: string;
  reload?: () => unknown;
  readonly hasMore?: boolean;
  readonly loadingMore?: boolean;
  more?: () => unknown;
};

/** A table source over a Resource that loads an array (a list that is not paged). */
export function listOf<T>(r: Resource<T[]>, filter?: (items: T[]) => T[]): TableSource<T> {
  return {
    get items() {
      const items = r.data ?? [];
      return filter ? filter(items) : items;
    },
    get loading() {
      return r.loading && !r.data;
    },
    get error() {
      return r.error;
    },
    reload: r.reload,
  };
}

/**
 * Two keyset lists as one source: every page of `a`, then the pages of `b` (the hosted
 * repositories of a format, then its proxies).
 */
export function chain<T>(a: Keyset<T>, b: Keyset<T>): TableSource<T> {
  return {
    get items() {
      return a.hasMore ? a.items : [...a.items, ...b.items];
    },
    get loading() {
      return a.loading || (!a.hasMore && b.loading && b.items.length === 0 && a.items.length === 0);
    },
    get error() {
      return a.error || (a.hasMore ? "" : b.error);
    },
    reload: () => Promise.all([a.reload(), b.reload()]),
    get hasMore() {
      return a.hasMore || b.hasMore;
    },
    get loadingMore() {
      return a.loadingMore || b.loadingMore;
    },
    more: () => (a.hasMore ? a.more() : b.more()),
  };
}

/**
 * The format of each repository a page names (activity events, the repositories of signing keys
 * and CI pipelines), read for the names it does not know yet (GET /api/repos?name=), a page at a
 * time. Call want() with the names in an $effect; format() is "" until a name is known.
 */
export class RepoFormats {
  #known = $state<Record<string, string>>({});
  #asked = new Set<string>();

  want(names: readonly string[]) {
    const ask = [...new Set(names)].filter((n) => n && !this.#asked.has(n));
    for (const n of ask) this.#asked.add(n);
    for (let i = 0; i < ask.length; i += 200) {
      const part = ask.slice(i, i + 200);
      api.repos.named(part).then(
        (rs) => (this.#known = { ...this.#known, ...Object.fromEntries(rs.map((r) => [r.name, r.format])) }),
        () => part.forEach((n) => this.#asked.delete(n)),
      );
    }
  }

  /** Remembers formats the page has read already. */
  add(repos: readonly { name: string; format: string }[]) {
    for (const r of repos) this.#asked.add(r.name);
    const add = repos.filter((r) => untrack(() => this.#known[r.name]) !== r.format);
    if (add.length) this.#known = { ...this.#known, ...Object.fromEntries(add.map((r) => [r.name, r.format])) };
  }

  format(name: string): string {
    return this.#known[name] ?? "";
  }
}

/**
 * A keyset-paged list. The first page loads at once; more() appends the next page. Pass it to
 * <DataTable>, which shows "Load more".
 *
 *   const devices = new Keyset((p) => api.devices.list(p));
 *
 * `first`, when given, is the first page already asked for (by a parent that shares it); a
 * reload asks again.
 */
export class Keyset<T> {
  items = $state<T[]>([]);
  next = $state<string | null>(null);
  error = $state("");
  /** True while the first page loads. */
  loading = $state(true);
  /** True while a following page loads. */
  loadingMore = $state(false);
  #seq = 0;
  #fn: (p: PageParams) => Promise<Page<T>>;
  #limit: number;

  constructor(fn: (p: PageParams) => Promise<Page<T>>, limit = 50, first?: Promise<Page<T>>) {
    this.#fn = fn;
    this.#limit = limit;
    this.#load(first ?? fn({ limit }));
  }

  get hasMore() {
    return this.next !== null;
  }

  /** Loads the first page again (after a change, or to retry). */
  reload = () => this.#load(this.#fn({ limit: this.#limit }));

  /** reload(), then throws the error of a failed request: the `run` of a Live (`lib/live.svelte.ts`). */
  fetch = async () => {
    await this.reload();
    if (this.error) throw new Error(this.error);
  };

  async #load(req: Promise<Page<T>>) {
    const seq = ++this.#seq;
    this.loading = true;
    this.error = "";
    try {
      const p = await req;
      if (seq !== this.#seq) return;
      this.items = p.items;
      this.next = p.next;
    } catch (e) {
      if (seq === this.#seq) this.error = errorMessage(e);
    } finally {
      if (seq === this.#seq) this.loading = false;
    }
  }

  more = async () => {
    if (this.next === null || this.loadingMore) return;
    const seq = this.#seq;
    this.loadingMore = true;
    this.error = "";
    try {
      const p = await this.#fn({ after: this.next, limit: this.#limit });
      if (seq !== this.#seq) return;
      this.items = [...this.items, ...p.items];
      this.next = p.next;
    } catch (e) {
      if (seq === this.#seq) this.error = errorMessage(e);
    } finally {
      this.loadingMore = false;
    }
  };
}
