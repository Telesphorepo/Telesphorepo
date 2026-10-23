<!--
  Asks for one second-factor proof. A passkey comes first: its button leads, and at sign-in
  (autoPasskey) the browser's passkey prompt opens as the form appears. Under it a 6-digit code from an authenticator app.
  A recovery code is the last resort: a small link at the end, and the code field only once it is
  chosen (or when nothing else works in this browser). onProof sends the proof; while it runs the
  form is busy, and when it throws the form shows the error and lets the user try again. Used by
  the sign-in page and by <FreshFactor>.
-->
<script lang="ts">
  import { onMount } from "svelte";
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
    autoPasskey = false,
  }: {
    methods: SecondFactorMethod[];
    /** Names the action: "Sign in", "Approve device", "Create session". */
    actionLabel: string;
    /** Starts a passkey check (the sign-in and the fresh check use different routes). */
    passkeyOptions: () => Promise<PasskeyOptions>;
    onProof: (p: Proof) => Promise<unknown>;
    /** Opens the passkey prompt as the form appears (the sign-in page, where it is the next step). */
    autoPasskey?: boolean;
  } = $props();

  const id = $props.id();
  let code = $state("");
  let recoveryChosen = $state(false);
  let busy = $state(false);
  let error = $state("");

  const hasTOTP = $derived(methods.includes("totp"));
  const hasRecovery = $derived(methods.includes("recovery"));
  const hasPasskey = $derived(methods.includes("passkey") && passkeysSupported());
  // The recovery field shows once chosen, or when it is the only way left in this browser.
  const recovery = $derived(hasRecovery && (recoveryChosen || (!hasTOTP && !hasPasskey)));
  const showCode = $derived(hasTOTP || recovery);
  // What the person signs in with when nothing is lost.
  const usual = $derived(hasPasskey && hasTOTP ? "passkey and app" : hasPasskey ? "passkey" : "app");

  async function send(p: () => Promise<Proof>, quiet = false) {
    busy = true;
    error = "";
    try {
      await onProof(await p());
    } catch (e) {
      // A prompt the browser would not open by itself (Safari wants a tap) or that the person
      // closed says nothing: the button is right there.
      if (!quiet) error = errorMessage(e);
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

  const passkey = (quiet = false) => send(async () => ({ passkey: await getPasskeyAssertion(await passkeyOptions()) }), quiet);

  onMount(() => {
    if (autoPasskey && hasPasskey) passkey(true);
  });
</script>

<div class="flex flex-col gap-5">
  {#if hasPasskey && !recovery}
    <Button size="lg" class="self-start" disabled={busy} onclick={() => passkey()}>
      {#if busy}<Spinner />{:else}<KeyRoundIcon />{/if}
      {actionLabel} with a passkey
    </Button>
  {/if}

  {#if showCode}
    <form class="flex flex-col gap-2" onsubmit={submit}>
      <Label for="{id}-code">{recovery ? "Recovery code" : hasPasskey ? "Or a code from your authenticator app" : "Code from your authenticator app"}</Label>
      <div class="flex flex-wrap gap-2">
        <Input
          id="{id}-code"
          bind:value={code}
          autocomplete="one-time-code"
          inputmode={recovery ? "text" : "numeric"}
          placeholder={recovery ? "xxxx-xxxx-xxxx-xxxx" : "123 456"}
          class="w-48 tracking-wider tabular-nums"
          disabled={busy}
        />
        <Button type="submit" variant={hasPasskey && !recovery ? "outline" : "default"} disabled={busy || !code.trim()}>
          {#if busy && !hasPasskey}<Spinner />{/if}
          {actionLabel}
        </Button>
      </div>
    </form>
  {/if}

  {#if !hasTOTP && !hasPasskey && !hasRecovery}
    <ErrorState inline error="This account has no two-step verification method that works in this browser." />
  {/if}
  {#if error}<ErrorState inline {error} />{/if}

  {#if hasRecovery && (hasTOTP || hasPasskey)}
    <button
      type="button"
      class="self-start text-meta text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
      onclick={() => {
        recoveryChosen = !recoveryChosen;
        code = "";
        error = "";
      }}
    >
      {recoveryChosen ? `Use your ${usual} instead` : `Lost your ${usual}? Use a recovery code`}
    </button>
  {/if}
</div>
