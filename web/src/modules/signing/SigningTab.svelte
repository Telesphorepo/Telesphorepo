<!--
  Repository settings, Signing (the signing module's repo tab; a format names it in repo_tabs):
  the key that signs the indexes of this repository. Clients check the indexes with its public
  key, which the format serves at the path public_key of its descriptor. The person with Edit
  settings picks another key, or with Signing keys creates a new one for this repository; the
  server then signs the indexes of this repository again. The change form is its own
  confirmation step: it asks before it changes the key.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import { api } from "$lib/api";
  import { formats } from "$lib/formats/registry.svelte";
  import type { RepoTabProps } from "../types";
  import { Resource } from "$lib/load.svelte";
  import { href } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { formatSize, groupFingerprint } from "$lib/format";
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

  let { repo, onChanged }: RepoTabProps = $props();

  const id = $props.id();
  const keys = new Resource(() => api.signingKeys.list());
  const usage = new Resource(() => api.repos.usage(repo.name));

  const currentName = $derived(typeof repo.fields.signing_key === "string" ? repo.fields.signing_key : "");
  const current = $derived(keys.data?.find((k) => k.name === currentName));
  const keyPath = $derived(formats.get(repo.format)?.public_key ?? "");
  const keyNote = $derived(formats.get(repo.format)?.key_note ?? "");
  // A format whose key change rewrites more than the indexes says what, with the repo's size.
  const keyChange = $derived(
    (formats.get(repo.format)?.key_change ?? "").replaceAll("{repo}", repo.name).replaceAll("{size}", formatSize(repo.total_size)),
  );
  const keyURL = $derived(
    keyPath ? `${(usage.data?.base_url || location.origin).replace(/\/+$/, "")}/${repo.format}/${encodeURIComponent(repo.name)}/${keyPath}` : "",
  );
  const sharedWith = $derived((current?.repos ?? []).filter((r) => r !== repo.name));

  let choice = $state<KeyChoice>({ mode: "existing", name: "" });
  const target = $derived(choice.mode === "existing" ? choice.name : choice.name.trim());
  const changed = $derived(target !== "" && target !== currentName);

  let confirmOpen = $state(false);

  async function save() {
    if (choice.mode === "new") await api.signingKeys.create({ name: target });
    await api.repos.update(repo.name, { fields: { signing_key: target } });
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
      <FormRow label="Key" for="{id}-name" help={[current ? (sharedWith.length ? `This key also signs ${sharedWith.join(", ")}.` : `This key signs only ${repo.name}.`) : "", keyNote].filter(Boolean).join(" ") || undefined}>
        <div id="{id}-name" class="flex min-h-9 items-center text-body font-medium">{currentName}</div>
      </FormRow>
      {#if current}
        <FormRow label="Fingerprint" for="{id}-fp">
          <div id="{id}-fp" class="flex min-h-9 items-center"><CopyValue value={groupFingerprint(current.fingerprint)} label="fingerprint" /></div>
        </FormRow>
      {/if}
      {#if keyURL}
        <FormRow label="Public key" for="{id}-url" help="Each machine that installs from {repo.name} trusts this key." full>
          <div id="{id}-url"><CodeSnippet value={keyURL} /></div>
        </FormRow>
      {/if}
    </FormGrid>
    {#snippet actions()}
      {#if current}
        <Button variant="outline" size="sm" href={api.signingKeys.publicURL(current.name)} download="{current.name}.asc"><DownloadIcon />Public key</Button>
      {/if}
    {/snippet}
  </FormSection>
  <FormSection title="Change the key">
    <SigningKeyChoice keys={keys.data ?? []} repoName={repo.name} exclude={currentName} canCreate={auth.can("signing.manage")} bind:choice />
    {#snippet footer()}
      <Button disabled={!changed} onclick={() => (confirmOpen = true)}>Change the key</Button>
      {#if auth.can("signing.manage")}<Button variant="ghost" href={href("/settings/signing-keys")}>Open signing keys</Button>{/if}
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
    `Signs the indexes of ${repo.name} again with ${target}`,
    ...(keyChange ? [keyChange] : []),
    `Clients refuse the indexes until each machine trusts the new public key${keyURL ? ` from ${keyURL}` : ""}`,
    `Other repositories keep their keys`,
  ]}
  onConfirm={save}
/>
