// The registered formats from GET /api/formats, loaded once per page load and shared by every
// component: their titles and their icons (each format plugin supplies its own SVG).

import { api, type Format } from "$lib/api";

class Formats {
  list = $state<Format[]>([]);
  #loading: Promise<void> | null = null;

  /** Loads the formats once; later calls return the same promise. A failed load tries again next time. */
  load(): Promise<void> {
    this.#loading ??= api.formats.list().then(
      (f) => {
        this.list = f;
      },
      () => {
        this.#loading = null;
      },
    );
    return this.#loading;
  }

  get(name: string): Format | undefined {
    return this.list.find((f) => f.name === name);
  }

  /** The SVG of a format, or of an artifact kind of it (a Gradle plugin in a Maven repo). */
  icon(name: string, kind?: string): string | undefined {
    const f = this.get(name);
    return (kind && f?.kind_icons?.[kind]) || f?.icon;
  }
}

export const formats = new Formats();
