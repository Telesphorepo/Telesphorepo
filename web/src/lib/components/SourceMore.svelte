<!--
  What follows the rows of a collection (DataTable, ItemList, SourceGrid): the error of a request
  that failed while rows show (a reload, a following page), and "Load more" for a keyset.
-->
<script lang="ts" generics="T">
  import { Button } from "$lib/components/ui/button";
  import { Spinner } from "$lib/components/ui/spinner";
  import ErrorState from "./ErrorState.svelte";
  import type { TableSource } from "$lib/load.svelte";

  let { source }: { source: TableSource<T> } = $props();
</script>

{#if source.items.length > 0 && source.error}
  <div class="mt-3"><ErrorState inline error={source.error} /></div>
{/if}
{#if source.hasMore && source.items.length > 0 && source.more}
  <div class="mt-6 flex justify-center">
    <Button variant="outline" size="sm" disabled={source.loadingMore} onclick={source.more}>
      {#if source.loadingMore}<Spinner />{/if}
      Load more
    </Button>
  </div>
{/if}
