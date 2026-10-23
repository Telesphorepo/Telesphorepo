<!--
  The downloads of each day as columns, oldest left, today right (GET /api/stats: days in UTC). One
  series, so one color (--chart-bar) and no legend: the card's title names it. Columns are at most
  24px wide with a 2px gap of the card between them, round at the top and square on the baseline,
  a hairline baseline and one hairline at the top tick (the largest day, rounded up). Under the
  pointer or with the arrow keys (the chart takes focus) a day shows its downloads and bytes in a
  tooltip; a visually hidden table holds every value for screen readers.
-->
<script lang="ts">
  import { formatCount, formatSize } from "$lib/format";
  import { count } from "$lib/words";

  type Day = { day: string; downloads: number; bytes: number };
  let { days }: { days: Day[] } = $props();

  /** A clean top for the axis: 1, 2 or 5 times a power of ten, at or above the largest day. */
  const top = $derived.by(() => {
    const max = Math.max(1, ...days.map((d) => d.downloads));
    const p = 10 ** Math.floor(Math.log10(max));
    return [1, 2, 5, 10].map((m) => m * p).find((v) => v >= max) ?? max;
  });
  const compact = new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 });

  const dateOf = (d: string) => new Date(`${d}T00:00:00Z`);
  const short = new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short", timeZone: "UTC" });
  const long = new Intl.DateTimeFormat(undefined, { weekday: "short", day: "numeric", month: "short", timeZone: "UTC" });

  let active = $state<number | null>(null);
  let focused = $state(false);
  const shown = $derived(active ?? (focused ? days.length - 1 : null));

  function onkeydown(e: KeyboardEvent) {
    const at = shown ?? days.length - 1;
    if (e.key === "ArrowLeft") active = Math.max(0, at - 1);
    else if (e.key === "ArrowRight") active = Math.min(days.length - 1, at + 1);
    else if (e.key === "Home") active = 0;
    else if (e.key === "End") active = days.length - 1;
    else return;
    e.preventDefault();
  }
</script>

<div class="flex flex-col gap-2">
  <div class="relative">
    <!-- The top tick and its hairline; the baseline is the bottom edge. -->
    <span class="pointer-events-none absolute top-0 left-0 -translate-y-1/2 bg-card pr-2 text-xs text-muted-foreground tabular-nums" aria-hidden="true">
      {compact.format(top)}
    </span>
    <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions -->
    <div
      role="img"
      aria-label="Downloads per day; use the arrow keys to read each day"
      tabindex="0"
      class="relative flex h-32 items-end gap-0.5 rounded-sm border-t border-b border-t-border/60 border-b-border pt-0 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
      {onkeydown}
      onfocus={() => (focused = true)}
      onblur={() => {
        focused = false;
        active = null;
      }}
      onpointerleave={() => (active = null)}
    >
      {#each days as d, i (d.day)}
        <!-- svelte-ignore a11y_no_static_element_interactions -->
        <div class="flex h-full min-w-0 flex-1 items-end justify-center" onpointerenter={() => (active = i)}>
          <div
            class={["w-full max-w-6 rounded-t-full bg-chart-bar transition-opacity", shown !== null && shown !== i && "opacity-60"]}
            style:height={d.downloads > 0 ? `max(2px, ${(d.downloads / top) * 100}%)` : "0"}
          ></div>
        </div>
      {/each}
      {#if shown !== null && days[shown]}
        {@const d = days[shown]}
        <div
          class="pointer-events-none absolute bottom-full z-10 mb-2 flex -translate-x-1/2 flex-col rounded-lg border bg-popover px-3 py-2 text-xs whitespace-nowrap text-popover-foreground shadow-md"
          style:left="clamp(4.5rem, {((shown + 0.5) / days.length) * 100}%, calc(100% - 4.5rem))"
          aria-hidden="true"
        >
          <span class="font-medium">{long.format(dateOf(d.day))}</span>
          <span class="text-muted-foreground tabular-nums">{count(d.downloads, "download", "downloads")} · {formatSize(d.bytes)}</span>
        </div>
      {/if}
    </div>
  </div>
  <div class="flex justify-between text-xs text-muted-foreground" aria-hidden="true">
    <span>{days.length ? short.format(dateOf(days[0].day)) : ""}</span>
    <span>{days.length ? short.format(dateOf(days[days.length - 1].day)) : ""}</span>
  </div>
  <table class="sr-only">
    <caption>Downloads per day (UTC)</caption>
    <thead><tr><th scope="col">Day</th><th scope="col">Downloads</th><th scope="col">Bytes</th></tr></thead>
    <tbody>
      {#each days as d (d.day)}
        <tr><th scope="row">{long.format(dateOf(d.day))}</th><td>{formatCount(d.downloads)}</td><td>{formatSize(d.bytes)}</td></tr>
      {/each}
    </tbody>
  </table>
</div>
