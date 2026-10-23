<!--
  The top bar, inside the page container: the server's mark and name (Home) on the left, search in the
  center (max 480px; a search icon under 640px), and on the right one Sign in button when signed
  out, or Settings (for a person who can open an admin page) and the avatar menu when signed in.
  The search shows on the Browse pages only: not on Home, which has its large search field, and
  not on the Settings and account pages. On the sign-in page the Sign in button is hidden: the
  page is the one place to sign in.
-->
<script lang="ts">
  import ServerMark from "$lib/components/ServerMark.svelte";
  import { brand } from "$lib/brand.svelte";
  import { Button } from "$lib/components/ui/button";
  import GlobalSearch from "./GlobalSearch.svelte";
  import AccountMenu from "./AccountMenu.svelte";
  import { auth } from "$lib/auth.svelte";
  import { href, isActive, router } from "$lib/router.svelte";
  import { cn } from "$lib/utils";
  import { adminNav } from "../../modules";
  import type { SidebarArea } from "../../modules/types";
  import MenuIcon from "@lucide/svelte/icons/menu";
  import SettingsIcon from "@lucide/svelte/icons/settings";
  import SearchIcon from "@lucide/svelte/icons/search";

  let {
    sidebar,
    onMenu,
  }: {
    /** The sidebar of the open page, if it has one. */
    sidebar?: SidebarArea;
    /** Opens the sidebar menu under 768px; set only on a page with a sidebar. */
    onMenu?: () => void;
  } = $props();

  const inSettings = $derived(isActive("/settings"));
  const admin = $derived(adminNav().length > 0);
  const search = $derived(router.path !== "/" && !sidebar);
  const next = $derived(router.path + (router.search ? "?" + router.search : ""));
</script>

<header class="sticky top-0 z-30 border-b bg-background/95 backdrop-blur supports-[backdrop-filter]:bg-background/80">
  <div class="mx-auto grid h-14 w-full max-w-page grid-cols-[1fr_auto] items-center gap-4 px-4 sm:grid-cols-[1fr_minmax(0,480px)_1fr] sm:px-6">
    <div class="flex min-w-0 items-center gap-2">
      {#if onMenu}
        <Button
          variant="ghost"
          size="icon"
          class="-ml-2 md:hidden"
          aria-label={sidebar === "account" ? "Open the account menu" : "Open the settings menu"}
          onclick={onMenu}
        >
          <MenuIcon />
        </Button>
      {/if}
      <a
        href={href("/")}
        class="-ml-1 flex min-w-0 items-center gap-2 rounded-lg px-1 py-1 font-semibold tracking-tight outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
      >
        <!-- The flat mark is 48 px wide: the brand scales it in multiples of 16 px wide. -->
        <ServerMark size={30} />
        <span class="truncate text-base">{brand.name}</span>
      </a>
    </div>

    <div class="hidden sm:block">{#if search}<GlobalSearch />{/if}</div>

    <div class="flex items-center justify-end gap-1">
      {#if search}
        <Button variant="ghost" size="icon" class="sm:hidden" href={href("/search")} aria-label="Search">
          <SearchIcon />
        </Button>
      {/if}
      {#if auth.user}
        {#if admin}
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
        {/if}
        <AccountMenu />
      {:else if router.path !== "/login"}
        <Button href={href("/login", { next: next === "/" ? undefined : next })}>Sign in</Button>
      {/if}
    </div>
  </div>
</header>
