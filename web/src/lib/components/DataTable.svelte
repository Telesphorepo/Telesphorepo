<!--
  Every table. It takes the full content width. Rows have zebra stripes and one fixed height (a
  single line, 40px): cells never wrap, a long value truncates with a tooltip (Cell `text`). Hover
  fills the whole row; the `selected` row has the selected fill. Numbers and times are
  right-aligned with tabular numerals (column `align: "right"`, Cell `num`).

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

<script lang="ts" generics="T">
  import type { Snippet } from "svelte";
  import * as Table from "$lib/components/ui/table";
  import { Button } from "$lib/components/ui/button";
  import { Spinner } from "$lib/components/ui/spinner";
  import ErrorState from "./ErrorState.svelte";
  import Loading from "./Loading.svelte";
  import type { TableSource } from "$lib/load.svelte";
  import { go } from "$lib/router.svelte";
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
  }: {
    source: TableSource<T>;
    columns: Column[];
    /** Accessible name of the table. */
    caption: string;
    key: (item: T) => string | number;
    /** The address a row opens ("#/..."). */
    href?: (item: T) => string | undefined;
    selected?: (item: T) => boolean;
    /** The row's cells (<Cell>, or <Table.Cell> for custom content). */
    row: Snippet<[T]>;
    /** Shown in place of the table when it has no rows: a CompactEmpty. */
    empty?: Snippet;
    emptyText?: string;
  } = $props();

  function open(e: MouseEvent, item: T) {
    const to = href?.(item);
    if (!to || e.defaultPrevented || e.button !== 0) return;
    const t = e.target as HTMLElement | null;
    if (t?.closest("a, button, input, select, textarea, label, [role=checkbox], [role=switch], [role=menuitem], [role=combobox]")) return;
    if (window.getSelection()?.toString()) return;
    if (e.metaKey || e.ctrlKey) window.open(to, "_blank");
    else if (to.startsWith("#/")) go(to);
    else window.location.href = to;
  }

  // One row look: a single line of fixed height, rounded ends like the sidebar items, stripes on
  // every second row, and a fill under the pointer.
  const rowClass =
    "group/row h-row border-0 even:bg-stripe hover:bg-hover data-[state=selected]:bg-selected [&>td]:py-0 [&>td:first-child]:rounded-l-lg [&>td:last-child]:rounded-r-lg";
</script>

{#if !source.loading && !source.error && source.items.length === 0}
  <div data-slot="table-empty">{#if empty}{@render empty()}{:else}<p class="py-3 text-body text-muted-foreground">{emptyText}</p>{/if}</div>
{:else}
  <Table.Root class="text-body">
    <Table.Caption class="sr-only">{caption}</Table.Caption>
    <Table.Header class="[&_tr]:border-b">
      <Table.Row class="border-b hover:bg-transparent">
        {#each columns as c, i (i)}
          <Table.Head class={cn("h-9 text-xs font-medium text-muted-foreground", c.align === "right" && "text-right", c.grow && "w-full", c.hideOnPhone && "max-sm:hidden", c.class)}>
            {#if c.label}{c.label}{:else if c.srLabel}<span class="sr-only">{c.srLabel}</span>{/if}
          </Table.Head>
        {/each}
      </Table.Row>
    </Table.Header>
    <Table.Body>
      {#if source.loading && source.items.length === 0}
        <Table.Row class="border-0 hover:bg-transparent">
          <Table.Cell colspan={columns.length} class="p-0"><Loading rows={4} /></Table.Cell>
        </Table.Row>
      {:else if source.error && source.items.length === 0}
        <Table.Row class="border-0 hover:bg-transparent">
          <Table.Cell colspan={columns.length} class="py-4 whitespace-normal">
            <div class="flex flex-wrap items-center gap-3">
              <ErrorState inline error={source.error} />
              {#if source.reload}<Button variant="outline" size="sm" onclick={source.reload}>Try again</Button>{/if}
            </div>
          </Table.Cell>
        </Table.Row>
      {:else}
        {#each source.items as item (key(item))}
          {@const on = selected?.(item) ?? false}
          <Table.Row
            data-state={on ? "selected" : undefined}
            aria-selected={selected ? on : undefined}
            class={cn(rowClass, href && "cursor-pointer")}
            onclick={href ? (e: MouseEvent) => open(e, item) : undefined}
          >
            {@render row(item)}
          </Table.Row>
        {/each}
      {/if}
    </Table.Body>
  </Table.Root>
{/if}

{#if source.items.length > 0 && source.error}
  <div class="mt-3"><ErrorState inline error={source.error} /></div>
{/if}
{#if source.hasMore && source.items.length > 0 && source.more}
  <div class="mt-4 flex justify-center">
    <Button variant="outline" size="sm" disabled={source.loadingMore} onclick={source.more}>
      {#if source.loadingMore}<Spinner />{/if}
      Load more
    </Button>
  </div>
{/if}
