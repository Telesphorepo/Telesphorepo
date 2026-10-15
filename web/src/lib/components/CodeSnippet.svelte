<!--
  One line to copy and paste: a command, a token, a repository address for a build file. Monospace,
  like CodeBlock. `secret` adds the note that the value is shown only this once. (The name keeps
  it apart from the Snippet type of Svelte.)

    <CodeSnippet label="Repository address" value={url} />
    <CodeSnippet label="Token" value={token} secret />
-->
<script lang="ts">
  import CopyButton from "./CopyButton.svelte";

  let { value, label, secret = false }: { value: string; label?: string; secret?: boolean } = $props();
</script>

<div class="flex min-w-0 flex-col gap-2">
  {#if label}<span class="text-sm font-medium">{label}</span>{/if}
  <div class="flex min-w-0 items-center gap-1 rounded-lg border bg-panel pl-3 pr-1">
    <code class="min-w-0 flex-1 overflow-x-auto py-2 font-mono text-[0.8125rem] whitespace-nowrap select-all [font-variant-ligatures:none]">{value}</code>
    <CopyButton {value} label={label ? `Copy ${label.toLowerCase()}` : "Copy"} />
  </div>
  {#if secret}<p class="text-sm text-muted-foreground">Copy it now. It is not shown again.</p>{/if}
</div>
