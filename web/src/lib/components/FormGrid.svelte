<!--
  The one form grid (principle 12): two columns, 24px apart, with each label above its control and
  every control filling its cell, so widths are uniform without a label column. A FormRow with
  `full` spans both columns (a list, a long address, a block of text). Under 640px the grid is one
  column. Put it in a FormSection; part sub-groups of one card with a <Separator /> between two
  grids.

    <FormGrid>
      <FormRow label="Name" for="name" help="Repositories refer to the list by this name.">
        <Input id="name" bind:value={name} aria-describedby="name-help" />
      </FormRow>
      <FormRow label="Largest upload" for="max" help="Uploads above this size are refused.">
        <UnitInput id="max" bind:num bind:unit units={SIZE_UNITS} describedby="max-help" />
      </FormRow>
      <SwitchRow id="pw" label="Password sign-in" help="Administrators can also use a password." bind:checked />
    </FormGrid>
-->
<script lang="ts">
  import type { Snippet } from "svelte";

  let { children }: { children: Snippet } = $props();
</script>

<div class="grid gap-6 sm:grid-cols-2">{@render children()}</div>
