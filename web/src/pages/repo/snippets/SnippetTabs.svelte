<!--
  Snippets with one tab per tool: Gradle (Kotlin) · Gradle (Groovy) · Maven, GitHub Actions ·
  Jenkins, apt, curl. Each tab holds that tool's snippets in the server's order, each a CodeBlock
  with its note under it. `tools` are the labels of the answer the snippets came from. The chosen
  tool is remembered in this browser (one choice for CI systems, one for the other tools), so every
  page opens on the reader's tool. One tool shows without tabs.

    <SnippetTabs snippets={usage.snippets.filter((s) => s.purpose === "use")} tools={usage.tools} label="Build tool" />
-->
<script lang="ts">
  import * as Tabs from "$lib/components/ui/tabs";
  import CodeBlock from "$lib/components/CodeBlock.svelte";
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
  // A block's title names the file it belongs in: ".github/workflows/publish.yml (Gradle)".
  const titleOf = (s: Snippet) => (s.file && !s.title.includes(s.file) ? (s.title ? `${s.file} (${s.title})` : s.file) : s.title);

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
    {#each snippets.filter((s) => s.tool === tool) as s (s.id)}
      <div class="flex min-w-0 flex-col gap-2">
        <CodeBlock title={titleOf(s)} code={s.code} />
        {#if s.note}<p class="text-meta text-muted-foreground">{s.note}</p>{/if}
      </div>
    {/each}
  </div>
{/snippet}

{#if ids.length === 1}
  {@render blocks(ids[0])}
{:else if ids.length > 1}
  <Tabs.Root {value} onValueChange={choose}>
    <div class="-mx-1 overflow-x-auto px-1 pb-1">
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
