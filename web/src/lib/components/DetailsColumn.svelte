<!--
  The details of the selected row, in a 360px column on the right of the table, inside the page
  (never an overlay). Under 1024px it stacks above the table. Close calls onClose; the page decides what it shows next.

    <div class="flex flex-col gap-6 lg:flex-row lg:items-start">
      <div class="min-w-0 flex-1"><DataTable ... /></div>
      {#if selected}
        <DetailsColumn title={selected.name} onClose={() => navigate(folderPath)}>
          ...facts...
          {#snippet actions()}<Button href={url}>Download</Button>{/snippet}
        </DetailsColumn>
      {/if}
    </div>
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import { Button } from "$lib/components/ui/button";
  import XIcon from "@lucide/svelte/icons/x";

  let {
    title,
    onClose,
    actions,
    children,
  }: {
    title: string;
    onClose: () => void;
    actions?: Snippet;
    children: Snippet;
  } = $props();
</script>

<aside
  aria-label="Details of {title}"
  class="order-first flex w-full shrink-0 flex-col rounded-lg bg-panel lg:sticky lg:top-22 lg:order-none lg:max-h-[calc(100svh-7rem)] lg:w-[360px] lg:overflow-y-auto"
>
  <div class="flex items-start gap-2 px-5 pt-4">
    <h2 class="min-w-0 flex-1 pt-1 font-semibold break-all">{title}</h2>
    <Button variant="ghost" size="icon-sm" class="-mr-2" aria-label="Close the details" onclick={onClose}><XIcon /></Button>
  </div>
  <div class="flex flex-col gap-6 px-5 pt-4 pb-5">{@render children()}</div>
  {#if actions}<div class="flex flex-wrap gap-2 border-t px-5 py-4">{@render actions()}</div>{/if}
</aside>
