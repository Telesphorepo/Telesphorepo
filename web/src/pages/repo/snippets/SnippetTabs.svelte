<!--
  The snippets of a format plugin, one tab per client: Gradle (Kotlin) · Gradle (Groovy) · Maven,
  GitHub Actions · Jenkins, apt, curl. Each tab holds that client's snippets in the plugin's order,
  each a CodeBlock with its note under it. The chosen client is remembered in this browser (one
  choice for build tools, one for CI systems), so every page opens on the reader's tool. One
  client shows without tabs.

    <SnippetTabs snippets={usage.snippets.filter((s) => s.purpose === "use")} label="Build tool" />
-->
<script lang="ts">
  import * as Tabs from "$lib/components/ui/tabs";
  import CodeBlock from "$lib/components/CodeBlock.svelte";
  import { clientLabel, type Snippet } from "../browse";

  let {
    snippets,
    label = "Client",
    fill = false,
  }: {
    snippets: Snippet[];
    /** Accessible name of the tab row. */
    label?: string;
    /** The tabs share the full width: for a narrow column (the details column). */
    fill?: boolean;
  } = $props();

  const clients = $derived([...new Set(snippets.map((s) => s.client))]);
  const family = (c: string) => (c === "github-actions" || c === "jenkins" ? "ci" : "build");
  const key = (c: string) => `telesphorepo.client.${family(c)}`;

  function remembered(cs: string[]): string | null {
    try {
      const v = cs.length ? localStorage.getItem(key(cs[0])) : null;
      return v && cs.includes(v) ? v : null;
    } catch {
      return null;
    }
  }

  let chosen = $state<string | null>(null);
  const value = $derived(chosen && clients.includes(chosen) ? chosen : (remembered(clients) ?? clients[0] ?? ""));

  function choose(v: string) {
    chosen = v;
    try {
      localStorage.setItem(key(v), v);
    } catch {
      /* storage blocked: the choice lasts for this page */
    }
  }
</script>

{#snippet blocks(client: string)}
  <div class="flex flex-col gap-5">
    {#each snippets.filter((s) => s.client === client) as s, i (i)}
      <div class="flex min-w-0 flex-col gap-2">
        <CodeBlock title={s.title} code={s.code} />
        {#if s.note}<p class="text-meta text-muted-foreground">{s.note}</p>{/if}
      </div>
    {/each}
  </div>
{/snippet}

{#if clients.length === 1}
  {@render blocks(clients[0])}
{:else if clients.length > 1}
  <Tabs.Root {value} onValueChange={choose}>
    <div class="-mx-1 overflow-x-auto px-1 pb-1">
      <Tabs.List aria-label={label} class={fill ? "w-full" : undefined}>
        {#each clients as c (c)}
          <Tabs.Trigger value={c} class={fill ? "min-w-0 flex-auto px-2" : "px-3"}>{clientLabel[c] ?? c}</Tabs.Trigger>
        {/each}
      </Tabs.List>
    </div>
    {#each clients as c (c)}
      <Tabs.Content value={c} class="mt-4">{@render blocks(c)}</Tabs.Content>
    {/each}
  </Tabs.Root>
{/if}
