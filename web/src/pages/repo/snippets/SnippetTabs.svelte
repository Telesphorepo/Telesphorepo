<!--
  Snippets with one tab per tool: Gradle (Kotlin) · Gradle (Groovy) · Maven, GitHub Actions ·
  Jenkins, apt, curl. Each tab holds that tool's snippets in the server's order, each a
  SnippetBlock with its note under it. Snippets of a tab that are the same file show once, with
  every title: the GitHub Actions workflow of Gradle and of Maven is one
  ".github/workflows/publish.yml (Gradle and Maven)". `tools` are the labels of the answer the snippets came from. The chosen
  tool is remembered in this browser (one choice for CI systems, one for the other tools), so every
  page opens on the reader's tool. One tool shows without tabs.

    <SnippetTabs snippets={usage.snippets.filter((s) => s.purpose === "use")} tools={usage.tools} label="Build tool" />
-->
<script lang="ts">
  import * as Tabs from "$lib/components/ui/tabs";
  import { fadeEdges } from "$lib/fade";
  import SnippetBlock, { andList, mergeSame } from "./SnippetBlock.svelte";
  import type { Snippet, ToolLabel } from "$lib/api";

  let {
    snippets,
    tools = [],
    label = "Tool",
    fill = false,
  }: {
    snippets: Snippet[];
    /** The labels of the tools; a tool without one shows its ID. */
    tools?: ToolLabel[];
    /** Accessible name of the tab row. */
    label?: string;
    /** The tabs share the full width: for a narrow column (the details column). */
    fill?: boolean;
  } = $props();

  const ids = $derived([...new Set(snippets.map((s) => s.tool))]);
  const labelOf = (id: string) => tools.find((t) => t.id === id)?.label ?? id;
  const family = (id: string) => (snippets.find((s) => s.tool === id)?.via === "ci" ? "ci" : "build");
  const key = (id: string) => `telesphorepo.client.${family(id)}`;

  function remembered(cs: string[]): string | null {
    try {
      const v = cs.length ? localStorage.getItem(key(cs[0])) : null;
      return v && cs.includes(v) ? v : null;
    } catch {
      return null;
    }
  }

  let chosen = $state<string | null>(null);
  const value = $derived(chosen && ids.includes(chosen) ? chosen : (remembered(ids) ?? ids[0] ?? ""));

  function choose(v: string) {
    chosen = v;
    try {
      localStorage.setItem(key(v), v);
    } catch {
      /* storage blocked: the choice lasts for this page */
    }
  }
</script>

{#snippet blocks(tool: string)}
  <div class="flex flex-col gap-5">
    {#each mergeSame(snippets.filter((s) => s.tool === tool)) as m (m.snippet.id)}
      <SnippetBlock snippet={m.snippet} label={andList(m.titles)} />
    {/each}
  </div>
{/snippet}

{#if ids.length === 1}
  {@render blocks(ids[0])}
{:else if ids.length > 1}
  <Tabs.Root {value} onValueChange={choose}>
    <!-- The row scrolls on a narrow screen, with no scrollbar and a fade where tabs are cut off; its 4px reach keeps the focus ring in view. -->
    <div use:fadeEdges class="-mx-1 overflow-x-auto px-1 py-1 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
      <Tabs.List aria-label={label} class={fill ? "w-full" : undefined}>
        {#each ids as c (c)}
          <Tabs.Trigger value={c} class={fill ? "min-w-0 flex-auto px-2" : "px-3"}>{labelOf(c)}</Tabs.Trigger>
        {/each}
      </Tabs.List>
    </div>
    {#each ids as c (c)}
      <Tabs.Content value={c} class="mt-4">{@render blocks(c)}</Tabs.Content>
    {/each}
  </Tabs.Root>
{/if}
