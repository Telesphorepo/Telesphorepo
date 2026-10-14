<!--
  The header of the Browse and Settings areas: logo and name, the two areas (Repositories and,
  when signed in, Settings), global search, account. The open area is filled, never underlined.
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import Logo from "$lib/components/Logo.svelte";
  import GlobalSearch from "./GlobalSearch.svelte";
  import AccountMenu from "./AccountMenu.svelte";
  import { auth } from "$lib/auth.svelte";
  import { href, router } from "$lib/router.svelte";
  import { cn } from "$lib/utils";
  import FolderIcon from "@lucide/svelte/icons/folder-open";
  import SettingsIcon from "@lucide/svelte/icons/settings";

  let { start, wide = false }: { start?: Snippet; /** Full width (Settings) instead of the page width. */ wide?: boolean } =
    $props();

  const inSettings = $derived(router.path.startsWith("/settings"));
  const areas = $derived([
    { label: "Repositories", path: "/", icon: FolderIcon, active: !inSettings },
    ...(auth.user ? [{ label: "Settings", path: "/settings", icon: SettingsIcon, active: inSettings }] : []),
  ]);
</script>

<header class="sticky top-0 z-30 h-14 border-b bg-background/90 backdrop-blur supports-[backdrop-filter]:bg-background/75">
  <div class={cn("mx-auto flex h-full items-center gap-2 px-4 sm:gap-3 sm:px-6", !wide && "max-w-page")}>
    {#if start}{@render start()}{/if}
    <a
      href={href("/")}
      class="-ml-1 flex shrink-0 items-center gap-2 rounded-lg px-1 py-1 font-semibold tracking-tight outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
    >
      <Logo size={28} />
      <span class="hidden text-[0.9375rem] lg:inline">Telesphorepo</span>
    </a>
    <nav aria-label="Areas" class="flex shrink-0 items-center gap-1">
      {#each areas as a (a.path)}
        <a
          href={href(a.path)}
          aria-current={a.active ? "page" : undefined}
          title={a.label}
          class={cn(
            "flex h-9 items-center gap-2 rounded-lg px-2.5 text-sm font-medium outline-none transition-colors focus-visible:ring-[3px] focus-visible:ring-ring/50 [&_svg]:size-4",
            a.active ? "bg-selected text-selected-foreground" : "text-muted-foreground hover:bg-hover hover:text-foreground",
          )}
        >
          <a.icon />
          <span class="hidden sm:inline">{a.label}</span>
        </a>
      {/each}
    </nav>
    <div class="flex min-w-0 flex-1 justify-end px-1 sm:justify-center"><GlobalSearch /></div>
    <div class="flex shrink-0 items-center gap-1"><AccountMenu /></div>
  </div>
</header>
