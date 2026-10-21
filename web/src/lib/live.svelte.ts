// Live data. A page that shows something the server changes on its own (a sync, a storage move, a
// backup, a region that fills) asks for it again on a timer. A Live owns that timer, so every
// page refreshes the same way: it pauses while the tab is hidden or the browser is offline and
// refreshes at once when the reader comes back, never runs two requests at once, and waits longer
// after each failed request. <LiveStatus> shows its state where the data is (see web/src/README.md,
// "Live data").

import { untrack } from "svelte";
import { createSubscriber } from "svelte/reactivity";
import { errorMessage } from "$lib/api";

/** The longest wait between two tries after failed requests (unless the interval is longer). */
const MAX_BACKOFF = 60_000;
/** A setTimeout delay above 2^31-1 ms fires at once, so a far-off refresh waits in steps of a day. */
const MAX_DELAY = 24 * 60 * 60 * 1000;

const second = createSubscriber((update) => {
  const t = setInterval(update, 1000);
  return () => clearInterval(t);
});

/** The time now; a template or an effect that reads it updates every second. */
export const clock = {
  get now() {
    second();
    return Date.now();
  },
};

export type LiveOptions = {
  /** Asks for the data and stores it; throws when the request fails. */
  run: () => Promise<unknown>;
  /** The wait between two refreshes, in ms. A function is read again before each wait. */
  every: number | (() => number);
  /** Refreshes only while this is true (read reactively); always when left out. */
  while?: () => boolean;
  /** Refreshes at once when it starts. Leave it out when the page has just loaded the data. */
  now?: boolean;
};

/**
 * A refresh on a timer. Create it in a component's script; it stops with the component.
 *
 *   const live = new Live({ run: () => overview.fetch(), every: 2000, while: () => !!job?.running });
 *   <LiveStatus {live} />
 */
export class Live {
  /** True while a refresh runs. */
  refreshing = $state(false);
  /** When the last refresh succeeded (ms; the start counts when the page has just loaded). */
  lastUpdated = $state<number | null>(null);
  /** When the next refresh runs (ms); null while none is due (hidden, offline, stopped). */
  nextAt = $state<number | null>(null);
  /** The error of the last refresh; "" after a success. */
  error = $state("");
  /** Failed refreshes in a row. */
  failures = $state(0);
  /** True while `while` holds. */
  active = $state(false);
  /** The browser says it is offline. */
  offline = $state(false);

  #opts: LiveOptions;
  #hidden = false;
  #timer: ReturnType<typeof setTimeout> | undefined;
  #on = $derived.by(() => (this.#opts.while ? this.#opts.while() : true));

  constructor(opts: LiveOptions) {
    this.#opts = opts;
    $effect(() => {
      const on = this.#on;
      untrack(() => (on ? this.#start() : this.#stop()));
    });
    $effect(() => {
      const visibility = () => {
        this.#hidden = document.hidden;
        if (this.#hidden) this.#clear();
        else if (this.active) void this.refresh();
      };
      const online = () => {
        this.offline = false;
        if (this.active && !this.#hidden) void this.refresh();
      };
      const offline = () => {
        this.offline = true;
        this.#clear();
      };
      document.addEventListener("visibilitychange", visibility);
      window.addEventListener("online", online);
      window.addEventListener("offline", offline);
      return () => {
        document.removeEventListener("visibilitychange", visibility);
        window.removeEventListener("online", online);
        window.removeEventListener("offline", offline);
        this.#stop();
      };
    });
  }

  /** The wait between two refreshes now, in ms. */
  get every(): number {
    const e = this.#opts.every;
    return typeof e === "function" ? e() : e;
  }

  /** The data on screen may be old: the last refresh failed, or the browser is offline. */
  get stale(): boolean {
    return this.failures > 0 || this.offline;
  }

  /** Refreshes now (a refresh that runs already is not repeated) and waits the interval again. */
  refresh = async () => {
    if (this.refreshing) return;
    this.#clear();
    this.refreshing = true;
    try {
      await this.#opts.run();
      this.lastUpdated = Date.now();
      this.failures = 0;
      this.error = "";
    } catch (e) {
      this.failures++;
      this.error = errorMessage(e);
    } finally {
      this.refreshing = false;
      this.#schedule();
    }
  };

  #start() {
    if (this.active) return;
    this.active = true;
    this.#hidden = document.hidden;
    this.offline = !navigator.onLine;
    this.failures = 0;
    this.error = "";
    if (this.#opts.now) void this.refresh();
    else {
      this.lastUpdated = Date.now();
      this.#schedule();
    }
  }

  #stop() {
    this.active = false;
    this.#clear();
  }

  #clear() {
    clearTimeout(this.#timer);
    this.#timer = undefined;
    this.nextAt = null;
  }

  #schedule() {
    this.#clear();
    if (!this.active || this.#hidden || this.offline) return;
    const base = Math.max(0, untrack(() => this.every));
    const wait = this.failures > 0 ? Math.min(base * 2 ** this.failures, Math.max(base, MAX_BACKOFF)) : base;
    this.nextAt = Date.now() + wait;
    this.#timer = wait > MAX_DELAY ? setTimeout(() => this.#schedule(), MAX_DELAY) : setTimeout(() => void this.refresh(), wait);
  }
}

/**
 * Waits ms, then until the tab is visible and the browser online: the pause of a loop that
 * follows a process on the server outside a page (a repository delete in a toast).
 */
export async function pause(ms: number): Promise<void> {
  await new Promise((r) => setTimeout(r, ms));
  while (document.hidden || !navigator.onLine) {
    await new Promise<void>((resolve) => {
      const back = () => {
        if (document.hidden || !navigator.onLine) return;
        document.removeEventListener("visibilitychange", back);
        window.removeEventListener("online", back);
        resolve();
      };
      document.addEventListener("visibilitychange", back);
      window.addEventListener("online", back);
    });
  }
}

/**
 * Action: a brief tint behind the element when `value` changes, so the reader sees that a number
 * is live. Nothing under prefers-reduced-motion. <LiveValue> uses it.
 */
export function flash(node: HTMLElement, value: unknown) {
  let last = value;
  return {
    update(next: unknown) {
      if (next === last) return;
      last = next;
      if (matchMedia("(prefers-reduced-motion: reduce)").matches) return;
      // The brand color, faint: it shows on the card and on a badge's fill alike.
      const brand = getComputedStyle(node).getPropertyValue("--primary").trim();
      if (!brand) return;
      node.animate([{ backgroundColor: `color-mix(in oklch, ${brand} 30%, transparent)` }, { backgroundColor: "transparent" }], {
        duration: 1200,
        easing: "ease-out",
      });
    },
  };
}
