<!--
  The selection bar of a collection with bulk actions (DataTable, ItemList): "12 selected", the
  `bulk` actions, "Select all 50" and "Clear". The collection places it (over the column headings
  of a table, in the head row of a list); on a phone it scrolls sideways when the actions do not fit.
-->
<script lang="ts" generics="T, K">
  import type { Snippet } from "svelte";
  import { Button } from "$lib/components/ui/button";
  import type { Pick } from "$lib/pick.svelte";
  import { formatCount } from "$lib/format";
  import { selectedText } from "$lib/words";
  import { cn } from "$lib/utils";

  let {
    pick,
    bulk,
    pickOff = false,
    selectAllLabel,
    class: className,
  }: {
    pick: Pick<T, K>;
    bulk?: Snippet;
    pickOff?: boolean;
    selectAllLabel?: string;
    class?: string;
  } = $props();
</script>

<div
  role="toolbar"
  aria-label="Actions for the selected rows"
  class={cn("flex items-center gap-2 overflow-x-auto pr-1 text-body font-normal text-foreground [scrollbar-width:none] max-sm:[mask-image:linear-gradient(to_right,black_85%,transparent)]", className)}
>
  <span class="shrink-0 font-medium tabular-nums">{selectedText(pick.size)}</span>
  {@render bulk?.()}
  <span class="ml-auto flex shrink-0 items-center gap-1">
    {#if !pick.all && !pickOff}<Button variant="ghost" size="sm" onclick={pick.pickAll}>{selectAllLabel ?? `Select all ${formatCount(pick.rows.length)}`}</Button>{/if}
    {#if !pickOff}<Button variant="ghost" size="sm" onclick={pick.clear}>Clear</Button>{/if}
  </span>
</div>
