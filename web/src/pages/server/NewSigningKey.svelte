<!--
  New signing key (administrators): create an Ed25519 key, or import an armored private key (with
  its passphrase, if it has one). The server stores the key encrypted. Repositories choose their
  key on their Signing tab.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import type { PageProps } from "$lib/router.svelte";
  import { href, navigate } from "$lib/router.svelte";
  import { api, errorMessage } from "$lib/api";
  import * as RadioGroup from "$lib/components/ui/radio-group";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Textarea } from "$lib/components/ui/textarea";
  import { Spinner } from "$lib/components/ui/spinner";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import FormPage from "$lib/components/FormPage.svelte";
  import FormSection from "$lib/components/FormSection.svelte";
  import FormGrid from "$lib/components/FormGrid.svelte";
  import FormRow from "$lib/components/FormRow.svelte";
  import OptionRow from "$lib/components/OptionRow.svelte";
  import CodeSnippet from "$lib/components/CodeSnippet.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";

  let {}: PageProps = $props();

  const id = $props.id();
  const listPath = "/settings/signing-keys";
  const nameRE = /^[a-z0-9][a-z0-9._-]{0,63}$/;

  let mode = $state("create");
  let name = $state("");
  let armored = $state("");
  let passphrase = $state("");
  let busy = $state(false);
  let error = $state("");

  const nameError = $derived(name && !nameRE.test(name) ? "Use lowercase letters, digits, dots, dashes and underscores." : "");
  const canSave = $derived(!!name && !nameError && (mode === "create" || armored.trim() !== "") && !busy);

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (!canSave) return;
    busy = true;
    error = "";
    try {
      const k = await api.signingKeys.create(mode === "create" ? { name } : { name, armored: armored.trim(), passphrase: passphrase || undefined });
      toast.success(mode === "create" ? `Created the signing key ${k.name}` : `Imported the signing key ${k.name}`);
      navigate(listPath);
    } catch (err) {
      error = errorMessage(err);
    } finally {
      passphrase = "";
      busy = false;
    }
  }
</script>

<PageHeader title="New signing key" />

<FormPage>
  <form onsubmit={submit} autocomplete="off">
    <FormSection title="Where the key comes from">
      <RadioGroup.Root class="gap-1" aria-label="Where the key comes from" bind:value={mode}>
        <OptionRow id="{id}-create" label="Create a new key" description="The server creates an Ed25519 key. Machines trust it after they download its public key.">
          {#snippet control()}<RadioGroup.Item id="{id}-create" value="create" />{/snippet}
        </OptionRow>
        <OptionRow id="{id}-import" label="Import a key" description="Keep the key that machines already trust, for example the one of an older package server.">
          {#snippet control()}<RadioGroup.Item id="{id}-import" value="import" />{/snippet}
        </OptionRow>
      </RadioGroup.Root>
    </FormSection>

    <FormSection title="Key">
      <FormGrid>
        <FormRow label="Name" for="{id}-name" help="Repositories refer to the key by this name." error={nameError || undefined}>
          <Input id="{id}-name" spellcheck={false} aria-describedby="{id}-name-help" aria-invalid={nameError ? true : undefined} bind:value={name} />
        </FormRow>
        {#if mode === "import"}
          <FormRow label="Passphrase" for="{id}-pass" help="Leave it empty when the key has no passphrase.">
            <Input id="{id}-pass" type="password" autocomplete="off" aria-describedby="{id}-pass-help" bind:value={passphrase} />
          </FormRow>
          <FormRow label="Private key" for="{id}-armored" help="Paste the key in ASCII armor, as gpg exports it." full>
            <Textarea id="{id}-armored" rows={8} spellcheck={false} placeholder="-----BEGIN PGP PRIVATE KEY BLOCK-----" aria-describedby="{id}-armored-help" bind:value={armored} />
            {#snippet more()}
              <p>Export the key with this command:</p>
              <div class="mt-2"><CodeSnippet value="gpg --armor --export-secret-keys KEY_ID" /></div>
              <p class="mt-2">The server keeps the key encrypted, without its passphrase. Nobody can download the private key again, only its public key.</p>
            {/snippet}
          </FormRow>
        {/if}
      </FormGrid>
      {#snippet footer()}
        <Button type="submit" disabled={!canSave}>
          {#if busy}<Spinner />{/if}
          {mode === "create" ? "Create key" : "Import key"}
        </Button>
        <Button variant="ghost" href={href(listPath)}>Cancel</Button>
        {#if error}<div class="w-full"><ErrorState inline {error} /></div>{/if}
      {/snippet}
    </FormSection>
  </form>
</FormPage>
