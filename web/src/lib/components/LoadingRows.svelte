<!--
  Placeholder rows while a list or a page loads. Inside a <Table.Body>, pass `columns` to render
  table rows; otherwise it renders stacked bars.
-->
<script lang="ts">
  import { Skeleton } from "$lib/components/ui/skeleton";
  import * as Table from "$lib/components/ui/table";

  let { rows = 3, columns = 0 }: { rows?: number; columns?: number } = $props();
  // Bars of varying width read as text, not as a grid of boxes.
  const widths = ["w-3/5", "w-2/5", "w-4/5", "w-1/2", "w-3/4"];
</script>

{#if columns > 0}
  {#each { length: rows }, r (r)}
    <Table.Row aria-hidden="true">
      {#each { length: columns }, c (c)}
        <Table.Cell><Skeleton class="h-4 {widths[(r + c) % widths.length]}" /></Table.Cell>
      {/each}
    </Table.Row>
  {/each}
{:else}
  <div class="flex flex-col gap-3" role="status" aria-label="Loading">
    {#each { length: rows }, r (r)}
      <Skeleton class="h-5 {widths[r % widths.length]}" />
    {/each}
  </div>
{/if}
