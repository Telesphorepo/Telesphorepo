<!--
  One field of a dialog: label, control, hint, error. Fields stack with a 16px gap (`flex flex-col
  gap-4` around them). The control fills the field. The hint is a full sentence that stands alone.

    <FormField label="Name" for="token-name" hint="Name the build or script that uses the token, for example CI runner.">
      <Input id="token-name" bind:value={name} />
    </FormField>
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import { Label } from "$lib/components/ui/label";
  import ErrorState from "./ErrorState.svelte";

  let {
    label,
    for: id,
    hint,
    error,
    children,
  }: {
    label: string;
    /** The id of the control. */
    for?: string;
    hint?: string;
    error?: string;
    children: Snippet;
  } = $props();
</script>

<div class="flex min-w-0 flex-col gap-2">
  <Label for={id}>{label}</Label>
  {@render children()}
  {#if hint}<p class="text-sm text-muted-foreground">{hint}</p>{/if}
  {#if error}<ErrorState inline {error} />{/if}
</div>
