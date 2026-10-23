<!--
  The error of a collection's source that has no rows to show (DataTable, ItemList, SourceGrid):
  the error on one line and Try again.
-->
<script lang="ts" generics="T">
  import { Button } from "$lib/components/ui/button";
  import ErrorState from "./ErrorState.svelte";
  import type { TableSource } from "$lib/load.svelte";

  let { source }: { source: TableSource<T> } = $props();
</script>

<div class="flex flex-wrap items-center gap-3">
  <ErrorState inline error={source.error} />
  {#if source.reload}<Button variant="outline" size="sm" onclick={source.reload}>Try again</Button>{/if}
</div>
