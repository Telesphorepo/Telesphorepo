<!--
  A dialog that asks for a fresh second factor before a sensitive action: approve a device, create
  a publish session, upload or delete. onProof receives the proof and does the action; when it
  resolves the dialog closes, when it throws the dialog shows the error.

    <SecondFactorPrompt bind:open title="Create a publish session" actionLabel="Create session"
      onProof={async (proof) => { session = await api.publish.manualSession(repo, proof); }} />

  A user without a second factor gets a link to set one up (in dev mode, a way to continue without).
-->
<script lang="ts">
  import * as Dialog from "$lib/components/ui/dialog";
  import { Button } from "$lib/components/ui/button";
  import { Spinner } from "$lib/components/ui/spinner";
  import SecondFactorForm from "./SecondFactorForm.svelte";
  import LoadingRows from "./LoadingRows.svelte";
  import ErrorState from "./ErrorState.svelte";
  import { api, errorMessage, type Proof, type SecondFactorMethod } from "$lib/api";
  import { href } from "$lib/router.svelte";

  let {
    open = $bindable(false),
    title,
    description = "Confirm it is you with your authenticator app or a passkey.",
    actionLabel,
    onProof,
  }: {
    open?: boolean;
    title: string;
    description?: string;
    actionLabel: string;
    onProof: (p: Proof) => Promise<unknown>;
  } = $props();

  let methods = $state<SecondFactorMethod[] | null>(null);
  let dev = $state(false);
  let loadError = $state("");
  let devBusy = $state(false);
  let devError = $state("");

  async function loadMethods() {
    methods = null;
    loadError = "";
    try {
      const [f, cfg] = await Promise.all([api.security.get(), api.auth.config()]);
      const m: SecondFactorMethod[] = [];
      if (f.totp.length) m.push("totp");
      if (f.passkeys.length) m.push("passkey");
      methods = m;
      dev = cfg.second_factor_optional;
    } catch (e) {
      loadError = errorMessage(e);
    }
  }

  $effect(() => {
    if (open) loadMethods();
  });

  async function proof(p: Proof) {
    await onProof(p);
    open = false;
  }

  async function withoutFactor() {
    devBusy = true;
    devError = "";
    try {
      await proof({});
    } catch (e) {
      devError = errorMessage(e);
    } finally {
      devBusy = false;
    }
  }
</script>

<Dialog.Root bind:open>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{title}</Dialog.Title>
      <Dialog.Description>{description}</Dialog.Description>
    </Dialog.Header>
    {#if loadError}
      <ErrorState inline error={loadError} />
    {:else if methods === null}
      <LoadingRows rows={2} />
    {:else if methods.length === 0}
      <div class="flex flex-col gap-3 text-sm">
        <p>This action needs a second factor, and your account has none yet.</p>
        <Button href={href("/settings/security")} class="self-start" onclick={() => (open = false)}>
          Set up a second factor
        </Button>
        {#if dev}
          <p class="text-muted-foreground">Dev mode makes the second factor optional.</p>
          <Button variant="outline" class="self-start" disabled={devBusy} onclick={withoutFactor}>
            {#if devBusy}<Spinner />{/if}
            {actionLabel} without a second factor
          </Button>
          {#if devError}<ErrorState inline error={devError} />{/if}
        {/if}
      </div>
    {:else}
      <SecondFactorForm {methods} {actionLabel} passkeyOptions={api.security.assertPasskeyOptions} onProof={proof} />
    {/if}
  </Dialog.Content>
</Dialog.Root>
