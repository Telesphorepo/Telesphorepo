<!--
  The Test action of a connection to another system (an identity provider, a bucket, a Nexus
  server), where the connection is configured: in the header or the footer of its Section or
  Step. Its result shows beside the button, and only after a test: a check and what answered, or
  the error and what to do next. Nothing shows before the first test.

    <ConnectionTest testing={testing} result={result} onTest={test} />
    result = { ok: true, text: "Connected to login.example.com" }
    result = { ok: false, text: "The provider did not answer. Check the address." }
-->
<script lang="ts" module>
  export type TestResult = { ok: boolean; text: string };
</script>

<script lang="ts">
  import CheckIcon from "@lucide/svelte/icons/check";
  import XIcon from "@lucide/svelte/icons/x";
  import { Button } from "$lib/components/ui/button";
  import { Spinner } from "$lib/components/ui/spinner";

  let {
    onTest,
    testing = false,
    result,
    label = "Test connection",
    disabled = false,
  }: {
    onTest: () => unknown;
    testing?: boolean;
    /** The last result; leave it out before the first test. */
    result?: TestResult | null;
    label?: string;
    disabled?: boolean;
  } = $props();
</script>

<div class="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-2">
  <Button type="button" variant="outline" disabled={disabled || testing} onclick={onTest}>
    {#if testing}<Spinner />{/if}
    {label}
  </Button>
  <p role="status" class="flex min-w-0 items-start gap-1.5 text-meta">
    {#if result && !testing}
      {#if result.ok}
        <CheckIcon aria-hidden="true" class="mt-px size-4 shrink-0 text-success" />
        <span class="text-foreground">{result.text}</span>
      {:else}
        <XIcon aria-hidden="true" class="mt-px size-4 shrink-0 text-destructive" />
        <span class="text-destructive">{result.text}</span>
      {/if}
    {/if}
  </p>
</div>
