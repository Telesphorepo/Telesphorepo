<!--
  One area of the CI pipeline form: a numbered Step while the pipeline is new (a setup), a
  FormSection with the same title once it exists.
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import Step from "$lib/components/Step.svelte";
  import FormSection from "$lib/components/FormSection.svelte";

  let {
    n,
    title,
    description,
    actions,
    footer,
    children,
  }: { n?: number; title: string; description?: string; actions?: Snippet; footer?: Snippet; children: Snippet } = $props();
</script>

{#if n}
  <Step {n} {title} {description} {actions} {footer}>{@render children()}</Step>
{:else}
  <FormSection {title} {description} {actions} {footer}>{@render children()}</FormSection>
{/if}
