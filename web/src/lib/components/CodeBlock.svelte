<!--
  A block of code to copy: a client snippet, a config file, a set of commands. CodeBlock and
  CodeSnippet are the only places that use the monospace font. `title` names the file or the tool;
  `actions` sit at the end of the title row.

  With `diff` (the lines of a diff, each after its mark: "+" added, " " context) the block shows
  the change to a file the reader already has: the added lines marked and filled, the context
  quiet. Copy then copies the added lines, a blank line between the runs of them; the marks never
  copy, also when the reader selects the text.

    <CodeBlock title="Maven (pom.xml)" code={xml} />
    <CodeBlock title="Your workflow" code={file} diff={patch} />
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import { cn } from "$lib/utils";
  import CopyButton from "./CopyButton.svelte";

  let { code, title, diff, actions }: { code: string; title?: string; diff?: string; actions?: Snippet } = $props();

  type Line = { added: boolean; text: string };
  const lines = $derived<Line[] | null>(
    diff ? diff.replace(/\n$/, "").split("\n").map((l) => ({ added: l.startsWith("+"), text: l.slice(1) })) : null,
  );
  const copied = $derived.by(() => {
    if (!lines) return code;
    const runs: string[][] = [];
    let prev = false;
    for (const l of lines) {
      if (l.added && !prev) runs.push([]);
      if (l.added) runs[runs.length - 1].push(l.text);
      prev = l.added;
    }
    return runs.map((r) => r.join("\n") + "\n").join("\n");
  });
  const copyLabel = $derived(lines ? (title ? `Copy the added lines of ${title}` : "Copy the added lines") : title ? `Copy ${title}` : "Copy the code");
</script>

<figure class="flex min-w-0 flex-col gap-2">
  {#if title || actions}
    <figcaption class={cn("flex flex-wrap items-center justify-between gap-x-4 gap-y-2", actions && "min-h-8")}>
      {#if title}<span class="min-w-0 text-sm font-medium break-words">{title}</span>{/if}
      {#if actions}<span class="ml-auto">{@render actions()}</span>{/if}
    </figcaption>
  {/if}
  <div class="relative min-w-0 rounded-lg border bg-panel">
    {#if lines}
      <div class="max-h-96 overflow-auto py-3 font-mono text-[0.8125rem] leading-relaxed [font-variant-ligatures:none]">
        <code class="flex w-max min-w-full flex-col">
          {#each lines as l, i (i)}
            <span class={cn("flex pr-12 pl-4", l.added ? "bg-success/10" : "text-muted-foreground")}>
              <span class={cn("w-4 shrink-0 select-none", l.added && "text-success")}>{#if l.added}<span aria-hidden="true">+</span><span class="sr-only">Added: </span>{/if}</span>
              <span class="whitespace-pre">{l.text || " "}</span>
            </span>
          {/each}
        </code>
      </div>
    {:else}
      <pre class="max-h-96 overflow-auto px-4 py-3 pr-12 font-mono text-[0.8125rem] leading-relaxed [font-variant-ligatures:none]"><code>{code}</code></pre>
    {/if}
    <CopyButton value={copied} label={copyLabel} class="absolute top-1.5 right-1.5 bg-panel" />
  </div>
</figure>
