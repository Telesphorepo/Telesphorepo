<!--
  The conditions of a CI pipeline as TicketBadges, each with the icon of its kind (a branch, a
  tag, a workflow file, a Jenkins job, a token condition), on one line that wraps. `max` shows the
  first ones and "and N more". A pipeline that takes every branch and tag says so in the warning
  color. The CI pipeline cards, the admission ticket and the CI pipeline rows of a repository's
  Access tab use it.
-->
<script lang="ts" module>
  import type { Component } from "svelte";
  import type { ConditionItem } from "./presets";
  import GitBranchIcon from "@lucide/svelte/icons/git-branch";
  import TagIcon from "@lucide/svelte/icons/tag";
  import FileCodeIcon from "@lucide/svelte/icons/file-code";
  import BriefcaseIcon from "@lucide/svelte/icons/briefcase";
  import ListChecksIcon from "@lucide/svelte/icons/list-checks";

  export const kindIcon: Record<ConditionItem["kind"], Component<{ class?: string }>> = {
    branch: GitBranchIcon,
    tag: TagIcon,
    workflow: FileCodeIcon,
    job: BriefcaseIcon,
    condition: ListChecksIcon,
  };
</script>

<script lang="ts">
  import TicketBadge from "$lib/components/TicketBadge.svelte";
  import { cn } from "$lib/utils";

  let {
    items,
    every = false,
    max,
    nowrap = false,
    class: className,
  }: {
    items: ConditionItem[];
    every?: boolean;
    max?: number;
    /** One line that clips, for a row. */
    nowrap?: boolean;
    class?: string;
  } = $props();

  const shown = $derived(max ? items.slice(0, max) : items);
</script>

{#if items.length || every}
  <ul class={cn("flex min-w-0 items-center gap-1.5", nowrap ? "overflow-hidden" : "flex-wrap", className)} aria-label="Conditions">
    {#each shown as c, i (c.kind + c.value + i)}
      <li class="flex max-w-full min-w-0"><TicketBadge icon={kindIcon[c.kind]} title="{c.label}: {c.value}">{c.value}</TicketBadge></li>
    {/each}
    {#if items.length > shown.length}<li class="text-meta whitespace-nowrap text-muted-foreground">and {items.length - shown.length} more</li>{/if}
    {#if every}<li class="text-meta whitespace-nowrap text-warning">Every branch and tag</li>{/if}
  </ul>
{/if}
