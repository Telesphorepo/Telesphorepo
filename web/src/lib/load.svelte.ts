// Request state for pages. Every request shows loading, error and data states, so pages hold
// one of these per request instead of their own flags.

import { errorMessage, type Page, type PageParams } from "$lib/api";

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
