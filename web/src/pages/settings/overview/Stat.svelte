<!--
  One figure of the Settings overview: the label, the value in the title size, and an optional
  muted line under it (what the value is made of). With `href` the whole figure is the link (the
  label is the link for the keyboard) and fills under the pointer; it reaches 12px into the card's
  padding like a list row, so its text stays on the card's edge.

    <Stat label="Repositories" value="17" detail="12 hosted · 3 proxies · 2 groups" href={href("/settings/repositories")} />

  `children` replace the value (a RelativeTime, Loading inline, an inline ErrorState).
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import { cn } from "$lib/utils";

  let {
    label,
    value,
    detail,
    href,
    children,
  }: {
    label: string;
    value?: string;
    /** One short phrase under the value. */
    detail?: string;
    href?: string;
    children?: Snippet;
  } = $props();
</script>

<div
  class={cn(
    "relative -m-3 flex min-w-0 flex-col rounded-lg p-3",
    href && "transition-colors hover:bg-hover has-[a:focus-visible]:ring-[3px] has-[a:focus-visible]:ring-ring/50",
  )}
>
  {#if href}
    <a {href} class="truncate text-meta text-muted-foreground outline-none after:absolute after:inset-0 after:rounded-lg">{label}</a>
  {:else}
    <p class="truncate text-meta text-muted-foreground">{label}</p>
  {/if}
  <div class="mt-1 flex min-h-(--text-title--line-height) min-w-0 items-center truncate text-title">
    {#if children}{@render children()}{:else}{value}{/if}
  </div>
  {#if detail}<p class="mt-0.5 truncate text-meta text-muted-foreground" title={detail}>{detail}</p>{/if}
</div>
