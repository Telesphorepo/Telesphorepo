<!--
  The row above a table: filters on the left, the page's main action on the right (`end`).

    <Toolbar>
      <InputGroup.Root class="sm:max-w-xs">...</InputGroup.Root>
      {#snippet end()}<Button href={...}><PlusIcon />New repository</Button>{/snippet}
    </Toolbar>
-->
<script lang="ts">
  import type { Snippet } from "svelte";

  let { children, end }: { children?: Snippet; end?: Snippet } = $props();
</script>

<div class="mb-(--toolbar-gap) flex flex-col gap-2 sm:flex-row sm:flex-wrap sm:items-center">
  {#if children}{@render children()}{/if}
  {#if end}<div class="flex items-center gap-2 sm:ml-auto">{@render end()}</div>{/if}
</div>
