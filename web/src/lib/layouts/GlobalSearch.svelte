<!--
  The header search. Enter opens the Search page with the query; "/" anywhere focuses the field.
  On narrow screens it is an icon that opens the Search page.
-->
<script lang="ts">
  import * as InputGroup from "$lib/components/ui/input-group";
  import { Button } from "$lib/components/ui/button";
  import { Kbd } from "$lib/components/ui/kbd";
  import SearchIcon from "@lucide/svelte/icons/search";
  import { href, navigate, router } from "$lib/router.svelte";

  let input = $state<HTMLInputElement | null>(null);
  let q = $state("");

  // On the Search page the field shows the current query.
  $effect(() => {
    if (router.path === "/search") q = router.query.get("q") ?? "";
  });

  function submit(e: SubmitEvent) {
    e.preventDefault();
    navigate("/search", { query: { q: q.trim() } });
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

<form role="search" class="hidden w-full max-w-md sm:block" onsubmit={submit}>
  <InputGroup.Root>
    <InputGroup.Addon><SearchIcon /></InputGroup.Addon>
    <InputGroup.Input
      bind:ref={input}
      bind:value={q}
      type="search"
      placeholder="Search files in all repositories"
      aria-label="Search files"
    />
    <InputGroup.Addon align="inline-end"><Kbd>/</Kbd></InputGroup.Addon>
  </InputGroup.Root>
</form>
<Button variant="ghost" size="icon" class="sm:hidden" href={href("/search")} aria-label="Search">
  <SearchIcon />
</Button>
