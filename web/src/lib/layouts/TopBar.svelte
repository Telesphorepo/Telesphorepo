<!--
  The top bar, inside the page container: logo and name (Browse home) on the left, search in the
  center (max 480px), and on the right one Sign in button when signed out, or Settings and the
  avatar menu when signed in. On the sign-in page the Sign in button is hidden: the page is the
  one place to sign in.
-->
<script lang="ts">
  import Logo from "$lib/components/Logo.svelte";
  import { Button } from "$lib/components/ui/button";
  import GlobalSearch from "./GlobalSearch.svelte";
  import AccountMenu from "./AccountMenu.svelte";
  import { auth } from "$lib/auth.svelte";
  import { href, isActive, router } from "$lib/router.svelte";
  import { cn } from "$lib/utils";
  import MenuIcon from "@lucide/svelte/icons/menu";
  import SettingsIcon from "@lucide/svelte/icons/settings";
  import SearchIcon from "@lucide/svelte/icons/search";

  let { onMenu }: { /** Opens the Settings menu under 768px; set only in Settings. */ onMenu?: () => void } = $props();

  const inSettings = $derived(isActive("/settings"));
  const next = $derived(router.path + (router.search ? "?" + router.search : ""));
</script>

<header class="sticky top-0 z-30 border-b bg-background/95 backdrop-blur supports-[backdrop-filter]:bg-background/80">
  <div class="mx-auto grid h-14 w-full max-w-page grid-cols-[1fr_auto] items-center gap-4 px-4 sm:grid-cols-[1fr_minmax(0,480px)_1fr] sm:px-6">
    <div class="flex min-w-0 items-center gap-2">
      {#if onMenu}
        <Button variant="ghost" size="icon" class="-ml-2 md:hidden" aria-label="Open the settings menu" onclick={onMenu}>
          <MenuIcon />
        </Button>
      {/if}
      <a
        href={href("/")}
        class="-ml-1 flex items-center gap-2 rounded-lg px-1 py-1 font-semibold tracking-tight outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
      >
        <!-- 48 px wide: the brand scales the flat mark in multiples of 16 px wide. -->
        <Logo size={30} />
        <span class="text-base">Telesphorepo</span>
      </a>
    </div>

    <div class="hidden sm:block"><GlobalSearch /></div>

    <div class="flex items-center justify-end gap-1">
      <Button variant="ghost" size="icon" class="sm:hidden" href={href("/search")} aria-label="Search">
        <SearchIcon />
      </Button>
      {#if auth.user}
        <a
          href={href("/settings")}
          aria-current={inSettings ? "page" : undefined}
          aria-label="Settings"
          class={cn(
            "flex h-9 items-center gap-2 rounded-lg px-3 text-sm font-medium outline-none transition-colors focus-visible:ring-[3px] focus-visible:ring-ring/50 [&_svg]:size-4",
            inSettings ? "bg-selected text-selected-foreground" : "hover:bg-hover",
          )}
        >
          <SettingsIcon />
          <span class="hidden md:inline">Settings</span>
        </a>
        <AccountMenu />
      {:else if router.path !== "/login"}
        <Button href={href("/login", { next: next === "/" ? undefined : next })}>Sign in</Button>
      {/if}
    </div>
  </div>
</header>
