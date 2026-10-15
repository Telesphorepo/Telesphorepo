// Unsaved changes. A page holds its edits in a Draft and shows a <SaveBar>; the SaveBar registers
// the count here. While a count is above zero, leaving the view asks first: an in-app link, the
// back and forward buttons and navigate() open the guard dialog (layouts/UnsavedGuard.svelte), and
// closing or reloading the tab gets the browser's own prompt.

import { go, setGuard } from "$lib/router.svelte";

type Form = { count: () => number; discard: () => void };

class Unsaved {
  // Raw, not deep: a deep $state would store proxies, and register's removal compares identity.
  #forms = $state.raw<Form[]>([]);
  /** Unsaved changes on the current view, over every SaveBar. */
  count = $derived(this.#forms.reduce((n, f) => n + f.count(), 0));
  /** The address the user tried to open while there were unsaved changes. */
  pending = $state<{ target: string; fromHistory: boolean } | null>(null);

  /** Adds a form; returns the function that removes it. SaveBar calls this. */
  register(form: Form): () => void {
    this.#forms = [...this.#forms, form];
    return () => {
      this.#forms = this.#forms.filter((f) => f !== form);
    };
  }

  /** Discards every unsaved change and opens the pending address. */
  leave = () => {
    const p = this.pending;
    this.pending = null;
    for (const f of this.#forms) f.discard();
    if (p) go(p.target, { force: true, replace: p.fromHistory });
  };

  /** Keeps the user on the view. */
  stay = () => {
    this.pending = null;
  };

  install() {
    setGuard((target, fromHistory) => {
      if (this.count === 0) return true;
      this.pending = { target, fromHistory };
      return false;
    });
    // In-app links: ask before the address changes, so no history entry is added.
    document.addEventListener(
      "click",
      (e) => {
        if (this.count === 0 || e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
        const a = (e.target as Element | null)?.closest?.("a[href^='#/']") as HTMLAnchorElement | null;
        if (!a || (a.target && a.target !== "_self")) return;
        const target = a.getAttribute("href")!;
        if (target === location.hash) return;
        e.preventDefault();
        this.pending = { target, fromHistory: false };
      },
      true,
    );
    window.addEventListener("beforeunload", (e) => {
      if (this.count > 0) e.preventDefault();
    });
  }
}

export const unsaved = new Unsaved();

const copy = <T>(v: T): T => structuredClone($state.snapshot(v)) as T;
const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);

/**
 * The edits of a form: `value` is what the controls bind to, `saved` what the server holds.
 * `changed` lists the top-level keys that differ, so a SaveBar can count them.
 *
 *   const draft = new Draft({ name: repo.name, description: repo.description });
 *   <Input bind:value={draft.value.description} />
 *   <SaveBar count={draft.count} onDiscard={draft.reset} onSave={async () => draft.commit(await api.save(draft.value))} />
 */
export class Draft<T extends object> {
  saved = $state() as T;
  value = $state() as T;
  changed = $derived(Object.keys(this.value ?? {}).filter((k) => !same((this.value as Record<string, unknown>)[k], (this.saved as Record<string, unknown>)[k])));
  count = $derived(this.changed.length);

  constructor(initial: T) {
    this.saved = copy(initial);
    this.value = copy(initial);
  }

  /** Puts back what the server holds. */
  reset = () => {
    this.value = copy(this.saved);
  };

  /** Marks `next` (by default the current edits) as saved. */
  commit = (next?: T) => {
    const v = next ?? this.value;
    this.saved = copy(v);
    this.value = copy(v);
  };
}
