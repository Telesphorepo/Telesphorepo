<!--
  One area of a form: a Section (a card with its title, the area's actions at the header's end,
  and a footer for the submit row) that holds a FormGrid. Sections stack --section-gap apart.

    <FormSection title="Repository">
      <FormGrid>...</FormGrid>
      {#snippet footer()}<Button type="submit">Create repository</Button>{/snippet}
    </FormSection>
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import Section from "./Section.svelte";

  let {
    title,
    description,
    actions,
    footer,
    children,
  }: {
    title?: string;
    description?: string;
    /** The area's actions, at the end of the header. */
    actions?: Snippet;
    /** The submit row, under the fields. */
    footer?: Snippet;
    children: Snippet;
  } = $props();
</script>

<Section {title} {description} {actions} {footer}>{@render children()}</Section>
