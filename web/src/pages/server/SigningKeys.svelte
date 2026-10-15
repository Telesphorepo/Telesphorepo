<!--
  Signing keys (Signing keys permission): the keys that sign the package lists of Debian repositories, and
  which repositories use each one (links to their Signing tab). Download a public key; delete a key
  that no repository uses. New signing key opens a page that creates or imports one.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import type { PageProps } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { href, repoPath, repoSettingsPath } from "$lib/router.svelte";
  import { api, type SigningKey } from "$lib/api";
  import { Resource, listOf } from "$lib/load.svelte";
  import { groupFingerprint } from "$lib/format";
  import { Button } from "$lib/components/ui/button";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import DataTable from "$lib/components/DataTable.svelte";
  import Cell from "$lib/components/Cell.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import RepoLinks from "./RepoLinks.svelte";
  import FileKeyIcon from "@lucide/svelte/icons/file-key";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import DownloadIcon from "@lucide/svelte/icons/download";

  let {}: PageProps = $props();

  const keys = new Resource(() => api.signingKeys.list());
  const source = listOf(keys);
  const newKey = href("/settings/signing-keys/new");
  const signingTab = (r: string) => href(auth.canRepo(r, "settings") ? repoSettingsPath(r, "signing") : repoPath(r));


  let target = $state<SigningKey | null>(null);
  let deleteOpen = $state(false);

  async function remove() {
    if (!target) return;
    await api.signingKeys.delete(target.name);
    toast.success(`Deleted the signing key ${target.name}`);
    await keys.reload();
  }
  const quietDanger = "hover:bg-destructive/10 hover:text-destructive";
</script>

<PageHeader title="Signing keys" description="Keys that sign the package lists of Debian repositories, so apt can check them.">
  {#snippet actions()}{#if keys.data?.length}<Button href={newKey}><PlusIcon />New signing key</Button>{/if}{/snippet}
</PageHeader>

<Section>
<DataTable
  {source}
  caption="Signing keys"
  key={(k) => k.name}
  columns={[
    { label: "Name" },
    { label: "Fingerprint", grow: true, hideOnPhone: true },
    { label: "Used by" },
    { label: "Added", align: "right", hideOnPhone: true },
    { label: "", srLabel: "Actions" },
  ]}
>
  {#snippet row(k)}
    <Cell>
      <span class="flex items-center gap-2">
        <span class="font-medium">{k.name}</span>
        {#if k.error}<StatusBadge tone="danger" title={k.error}>Cannot sign</StatusBadge>{/if}
      </span>
    </Cell>
    <Cell grow hideOnPhone muted text={groupFingerprint(k.fingerprint)} copy={k.fingerprint} copyLabel="Copy the fingerprint of {k.name}" />
    <Cell class="max-w-56"><RepoLinks repos={k.repos} to={signingTab} empty="No repository" /></Cell>
    <Cell num muted hideOnPhone><RelativeTime at={k.created_at} /></Cell>
    <Cell actions>
      <Button variant="ghost" size="sm" href={api.signingKeys.publicURL(k.name)} download="{k.name}.asc" aria-label="Download the public key of {k.name}">
        <DownloadIcon /><span class="max-sm:sr-only">Public key</span>
      </Button>
      <Button
        variant="ghost"
        size="sm"
        class={quietDanger}
        disabled={k.repos.length > 0}
        title={k.repos.length > 0 ? "Move its repositories to another key first." : undefined}
        onclick={() => {
          target = k;
          deleteOpen = true;
        }}>Delete</Button
      >
    </Cell>
  {/snippet}
  {#snippet empty()}
    <CompactEmpty icon={FileKeyIcon} text="None yet. Debian repositories need a key, so apt can check their package lists.">
      <Button variant="outline" href={newKey}><PlusIcon />New signing key</Button>
    </CompactEmpty>
  {/snippet}
</DataTable>
</Section>

<ConfirmDialog
  bind:open={deleteOpen}
  title="Delete the signing key {target?.name ?? ''}?"
  confirmLabel="Delete key"
  destructive
  changes={["Deletes the private key from this server. You cannot get it back", "Machines that trust its public key keep it until you remove it there"]}
  onConfirm={remove}
/>
