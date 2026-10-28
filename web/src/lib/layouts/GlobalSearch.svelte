<!--
  The search field of the top bar. Enter opens the Search page with the query; Ctrl+K (⌘K on a
  Mac) anywhere focuses the field, also from another field, and the field shows the key of the
  platform. On the Search page the field shows the current query, so the page needs no second
  search box; on any other page it is empty. `large` is the big field of Home: the same search,
  taller and at most 672px wide. Home has no top bar field, so Ctrl+K focuses the large one.
-->
<script lang="ts">
  import * as InputGroup from "$lib/components/ui/input-group";
  import { Kbd, KbdGroup } from "$lib/components/ui/kbd";
  import SearchIcon from "@lucide/svelte/icons/search";
  import { navigate, router } from "$lib/router.svelte";
  import { cn } from "$lib/utils";

  let { large = false }: { large?: boolean } = $props();

  let input = $state<HTMLInputElement | null>(null);
  let q = $state("");

  const platform = (navigator as Navigator & { userAgentData?: { platform?: string } }).userAgentData?.platform || navigator.platform || navigator.userAgent;
  const mac = /mac|iphone|ipad/i.test(platform);

  $effect(() => {
    // Elsewhere the field is empty, so it never shows a search the page is not about.
    q = router.path === "/search" ? (router.query.get("q") ?? "") : "";
  });

  function submit(e: SubmitEvent) {
    e.preventDefault();
    const repo = router.path === "/search" ? (router.query.get("repo") ?? undefined) : undefined;
    navigate("/search", { query: { q: q.trim(), repo } });
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.key.toLowerCase() !== "k" || e.altKey || e.shiftKey || (mac ? !e.metaKey || e.ctrlKey : !e.ctrlKey || e.metaKey)) return;
    e.preventDefault();
    input?.focus();
    input?.select();
  }
</script>

<svelte:window {onkeydown} />

<form role="search" aria-label={large ? "Find a file" : "Search"} class={cn("w-full", large && "max-w-2xl")} onsubmit={submit}>
  <InputGroup.Root class={cn(large && "h-12 px-2 [&_svg]:size-5")}>
    <InputGroup.Addon><SearchIcon /></InputGroup.Addon>
    <InputGroup.Input
      bind:ref={input}
      bind:value={q}
      type="search"
      placeholder={large ? "Search files by path" : "Search files"}
      aria-label="Search files"
      aria-keyshortcuts={mac ? "Meta+K" : "Control+K"}
    />
    <InputGroup.Addon align="inline-end">
      <KbdGroup aria-hidden="true">{#if mac}<Kbd>⌘</Kbd><Kbd>K</Kbd>{:else}<Kbd>Ctrl</Kbd><Kbd>K</Kbd>{/if}</KbdGroup>
    </InputGroup.Addon>
  </InputGroup.Root>
</form>
