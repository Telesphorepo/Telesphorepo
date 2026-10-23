<!--
  The sidebar of a page with one: `area` "settings" (the admin panel) or "account" (Your account).
  Settings: All settings (the Settings overview) first, above the groups, for a person who would
  see a card there; then the groups Repositories, People and CI, Server. Your account: its one
  group. The groups and pages come from the UI modules (web/src/modules), a heading per group, an
  icon on every item. Groups are separated by space, never by a line, so no line in the sidebar
  nearly meets a line in the content. The open page (or a page below it) fills its whole item.
  Each item shows only to a person who may open its page, and a group without items is hidden; a
  setup-only session sees only the pages it may open.
-->
<script lang="ts">
  import type { Component } from "svelte";
  import type { SidebarArea } from "../../modules/types";
  import { href, isActive, router } from "$lib/router.svelte";
  import { cn } from "$lib/utils";
  import { canOpen, navPlace, visibleNav } from "../../modules";
  import LayoutGridIcon from "@lucide/svelte/icons/layout-grid";

  let { area }: { area: SidebarArea } = $props();

  const groups = $derived(visibleNav(area, canOpen));
  const overview = $derived(area === "settings" && groups.length > 0);
  // The item the open page belongs to: the longest item path above it, so Overview (/account) is
  // not open on /account/devices.
  const open = $derived(navPlace(router.path)?.item.path);
</script>

{#snippet item(path: string, label: string, Icon: Component<{ class?: string }>)}
  {@const active = open ? open === path : isActive(path)}
  <a
    href={href(path)}
    aria-current={active ? "page" : undefined}
    class={cn(
      "flex h-8 items-center gap-3 rounded-lg px-3 text-sm outline-none transition-colors focus-visible:ring-[3px] focus-visible:ring-ring/50 [&_svg]:size-4 [&_svg]:shrink-0",
      active ? "bg-selected font-medium text-selected-foreground" : "text-foreground/85 hover:bg-hover hover:text-foreground",
    )}
  >
    <Icon />
    <span class="truncate">{label}</span>
  </a>
{/snippet}

<nav aria-label={area === "account" ? "Your account" : "Settings"} class="flex flex-col gap-6">
  {#if overview}{@render item("/settings/overview", "All settings", LayoutGridIcon)}{/if}
  {#each groups as g, gi (g.id)}
    <section aria-labelledby="{area}-nav-{gi}">
      <h2 id="{area}-nav-{gi}" class="px-3 pb-1.5 text-xs font-medium text-muted-foreground">{g.label}</h2>
      <ul class="flex flex-col gap-0.5">
        {#each g.items as p (p.path)}
          <li>{@render item(p.path, p.nav!.label, p.nav!.icon)}</li>
        {/each}
      </ul>
    </section>
  {/each}
</nav>
