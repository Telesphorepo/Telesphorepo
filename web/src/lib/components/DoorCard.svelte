<!--
  A door: a large card that is one link or one button, with a 32px icon in the mark's gold, a title
  and one sentence. Home's doors (Publish, Browse, Administer) and the admin home's actions (New
  repository, New account, New CI pipeline) use it. Put doors in a DoorRow.

    <DoorCard title="Browse" text="Find a package and see how to use it." href={href("/browse")} icon={CompassIcon} />
    <DoorCard title="New account" text="…" onclick={() => (open = true)} icon={UserPlusIcon} />
-->
<script lang="ts">
  import type { Component } from "svelte";
  import { cn } from "$lib/utils";

  let {
    title,
    text,
    icon: Icon,
    href,
    onclick,
  }: { title: string; text: string; icon: Component<{ class?: string }>; href?: string; onclick?: () => void } = $props();

  const look =
    "flex flex-col gap-3 rounded-lg bg-card p-6 text-left text-card-foreground ring-1 ring-foreground/10 outline-none transition-colors hover:bg-hover focus-visible:ring-[3px] focus-visible:ring-ring/50 md:w-[calc((100%-2rem)/3)]";
</script>

{#snippet body()}
  <span aria-hidden="true" class="flex text-(--mark)"><Icon class="size-8" /></span>
  <span class="text-section">{title}</span>
  <span class="text-body text-muted-foreground">{text}</span>
{/snippet}

{#if href}
  <a {href} class={look}>{@render body()}</a>
{:else}
  <button type="button" {onclick} class={cn(look, "cursor-pointer")}>{@render body()}</button>
{/if}
