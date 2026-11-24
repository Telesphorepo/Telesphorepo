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

/** True once a source has answered its first page (rows, none, or an error). */
const answered = <T>(s: TableSource<T>) => !s.loading || s.items.length > 0 || !!s.error;

/**
 * A list searched by name: a source per query (a Keyset, or a chain), asked 250 ms after the last
 * key of `text`. The last answer stays on screen until the next query has its first page, so typing
 * never empties the list. `query` is the text the rows answer (for "No repository's name holds
 * “x”"); flush() asks at once (Enter). A table source itself: pass it to ItemList or SourceMore.
 *
 *   const found = new Searched((q) => new Keyset((p) => api.repos.list({ q }, p), 100));
 *   <input bind:value={found.text} />  <ItemList source={found} … />
 */
export class Searched<T> implements TableSource<T> {
  #make: (q: string) => TableSource<T>;
  #text = $state("");
  #cur = $state.raw<{ q: string; src: TableSource<T> }>(null!);
  #prev = $state.raw<{ q: string; src: TableSource<T> } | null>(null);
  #timer: ReturnType<typeof setTimeout> | undefined;

  constructor(make: (q: string) => TableSource<T>) {
    this.#make = make;
    this.#cur = { q: "", src: make("") };
  }

  /** What is typed; the search follows it 250 ms after the last change. */
  get text() {
    return this.#text;
  }
  set text(v: string) {
    this.#text = v;
    clearTimeout(this.#timer);
    this.#timer = setTimeout(() => this.#search(), 250);
  }

  /** Asks for the typed text now, without the wait. */
  flush = () => {
    clearTimeout(this.#timer);
    this.#search();
  };

  /** Empties the field and shows the whole list again at once. */
  clear = () => {
    this.#text = "";
    this.flush();
  };

  #search() {
    const q = this.#text.trim();
    if (q === this.#cur.q) return;
    if (answered(this.#cur.src)) this.#prev = this.#cur;
    this.#cur = { q, src: this.#make(q) };
  }

  get #shown() {
    return answered(this.#cur.src) || !this.#prev ? this.#cur : this.#prev;
  }
  /** The query that the rows on screen answer ("" for the whole list). */
  get query() {
    return this.#shown.q;
  }
  /** True while the typed text is not asked for yet or its first page is on its way. */
  get pending() {
    return this.#text.trim() !== this.#cur.q || !answered(this.#cur.src);
  }
  get items() {
    return this.#shown.src.items;
  }
  get loading() {
    return this.#shown.src.loading || !answered(this.#cur.src);
  }
  get error() {
    return this.#shown.src.error;
  }
  get hasMore() {
    return !!this.#shown.src.hasMore;
  }
  get loadingMore() {
    return !!this.#shown.src.loadingMore;
  }
  more = () => this.#shown.src.more?.();
  reload = () => this.#cur.src.reload?.();
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
