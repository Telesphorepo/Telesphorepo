<!--
  One part of a section of the Access tab ("People who can download", "CI pipelines", "From where"):
  an h3 with one line of help, its actions on the right, then its table. Parts of a section are
  24px apart with a thin Separator between them; the section (FormSection, h2) holds them.

    <AccessPart title="People who can download" help="People who can publish can download too.">
      {#snippet actions()}<Button size="sm">Add a person</Button>{/snippet}
      <DataTable ... />
    </AccessPart>
-->
<script lang="ts">
  import type { Snippet } from "svelte";

  let { title, help, actions, children }: { title: string; help?: string; actions?: Snippet; children: Snippet } = $props();
</script>

<div class="flex flex-col gap-3">
  <div class="flex min-h-8 flex-wrap items-center justify-between gap-x-6 gap-y-2">
    <div class="min-w-0 grow basis-64">
      <h3 class="text-label">{title}</h3>
      {#if help}<p class="text-meta text-muted-foreground">{help}</p>{/if}
    </div>
    {#if actions}<div class="flex shrink-0 flex-wrap items-center gap-2">{@render actions()}</div>{/if}
  </div>
  {@render children()}
</div>
