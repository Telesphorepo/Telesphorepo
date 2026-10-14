<!-- Sign-in and device approval: the logo and one narrow centered column, no navigation. -->
<script lang="ts">
  import type { Snippet } from "svelte";
  import Logo from "$lib/components/Logo.svelte";
  import AccountMenu from "./AccountMenu.svelte";
  import { href } from "$lib/router.svelte";

  let { children }: { children: Snippet } = $props();
</script>

<div class="flex min-h-svh flex-col">
  <header class="flex h-14 items-center justify-between px-4 sm:px-6">
    <a href={href("/")} class="flex items-center gap-2 rounded-lg px-1 py-1 font-semibold tracking-tight outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50">
      <Logo size={28} />
      <span class="text-[0.9375rem]">Telesphorepo</span>
    </a>
    <div class="flex items-center gap-1"><AccountMenu /></div>
  </header>
  <main id="main" class="mx-auto flex w-full max-w-form flex-1 flex-col justify-center px-4 pt-6 pb-24">
    {@render children()}
  </main>
</div>
