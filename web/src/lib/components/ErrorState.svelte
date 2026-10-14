<!--
  A failed request. Shows the server's message and, with onRetry, a "Try again" button.
  `inline` is a one-line form for use inside a table, a dialog or under a form.
-->
<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
  import { Button } from "$lib/components/ui/button";
  import CircleAlertIcon from "@lucide/svelte/icons/circle-alert";

  let {
    error,
    title = "The request failed",
    onRetry,
    inline = false,
  }: {
    error: string;
    title?: string;
    onRetry?: () => void;
    inline?: boolean;
  } = $props();
</script>

{#if inline}
  <p role="alert" class="flex items-start gap-2 text-sm text-destructive">
    <CircleAlertIcon class="mt-0.5 size-4 shrink-0" />
    <span class="min-w-0 break-words">{error}</span>
  </p>
{:else}
  <Alert.Root variant="destructive">
    <CircleAlertIcon />
    <Alert.Title>{title}</Alert.Title>
    <Alert.Description>
      <p class="break-words">{error}</p>
      {#if onRetry}
        <Button variant="outline" size="sm" class="mt-2" onclick={onRetry}>Try again</Button>
      {/if}
    </Alert.Description>
  </Alert.Root>
{/if}
