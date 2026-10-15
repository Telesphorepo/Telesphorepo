<!--
  What a page or a table shows when it has nothing yet. Say what is missing and, in the children,
  give the action that fills it ("New read token"). Inside a DataTable, pass it as `empty`.
-->
<script lang="ts">
  import type { Component, Snippet } from "svelte";
  import InboxIcon from "@lucide/svelte/icons/inbox";

  let {
    title,
    description,
    icon: Icon = InboxIcon,
    children,
  }: {
    title: string;
    description?: string;
    icon?: Component<{ class?: string }>;
    children?: Snippet;
  } = $props();
</script>

<div class="flex flex-col items-center px-4 py-12 text-center">
  <span aria-hidden="true" class="mb-3 flex text-muted-foreground"><Icon class="size-6" /></span>
  <p class="text-label">{title}</p>
  {#if description}<p class="mt-1 max-w-md text-body text-balance text-muted-foreground">{description}</p>{/if}
  {#if children}<div class="mt-5 flex flex-wrap justify-center gap-2">{@render children()}</div>{/if}
</div>
