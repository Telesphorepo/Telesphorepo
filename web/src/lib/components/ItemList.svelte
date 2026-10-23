<!--
  A collection as a list: the default for the things a page lists (devices, tokens, people, syncs).
  The rows sit in the Section's card, parted by thin lines, with no column headings and no
  stripes. Each row has an optional `leading` icon or avatar (an icon is 20px: `class="size-5"`),
  a title line (`title`, which truncates and is the row's link, then `badges`), a `meta` line
  under it (muted: the one or two facts that matter, joined by " · "; it truncates from 640px and
  wraps on a phone, so a warning there stays readable), and `trailing` content at
  the end (a state badge, a switch, the row's actions). A row without `meta` is one line.
  Inside a card or a dialog the list reaches 12px into the padding, so the hover fill lies there
  and the text stays on the title's edge.

  The props mirror DataTable's, so a page swaps one for the other: `source` (a Keyset, or
  listOf(resource)), `key`, `caption`, `href`, `selected`, `empty`. Loading shows nothing for
  300 ms, then Loading in the space of four rows; a reload keeps the current rows. An error with
  no rows shows with Try again; an empty list is never drawn: `empty` (a CompactEmpty) shows in its
  place. A Keyset gets "Load more". With `href`, a click anywhere on a row opens it and the title
  is the link for the keyboard; controls inside the row keep their own click.

    const devices = new Keyset((p) => api.devices.list(p));
    <ItemList source={devices} caption="Devices" key={(d) => d.id}>
      {#snippet leading()}<LaptopIcon class="size-5" />{/snippet}
      {#snippet title(d)}{d.name}{/snippet}
      {#snippet meta(d)}{d.client} · used <RelativeTime at={d.last_used_at} />{/snippet}
      {#snippet trailing(d)}<Button variant="ghost" size="sm">Revoke</Button>{/snippet}
      {#snippet empty()}<CompactEmpty icon={LaptopIcon} title="Devices" state="None yet" text="..." />{/snippet}
    </ItemList>

  Bulk actions work as in DataTable: `bind:picked` (a Set of row keys), `pickable?`, `pickLabel?`
  and a `bulk` snippet. Each row then gets a checkbox at its start, and a head row over the list
  holds the checkbox that picks every pickable loaded row; while rows are picked, the selection bar
  ("12 selected", the `bulk` actions, "Select all 50", "Clear") lies over that head row.
  Shift+click picks a range, Esc clears the pick when no dialog is open. `pickOnRow` (a list
  without `href`) makes a click anywhere on a pickable row toggle its checkbox; `pickOff` shows the
  checkboxes turned off; `selectAllLabel` replaces the text of "Select all 50".
-->
<script lang="ts" generics="T, K extends string | number">
  import type { Snippet } from "svelte";
  import Loading from "./Loading.svelte";
  import PickBar from "./PickBar.svelte";
  import PickBox from "./PickBox.svelte";
  import SourceError from "./SourceError.svelte";
  import SourceMore from "./SourceMore.svelte";
  import type { TableSource } from "$lib/load.svelte";
  import { Pick, openRow } from "$lib/pick.svelte";
  import { cn } from "$lib/utils";

  let {
    source,
    caption,
    key,
    href,
    selected,
    leading,
    title,
    badges,
    meta,
    trailing,
    empty,
    emptyText = "Nothing here yet.",
    picked = $bindable(new Set<K>()),
    pickable,
    pickLabel,
    pickOnRow = false,
    pickOff = false,
    selectAllLabel,
    bulk,
  }: {
    source: TableSource<T>;
    /** Accessible name of the list. */
    caption: string;
    key: (item: T) => K;
    /** The address a row opens ("#/..."). */
    href?: (item: T) => string | undefined;
    /** The row that a details column shows (the selected fill). Not the same as `picked`. */
    selected?: (item: T) => boolean;
    /** An icon (20px) or an avatar at the start of the row. */
    leading?: Snippet<[T]>;
    /** The row's name: one line that truncates, and the link of a row with `href`. Put no link in it. */
    title: Snippet<[T]>;
    /** Badges after the title (a role, Latest, a lock). */
    badges?: Snippet<[T]>;
    /** The muted line under the title: one or two facts, joined by " · ". */
    meta?: Snippet<[T]>;
    /** At the end of the row: a state badge, a switch, the row's actions. */
    trailing?: Snippet<[T]>;
    /** Shown in place of the list when it has no rows: a CompactEmpty. */
    empty?: Snippet;
    emptyText?: string;
    /** The keys of the rows picked for a bulk action. Bind it; replace the Set to change it. */
    picked?: Set<K>;
    /** Which rows can be picked (default: every row). */
    pickable?: (item: T) => boolean;
    /** The name of a row for its checkbox: "Select {name}". Default: the row's key. */
    pickLabel?: (item: T) => string;
    /** A click on a pickable row toggles its checkbox (only for a list without `href`). */
    pickOnRow?: boolean;
    /** Show the checkboxes, but turned off. */
    pickOff?: boolean;
    /** The text of the "Select all" button of the selection bar. */
    selectAllLabel?: string;
    /** The bulk actions in the selection bar. With it, every row gets a checkbox. */
    bulk?: Snippet;
  } = $props();

  const pick = new Pick<T, K>({
    on: () => bulk !== undefined,
    items: () => source.items,
    key: (item) => key(item),
    pickable: (item) => pickable?.(item) ?? true,
    more: () => !!source.hasMore,
    get: () => picked,
    set: (v) => (picked = v),
  });
  const rowPicks = $derived(pick.on && pickOnRow && !href && !pickOff);
</script>

<svelte:window onpointerdown={pick.onpointerdown} onmousedown={pick.onmousedown} onkeydown={pick.onkeydown} />

{#if !source.loading && !source.error && source.items.length === 0}
  <div data-slot="table-empty">{#if empty}{@render empty()}{:else}<p class="py-3 text-body text-muted-foreground">{emptyText}</p>{/if}</div>
{:else}
  <div data-slot="item-list">
  {#if pick.on && (pick.head || pick.bar)}
    <!-- The selection bar lies over the head row, which keeps its height, so the rows do not move. -->
    <div class="relative flex h-11 items-center after:absolute after:inset-x-3 after:bottom-0 after:h-px after:bg-border">
      <PickBox {pick} {pickOff} class="h-full" />
      {#if pick.bar}
        <PickBar {pick} {bulk} {pickOff} {selectAllLabel} class="absolute inset-y-0 right-0 left-10" />
      {:else}
        <span aria-hidden="true" class="text-xs font-medium text-muted-foreground">{pick.allLabel}</span>
      {/if}
    </div>
  {/if}
  {#if source.loading && source.items.length === 0}
    <Loading rows={4} />
  {:else if source.error && source.items.length === 0}
    <div class="py-4"><SourceError {source} /></div>
  {:else}
    <!-- The lines between the rows run on the content edges; the line next to a filled row hides, so the fill reads whole. -->
    <ul
      aria-label={caption}
      class="text-body [&>li[data-fills]:hover]:before:opacity-0 [&>li[data-fills]:hover+li]:before:opacity-0 [&>li[data-state=selected]]:before:opacity-0 [&>li[data-state=selected]+li]:before:opacity-0"
    >
      {#each source.items as item (key(item))}
        {@const on = selected?.(item) ?? false}
        {@const to = href?.(item)}
        <!-- The keyboard reaches the row through its title link and its checkbox; the click on the row is for the pointer. -->
        <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
        <li
          data-state={on ? "selected" : undefined}
          data-fills={to || rowPicks ? "" : undefined}
          aria-current={on ? "true" : undefined}
          class={cn(
            "relative flex min-h-row items-center gap-3 rounded-lg py-3 pr-3 before:absolute before:inset-x-3 before:top-0 before:h-px before:bg-border first:before:hidden",
            pick.on ? "pl-0" : "pl-3",
            (to || rowPicks) && "hover:bg-hover",
            (to || (rowPicks && pick.can(item))) && "cursor-pointer",
            on && "bg-selected text-selected-foreground hover:bg-selected",
          )}
          onclick={to ? (e: MouseEvent) => openRow(e, to) : rowPicks ? (e: MouseEvent) => pick.pickRow(e, item) : undefined}
        >
          {#if pick.on}
            {#if pick.can(item)}
              <PickBox {pick} {item} {pickOff} label="Select {pickLabel ? pickLabel(item) : String(key(item))}" class="-my-3 self-stretch" />
            {:else}
              <span class="w-10 shrink-0"></span>
            {/if}
          {/if}
          {#if leading}<span class="flex shrink-0 text-muted-foreground">{@render leading(item)}</span>{/if}
          <div class="flex min-w-0 flex-1 flex-col">
            <div class="flex min-w-0 items-center gap-2 text-label">
              {#if to}
                <a href={to} class="min-w-0 truncate rounded-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50">{@render title(item)}</a>
              {:else}
                <span class="min-w-0 truncate">{@render title(item)}</span>
              {/if}
              {#if badges}<span class="flex shrink-0 items-center gap-1.5">{@render badges(item)}</span>{/if}
            </div>
            {#if meta}<div class="text-meta text-muted-foreground sm:truncate">{@render meta(item)}</div>{/if}
          </div>
          {#if trailing}<div data-slot="row-end" class="flex shrink-0 items-center gap-2">{@render trailing(item)}</div>{/if}
        </li>
      {/each}
    </ul>
  {/if}
  </div>
{/if}

<SourceMore {source} />
