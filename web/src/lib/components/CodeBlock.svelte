<!--
  A block of code to copy: a client snippet, a config file, a set of commands. CodeBlock and
  CodeSnippet are the only places that use the monospace font. `title` names the file or the tool.

    <CodeBlock title="Maven (pom.xml)" code={xml} />
-->
<script lang="ts">
  import CopyButton from "./CopyButton.svelte";

  let { code, title }: { code: string; title?: string } = $props();
</script>

<figure class="flex min-w-0 flex-col gap-2">
  {#if title}<figcaption class="text-sm font-medium">{title}</figcaption>{/if}
  <div class="relative min-w-0 rounded-lg border bg-panel">
    <pre class="max-h-96 overflow-auto px-4 py-3 pr-12 font-mono text-[0.8125rem] leading-relaxed [font-variant-ligatures:none]"><code>{code}</code></pre>
    <CopyButton value={code} label={title ? `Copy ${title}` : "Copy the code"} class="absolute top-1.5 right-1.5 bg-panel" />
  </div>
</figure>
