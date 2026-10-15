<!--
  A short state word: Public / Private, Active / Revoked / Expired, Running / Failed. The tone
  gives the color; the word alone must say the state.

    <StatusBadge tone="success">Active</StatusBadge>
-->
<script lang="ts" module>
  export type Tone = "neutral" | "success" | "warning" | "danger" | "info";
</script>

<script lang="ts">
  import type { Component, Snippet } from "svelte";
  import { cn } from "$lib/utils";

  let {
    tone = "neutral",
    icon: Icon,
    title,
    children,
  }: { tone?: Tone; icon?: Component<{ class?: string }>; title?: string; children: Snippet } = $props();

  const tones: Record<Tone, string> = {
    neutral: "bg-muted text-muted-foreground",
    success: "bg-success/12 text-success",
    warning: "bg-warning/15 text-warning",
    danger: "bg-destructive/10 text-destructive",
    info: "bg-selected text-selected-foreground",
  };
</script>

<span class={cn("inline-flex h-6 shrink-0 items-center gap-1 rounded-lg px-2 text-xs font-medium whitespace-nowrap", tones[tone])} {title}>
  {#if Icon}<Icon class="size-3.5" />{/if}
  {@render children()}
</span>
