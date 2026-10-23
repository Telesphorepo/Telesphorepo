<!--
  Sizes as bars, largest first: per row the format icon and a name, a bar whose length is the size
  against the largest, and the size at its end (from 640px on one line; under it the bar sits under
  the name). With `href` the row is a link. One series, so one color (--chart-bar) and no legend;
  `note` is the row's tooltip (its share of the total and what the size is made of). The bar is
  8px thick on a track of the muted fill, round at both ends like its track.
-->
<script lang="ts">
  import { formatSize } from "$lib/format";
  import FormatIcon from "$lib/components/FormatIcon.svelte";

  type Row = { key: string; format: string; name: string; size: number; href?: string; note?: string };
  let { rows, label }: { rows: Row[]; label: string } = $props();

  const max = $derived(Math.max(1, ...rows.map((r) => r.size)));
</script>

<ul aria-label={label} class="-mx-3 flex flex-col">
  {#each rows as r (r.key)}
    <li class="relative rounded-lg px-3 py-2 {r.href ? 'transition-colors hover:bg-hover has-[a:focus-visible]:ring-[3px] has-[a:focus-visible]:ring-ring/50' : ''}" title={r.note}>
      <div class="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-2 sm:grid-cols-[minmax(0,12rem)_minmax(0,1fr)_5rem]">
        <span class="col-start-1 row-start-1 flex min-w-0 items-center gap-3">
          <span aria-hidden="true" class="flex shrink-0 text-muted-foreground"><FormatIcon format={r.format} class="size-5" /></span>
          {#if r.href}
            <a href={r.href} class="min-w-0 truncate text-label outline-none after:absolute after:inset-0 after:rounded-lg">{r.name}</a>
          {:else}
            <span class="min-w-0 truncate text-label">{r.name}</span>
          {/if}
        </span>
        <span aria-hidden="true" class="col-span-2 col-start-1 row-start-2 h-2 overflow-hidden rounded-full bg-muted sm:col-span-1 sm:col-start-2 sm:row-start-1">
          {#if r.size > 0}<span class="block h-full min-w-2 rounded-full bg-chart-bar" style:width="{(r.size / max) * 100}%"></span>{/if}
        </span>
        <span class="col-start-2 row-start-1 text-right text-body tabular-nums sm:col-start-3">{formatSize(r.size)}</span>
      </div>
    </li>
  {/each}
</ul>
