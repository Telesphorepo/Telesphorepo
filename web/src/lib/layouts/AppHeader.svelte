<!-- The header of the Browse and Settings areas: logo and name, global search, account. -->
<script lang="ts">
  import type { Snippet } from "svelte";
  import Logo from "$lib/components/Logo.svelte";
  import GlobalSearch from "./GlobalSearch.svelte";
  import AccountMenu from "./AccountMenu.svelte";
  import { href } from "$lib/router.svelte";
  import { cn } from "$lib/utils";

  let { start, wide = false }: { start?: Snippet; /** Full width (Settings) instead of the page width. */ wide?: boolean } =
    $props();
</script>

<header class="sticky top-0 z-30 h-14 border-b bg-background/90 backdrop-blur supports-[backdrop-filter]:bg-background/75">
  <div class={cn("mx-auto flex h-full items-center gap-3 px-4 sm:px-6", !wide && "max-w-page")}>
    {#if start}{@render start()}{/if}
    <a
      href={href("/")}
      class="-ml-1 flex shrink-0 items-center gap-2 rounded-lg px-1 py-1 font-semibold tracking-tight outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
    >
      <Logo size={28} />
      <span class="text-[0.9375rem]">Telesphorepo</span>
    </a>
    <div class="flex flex-1 justify-center px-2"><GlobalSearch /></div>
    <div class="flex shrink-0 items-center gap-1"><AccountMenu /></div>
  </div>
</header>
