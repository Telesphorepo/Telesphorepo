<!--
  One part of a card of the Access tab ("From any network", "CI pipelines"): an optional bare
  icon, an h3 with one line of help, its actions on the right, then its content (a list, an
  editor). Parts of a card are 24px apart with a thin Separator between them; the card
  (FormSection, h2) holds them.

    <AccessPart title="CI pipelines" help="A CI job publishes with the token of its CI system.">
      {#snippet actions()}<Button size="sm">Add CI pipeline</Button>{/snippet}
      <ItemList ... />
    </AccessPart>
-->
<script lang="ts">
  import type { Component, Snippet } from "svelte";

  let {
    title,
    help,
    icon: Icon,
    actions,
    children,
  }: { title: string; help?: string; icon?: Component<{ class?: string }>; actions?: Snippet; children?: Snippet } = $props();
</script>

<div class="flex flex-col gap-3">
  <div class="flex min-h-8 flex-wrap items-center justify-between gap-x-6 gap-y-2">
    <div class="flex min-w-0 grow basis-64 items-start gap-3">
      {#if Icon}<Icon class="mt-0.5 size-5 shrink-0 text-muted-foreground" />{/if}
      <div class="min-w-0">
        <h3 class="text-label">{title}</h3>
        {#if help}<p class="text-meta text-muted-foreground">{help}</p>{/if}
      </div>
    </div>
    {#if actions}<div data-slot="row-end" class="flex min-w-0 flex-wrap items-center gap-2 sm:shrink-0">{@render actions()}</div>{/if}
  </div>
  {#if children}{@render children()}{/if}
</div>
