<!--
  One thing in a CardGrid: a bare icon and the title (up to two lines) with its badges, up to three short fact lines
  (each a phrase, never a label and a value), and a row at the bottom (the card's actions, or a
  closing fact). With `href` the whole card is the link (the title is the link for the keyboard
  and the pointer covers the card); buttons in `actions` keep their own click. Cards of one grid
  are as tall as the tallest in their row, and the bottom row lines up across them.

    <ItemCard title={k.name} href={href(`/settings/signing-keys/${k.name}`)}>
      {#snippet icon()}<KeyRoundIcon class="size-5" />{/snippet}
      {#snippet badges()}<StatusBadge tone="success">In use</StatusBadge>{/snippet}
      <p>Fingerprint …{k.fingerprint.slice(-16)}</p>
      <p>Used by maven, apt</p>
      {#snippet actions()}<Button variant="ghost" size="sm" onclick={del}>Delete</Button>{/snippet}
    </ItemCard>
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import { cn } from "$lib/utils";

  let {
    title,
    href,
    icon,
    badges,
    actions,
    titleClass,
    class: className,
    children,
  }: {
    title: string;
    /** The address the card opens. */
    href?: string;
    /** A bare icon before the title (20px: `class="size-5"`). */
    icon?: Snippet;
    /** Badges after the title (a state, a lock). */
    badges?: Snippet;
    /** The row at the bottom: the card's actions, or a closing fact. */
    actions?: Snippet;
    /** Classes of the title (`text-section` for a card that leads with its name). */
    titleClass?: string;
    class?: string;
    /** Up to three fact lines, each a <p>. */
    children?: Snippet;
  } = $props();
</script>

<div
  data-slot="item-card"
  class={cn(
    "relative flex min-w-0 flex-col gap-3 rounded-lg bg-card p-5 text-card-foreground ring-1 ring-foreground/10 transition-colors",
    href && "hover:bg-hover has-[[data-card-link]:focus-visible]:ring-[3px] has-[[data-card-link]:focus-visible]:ring-ring/50",
    className,
  )}
>
  <div class="flex min-w-0 items-center gap-3">
    {#if icon}<span aria-hidden="true" class="flex shrink-0 text-muted-foreground">{@render icon()}</span>{/if}
    {#if href}
      <a data-card-link {href} class={cn("line-clamp-2 min-w-0 break-words text-label outline-none after:absolute after:inset-0 after:rounded-lg", titleClass)}>{title}</a>
    {:else}
      <h3 class={cn("line-clamp-2 min-w-0 break-words text-label", titleClass)}>{title}</h3>
    {/if}
    {#if badges}<span class="flex shrink-0 items-center gap-1.5">{@render badges()}</span>{/if}
  </div>
  {#if children}<div class="flex min-w-0 flex-col gap-0.5 text-meta text-muted-foreground">{@render children()}</div>{/if}
  {#if actions}
    <!-- Its buttons and links lie above the card's link and keep their own click; a click on its text opens the card. -->
    <div data-slot="card-actions" class="pointer-events-none relative mt-auto flex flex-wrap items-center gap-2 pt-1 text-meta text-muted-foreground [&_a,&_button,&_label,&_[role=switch]]:pointer-events-auto">{@render actions()}</div>
  {/if}
</div>
