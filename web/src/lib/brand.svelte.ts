// The identity of the server as the UI shows it (GET /api/identity): its name and logo, loaded
// once per page load (App.svelte, with the formats) and read by the top bar, the sign-in page, the
// page title, Home's welcome and the Settings overview. The settings page of the identity puts the
// answer of a change here.

import { api, type Identity } from "$lib/api";

/** The name of the product: the "Powered by" line, and the name while the identity cannot load. */
export const productName = "Telesphorepo";

class Brand {
  #identity = $state<Identity | null>(null);
  /** True once the identity has loaded, or failed to. */
  ready = $state(false);
  #loading: Promise<void> | null = null;

  /** Loads the identity once; later calls return the same promise. */
  load(): Promise<void> {
    this.#loading ??= api.identity.get().then(
      (id) => {
        this.#identity = id;
        this.ready = true;
      },
      () => {
        this.#loading = null;
        this.ready = true;
      },
    );
    return this.#loading;
  }

  /** The name of the server. */
  get name(): string {
    return this.#identity?.name ?? productName;
  }

  /** The URL of the server's logo, or null for the brand's mark. */
  get logo(): string | null {
    return this.#identity?.logo ?? null;
  }

  /** Puts the identity that a change answered. */
  set(id: Identity) {
    this.#identity = id;
  }
}

export const brand = new Brand();
