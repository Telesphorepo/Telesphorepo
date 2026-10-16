// The formats of the server (GET /api/formats), loaded once per page load (App.svelte) and shared by
// every component. Each format is a UI descriptor: its title, icon, config fields, upload rules,
// delete notes, file-detail parts, remote presets and words. Every format choice of the UI reads
// the descriptor; a format-specific component exists only as a named part of the format's UI
// module (web/src/modules/<format>/module.ts, formatParts), which the descriptor names.

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

  /** The title of a format; its name while the formats load or for an unknown one. */
  title(name: string): string {
    return this.get(name)?.title ?? name;
  }

  /** The SVG of a format, or of an artifact kind of it (a Gradle plugin in a Maven repo). */
  icon(name: string, kind?: string): string | undefined {
    const f = this.get(name);
    return (kind && f?.kind_icons?.[kind]) || f?.icon;
  }

  /** The words for an artifact kind of a format, e.g. "Bill of materials"; the kind itself when it has none. */
  artifactKind(name: string, kind: string): string {
    return this.get(name)?.labels.artifact_kinds[kind] ?? kind;
  }

  /** The word for the title of an artifact of a kind, e.g. "Coordinates"; "Name" when it has none. */
  artifactName(name: string, kind: string): string {
    return this.get(name)?.labels.artifact_name[kind] || "Name";
  }

  /** The format that imports the files of a Nexus format (import_sources), or undefined. */
  importer(sourceFormat: string): Format | undefined {
    return this.list.find((f) => f.import_sources.includes(sourceFormat));
  }
}

export const formats = new Formats();
