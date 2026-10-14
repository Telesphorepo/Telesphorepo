<!--
  The footer of a keyset list: "Load more" while the server has more pages, and the error of a
  failed page. Lists never use page numbers (the API pages by key, not by offset).
-->
<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import { Spinner } from "$lib/components/ui/spinner";
  import ErrorState from "./ErrorState.svelte";

  /** The parts of a Keyset this footer reads. */
  type Paged = {
    loading: boolean;
    loadingMore: boolean;
    hasMore: boolean;
    error: string;
    items: readonly unknown[];
    more: () => unknown;
  };

  let { list }: { list: Paged } = $props();
</script>

{#if !list.loading && list.items.length > 0 && list.error}
  <div class="mt-3"><ErrorState inline error={list.error} /></div>
{/if}
{#if list.hasMore && !list.loading}
  <div class="mt-4 flex justify-center">
    <Button variant="outline" size="sm" disabled={list.loadingMore} onclick={list.more}>
      {#if list.loadingMore}<Spinner />{/if}
      Load more
    </Button>
  </div>
{/if}
