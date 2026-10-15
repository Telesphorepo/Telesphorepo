<!--
  The Settings navigation (src/lib/nav.ts): a heading per group, an icon on every item. Groups are
  separated by space, never by a line, so no line in the sidebar nearly meets a line in the
  content. The open page (or a page below it) fills its whole item. Admin groups show only to
  admins; a setup-only session sees only the second-factor page.
-->
<script lang="ts">
  import { settingsNav, setupOnlyPaths } from "$lib/nav";
  import { auth } from "$lib/auth.svelte";
  import { href, isActive } from "$lib/router.svelte";
  import { cn } from "$lib/utils";

  const groups = $derived(
    settingsNav
      .filter((g) => !g.admin || auth.isAdmin)
      .map((g) => (auth.setupOnly ? { ...g, items: g.items.filter((i) => setupOnlyPaths.includes(i.path)) } : g))
      .filter((g) => g.items.length > 0),
  );
</script>

<nav aria-label="Settings" class="flex flex-col gap-6">
  {#each groups as g, gi (g.label)}
    <section aria-labelledby="settings-nav-{gi}">
      <h2 id="settings-nav-{gi}" class="px-3 pb-1.5 text-xs font-medium text-muted-foreground">{g.label}</h2>
      <ul class="flex flex-col gap-0.5">
        {#each g.items as item (item.path)}
          {@const active = isActive(item.path)}
          <li>
            <a
              href={href(item.path)}
              aria-current={active ? "page" : undefined}
              class={cn(
                "flex h-8 items-center gap-3 rounded-lg px-3 text-sm outline-none transition-colors focus-visible:ring-[3px] focus-visible:ring-ring/50 [&_svg]:size-4 [&_svg]:shrink-0",
                active ? "bg-selected font-medium text-selected-foreground" : "text-foreground/85 hover:bg-hover hover:text-foreground",
              )}
            >
              <item.icon />
              <span class="truncate">{item.label}</span>
            </a>
          </li>
        {/each}
      </ul>
    </section>
  {/each}
</nav>
