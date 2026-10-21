<!--
  A secret value (a client secret, a password for another system) in a box the size and look of an
  Input. It never shows the saved value: it shows "Set" or "Not set", with Replace and Clear (or Set).
  Replace opens a password field; Cancel goes back. Clear marks the value to clear on save.

  `change` is what the parent sends: undefined (no change), a string (the new value), or null
  (clear). The parent owns it, so a saved form resets the control.

    <SecretInput id="secret" set={isSet} change={draft} onchange={(v) => (draft = v)} />
-->
<script lang="ts">
  import { tick } from "svelte";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import LockIcon from "@lucide/svelte/icons/lock";
  import { cn } from "$lib/utils";

  let {
    id,
    set,
    change,
    onchange,
    disabled = false,
    canClear = true,
    invalid = false,
    describedby,
  }: {
    id: string;
    /** A value is saved. */
    set: boolean;
    change: string | null | undefined;
    onchange: (v: string | null | undefined) => void;
    disabled?: boolean;
    canClear?: boolean;
    invalid?: boolean;
    describedby?: string;
  } = $props();

  let editing = $state(false);
  let field = $state<HTMLInputElement | null>(null);
  // A parent reset (a typed value back to undefined, as after a save) closes the field. Opening the
  // field also has change undefined, before the focus reaches it, so only a change from a value
  // counts.
  let prev: string | null | undefined;
  $effect(() => {
    const c = change;
    if (prev !== undefined && c === undefined && editing && document.activeElement !== field) editing = false;
    prev = c;
  });

  async function replace() {
    editing = true;
    await tick();
    field?.focus();
  }

  function cancel() {
    editing = false;
    onchange(undefined);
  }
</script>

{#if editing}
  <div class="flex items-center gap-2">
    <Input
      bind:ref={field}
      {id}
      type="password"
      autocomplete="new-password"
      placeholder={set ? "New value" : "Value"}
      value={change ?? ""}
      oninput={(e) => onchange(e.currentTarget.value === "" ? undefined : e.currentTarget.value)}
      aria-invalid={invalid || undefined}
      aria-describedby={describedby}
    />
    <Button type="button" variant="ghost" onclick={cancel}>Cancel</Button>
  </div>
{:else}
  <div
    {id}
    role="group"
    aria-describedby={describedby}
    class={cn(
      "flex h-9 w-full items-center gap-2 rounded-lg border border-input bg-input/30 pr-1 pl-3 text-body",
      invalid && "border-destructive",
      disabled && "opacity-50",
    )}
  >
    <LockIcon aria-hidden="true" class="size-3.5 shrink-0 text-muted-foreground" />
    {#if change === null}
      <span class="min-w-0 flex-1 truncate text-warning">Clears when you save</span>
      {#if !disabled}<Button type="button" variant="ghost" size="sm" onclick={() => onchange(undefined)}>Undo</Button>{/if}
    {:else if set}
      <span class="min-w-0 flex-1 truncate">Set <span class="text-muted-foreground">· hidden</span></span>
      {#if !disabled}
        <Button type="button" variant="ghost" size="sm" onclick={replace}>Replace</Button>
        {#if canClear}<Button type="button" variant="ghost" size="sm" onclick={() => onchange(null)}>Clear</Button>{/if}
      {/if}
    {:else}
      <span class="min-w-0 flex-1 truncate text-muted-foreground">Not set</span>
      {#if !disabled}<Button type="button" variant="ghost" size="sm" onclick={replace}>Set</Button>{/if}
    {/if}
  </div>
{/if}
