<!--
  A centered dialog with a short form: at most five fields. A longer flow is a page of its own.
  Enter submits. onSubmit runs while the dialog stays open with a spinner; if it throws, the
  dialog shows the error and stays open; when it resolves, the dialog closes.

    <ShortFormDialog bind:open title="New read token" submitLabel="Create token" canSubmit={!!name.trim()}
      onSubmit={async () => { created = await api.tokens.create(name.trim()); }}>
      <FormField label="Name" for="t-name"><Input id="t-name" bind:value={name} /></FormField>
    </ShortFormDialog>
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import * as Dialog from "$lib/components/ui/dialog";
  import { Button } from "$lib/components/ui/button";
  import { Spinner } from "$lib/components/ui/spinner";
  import ErrorState from "./ErrorState.svelte";
  import { errorMessage } from "$lib/api";

  let {
    open = $bindable(false),
    title,
    description,
    submitLabel,
    canSubmit = true,
    destructive = false,
    wide = false,
    onSubmit,
    children,
  }: {
    open?: boolean;
    title: string;
    description?: string;
    /** Names the action ("Create token"); never "OK" or "Submit". */
    submitLabel: string;
    canSubmit?: boolean;
    destructive?: boolean;
    /** 768px wide in place of 512px: for choices that each have a line of help (the grant dialog). */
    wide?: boolean;
    onSubmit: () => unknown | Promise<unknown>;
    /** The fields, each a <FormField>. */
    children: Snippet;
  } = $props();

  let busy = $state(false);
  let error = $state("");

  $effect(() => {
    if (open) error = "";
  });

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (!canSubmit || busy) return;
    busy = true;
    error = "";
    try {
      await onSubmit();
      open = false;
    } catch (err) {
      error = errorMessage(err);
    } finally {
      busy = false;
    }
  }
</script>

<Dialog.Root bind:open={() => open, (v) => { if (!busy) open = v; }}>
  <Dialog.Content class="max-h-[calc(100dvh-2rem)] overflow-y-auto {wide ? 'sm:max-w-3xl' : 'sm:max-w-lg'}">
    <Dialog.Header>
      <Dialog.Title>{title}</Dialog.Title>
      {#if description}<Dialog.Description>{description}</Dialog.Description>{/if}
    </Dialog.Header>
    <form class="flex flex-col gap-6" onsubmit={submit}>
      <div class="flex flex-col gap-4">
        {@render children()}
        {#if error}<ErrorState inline {error} />{/if}
      </div>
      <Dialog.Footer>
        <Button variant="outline" disabled={busy} onclick={() => (open = false)}>Cancel</Button>
        <Button type="submit" variant={destructive ? "destructive" : "default"} disabled={busy || !canSubmit}>
          {#if busy}<Spinner />{/if}
          {submitLabel}
        </Button>
      </Dialog.Footer>
    </form>
  </Dialog.Content>
</Dialog.Root>
