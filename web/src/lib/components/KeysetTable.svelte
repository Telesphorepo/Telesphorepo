<!--
  A full-width table over a Keyset list, with the loading, error, empty and "Load more" states.

    const tokens = new Keyset((p) => api.devices.list(p));
    <KeysetTable list={tokens} columns={3}>
      {#snippet head()}<Table.Head>Name</Table.Head>...{/snippet}
      {#snippet row(d)}<Table.Row><Table.Cell>{d.name}</Table.Cell>...</Table.Row>{/snippet}
      {#snippet empty()}<EmptyState title="No devices yet" />{/snippet}
    </KeysetTable>
-->
<script lang="ts" generics="T">
  import type { Snippet } from "svelte";
  import * as Table from "$lib/components/ui/table";
  import LoadingRows from "./LoadingRows.svelte";
  import ErrorState from "./ErrorState.svelte";
  import LoadMore from "./LoadMore.svelte";
  import type { Keyset } from "$lib/load.svelte";

  let {
    list,
    columns,
    head,
    row,
    empty,
    caption,
  }: {
    list: Keyset<T>;
    /** Number of columns, for the loading rows. */
    columns: number;
    head: Snippet;
    row: Snippet<[T, number]>;
    empty?: Snippet;
    /** Accessible name of the table (screen readers). */
    caption?: string;
  } = $props();
</script>

{#if list.error && list.items.length === 0 && !list.loading}
  <ErrorState error={list.error} onRetry={list.reload} />
{:else if !list.loading && list.items.length === 0 && empty}
  {@render empty()}
{:else}
  <div class="w-full overflow-x-auto rounded-2xl border">
    <Table.Root>
      {#if caption}<Table.Caption class="sr-only">{caption}</Table.Caption>{/if}
      <Table.Header>
        <Table.Row class="hover:bg-transparent">{@render head()}</Table.Row>
      </Table.Header>
      <Table.Body>
        {#if list.loading}
          <LoadingRows rows={4} {columns} />
        {:else}
          {#each list.items as item, i (i)}
            {@render row(item, i)}
          {/each}
        {/if}
      </Table.Body>
    </Table.Root>
  </div>
  <LoadMore {list} />
{/if}
