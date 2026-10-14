<!--
  The top of every page: the title (the page's only h1), an optional description, and actions on
  the right. Put breadcrumbs above the title with the `breadcrumbs` snippet.
-->
<script lang="ts">
  import type { Snippet } from "svelte";

  let {
    title,
    description,
    breadcrumbs,
    actions,
    children,
  }: {
    title: string;
    description?: string;
    breadcrumbs?: Snippet;
    actions?: Snippet;
    /** Extra content under the description, e.g. badges. */
    children?: Snippet;
  } = $props();
</script>

<header class="mb-8 flex flex-col gap-3">
  {#if breadcrumbs}{@render breadcrumbs()}{/if}
  <div class="flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
    <div class="min-w-0 flex-1">
      <h1 class="text-2xl font-semibold tracking-tight text-balance break-words">{title}</h1>
      {#if description}
        <p class="mt-1.5 max-w-prose text-sm text-muted-foreground">{description}</p>
      {/if}
      {#if children}<div class="mt-3">{@render children()}</div>{/if}
    </div>
    {#if actions}
      <div class="flex shrink-0 flex-wrap items-center gap-2">{@render actions()}</div>
    {/if}
  </div>
</header>
