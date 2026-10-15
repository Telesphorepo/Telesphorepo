<!--
  Repository settings, Signing (Debian): the key that signs the package lists of this repository.
  apt checks the lists with its public key, which it downloads from /debian/{repo}/key.asc. The
  person with Edit settings picks another key, or with Signing keys creates a new one for this repository; the server then signs
  the lists of this repository again. The change form is its own confirmation step: it asks
  before it changes the key.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import { api, type Repo } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import { href } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { groupFingerprint } from "$lib/format";
  import { Button } from "$lib/components/ui/button";
  import FormSection from "$lib/components/FormSection.svelte";
  import FormGrid from "$lib/components/FormGrid.svelte";
  import FormRow from "$lib/components/FormRow.svelte";
  import CodeSnippet from "$lib/components/CodeSnippet.svelte";
  import CopyValue from "$lib/components/CopyValue.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import SigningKeyChoice, { type KeyChoice } from "$lib/components/SigningKeyChoice.svelte";
  import DownloadIcon from "@lucide/svelte/icons/download";

  let { repo, onChanged }: { repo: Repo; onChanged: () => void } = $props();

  const id = $props.id();
  const keys = new Resource(() => api.signingKeys.list());
  const usage = new Resource(() => api.repos.usage(repo.name));

  const currentName = $derived(typeof repo.config.signing_key === "string" && repo.config.signing_key ? repo.config.signing_key : "default");
  const current = $derived(keys.data?.find((k) => k.name === currentName));
  const keyURL = $derived(`${(usage.data?.base_url || location.origin).replace(/\/+$/, "")}/debian/${encodeURIComponent(repo.name)}/key.asc`);
  const sharedWith = $derived((current?.repos ?? []).filter((r) => r !== repo.name));

  let choice = $state<KeyChoice>({ mode: "existing", name: "" });
  const target = $derived(choice.mode === "existing" ? choice.name : choice.name.trim());
  const changed = $derived(target !== "" && target !== currentName);

  let confirmOpen = $state(false);

  async function save() {
    if (choice.mode === "new") await api.signingKeys.create({ name: target });
    await api.repos.update(repo.name, { config: { ...repo.config, signing_key: target } });
    toast.success(`${repo.name} now signs with ${target}`);
    keys.reload();
    onChanged();
  }
</script>

{#if keys.loading && !keys.data}
  <Loading rows={4} />
{:else if keys.error && !keys.data}
  <ErrorState error={keys.error} title="The signing keys did not load" onRetry={keys.reload} />
{:else}
  <FormSection title="Signing key">
    {#if !current}
      <div class="mb-6"><ErrorState inline error="No key named {currentName} exists. Choose another key below." /></div>
    {:else if current.error}
      <div class="mb-6"><ErrorState inline error="{currentName} cannot sign: {current.error}" /></div>
    {/if}
    <FormGrid>
      <FormRow label="Key" for="{id}-name" help={current ? (sharedWith.length ? `This key also signs ${sharedWith.join(", ")}.` : `This key signs only ${repo.name}.`) : undefined}>
        <div id="{id}-name" class="flex min-h-9 items-center text-body font-medium">{currentName}</div>
      </FormRow>
      {#if current}
        <FormRow label="Fingerprint" for="{id}-fp">
          <div id="{id}-fp" class="flex min-h-9 items-center"><CopyValue value={groupFingerprint(current.fingerprint)} label="fingerprint" /></div>
        </FormRow>
      {/if}
      <FormRow label="Public key for apt" for="{id}-url" help="Each machine that installs from {repo.name} trusts this key." full>
        <div id="{id}-url"><CodeSnippet value={keyURL} /></div>
      </FormRow>
    </FormGrid>
    {#snippet actions()}
      {#if current}
        <Button variant="outline" size="sm" href={api.signingKeys.publicURL(current.name)} download="{current.name}.asc"><DownloadIcon />Public key</Button>
      {/if}
    {/snippet}
  </FormSection>
  <FormSection title="Change the key">
    <SigningKeyChoice keys={keys.data ?? []} repoName={repo.name} exclude={currentName} canCreate={auth.can("keys")} bind:choice />
    {#snippet footer()}
      <Button disabled={!changed} onclick={() => (confirmOpen = true)}>Change the key</Button>
      {#if auth.can("keys")}<Button variant="ghost" href={href("/settings/signing-keys")}>Open signing keys</Button>{/if}
    {/snippet}
  </FormSection>
{/if}

<ConfirmDialog
  bind:open={confirmOpen}
  title="Sign {repo.name} with {target}?"
  confirmLabel="Change the key"
  destructive
  changes={[
    ...(choice.mode === "new" ? [`Creates the signing key ${target}`] : []),
    `Signs the package lists of ${repo.name} again with ${target}`,
    `apt refuses the lists until each machine trusts the new public key from ${keyURL}`,
    `Other repositories keep their keys`,
  ]}
  onConfirm={save}
/>
