<!--
  Admin: CI trust policies. A policy lets a CI system (GitHub Actions, Jenkins) publish without a
  stored secret. Presets fill the issuer and the claims in the editor sheet.
-->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import { toast } from "svelte-sonner";
  import { api, type Trust } from "$lib/api";
  import { Keyset, Resource } from "$lib/load.svelte";
  import { Button } from "$lib/components/ui/button";
  import { Badge } from "$lib/components/ui/badge";
  import * as Table from "$lib/components/ui/table";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import KeysetTable from "$lib/components/KeysetTable.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import TrustSheet from "$lib/components/TrustSheet.svelte";
  import ShieldCheckIcon from "@lucide/svelte/icons/shield-check";
  import PlusIcon from "@lucide/svelte/icons/plus";

  let { params, query }: PageProps = $props();

  const list = new Keyset((p) => api.publish.trusts.list(p));
  const repos = new Resource(() => api.repos.list());

  let sheetOpen = $state(false);
  let sheetSeq = $state(0);
  let editing = $state<Trust | null>(null);

  function openSheet(t: Trust | null) {
    editing = t;
    sheetSeq++;
    sheetOpen = true;
  }

  let toggling = $state<Trust | null>(null);
  let confirmToggle = $state(false);
  async function toggle() {
    if (!toggling) return;
    const t = toggling;
    await api.publish.trusts.update(t.id, { disabled: !t.disabled });
    toast.success(`${t.disabled ? "Enabled" : "Disabled"} ${t.name}`);
    list.reload();
  }

  let deleting = $state<Trust | null>(null);
  let confirmDelete = $state(false);
  async function remove() {
    if (!deleting) return;
    const t = deleting;
    await api.publish.trusts.delete(t.id);
    toast.success(`Deleted ${t.name}`);
    list.reload();
  }
</script>

<PageHeader
  title="Trust policies"
  description="A trust policy lets a CI system publish without a stored secret. The CI job sends its signed identity token, and the server opens a short publish session when the token matches a policy."
>
  {#snippet actions()}
    <Button onclick={() => openSheet(null)}><PlusIcon /> New trust policy</Button>
  {/snippet}
</PageHeader>

{#if repos.error}<div class="mb-8"><ErrorState error={repos.error} title="The repository list did not load" onRetry={repos.reload} /></div>{/if}

<KeysetTable {list} columns={6} caption="Trust policies">
  {#snippet head()}
    <Table.Head>Name</Table.Head>
    <Table.Head>Issuer</Table.Head>
    <Table.Head>Claims</Table.Head>
    <Table.Head>Repositories</Table.Head>
    <Table.Head>Created</Table.Head>
    <Table.Head class="w-px"><span class="sr-only">Actions</span></Table.Head>
  {/snippet}
  {#snippet row(t)}
    <Table.Row>
      <Table.Cell class="font-medium">
        {t.name}
        {#if t.disabled}<Badge variant="secondary" class="ml-1">Disabled</Badge>{/if}
      </Table.Cell>
      <Table.Cell class="max-w-64 font-mono text-[0.8125rem] break-all whitespace-normal">{t.issuer}</Table.Cell>
      <Table.Cell class="font-mono text-[0.8125rem] whitespace-normal">
        {#each Object.entries(t.claims) as [k, v] (k)}
          <div class="break-all">{k}: {v.join(", ")}</div>
        {/each}
      </Table.Cell>
      <Table.Cell class="whitespace-normal">{t.repos.join(", ")}</Table.Cell>
      <Table.Cell><RelativeTime at={t.created_at} /></Table.Cell>
      <Table.Cell>
        <div class="flex justify-end gap-2">
          <Button variant="outline" size="sm" onclick={() => openSheet(t)}>Edit</Button>
          <Button
            variant="outline"
            size="sm"
            onclick={() => {
              toggling = t;
              confirmToggle = true;
            }}>{t.disabled ? "Enable" : "Disable"}</Button
          >
          <Button
            variant="destructive"
            size="sm"
            onclick={() => {
              deleting = t;
              confirmDelete = true;
            }}>Delete</Button
          >
        </div>
      </Table.Cell>
    </Table.Row>
  {/snippet}
  {#snippet empty()}
    <EmptyState
      icon={ShieldCheckIcon}
      title="No trust policies yet"
      description="Add a policy to let GitHub Actions or Jenkins publish without a stored secret."
    >
      <Button onclick={() => openSheet(null)}><PlusIcon /> New trust policy</Button>
    </EmptyState>
  {/snippet}
</KeysetTable>

{#key sheetSeq}
  <TrustSheet bind:open={sheetOpen} trust={editing} repos={repos.data ?? []} onSaved={list.reload} />
{/key}

<ConfirmDialog
  bind:open={confirmToggle}
  title={toggling?.disabled ? "Enable trust policy" : "Disable trust policy"}
  confirmLabel={toggling?.disabled ? `Enable ${toggling.name}` : `Disable ${toggling?.name ?? "the policy"}`}
  destructive={!toggling?.disabled}
  changes={toggling?.disabled
    ? [`CI jobs that match ${toggling.name} can publish again`]
    : ["Stops CI jobs from opening new publish sessions with this policy", "Keeps the policy, so you can enable it again"]}
  onConfirm={toggle}
/>

<ConfirmDialog
  bind:open={confirmDelete}
  title="Delete trust policy"
  confirmLabel={`Delete ${deleting?.name ?? "the policy"}`}
  destructive
  changes={[
    "Ends the open publish sessions that this policy created",
    "Stops CI jobs from publishing with this policy",
  ]}
  onConfirm={remove}
/>
