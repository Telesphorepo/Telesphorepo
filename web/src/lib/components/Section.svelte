<!--
  A surface for one logical area of a page (principle 12): a card with a header (the title, an
  optional one-sentence description, and the area's actions at the header's end), then its
  content, then an optional footer (a submit row). Sections stack 32px apart on Browse pages and 28px in Settings (--section-gap), so the eye takes one
  area at a time. Inside a section, a thin <Separator /> may part sub-groups; never put a line
  between sections.

    <Section title="Passkeys">
      {#snippet actions()}<Button variant="outline" onclick={add}><PlusIcon />Add passkey</Button>{/snippet}
      <DataTable ... />
    </Section>

  FormSection is the same surface for a form. A section without a title is a plain surface.
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import * as Card from "$lib/components/ui/card";
  import { cn } from "$lib/utils";

  let {
    title,
    description,
    actions,
    footer,
    class: className,
    children,
  }: {
    title?: string;
    /** One sentence; leave it out rather than repeat the title. */
    description?: string;
    /** The area's actions, at the end of the header; on a phone they wrap under the title. */
    actions?: Snippet;
    /** The submit row, under the content. */
    footer?: Snippet;
    class?: string;
    children: Snippet;
  } = $props();
</script>

<Card.Root class={cn("mt-(--section-gap) text-body first:mt-0 max-sm:[--card-spacing:--spacing(5)]", className)}>
  {#if title || actions}
    <Card.Header class="flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
      <div class="min-w-0 grow basis-56">
        {#if title}<h2 class="text-section">{title}</h2>{/if}
        {#if description}<p class="mt-1 text-meta text-muted-foreground">{description}</p>{/if}
      </div>
      {#if actions}<div class="flex shrink-0 flex-wrap items-center gap-2">{@render actions()}</div>{/if}
    </Card.Header>
  {/if}
  <Card.Content class="min-w-0">{@render children()}</Card.Content>
  {#if footer}<Card.Footer class="flex-wrap gap-2">{@render footer()}</Card.Footer>{/if}
</Card.Root>
