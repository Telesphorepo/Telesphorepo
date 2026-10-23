<!--
  The stored size by format, largest first: per format its icon and title, a bar whose length is
  the size against the largest, and the size at its end (from 640px on one line; under it the bar
  sits under the name). One series, so one color (--chart-bar) and no legend; the tooltip of a row
  adds its share of the total and its repositories. The bar is 8px thick on a track of the muted
  fill, round at its end and square at the start, where every bar begins.
-->
<script lang="ts">
  import { formats } from "$lib/formats/registry.svelte";
  import { formatSize } from "$lib/format";
  import { count } from "$lib/words";
  import FormatIcon from "$lib/components/FormatIcon.svelte";

  type Row = { format: string; size: number; repos: number };
  let { rows }: { rows: Row[] } = $props();

  const max = $derived(Math.max(1, ...rows.map((r) => r.size)));
  const total = $derived(rows.reduce((n, r) => n + r.size, 0));
  const share = (r: Row) => (total ? Math.round((r.size / total) * 100) : 0);
</script>

<ul aria-label="Stored size by format" class="flex flex-col gap-4">
  {#each rows as r (r.format)}
    {@const title = formats.title(r.format)}
    <li
      class="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-2 sm:grid-cols-[minmax(0,10rem)_minmax(0,1fr)_5rem]"
      title="{title}: {formatSize(r.size)}, {share(r)}% of the total, in {count(r.repos, 'repository', 'repositories')}"
    >
      <span class="col-start-1 row-start-1 flex min-w-0 items-center gap-3">
        <span aria-hidden="true" class="flex shrink-0 text-muted-foreground"><FormatIcon format={r.format} class="size-5" /></span>
        <span class="min-w-0 truncate text-label">{title}</span>
      </span>
      <span aria-hidden="true" class="col-span-2 col-start-1 row-start-2 h-2 overflow-hidden rounded-r-full bg-muted sm:col-span-1 sm:col-start-2 sm:row-start-1">
        {#if r.size > 0}<span class="block h-full min-w-0.5 rounded-r-full bg-chart-bar" style:width="{(r.size / max) * 100}%"></span>{/if}
      </span>
      <span class="col-start-2 row-start-1 text-right text-body tabular-nums sm:col-start-3">{formatSize(r.size)}</span>
    </li>
  {/each}
</ul>
