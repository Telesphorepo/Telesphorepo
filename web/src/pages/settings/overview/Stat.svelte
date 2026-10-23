<!--
  One stat tile of the Settings overview: its own small card, the content centered, the value in
  the title size with tabular figures and the label under it, nothing more. Tiles sit in an equal-width grid that gives a row one height, and the content
  is centered in the tile both ways. With `href` the whole tile is the link (the label is the link for the keyboard) and fills
  under the pointer.

    <Stat label="Repositories" value="17" href={href("/settings/repositories")} />

  `children` replace the value (Loading inline, an inline ErrorState).
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import { cn } from "$lib/utils";

  let {
    label,
    value,
    href,
    children,
  }: {
    label: string;
    value?: string;
    href?: string;
    children?: Snippet;
  } = $props();
</script>

<div
  class={cn(
    "relative flex min-w-0 flex-col items-center justify-center gap-1 rounded-lg bg-card px-4 py-5 text-center text-card-foreground ring-1 ring-foreground/10",
    href && "transition-colors hover:bg-hover has-[a:focus-visible]:ring-[3px] has-[a:focus-visible]:ring-ring/50",
  )}
>
  <div class="flex min-h-(--text-title--line-height) max-w-full items-center justify-center truncate text-title tabular-nums">
    {#if children}{@render children()}{:else}{value}{/if}
  </div>
  {#if href}
    <a {href} class="max-w-full truncate text-label text-muted-foreground outline-none after:absolute after:inset-0 after:rounded-lg">{label}</a>
  {:else}
    <p class="max-w-full truncate text-label text-muted-foreground">{label}</p>
  {/if}</div>
