<!--
  The state of a Live (lib/live.svelte.ts), in the actions of the Section whose data refreshes, or
  of the PageHeader when the whole page does: a small dot and "Updated 4 s ago", which ticks. The
  dot pulses while a refresh runs. The tooltip says how often it refreshes ("Refreshes every 3
  seconds"); a click refreshes now. When refreshes fail or the browser is offline it says so
  ("Not updated for 2 min · trying again in 20 s") in the warning color, so old data never looks
  live. It shows only while the Live is active.

    <Section title="Moving files">
      {#snippet actions()}<LiveStatus {live} /><Button ...>Cancel the move</Button>{/snippet}
-->
<script lang="ts">
  import * as Tooltip from "$lib/components/ui/tooltip";
  import { clock, type Live } from "$lib/live.svelte";
  import { formatEvery, formatSpan } from "$lib/format";
  import { cn } from "$lib/utils";

  let {
    live,
    verb = "Updated",
    cadence,
  }: {
    live: Live;
    /** The word of a refresh: "Updated 4 s ago", "Checked 4 s ago". */
    verb?: string;
    /** How often it refreshes, as a sentence ("Checks the Worker every 30 seconds"); "Refreshes every …" by default. */
    cadence?: string;
  } = $props();

  const now = $derived(clock.now);
  const since = $derived(live.lastUpdated === null ? null : now - live.lastUpdated);

  const text = $derived.by(() => {
    const old = since === null ? "Not updated yet" : `Not updated for ${formatSpan(since)}`;
    if (live.offline) return `Offline · ${old.toLowerCase()}`;
    if (live.stale) {
      if (live.refreshing) return `${old} · trying again`;
      return live.nextAt ? `${old} · trying again in ${formatSpan(live.nextAt - now)}` : old;
    }
    if (since === null) return "Updating";
    return since < 2000 ? `${verb} just now` : `${verb} ${formatSpan(since)} ago`;
  });

  const every = $derived(cadence ?? `Refreshes every ${formatEvery(Math.max(1, Math.round(live.every / 1000)))}`);
  const why = $derived(live.offline ? "The browser is offline." : live.error ? `The last refresh failed: ${live.error}` : "");
</script>

{#if live.active}
  <Tooltip.Root>
    <Tooltip.Trigger>
      {#snippet child({ props })}
        <button
          {...props}
          type="button"
          onclick={live.refresh}
          class={cn(
            "inline-flex h-8 items-center gap-2 rounded-sm px-1 text-meta whitespace-nowrap tabular-nums outline-none hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50",
            live.stale ? "text-warning" : "text-muted-foreground",
          )}
        >
          <span
            aria-hidden="true"
            class={cn("size-2 shrink-0 rounded-full", live.stale ? "bg-warning" : "bg-success", live.refreshing && "animate-live motion-reduce:animate-none")}
          ></span>
          <span>{text}</span>
          <span class="sr-only">. {every}. {why} Click to refresh now.</span>
        </button>
      {/snippet}
    </Tooltip.Trigger>
    <Tooltip.Content class="max-w-xs rounded-lg px-2.5 py-1.5 text-meta text-pretty">
      {every}. {why} Click to refresh now.
    </Tooltip.Content>
  </Tooltip.Root>
{/if}
