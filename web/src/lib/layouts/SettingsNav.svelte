<!-- The Settings sidebar. It must render inside <Sidebar.Provider> (SettingsLayout). -->
<script lang="ts">
  import * as Sidebar from "$lib/components/ui/sidebar";
  import { useSidebar } from "$lib/components/ui/sidebar";
  import { settingsNav, setupOnlyPaths, type NavItem } from "$lib/nav";
  import { auth } from "$lib/auth.svelte";
  import { href, isActive, router } from "$lib/router.svelte";
  import type { Component } from "svelte";

  const sidebar = useSidebar();

  const categories = $derived(
    settingsNav
      .filter((c) => !c.admin || auth.isAdmin)
      .map((c) => (auth.setupOnly ? { ...c, items: c.items.filter((i) => setupOnlyPaths.includes(i.path)) } : c))
      .filter((c) => c.items.length > 0),
  );

  // Close the mobile sheet after a navigation.
  $effect(() => {
    void router.path;
    sidebar.setOpenMobile(false);
  });
</script>

<!-- Every link text starts at the same edge: a page under a category heading is indented by the
     width of the category icon, and a single-page category shows its icon in the link. -->
{#snippet link(item: NavItem, label: string, Icon: Component<{ class?: string }> | undefined)}
  <Sidebar.MenuItem>
    <Sidebar.MenuButton isActive={isActive(item.path)} class={Icon ? "" : "pl-9"}>
      {#snippet child({ props })}
        <a href={href(item.path)} {...props} aria-current={isActive(item.path) ? "page" : undefined}>
          {#if Icon}<Icon />{/if}
          <span>{label}</span>
        </a>
      {/snippet}
    </Sidebar.MenuButton>
  </Sidebar.MenuItem>
{/snippet}

<Sidebar.Root class="top-14 h-[calc(100svh-3.5rem)]" collapsible="offcanvas">
  <Sidebar.Content class="py-4">
    <nav aria-label="Settings" class="flex flex-col gap-1">
      {#each categories as cat (cat.label)}
        <Sidebar.Group class="py-1">
          {#if cat.items.length > 1}
            <Sidebar.GroupLabel class="gap-2 text-sm text-sidebar-foreground/70">
              <cat.icon />
              {cat.label}
            </Sidebar.GroupLabel>
            <Sidebar.GroupContent>
              <Sidebar.Menu>
                {#each cat.items as item (item.path)}
                  {@render link(item, item.label, undefined)}
                {/each}
              </Sidebar.Menu>
            </Sidebar.GroupContent>
          {:else}
            <Sidebar.Menu>
              {@render link(cat.items[0], cat.label, cat.icon)}
            </Sidebar.Menu>
          {/if}
        </Sidebar.Group>
      {/each}
    </nav>
  </Sidebar.Content>
</Sidebar.Root>
