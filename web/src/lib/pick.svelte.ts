// The bulk pick of a collection (DataTable, ItemList): the picked keys, the header checkbox, the
// Shift+click range, Esc to clear, and the click on a row that opens it or toggles its checkbox.
// The component owns `picked` (a bindable prop) and hands it in through `get` and `set`.

import { count } from "$lib/words";
import { go } from "$lib/router.svelte";

/** The controls inside a row that keep their own click. */
const controls =
  "a, button, input, select, textarea, label, [role=checkbox], [role=switch], [role=menuitem], [role=combobox], [role=radio]";

/** True when a click on a row is meant for the row: a plain left click outside the row's controls, with no text selected. */
export function rowClick(e: MouseEvent): boolean {
  if (e.defaultPrevented || e.button !== 0) return false;
  if ((e.target as HTMLElement | null)?.closest(controls)) return false;
  return !window.getSelection()?.toString();
}

/** Opens a row's address from a click on the row: Ctrl or ⌘ opens it in a new tab. */
export function openRow(e: MouseEvent, to: string | undefined) {
  if (!to || !rowClick(e)) return;
  if (e.metaKey || e.ctrlKey) window.open(to, "_blank");
  else if (to.startsWith("#/")) go(to);
  else window.location.href = to;
}

export type PickOptions<T, K> = {
  /** The collection has bulk actions (a `bulk` snippet). */
  on: () => boolean;
  items: () => readonly T[];
  key: (item: T) => K;
  pickable?: (item: T) => boolean;
  /** True when more rows can load (a keyset), so "all" means the loaded rows. */
  more: () => boolean;
  get: () => Set<K>;
  set: (v: Set<K>) => void;
};

/**
 * The pick state of one collection, made in the component's script:
 *
 *   const pick = new Pick({ on: () => bulk !== undefined, items: () => source.items, key: (i) => key(i),
 *     pickable: (i) => pickable?.(i) ?? true, more: () => !!source.more, get: () => picked, set: (v) => (picked = v) });
 *   <svelte:window onpointerdown={pick.onpointerdown} onmousedown={pick.onmousedown} onkeydown={pick.onkeydown} />
 */
export class Pick<T, K> {
  #o: PickOptions<T, K>;
  // The last checkbox clicked, and whether Shift was down when the click started.
  #last: K | null = null;
  #shift = false;

  constructor(o: PickOptions<T, K>) {
    this.#o = o;
  }

  /** The collection has bulk actions: every row gets a checkbox. */
  readonly on = $derived.by(() => this.#o.on());
  readonly rows = $derived.by(() => (this.on ? this.#o.items().filter((r) => this.can(r)) : []));
  readonly all = $derived.by(() => this.rows.length > 0 && this.rows.every((r) => this.#o.get().has(this.#o.key(r))));
  /** Rows are picked: the selection bar shows. */
  readonly bar = $derived.by(() => this.on && this.#o.get().size > 0);
  readonly size = $derived.by(() => this.#o.get().size);
  /** The head checkbox shows: more than one row to pick, or more rows to load. */
  readonly head = $derived.by(() => this.rows.length > 1 || (this.rows.length > 0 && this.#o.more()));
  /** The name of the header checkbox. */
  readonly allLabel = $derived.by(() =>
    this.#o.more()
      ? `Select the ${count(this.rows.length, "row", "rows")} shown`
      : `Select all ${count(this.rows.length, "row", "rows")}`,
  );

  can = (item: T) => this.#o.pickable?.(item) ?? true;
  has = (item: T) => this.#o.get().has(this.#o.key(item));

  pickAll = () => this.#o.set(new Set(this.rows.map(this.#o.key)));

  clear = () => {
    this.#o.set(new Set());
    this.#last = null;
  };

  /** Picks or clears one row; with Shift down, the range from the last checkbox clicked. */
  toggle = (item: T, on: boolean) => {
    const key = this.#o.key;
    const k = key(item);
    const rows = this.rows;
    const next = new Set(this.#o.get());
    const from = this.#shift && this.#last !== null ? rows.findIndex((r) => key(r) === this.#last) : -1;
    const to = rows.findIndex((r) => key(r) === k);
    const range = from >= 0 && to >= 0 ? rows.slice(Math.min(from, to), Math.max(from, to) + 1) : [item];
    for (const r of range) {
      if (on) next.add(key(r));
      else next.delete(key(r));
    }
    this.#last = k;
    this.#o.set(next);
  };

  /** A click on a pickable row, outside its controls, toggles its checkbox. */
  pickRow = (e: MouseEvent, item: T) => {
    if (!this.can(item) || !rowClick(e)) return;
    this.#shift = e.shiftKey;
    this.toggle(item, !this.has(item));
  };

  // The window handlers (<svelte:window>): they track Shift for a range, keep Shift+click on a
  // checkbox from selecting the text between the rows, and clear the pick on Esc when no dialog is open.
  onpointerdown = (e: PointerEvent) => {
    if (this.on) this.#shift = e.shiftKey;
  };
  onmousedown = (e: MouseEvent) => {
    if (this.on && e.shiftKey && e.target instanceof Element && e.target.closest("[data-pick]")) e.preventDefault();
  };
  onkeydown = (e: KeyboardEvent) => {
    if (!this.on) return;
    this.#shift = e.shiftKey;
    if (e.key !== "Escape" || !this.bar || e.defaultPrevented) return;
    if (document.querySelector("[role=dialog], [role=alertdialog]")) return;
    this.clear();
  };
}
