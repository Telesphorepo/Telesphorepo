<!--
  The table of repositories that people manage (Settings › Repositories; Browse has its own
  table): the name and the description on one line, the format and the last publish. A
  private repository has a lock after its name; a public one has no mark. A group or a proxy has
  its kind icon after the name. A group's last publish is that of the files it serves, and
  Counting… until the server has counted them. The files and the size of a repository are on its
  Overview. The description shows only where at least 8rem of it fits; the row's tooltip holds it.
  On a phone the format icon sits before the name, and the last publish is the meta line. A click anywhere on a row opens
  `href`. With `bind:picked` and a `bulk` snippet (All
  repositories), every row gets a checkbox for bulk actions (DataTable).
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import * as Table from "$lib/components/ui/table";
  import DataTable from "./DataTable.svelte";
  import Cell from "./Cell.svelte";
  import FormatBadge from "./FormatBadge.svelte";
  import FormatIcon from "./FormatIcon.svelte";
  import RelativeTime from "./RelativeTime.svelte";
  import type { Repo } from "$lib/api";
  import type { TableSource } from "$lib/load.svelte";
  import RepoKindIcon from "./RepoKindIcon.svelte";
  import LockIcon from "@lucide/svelte/icons/lock";

  let {
    source,
    href,
    titles = {},
    empty,
    picked = $bindable(new Set<string>()),
    pickable,
    bulk,
  }: {
    source: TableSource<Repo>;
    href: (r: Repo) => string;
    /** Format titles by name (from /api/formats). */
    titles?: Record<string, string>;
    empty?: Snippet;
    /** The names of the repositories picked for a bulk action. */
    picked?: Set<string>;
    /** Which rows can be picked; undefined: every row. */
    pickable?: (r: Repo) => boolean;
    /** The bulk actions; with it, every row gets a checkbox. */
    bulk?: Snippet;
  } = $props();
</script>

<DataTable
  {source}
  {href}
  {empty}
  {bulk}
  bind:picked
  {pickable}
  pickLabel={(r) => r.name}
  caption="Repositories"
  key={(r) => r.name}
  columns={[
    { label: "Name", grow: true },
    { label: "Format", hideOnPhone: true },
    { label: "Last publish", align: "right" },
  ]}
>
  {#snippet row(r)}
    <!-- The description shows only where at least 8rem of it fits: one that does not wraps to a second
         line, which the cell clips. The padding inside the clip leaves room for the focus ring. -->
    <Table.Cell data-grow class="w-full max-w-0 min-w-40">
      <span class="-mx-1 flex h-[calc(1lh+0.5rem)] min-w-0 flex-wrap content-start items-center gap-x-3 gap-y-4 overflow-hidden p-1" title={r.description || undefined}>
        <FormatIcon format={r.format} class="text-muted-foreground sm:hidden" />
        <span class="flex shrink-0 items-center gap-1.5">
          <a href={href(r)} class="rounded-sm font-medium outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50">{r.name}</a>
          {#if !r.anonymous_read}<span class="flex text-muted-foreground" title="Private: only people with access can download"><LockIcon aria-hidden="true" class="size-3.5" /><span class="sr-only">Private</span></span>{/if}
          <RepoKindIcon kind={r.kind} />
        </span>
        {#if r.description}<span class="min-w-32 flex-1 basis-32 truncate text-muted-foreground">{r.description}</span>{/if}
      </span>
    </Table.Cell>
    <Table.Cell class="max-sm:hidden"><FormatBadge format={r.format} title={titles[r.format]} /></Table.Cell>
    {#if r.counts_pending}
      <!-- A group whose files the server has not counted for this reader yet. -->
      <Cell num muted>Counting…</Cell>
    {:else}
      <Cell num muted><RelativeTime at={r.last_published_at} empty="Never" /></Cell>
    {/if}
  {/snippet}
</DataTable>
