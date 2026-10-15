import type { Tone } from "$lib/components/StatusBadge.svelte";

/** The word and the badge tone of a sync state, or of the state of one repository in a sync. */
export const syncState: Record<string, { word: string; tone: Tone }> = {
  pending: { word: "Waiting", tone: "neutral" },
  running: { word: "Running", tone: "info" },
  done: { word: "Done", tone: "success" },
  failed: { word: "Failed", tone: "danger" },
  cancelled: { word: "Cancelled", tone: "neutral" },
};
