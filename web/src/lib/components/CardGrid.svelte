<!--
  A responsive grid of ItemCards, for a handful of things that each have a little more to say than
  a list row (keys, pipelines, regions, backups). The columns fill the width at 300px or more each,
  16px apart, one column on a phone; `max` caps the number of columns (Browse: 3). The grid sits in
  a `plain` Section, so no card sits inside a card; each ItemCard has its own frame.

    <Section plain title="Signing keys">
      <CardGrid label="Signing keys">
        {#each keys as k (k.name)}<ItemCard title={k.name}>...</ItemCard>{/each}
      </CardGrid>
    </Section>

  SourceGrid is the same grid over a source, with its loading, error and empty states.
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import { cn } from "$lib/utils";

  let {
    label,
    max,
    class: className,
    children,
  }: {
    /** Accessible name of the grid. */
    label?: string;
    /** The most columns on a wide screen. */
    max?: number;
    class?: string;
    children: Snippet;
  } = $props();
</script>

<!-- Each column is at least 300px (the whole width when it is narrower), and at least 1/max of the width. -->
<div
  role={label ? "group" : undefined}
  aria-label={label}
  style:--cols={max ?? 99}
  class={cn("grid grid-cols-[repeat(auto-fill,minmax(max(min(100%,18.75rem),calc((100%-(var(--cols)-1)*1rem)/var(--cols))),1fr))] gap-4", className)}
>
  {@render children()}
</div>
