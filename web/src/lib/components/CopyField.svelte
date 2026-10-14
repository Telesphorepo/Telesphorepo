<!--
  A value to copy: a token, a URL, a checksum, or a client snippet. One line renders as a read-only
  field; `block` (or a value with line breaks) renders as a code block. `secret` warns that the
  value shows only once.

    <CopyField label="sha256" value={asset.sha256} />
    <CopyField label="~/.m2/settings.xml" value={snippet.code} block />
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import { Button } from "$lib/components/ui/button";
  import CopyIcon from "@lucide/svelte/icons/copy";
  import CheckIcon from "@lucide/svelte/icons/check";
  import { cn } from "$lib/utils";

  let {
    value,
    label,
    block = false,
    secret = false,
    class: className = "",
  }: {
    value: string;
    /** Shown above the value (a file name for a snippet). */
    label?: string;
    block?: boolean;
    secret?: boolean;
    class?: string;
  } = $props();

  let copied = $state(false);
  let timer: ReturnType<typeof setTimeout> | undefined;
  const multiline = $derived(block || value.includes("\n"));

  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      copied = true;
      clearTimeout(timer);
      timer = setTimeout(() => (copied = false), 1600);
    } catch {
      toast.error("Copy failed. Select the text and copy it by hand.");
    }
  }
</script>

<div class={cn("flex min-w-0 flex-col gap-1.5", className)}>
  {#if label}
    <span class="truncate text-xs font-medium text-muted-foreground">{label}</span>
  {/if}
  <div
    class={cn(
      "group relative min-w-0 rounded-xl border bg-muted/50",
      secret && "border-primary/60 bg-primary/10",
    )}
  >
    {#if multiline}
      <pre class="max-h-96 overflow-auto px-3.5 py-3 pr-12 text-[0.8125rem] leading-relaxed"><code>{value}</code></pre>
    {:else}
      <code class="block overflow-x-auto px-3.5 py-2 pr-12 text-[0.8125rem] whitespace-nowrap select-all">{value}</code>
    {/if}
    <Button
      variant="ghost"
      size="icon-sm"
      class="absolute top-1 right-1"
      aria-label={copied ? "Copied" : label ? `Copy ${label}` : "Copy"}
      onclick={copy}
    >
      {#if copied}<CheckIcon />{:else}<CopyIcon />{/if}
    </Button>
  </div>
  {#if secret}
    <span class="text-xs text-muted-foreground">Copy it now. It is not shown again.</span>
  {/if}
  <span class="sr-only" aria-live="polite">{copied ? "Copied" : ""}</span>
</div>
