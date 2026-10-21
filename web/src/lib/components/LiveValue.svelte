<!--
  A value that a Live refreshes: a brief tint behind it when it changes, so the reader sees that it
  moves (nothing under prefers-reduced-motion). `value` is what changes; the text is the child, or
  `value` itself.

    <LiveValue value={move.copied}>{formatCount(move.copied)} of {files(move.total)}</LiveValue>
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import { flash } from "$lib/live.svelte";

  let { value, children }: { value: string | number; children?: Snippet } = $props();
</script>

<span class="-mx-1 rounded-sm px-1" use:flash={value}>{#if children}{@render children()}{:else}{value}{/if}</span>
