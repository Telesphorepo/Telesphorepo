<!-- The Browse area: header, then the page in the page width. -->
<script lang="ts">
  import type { Snippet } from "svelte";
  import AppHeader from "./AppHeader.svelte";

  let { children }: { children: Snippet } = $props();
</script>

<div class="flex min-h-svh flex-col">
  <AppHeader />
  <main id="main" class="mx-auto w-full max-w-page flex-1 px-4 py-8 sm:px-6 sm:py-10">
    {@render children()}
  </main>
</div>
