<!--
  Every table: for rows of one shape that people compare by column, sort or scan by the hundred (a
  collection is an ItemList otherwise). It takes the full content width. Rows have zebra stripes and one fixed height (a
  single line, `h-row`: 48px on Browse pages, 44px in Settings): cells never wrap, a long value truncates with a tooltip (Cell `text`). Hover
  fills the whole row; the `selected` row has the selected fill. Numbers and times are
  right-aligned with tabular numerals (column `align: "right"`, Cell `num`). Under 640px a row
  stacks (app.css): the `grow` cell on the first line, the other cells that show on a phone as a
  muted meta line parted by " · ", and the column headings hide. Inside a card or a dialog the
  table reaches 12px into the padding, so its text lines up with the title.

  Loading and error show in the table body, under the column headings. Loading shows nothing for
  300 ms, then a quiet indicator in the space of four rows (Loading); a reload keeps the current rows
  on screen. An empty table is never drawn: `empty` (a CompactEmpty) shows in its place, without
  column headings. Inside a Section the card is the surface, so the row drops its dashed frame. A keyset source gets "Load more"
  under it. With `href`, a click anywhere on a row opens it (put the same link on the row's name,
  so the keyboard reaches it); controls inside the row keep their own click.

    const devices = new Keyset((p) => api.devices.list(p));
    <DataTable source={devices} caption="Devices" key={(d) => d.id}
      columns={[{ label: "Name", grow: true }, { label: "Last used", align: "right" }, { label: "", srLabel: "Actions" }]}>
      {#snippet row(d)}
        <Cell grow strong text={d.name} />
        <Cell num muted><RelativeTime at={d.last_used_at} /></Cell>
        <Cell actions><Button variant="ghost" size="sm">Revoke</Button></Cell>
      {/snippet}
      {#snippet empty()}<CompactEmpty icon={LaptopIcon} title="Devices" state="None yet" text="..." />{/snippet}
    </DataTable>

  Bulk actions: pass `bind:picked` (a Set of row keys), `pickable?` (default: every row) and a
  `bulk` snippet with the actions. Each row then gets a checkbox in a first column, 40px wide over
  the full row height; a row that is not pickable has an empty cell there, and a table with no
  pickable row has no checkbox column. A click on the rest of
  the row still opens the row. Shift+click on a checkbox picks the range from the last checkbox
  clicked. The header checkbox picks every pickable row that is loaded and clears them on the next
  click (a keyset table says "Select the 50 rows shown"). While rows are picked, a bar takes the
  place of the header row, at the same height: "12 selected", the `bulk` actions, "Select all 50"
  and "Clear"; on a phone the bar scrolls sideways when the actions do not fit. Esc clears the
  pick when no dialog is open. `pickLabel` names a row for its
  checkbox ("Select oasis"). `picked` is not `selected`: `selected` is the row that the details
  column shows. After an action, the page reloads the table and empties `picked`. A table
  without `href` can pass `pickOnRow`: a click anywhere on a pickable row (outside its controls)
  then toggles its checkbox, so the whole row is the hit target. `pickOff` keeps the checkboxes
  in view but turns them off (the pick cannot change now). `selectAllLabel` replaces the text of
  the "Select all 50" button ("Select all supported").

    let picked = $state(new Set<string>());
    <DataTable source={src} key={(r) => r.name} pickLabel={(r) => r.name} bind:picked ...>
      {#snippet bulk()}<Button variant="outline" size="sm" onclick={makePublic}>Make public</Button>{/snippet}
    </DataTable>
-->
<script lang="ts" module>
  export type Column = {
    label: string;
    /** Screen-reader name of a column whose visible label is empty (actions). */
    srLabel?: string;
    /** "right" for numbers, sizes and times. */
    align?: "left" | "right";
    /** The column that takes the free width (its cells use Cell `grow`). */
    grow?: boolean;
    /** Hide the column under 640px (its cells use Cell `hideOnPhone`), so a phone shows the name and one fact. */
    hideOnPhone?: boolean;
    /** Extra classes of the header cell. */
    class?: string;
  };
</script>

<script lang="ts" generics="T, K extends string | number">
  import type { Snippet } from "svelte";
  import * as Table from "$lib/components/ui/table";
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
    columns,
    caption,
    key,
    href,
    selected,
    row,
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
    columns: Column[];
    /** Accessible name of the table. */
    caption: string;
    key: (item: T) => K;
    /** The address a row opens ("#/..."). */
    href?: (item: T) => string | undefined;
    /** The row that the details column shows (the selected fill). Not the same as `picked`. */
    selected?: (item: T) => boolean;
    /** The row's cells (<Cell>, or <Table.Cell> for custom content). */
    row: Snippet<[T]>;
    /** Shown in place of the table when it has no rows: a CompactEmpty. */
    empty?: Snippet;
    emptyText?: string;
    /** The keys of the rows picked for a bulk action. Bind it; replace the Set to change it. */
    picked?: Set<K>;
    /** Which rows can be picked (default: every row). */
    pickable?: (item: T) => boolean;
    /** The name of a row for its checkbox: "Select {name}". Default: the row's key. */
    pickLabel?: (item: T) => string;
    /** A click on a pickable row toggles its checkbox (only for a table without `href`). */
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
  // A table that picks rows has a taller heading row, so the selection bar that lies over it has
  // room above and below its buttons; the row keeps its height when the bar shows, so nothing moves.
  const headH = $derived(pick.on ? "h-11" : "h-(--row-head)");
  const span = $derived(columns.length + (pick.on ? 1 : 0));
  const rowPicks = $derived(pick.on && pickOnRow && !href && !pickOff);

  // One row look: a single line of fixed height, rounded ends like the sidebar items, stripes on
  // every second row, and a fill under the pointer.
  const rowClass =
    "group/row h-row border-0 even:bg-stripe hover:bg-hover data-[state=selected]:bg-selected [&>td]:py-0 [&>td:first-child]:rounded-l-lg [&>td:last-child]:rounded-r-lg";
</script>

<svelte:window onpointerdown={pick.onpointerdown} onmousedown={pick.onmousedown} onkeydown={pick.onkeydown} />

{#if !source.loading && !source.error && source.items.length === 0}
  <div data-slot="table-empty">{#if empty}{@render empty()}{:else}<p class="py-3 text-body text-muted-foreground">{emptyText}</p>{/if}</div>
{:else}
  <div data-slot="data-table">
  <Table.Root class="text-body">
    <Table.Caption class="sr-only">{caption}</Table.Caption>
    <Table.Header class="[&_tr]:border-b">
      <!-- The selection bar lies over the column headings, which keep their space, so the columns do not move. -->
      <Table.Row class="relative border-b hover:bg-transparent">
        {#if pick.on}
          <Table.Head class={cn("w-10 min-w-10 p-0", headH)}>
            {#if pick.head || pick.bar}<PickBox {pick} {pickOff} class="h-full cursor-pointer" />{/if}
            <!-- A stacked table on a phone has no column headings, so the head checkbox names itself. -->
            {#if pick.head && !pick.bar}<span aria-hidden="true" class="hidden text-xs font-medium text-muted-foreground max-sm:inline">{pick.allLabel}</span>{/if}
            {#if pick.bar}<PickBar {pick} {bulk} {pickOff} {selectAllLabel} class="absolute inset-y-0 right-0 left-10" />{/if}
          </Table.Head>
        {/if}
        {#each columns as c, i (i)}
          <Table.Head
            aria-hidden={pick.bar ? "true" : undefined}
            class={cn(headH, "text-xs font-medium text-muted-foreground", c.align === "right" && "text-right", c.grow && "w-full", c.hideOnPhone && "max-sm:hidden", pick.bar && "invisible", c.class)}
          >
            {#if c.label}{c.label}{:else if c.srLabel}<span class="sr-only">{c.srLabel}</span>{/if}
          </Table.Head>
        {/each}
      </Table.Row>
    </Table.Header>
    <Table.Body>
      {#if source.loading && source.items.length === 0}
        <Table.Row class="border-0 hover:bg-transparent">
          <Table.Cell colspan={span} class="p-0"><Loading rows={4} /></Table.Cell>
        </Table.Row>
      {:else if source.error && source.items.length === 0}
        <Table.Row class="border-0 hover:bg-transparent">
          <Table.Cell colspan={span} class="py-4 whitespace-normal"><SourceError {source} /></Table.Cell>
        </Table.Row>
      {:else}
        {#each source.items as item (key(item))}
          {@const on = selected?.(item) ?? false}
          <Table.Row
            data-row
            data-state={on ? "selected" : undefined}
            aria-selected={selected ? on : undefined}
            class={cn(rowClass, (href || (rowPicks && pick.can(item))) && "cursor-pointer")}
            onclick={href ? (e: MouseEvent) => openRow(e, href(item)) : rowPicks ? (e: MouseEvent) => pick.pickRow(e, item) : undefined}
          >
            {#if pick.on}
              <Table.Cell data-pick-cell class="w-10 min-w-10 p-0">
                {#if pick.can(item)}
                  <PickBox {pick} {item} {pickOff} label="Select {pickLabel ? pickLabel(item) : String(key(item))}" class="h-row" />
                {/if}
              </Table.Cell>
            {/if}
            {@render row(item)}
          </Table.Row>
        {/each}
      {/if}
    </Table.Body>
  </Table.Root>
  </div>
{/if}

<SourceMore {source} />
