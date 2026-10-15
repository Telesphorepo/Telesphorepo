<!--
  A saved username and password for another system (the remote of a proxy), in a box with the
  look of SecretInput. It never shows the saved values: it shows "Set" or "Not set", with Replace
  and Clear (or Set). Replace opens a username field and a password field; Cancel goes back. Clear
  marks the credentials to remove on save.

  `change` is what the parent sends: undefined (no change), {username, password} (new values), or
  null (remove them). The parent owns it, so a saved form resets the control.

    <CredentialsInput id="creds" set={proxy.has_credentials} change={draft.value.creds} onchange={(v) => (draft.value.creds = v)} />
-->
<script lang="ts" module>
  export type Credentials = { username: string; password: string };
</script>

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
    invalid = false,
    describedby,
  }: {
    id: string;
    /** Credentials are saved. */
    set: boolean;
    change: Credentials | null | undefined;
    onchange: (v: Credentials | null | undefined) => void;
    disabled?: boolean;
    invalid?: boolean;
    describedby?: string;
  } = $props();

  let field = $state<HTMLInputElement | null>(null);

  async function replace() {
    onchange({ username: "", password: "" });
    await tick();
    field?.focus();
  }
</script>

{#if change}
  <div class="flex flex-col gap-2">
    <Input
      bind:ref={field}
      {id}
      autocomplete="off"
      spellcheck={false}
      placeholder="Username"
      aria-label="Username"
      value={change.username}
      oninput={(e) => onchange({ username: e.currentTarget.value, password: change.password })}
      aria-invalid={invalid || undefined}
      aria-describedby={describedby}
    />
    <div class="flex items-center gap-2">
      <Input
        type="password"
        autocomplete="new-password"
        placeholder="Password"
        aria-label="Password"
        value={change.password}
        oninput={(e) => onchange({ username: change.username, password: e.currentTarget.value })}
        aria-describedby={describedby}
      />
      <Button type="button" variant="ghost" onclick={() => onchange(undefined)}>Cancel</Button>
    </div>
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
      <span class="min-w-0 flex-1 truncate text-warning">Removed when you save</span>
      {#if !disabled}<Button type="button" variant="ghost" size="sm" onclick={() => onchange(undefined)}>Undo</Button>{/if}
    {:else if set}
      <span class="min-w-0 flex-1 truncate">Set <span class="text-muted-foreground">· hidden</span></span>
      {#if !disabled}
        <Button type="button" variant="ghost" size="sm" onclick={replace}>Replace</Button>
        <Button type="button" variant="ghost" size="sm" onclick={() => onchange(null)}>Clear</Button>
      {/if}
    {:else}
      <span class="min-w-0 flex-1 truncate text-muted-foreground">Not set</span>
      {#if !disabled}<Button type="button" variant="ghost" size="sm" onclick={replace}>Set</Button>{/if}
    {/if}
  </div>
{/if}
