<!--
  The search field of the top bar. Enter opens the Search page with the query; "/" anywhere
  focuses the field. On the Search page the field shows the current query, so the page needs no
  second search box; on any other page it is empty.
-->
<script lang="ts">
  import * as InputGroup from "$lib/components/ui/input-group";
  import { Kbd } from "$lib/components/ui/kbd";
  import SearchIcon from "@lucide/svelte/icons/search";
  import { navigate, router } from "$lib/router.svelte";

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

<form role="search" class="w-full" onsubmit={submit}>
  <InputGroup.Root>
    <InputGroup.Addon><SearchIcon /></InputGroup.Addon>
    <InputGroup.Input bind:ref={input} bind:value={q} type="search" placeholder="Search files" aria-label="Search files" />
    <InputGroup.Addon align="inline-end"><Kbd>/</Kbd></InputGroup.Addon>
  </InputGroup.Root>
</form>
