<!--
  What a page or a whole result shows when it has nothing yet. Say what is missing and, in the
  children, give the action that fills it ("New read token"). A list uses CompactEmpty instead.

  The picture is the brand's empty state (branding/README.md, Empty states): the ticket drawn
  flat in outline in the muted ink, its stub torn off along the perforation and dropped a little.
  `icon` sits in the ticket where the T would be, and says what is missing (a lock, a compass);
  without it the ticket is blank.
-->
<script lang="ts">
  import type { Component, Snippet } from "svelte";

  let {
    title,
    description,
    icon: Icon,
    children,
  }: {
    title: string;
    description?: string;
    icon?: Component<{ class?: string }>;
    children?: Snippet;
  } = $props();

  // The mark's grid (64 × 40): the panel left of the perforation, then the stub to the right.
  const PANEL = "M45 0H4A4 4 0 0 0 0 4V15A5 5 0 0 1 0 25V36A4 4 0 0 0 4 40H45";
  const STUB = "M47 0H60A4 4 0 0 1 64 4V15A5 5 0 0 0 64 25V36A4 4 0 0 1 60 40H47";
  const SLOTS = [3, 9, 15, 21, 27, 33];
</script>

<div class="flex flex-col items-center px-4 py-12 text-center">
  <div aria-hidden="true" class="relative mb-5 h-[78px] w-[108px] text-muted-foreground">
    <svg viewBox="-4 -4 72 52" class="absolute inset-0 size-full overflow-visible" fill="none" focusable="false">
      <g stroke="currentColor" stroke-width="1.5" stroke-linejoin="round" opacity=".7">
        <path d={PANEL} vector-effect="non-scaling-stroke" />
        <g transform="translate(3 3) rotate(7 47 40)">
          <path d={STUB} vector-effect="non-scaling-stroke" />
          <path d="M47 0V40" stroke-dasharray="2 2.5" opacity=".6" vector-effect="non-scaling-stroke" />
        </g>
      </g>
      <g fill="currentColor" opacity=".45">
        {#each SLOTS as y (y)}<rect x="44" {y} width="2" height="4" rx="1" />{/each}
      </g>
    </svg>
    {#if Icon}<span class="absolute top-6 left-[27px] flex"><Icon class="size-6" /></span>{/if}
  </div>
  <p class="text-label">{title}</p>
  {#if description}<p class="mt-1 max-w-md text-body text-balance text-muted-foreground">{description}</p>{/if}
  {#if children}<div class="mt-5 flex flex-wrap justify-center gap-2">{@render children()}</div>{/if}
</div>
