<!--
  Tabs that are pages: each tab is a link with its own address (the tabs of a repository, the tabs
  of a repository's settings, Activity). The open tab fills its whole item, as in the sidebar. No
  line runs under the row: space separates the tabs from their content, so no rule in the content
  nearly meets a line of the sidebar. On a narrow screen the row scrolls sideways without a
  scrollbar, and the open tab scrolls into view.

    <TabNav label="Repository" items={[{ label: "Files", href: href(base), active: true, icon: FolderIcon }, ...]} />
-->
<script lang="ts">
  import type { Component } from "svelte";
  import { cn } from "$lib/utils";

  let {
    label,
    items,
  }: {
    /** Accessible name of the tab row. */
    label: string;
    items: { label: string; href: string; active: boolean; icon?: Component<{ class?: string }> }[];
  } = $props();

  let row = $state<HTMLElement | null>(null);

  // Keep the open tab in view when the row is wider than the screen.
  $effect(() => {
    const i = items.findIndex((t) => t.active);
    const el = row?.children[i] as HTMLElement | undefined;
    if (!row || !el || row.scrollWidth <= row.clientWidth) return;
    row.scrollLeft = Math.max(0, el.offsetLeft - row.offsetLeft - 16);
  });
</script>

<nav
  bind:this={row}
  aria-label={label}
  class="-mx-1 mb-8 flex gap-1 overflow-x-auto px-1 py-1 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
>
  {#each items as t (t.href)}
    <a
      href={t.href}
      aria-current={t.active ? "page" : undefined}
      class={cn(
        "flex h-9 shrink-0 items-center gap-2 rounded-lg px-3 text-sm font-medium outline-none transition-colors focus-visible:ring-[3px] focus-visible:ring-ring/50 [&_svg]:size-4",
        t.active ? "bg-selected text-selected-foreground" : "text-muted-foreground hover:bg-hover hover:text-foreground",
      )}
    >
      {#if t.icon}<t.icon />{/if}
      {t.label}
    </a>
  {/each}
</nav>
