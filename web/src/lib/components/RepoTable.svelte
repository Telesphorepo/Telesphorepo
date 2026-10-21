<!--
  The table of repositories that people manage (Settings › Repositories; Browse home lists them
  with RepoList): name and description on one line,
  format, files, size and last publish. A private repository has a lock after its name; a public
  one has no mark. A group or a proxy has its kind icon after the name; a group counts each file it
  serves once, and shows Counting… until the server has counted it. On a phone the format icon sits before the name. A
  click anywhere on a row opens `href`. With `bind:picked` and a `bulk` snippet (All
  repositories), every row gets a checkbox for bulk actions (DataTable).
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import * as Table from "$lib/components/ui/table";
  import DataTable from "./DataTable.svelte";
  import Cell from "./Cell.svelte";
  import FormatBadge from "./FormatBadge.svelte";
  import FormatIcon from "./FormatIcon.svelte";
  import FileSize from "./FileSize.svelte";
  import RelativeTime from "./RelativeTime.svelte";
  import type { Repo } from "$lib/api";
  import type { TableSource } from "$lib/load.svelte";
  import { formatCount } from "$lib/format";
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
    { label: "Files", align: "right", hideOnPhone: true },
    { label: "Size", align: "right", hideOnPhone: true },
    { label: "Last publish", align: "right" },
  ]}
>
  {#snippet row(r)}
    <Table.Cell class="w-full max-w-0 min-w-40">
      <span class="flex min-w-0 items-center gap-3" title={r.description || undefined}>
        <FormatIcon format={r.format} class="text-muted-foreground sm:hidden" />
        <span class="flex shrink-0 items-center gap-1.5">
          <a href={href(r)} class="rounded-sm font-medium outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50">{r.name}</a>
          {#if !r.anonymous_read}<span class="flex text-muted-foreground" title="Private: only people with access can download"><LockIcon aria-hidden="true" class="size-3.5" /><span class="sr-only">Private</span></span>{/if}
          <RepoKindIcon kind={r.kind} />
        </span>
        {#if r.description}<span class="min-w-0 truncate text-muted-foreground">{r.description}</span>{/if}
      </span>
    </Table.Cell>
    <Table.Cell class="max-sm:hidden"><FormatBadge format={r.format} title={titles[r.format]} /></Table.Cell>
    {#if r.counts_pending}
      <!-- A group whose files the server has not counted for this reader yet. -->
      <Cell num muted hideOnPhone>Counting…</Cell>
      <Cell num hideOnPhone />
      <Cell num muted />
    {:else}
      <Cell num hideOnPhone>{formatCount(r.file_count)}</Cell>
      <Cell num hideOnPhone><FileSize bytes={r.total_size} /></Cell>
      <Cell num muted><RelativeTime at={r.last_published_at} empty="Never" /></Cell>
    {/if}
  {/snippet}
</DataTable>
