<!--
  A short dialog that asks for a fresh second factor before an action started from a button
  (delete a file, create a publish token). It holds a <FreshFactor>: onProof does the action;
  when it resolves the dialog closes, when it throws the dialog shows the error.

    <SecondFactorPrompt bind:open title="Confirm it is you" actionLabel="Continue"
      onProof={async (proof) => { keepSession(repo, await api.publish.manualSession(repo, proof)); }} />
-->
<script lang="ts">
  import * as Dialog from "$lib/components/ui/dialog";
  import FreshFactor from "./FreshFactor.svelte";
  import type { Proof } from "$lib/api";

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

  async function proof(p: Proof) {
    await onProof(p);
    open = false;
  }
</script>

<Dialog.Root bind:open>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{title}</Dialog.Title>
      <Dialog.Description>{description}</Dialog.Description>
    </Dialog.Header>
    {#if open}
      <FreshFactor {actionLabel} onProof={proof} onSetup={() => (open = false)} />
    {/if}
  </Dialog.Content>
</Dialog.Root>
