<!--
  What a long choice holds, over the search that finds more (the repositories of a CI pipeline,
  the members of a new group): each chosen thing as a small chip with its icon, its name and × to
  remove it, whatever the search shows under it. `hint` is a chip's tooltip, also read to screen
  readers. While nothing is chosen, `none` holds the chips' line, so the first choice moves nothing
  under the pointer.

    <ChosenChips label="Chosen repositories" items={chosen.map((n) => ({ key: n, label: n }))} none="None chosen yet."
      onremove={(c) => choose(c.key, false)}>
      {#snippet icon(c)}<FormatIcon format={formatOf(c.key)} />{/snippet}
    </ChosenChips>
-->
<script lang="ts" module>
  export type Chosen = { key: string; label: string; hint?: string };
</script>

<script lang="ts">
  import type { Snippet } from "svelte";
  import XIcon from "@lucide/svelte/icons/x";

  let {
    label,
    items,
    none,
    icon,
    onremove,
  }: {
    /** The accessible name of the list: "Chosen repositories". */
    label: string;
    items: Chosen[];
    /** One short sentence while nothing is chosen. */
    none?: string;
    /** A bare 16px icon before a chip's name: a FormatIcon. */
    icon?: Snippet<[Chosen]>;
    onremove: (item: Chosen) => void;
  } = $props();
</script>

{#if items.length}
  <ul class="flex flex-wrap gap-2" aria-label={label}>
    {#each items as c (c.key)}
      <li class="inline-flex h-7 max-w-full min-w-0 items-center gap-1.5 rounded-lg bg-muted pr-0.5 pl-2.5 text-sm" title={c.hint}>
        {#if icon}<span aria-hidden="true" class="flex shrink-0 text-muted-foreground">{@render icon(c)}</span>{/if}
        <span class="min-w-0 truncate">{c.label}</span>
        {#if c.hint}<span class="sr-only">: {c.hint}</span>{/if}
        <button
          type="button"
          aria-label="Remove {c.label}"
          class="flex size-6 shrink-0 items-center justify-center rounded-lg text-muted-foreground outline-none hover:bg-hover hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50"
          onclick={() => onremove(c)}
        >
          <XIcon class="size-3.5" />
        </button>
      </li>
    {/each}
  </ul>
{:else if none}
  <p class="flex h-7 items-center text-meta text-muted-foreground">{none}</p>
{/if}
