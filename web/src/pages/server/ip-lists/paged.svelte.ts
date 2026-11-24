import type { TableSource } from "$lib/load.svelte";

/**
 * A list source (RangeGrid) over a list that the page already holds: the first `step` items, and
 * "Load more" for the next; `reset()` shows the first `step` again (a new search). A downloaded
 * network list holds thousands of ranges. 240 fills whole rows of 2, 3, 4, 5, 6 or 8 columns.
 */
export class Paged<T> implements TableSource<T> {
  #get: () => readonly T[];
  #step: number;
  shown = $state(0);
  loading = false;
  error = "";

  constructor(get: () => readonly T[], step = 240) {
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
  reset = () => {
    this.shown = this.#step;
  };
}
