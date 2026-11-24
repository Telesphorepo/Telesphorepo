<!--
  Addresses and ranges as a dense grid of small cells, for a collection of short values of one
  shape: the ranges of a network list, the addresses a repository allows, the trusted proxies. The
  cells flow in as many columns as fit (11rem each at least, 15rem with `wide`, 8px apart; one
  column on a phone); a value too long for one cell (a long IPv6 range) takes two where the grid
  has two, and a value cut off still has its tooltip. The cells are measured, not counted in
  characters, once per change of the width or of the values.
  Each cell is the value in tabular numerals with an optional `leading` icon (16px), an optional
  `note` under it (a label, where the address is), and at the end of its first line Copy and,
  with `onremove`, Remove. Under a pointer the buttons show while the pointer or the focus is in
  the cell, so the keyboard reaches them by Tab; on a touch screen they always show. The states are
  those of ItemList: Loading, an error with Try again, `empty` (a CompactEmpty) in place of an empty
  grid, and Load more for a source with more.

    <RangeGrid source={ranges} caption="Address ranges" text={(r) => r} onremove={removeRange}>
      {#snippet empty()}<CompactEmpty icon={NetworkIcon} text="An empty list allows no address." />{/snippet}
    </RangeGrid>
-->
<script lang="ts" generics="T">
  import type { Snippet } from "svelte";
  import { Button } from "$lib/components/ui/button";
  import CopyButton from "./CopyButton.svelte";
  import Loading from "./Loading.svelte";
  import SourceError from "./SourceError.svelte";
  import SourceMore from "./SourceMore.svelte";
  import type { TableSource } from "$lib/load.svelte";
  import XIcon from "@lucide/svelte/icons/x";

  let {
    source,
    caption,
    text,
    key = text,
    copy = text,
    leading,
    note,
    onremove,
    wide = false,
    empty,
    emptyText = "Nothing here yet.",
  }: {
    source: TableSource<T>;
    /** Accessible name of the grid. */
    caption: string;
    /** The address or the range a cell shows. */
    text: (item: T) => string;
    /** The cell's key; its text by default. */
    key?: (item: T) => string;
    /** What Copy takes (the text by default); undefined: no Copy. */
    copy?: (item: T) => string | undefined;
    /** A 16px icon before the text. */
    leading?: Snippet<[T]>;
    /** The line under the value: a label, or where the address is. */
    note?: Snippet<[T]>;
    /** With it, each cell has Remove. */
    onremove?: (item: T) => void;
    /** Wider cells, for a note. */
    wide?: boolean;
    /** Shown in place of the grid when it has no cells: a CompactEmpty. */
    empty?: Snippet;
    emptyText?: string;
  } = $props();

  // Measures every value, then marks the ones that need two columns (data-long) and gives a value
  // that is cut off its tooltip. The spans come off first: a span past the last column adds a
  // column. A grid's columns do not depend on its cells, so a span changes no other cell. Then it
  // reads all and writes all, so the layout runs once more.
  let grid = $state<HTMLUListElement | null>(null);
  function fit() {
    const el = grid;
    if (!el?.isConnected) return;
    const cells = [...el.children] as HTMLElement[];
    for (const li of cells) li.removeAttribute("data-long");
    const values = cells.map((li) => li.querySelector<HTMLElement>("[data-value]"));
    if (!values[0]) return;
    const cs = getComputedStyle(el);
    const tracks = cs.gridTemplateColumns.split(" ");
    const span = parseFloat(tracks[0]) + (parseFloat(cs.columnGap) || 0);
    const room = values[0].clientWidth;
    const range = document.createRange();
    const widths = values.map((v) => {
      if (!v) return 0;
      range.selectNodeContents(v);
      return range.getBoundingClientRect().width;
    });
    const two = tracks.length >= 2;
    cells.forEach((li, i) => {
      const long = two && widths[i] > room + 0.5;
      if (long) li.setAttribute("data-long", "");
      const v = values[i];
      if (!v) return;
      if (widths[i] > (long ? room + span : room) + 0.5) v.title = v.textContent ?? "";
      else v.removeAttribute("title");
    });
  }
  $effect(() => {
    const el = grid;
    if (!el) return;
    let width = -1;
    const ro = new ResizeObserver(([e]) => {
      if (e.contentRect.width === width) return;
      width = e.contentRect.width;
      fit();
    });
    ro.observe(el);
    document.fonts?.ready.then(fit);
    return () => ro.disconnect();
  });
  // New values (Load more, a search): measure them once they are in the page.
  $effect(() => {
    void source.items;
    void wide;
    fit();
  });
</script>

{#if source.loading && source.items.length === 0}
  <Loading rows={4} />
{:else if source.error && source.items.length === 0}
  <div class="py-4"><SourceError {source} /></div>
{:else if source.items.length === 0}
  <div data-slot="table-empty">{#if empty}{@render empty()}{:else}<p class="py-3 text-body text-muted-foreground">{emptyText}</p>{/if}</div>
{:else}
  <ul bind:this={grid} aria-label={caption} data-wide={wide ? "" : undefined} class="range-grid grid gap-2">
    {#each source.items as item (key(item))}
      {@const t = text(item)}
      {@const c = copy(item)}
      <li
        class="flex min-w-0 flex-col justify-center rounded-lg border px-3 py-1 focus-within:bg-hover hover:bg-hover"
      >
        <div class="relative flex min-h-7 min-w-0 items-center gap-2">
          <!-- A cell whose leading snippet renders nothing has no gap before its value. -->
          {#if leading}<span class="flex shrink-0 text-muted-foreground empty:hidden">{@render leading(item)}</span>{/if}
          <span data-value class="min-w-0 flex-1 truncate text-body tabular-nums">{t}</span>
          {#if c !== undefined || onremove}
            <span class="acts flex shrink-0 items-center">
              {#if c !== undefined}<CopyButton value={c} label="Copy {t}" size="icon-xs" />{/if}
              {#if onremove}
                <Button
                  variant="ghost"
                  size="icon-xs"
                  class="hover:bg-destructive/10 hover:text-destructive"
                  aria-label="Remove {t}"
                  title="Remove"
                  onclick={() => onremove(item)}><XIcon /></Button
                >
              {/if}
            </span>
          {/if}
        </div>
        {#if note}<div class="min-w-0 text-meta text-muted-foreground">{@render note(item)}</div>{/if}
      </li>
    {/each}
  </ul>
{/if}

<SourceMore {source} />

<style>
  /* Cells of at least --cell, as many as fit; never wider than the grid (a phone). */
  .range-grid {
    --cell: 11rem;
    grid-template-columns: repeat(auto-fill, minmax(min(100%, var(--cell)), 1fr));
  }
  .range-grid[data-wide] {
    --cell: 15rem;
  }
  /* A touch screen shows the buttons in the line, so each cell needs their room. */
  @media (hover: none) {
    .range-grid {
      --cell: 14rem;
    }
    .range-grid[data-wide] {
      --cell: 18rem;
    }
  }
  /* A value that does not fit one column takes two (fit() marks it). */
  .range-grid > :global(li[data-long]) {
    grid-column: span 2;
  }
  /* Under a pointer the buttons lie over the end of the line while the cell is hovered or focused. */
  @media (hover: hover) {
    .acts {
      position: absolute;
      inset-block: 0;
      right: -0.25rem;
      padding-left: 1rem;
      background: linear-gradient(to right, transparent, var(--hover) 1rem);
      opacity: 0;
    }
    li:hover .acts,
    li:focus-within .acts {
      opacity: 1;
    }
  }
</style>
