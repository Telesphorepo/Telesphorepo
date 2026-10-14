<!--
  Asks before an action that changes or removes something. `changes` lists exactly what the action
  will do, one line each ("Deletes 14 files under com/acme/app/1.0/"). onConfirm runs while the
  dialog stays open with a spinner; if it throws, the dialog shows the error and stays open; when
  it resolves, the dialog closes.

    <ConfirmDialog bind:open title="Delete repository" confirmLabel="Delete repository" destructive
      changes={["Removes every file of maven-releases", "Removes its permissions"]}
      onConfirm={() => api.repos.delete(name)} />
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import * as AlertDialog from "$lib/components/ui/alert-dialog";
  import { Button } from "$lib/components/ui/button";
  import { Spinner } from "$lib/components/ui/spinner";
  import ErrorState from "./ErrorState.svelte";
  import { errorMessage } from "$lib/api";

  let {
    open = $bindable(false),
    title,
    description,
    changes = [],
    confirmLabel,
    destructive = false,
    onConfirm,
    children,
  }: {
    open?: boolean;
    title: string;
    description?: string;
    changes?: string[];
    /** Names the action, e.g. "Delete 3 files"; never "OK". */
    confirmLabel: string;
    destructive?: boolean;
    onConfirm: () => unknown | Promise<unknown>;
    /** Extra content, e.g. a field the user types to confirm. */
    children?: Snippet;
  } = $props();

  let busy = $state(false);
  let error = $state("");

  $effect(() => {
    if (open) error = "";
  });

  async function confirm() {
    busy = true;
    error = "";
    try {
      await onConfirm();
      open = false;
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }
</script>

<AlertDialog.Root bind:open={() => open, (v) => { if (!busy) open = v; }}>
  <AlertDialog.Content class="max-h-[calc(100dvh-2rem)] max-w-[calc(100%-2rem)] overflow-y-auto data-[size=default]:max-w-[calc(100%-2rem)] data-[size=default]:sm:max-w-lg">
    <AlertDialog.Header>
      <AlertDialog.Title>{title}</AlertDialog.Title>
      {#if description}<AlertDialog.Description>{description}</AlertDialog.Description>{/if}
    </AlertDialog.Header>
    {#if changes.length}
      <ul class="flex flex-col gap-1.5 rounded-xl bg-muted/60 px-4 py-3 text-sm">
        {#each changes as c, i (i)}
          <li class="flex gap-2">
            <span aria-hidden="true" class="mt-2 size-1.5 shrink-0 rounded-full {destructive ? 'bg-destructive' : 'bg-primary'}"></span>
            <span class="min-w-0 break-words">{c}</span>
          </li>
        {/each}
      </ul>
    {/if}
    {#if children}{@render children()}{/if}
    {#if error}<ErrorState inline {error} />{/if}
    <AlertDialog.Footer>
      <AlertDialog.Cancel disabled={busy}>Cancel</AlertDialog.Cancel>
      <Button variant={destructive ? "destructive" : "default"} disabled={busy} onclick={confirm}>
        {#if busy}<Spinner />{/if}
        {confirmLabel}
      </Button>
    </AlertDialog.Footer>
  </AlertDialog.Content>
</AlertDialog.Root>
