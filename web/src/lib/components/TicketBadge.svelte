<!--
  A short label on a small flat ticket (branding/README.md, Ticket badge): a semicircular notch in
  each short end, the label, then a dashed perforation and the stub. Without `stub` the stub is a
  short blank end, as on the mark. Only for what admits or is admitted: the conditions of a CI
  pipeline (a branch, a tag, a workflow file), the CI system, a publish token. Everywhere else a
  state is a StatusBadge.

    <TicketBadge icon={GitBranchIcon}>main</TicketBadge>
    <TicketBadge tone="gold" stub="2">acme/app</TicketBadge>

  The shape follows the mark: corners of 1/8 of its height, notches of 1/6. Its 24px height is the
  height of a StatusBadge, so both sit on one line of body text.
-->
<script lang="ts">
  import type { Component, Snippet } from "svelte";
  import { cn } from "$lib/utils";

  let {
    tone = "neutral",
    icon: Icon,
    stub,
    title,
    class: className,
    children,
  }: {
    tone?: "neutral" | "gold";
    /** A 14px icon before the label: what kind of value it is. */
    icon?: Component<{ class?: string }>;
    /** A word or a number on the stub, after the perforation. */
    stub?: string;
    /** The tooltip: the whole value when the label is cut. */
    title?: string;
    class?: string;
    children: Snippet;
  } = $props();
</script>

<span
  data-slot="ticket-badge"
  class={cn(
    "ticket-badge inline-flex h-6 max-w-full min-w-0 shrink-0 items-center rounded-[3px] text-xs font-medium whitespace-nowrap",
    tone === "gold" ? "bg-ticket-gold text-ticket-gold-foreground" : "bg-ticket text-ticket-foreground",
    className,
  )}
  {title}
>
  <span class="flex min-w-0 items-center gap-1 pr-2 pl-2.5">
    {#if Icon}<Icon class="size-3.5 shrink-0 opacity-75" />{/if}
    <span class="truncate">{@render children()}</span>
  </span>
  <span aria-hidden="true" class="perf h-4 w-px shrink-0"></span>
  <span class={cn("flex h-full items-center tabular-nums", stub ? "pr-2.5 pl-1.5 opacity-80" : "w-2")}>{stub ?? ""}</span>
</span>

<style>
  .ticket-badge {
    --notch: 4px;
    mask:
      radial-gradient(circle var(--notch) at 0 50%, #0000 calc(var(--notch) - 0.5px), #000 var(--notch)) 0 0 / 51% 100% no-repeat,
      radial-gradient(circle var(--notch) at 100% 50%, #0000 calc(var(--notch) - 0.5px), #000 var(--notch)) 100% 0 / 51% 100% no-repeat;
  }
  /* The perforation: short dashes of the ink, as the slot column of the mark. */
  .perf {
    background: repeating-linear-gradient(to bottom, currentColor 0 2.5px, transparent 2.5px 4.5px);
    opacity: 0.45;
  }
</style>
