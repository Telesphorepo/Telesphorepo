<!--
  One active filter of a list as a small removable chip: its icon, the value, and a button that
  removes it. A row of them sits under the filter controls, with "Clear filters" at its end.

    <FilterChip label="Signed in" onremove={() => setFilter({ action: "" })} />
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import XIcon from "@lucide/svelte/icons/x";

  let { label, kind, icon, onremove }: { label: string; kind?: string; icon?: Snippet; onremove: () => void } = $props();
</script>

<span class="inline-flex h-7 max-w-full min-w-0 items-center gap-1.5 rounded-lg bg-muted pr-0.5 pl-2.5 text-xs font-medium">
  {#if icon}<span class="flex shrink-0 text-muted-foreground">{@render icon()}</span>{/if}
  {#if kind}<span class="shrink-0 font-normal text-muted-foreground">{kind}</span>{/if}
  <span class="min-w-0 truncate">{label}</span>
  <button
    type="button"
    aria-label="Remove the filter {label}"
    class="flex size-6 shrink-0 items-center justify-center rounded-lg text-muted-foreground outline-none hover:bg-hover hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50"
    onclick={onremove}
  >
    <XIcon class="size-3.5" />
  </button>
</span>
