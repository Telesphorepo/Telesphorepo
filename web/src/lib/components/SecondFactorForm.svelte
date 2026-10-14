<!--
  Asks for one second-factor proof: a 6-digit code, a passkey, or (at sign-in) a recovery code.
  onProof sends the proof; while it runs the form is busy, and when it throws the form shows the
  error and lets the user try again. Used by the sign-in page and by <SecondFactorPrompt>.
-->
<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import { Spinner } from "$lib/components/ui/spinner";
  import KeyRoundIcon from "@lucide/svelte/icons/key-round";
  import ErrorState from "./ErrorState.svelte";
  import { errorMessage, type PasskeyOptions, type Proof, type SecondFactorMethod } from "$lib/api";
  import { getPasskeyAssertion, passkeysSupported } from "$lib/webauthn";

  let {
    methods,
    actionLabel,
    passkeyOptions,
    onProof,
  }: {
    methods: SecondFactorMethod[];
    /** Names the action: "Sign in", "Approve device", "Create session". */
    actionLabel: string;
    /** Starts a passkey check (the sign-in and the fresh check use different routes). */
    passkeyOptions: () => Promise<PasskeyOptions>;
    onProof: (p: Proof) => Promise<unknown>;
  } = $props();

  const id = $props.id();
  let code = $state("");
  let recovery = $state(false);
  let busy = $state(false);
  let error = $state("");

  const hasTOTP = $derived(methods.includes("totp"));
  const hasRecovery = $derived(methods.includes("recovery"));
  const hasPasskey = $derived(methods.includes("passkey") && passkeysSupported());
  const useCode = $derived(hasTOTP || hasRecovery);
  // Only recovery codes left: the code field takes one.
  $effect(() => {
    if (!hasTOTP && hasRecovery) recovery = true;
  });

  async function send(p: () => Promise<Proof>) {
    busy = true;
    error = "";
    try {
      await onProof(await p());
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }

  function submit(e: SubmitEvent) {
    e.preventDefault();
    const c = code.trim();
    if (!c) return;
    code = "";
    send(async () => (recovery ? { recovery_code: c } : { totp_code: c.replace(/\s/g, "") }));
  }

  const passkey = () => send(async () => ({ passkey: await getPasskeyAssertion(await passkeyOptions()) }));
</script>

<div class="flex flex-col gap-4">
  {#if useCode}
    <form class="flex flex-col gap-2" onsubmit={submit}>
      <Label for="{id}-code">{recovery ? "Recovery code" : "Code from your authenticator app"}</Label>
      <div class="flex flex-wrap gap-2">
        <!-- svelte-ignore a11y_autofocus -->
        <Input
          id="{id}-code"
          bind:value={code}
          autofocus
          autocomplete="one-time-code"
          inputmode={recovery ? "text" : "numeric"}
          placeholder={recovery ? "xxxx-xxxx-xxxx" : "123 456"}
          class="w-44 font-mono tracking-wider"
          disabled={busy}
        />
        <Button type="submit" disabled={busy || !code.trim()}>
          {#if busy}<Spinner />{/if}
          {actionLabel}
        </Button>
      </div>
      {#if hasTOTP && hasRecovery}
        <button
          type="button"
          class="self-start text-sm text-link underline-offset-4 hover:underline"
          onclick={() => {
            recovery = !recovery;
            code = "";
          }}
        >
          {recovery ? "Use a code from your authenticator app" : "Use a recovery code"}
        </button>
      {/if}
    </form>
  {/if}
  {#if hasPasskey}
    <Button variant={useCode ? "outline" : "default"} class="self-start" disabled={busy} onclick={passkey}>
      <KeyRoundIcon />
      {useCode ? "Use a passkey" : `${actionLabel} with a passkey`}
    </Button>
  {/if}
  {#if !useCode && !hasPasskey}
    <ErrorState inline error="This account has no two-step verification method that works in this browser." />
  {/if}
  {#if error}<ErrorState inline {error} />{/if}
</div>
