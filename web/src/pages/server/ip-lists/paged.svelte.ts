import type { TableSource } from "$lib/load.svelte";

/**
 * A list source (ItemList) over a list that the page already holds: the first `step` rows, and "Load
 * more" for the next. A downloaded network list holds thousands of ranges.
 */
export class Paged<T> implements TableSource<T> {
  #get: () => readonly T[];
  #step: number;
  shown = $state(0);
  loading = false;
  error = "";

  constructor(get: () => readonly T[], step = 200) {
    this.#get = get;
    this.#step = step;
    this.shown = step;
  }

  get items() {
    return this.#get().slice(0, this.shown);
  }
  get hasMore() {
    return this.#get().length > this.shown;
  }
  more = () => {
    this.shown += this.#step;
  };
}
