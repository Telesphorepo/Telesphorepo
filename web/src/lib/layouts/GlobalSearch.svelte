<!--
  The search field of the top bar. Enter opens the Search page with the query; "/" anywhere
  focuses the field. On the Search page the field shows the current query, so the page needs no
  second search box; on any other page it is empty. `large` is the big field of Home: the same
  search, taller and at most 672px wide. Home has no top bar field, so "/" focuses the large one.
-->
<script lang="ts">
  import * as InputGroup from "$lib/components/ui/input-group";
  import { Kbd } from "$lib/components/ui/kbd";
  import SearchIcon from "@lucide/svelte/icons/search";
  import { navigate, router } from "$lib/router.svelte";
  import { cn } from "$lib/utils";

  let { large = false }: { large?: boolean } = $props();

  let input = $state<HTMLInputElement | null>(null);
  let q = $state("");

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
    if (e.key !== "/" || e.ctrlKey || e.metaKey || e.altKey) return;
    const t = e.target as HTMLElement | null;
    if (t && (t.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(t.tagName))) return;
    e.preventDefault();
    input?.focus();
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
    />
    <InputGroup.Addon align="inline-end"><Kbd>/</Kbd></InputGroup.Addon>
  </InputGroup.Root>
</form>
