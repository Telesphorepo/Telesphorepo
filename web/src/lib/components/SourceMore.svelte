<!--
  What follows the rows of a collection (DataTable, ItemList, SourceGrid, Search): the error of a
  request that failed while rows show (a reload, a following page), and for a keyset the way to
  the next page: "Load more", or with `auto` the next page on its own once the end of the rows
  comes within a screen of the view, with a spinner while it loads. After an error the page waits
  for Try again.
-->
<script lang="ts" generics="T">
  import { Button } from "$lib/components/ui/button";
  import { Spinner } from "$lib/components/ui/spinner";
  import ErrorState from "./ErrorState.svelte";
  import type { TableSource } from "$lib/load.svelte";

  let { source, auto = false }: { source: TableSource<T>; auto?: boolean } = $props();

  let end = $state<HTMLElement | null>(null);
  let near = $state(false);
  $effect(() => {
    if (!auto || !end) return;
    const io = new IntersectionObserver((es) => (near = es.some((e) => e.isIntersecting)), { rootMargin: "100% 0px" });
    io.observe(end);
    return () => io.disconnect();
  });
  // A page that leaves the end in view asks for the next one too.
  $effect(() => {
    if (auto && near && source.hasMore && !source.loadingMore && !source.error && source.items.length > 0) source.more?.();
  });
</script>

{#if source.items.length > 0 && source.error}
  <div class="mt-3 flex flex-wrap items-center gap-3">
    <ErrorState inline error={source.error} />
    {#if auto && source.hasMore}<Button variant="outline" size="sm" onclick={source.more}>Try again</Button>{/if}
  </div>
{/if}
{#if auto}
  <div bind:this={end} class="flex min-h-px justify-center">
    {#if source.loadingMore}<div class="mt-6"><Spinner /><span class="sr-only">Loading more</span></div>{/if}
  </div>
{:else if source.hasMore && source.items.length > 0 && source.more}
  <div class="mt-6 flex justify-center">
    <Button variant="outline" size="sm" disabled={source.loadingMore} onclick={source.more}>
      {#if source.loadingMore}<Spinner />{/if}
      Load more
    </Button>
  </div>
{/if}
