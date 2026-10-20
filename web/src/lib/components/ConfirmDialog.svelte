<!--
  A centered dialog that asks before an action that changes or removes something. `changes`
  lists exactly what the action does, one line each ("Deletes 14 files under com/acme/app/1.0/").
  onConfirm runs while the dialog stays open with a spinner; if it throws, the dialog shows the
  error and stays open; when it resolves, the dialog closes.

    <ConfirmDialog bind:open title="Revoke this device?" confirmLabel="Revoke device" destructive
      changes={["Stops the device at once", "Ends its open publish tokens"]}
      onConfirm={() => api.devices.revoke(id)} />

  With `typeToConfirm`, the dialog shows a field, and the confirm button stays off until the
  field holds exactly that text: the name of the one thing ("Type oasis to confirm"), or the
  action of a bulk delete ("Type delete 12 repositories to confirm"), never a bare count.
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import * as AlertDialog from "$lib/components/ui/alert-dialog";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Spinner } from "$lib/components/ui/spinner";
  import ErrorState from "./ErrorState.svelte";
  import FormField from "./FormField.svelte";
  import { errorMessage } from "$lib/api";

  let {
    open = $bindable(false),
    title,
    description,
    changes = [],
    confirmLabel,
    destructive = false,
    typeToConfirm,
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
    /** The text the user types before the confirm button turns on, e.g. a count or a name. */
    typeToConfirm?: string;
    onConfirm: () => unknown | Promise<unknown>;
    /** Extra content, e.g. a note under the changes. */
    children?: Snippet;
  } = $props();

  const id = $props.id();
  let busy = $state(false);
  let error = $state("");
  let typed = $state("");
  const typedOk = $derived(typeToConfirm === undefined || typed.trim() === typeToConfirm);

  $effect(() => {
    if (open) {
      error = "";
      typed = "";
    }
  });

  async function confirm() {
    if (!typedOk) return;
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
      <ul class="flex flex-col gap-2 rounded-lg bg-panel px-4 py-3 text-sm">
        {#each changes as c, i (i)}
          <li class="flex gap-2">
            <span aria-hidden="true" class="mt-2 size-1.5 shrink-0 rounded-full {destructive ? 'bg-destructive' : 'bg-foreground/40'}"></span>
            <span class="min-w-0 break-words">{c}</span>
          </li>
        {/each}
      </ul>
    {/if}
    {#if children}{@render children()}{/if}
    {#if typeToConfirm !== undefined}
      <FormField label="Type “{typeToConfirm}” to confirm" for="{id}-type">
        <Input
          id="{id}-type"
          placeholder={typeToConfirm}
          autocomplete="off"
          spellcheck={false}
          bind:value={typed}
          onkeydown={(e: KeyboardEvent) => {
            if (e.key === "Enter" && typedOk && !busy) confirm();
          }}
        />
      </FormField>
    {/if}
    {#if error}<ErrorState inline {error} />{/if}
    <AlertDialog.Footer>
      <AlertDialog.Cancel disabled={busy}>Cancel</AlertDialog.Cancel>
      <Button variant={destructive ? "destructive" : "default"} disabled={busy || !typedOk} onclick={confirm}>
        {#if busy}<Spinner />{/if}
        {confirmLabel}
      </Button>
    </AlertDialog.Footer>
  </AlertDialog.Content>
</AlertDialog.Root>
