<!-- An icon button that copies `value` and shows a check for a moment. Used by CodeBlock, Snippet and CopyValue. -->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import { Button } from "$lib/components/ui/button";
  import CopyIcon from "@lucide/svelte/icons/copy";
  import CheckIcon from "@lucide/svelte/icons/check";

  let {
    value,
    label = "Copy",
    size = "icon-sm",
    class: className = "",
  }: { value: string; label?: string; size?: "icon-sm" | "icon-xs"; class?: string } = $props();

  let copied = $state(false);
  let timer: ReturnType<typeof setTimeout> | undefined;

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

<Button variant="ghost" {size} class={className} aria-label={copied ? "Copied" : label} title={label} onclick={copy}>
  {#if copied}<CheckIcon />{:else}<CopyIcon />{/if}
</Button>
<span class="sr-only" aria-live="polite">{copied ? "Copied" : ""}</span>
