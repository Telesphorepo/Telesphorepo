<!--
  The Overview card of a repository's redirects: other names that answer for it, publishes
  included. After a rename the old name shows here; with All repositories, Add redirect opens a
  short form to add one without a rename. Each row says when a request last used the name; Remove (All repositories) asks
  first, names what stops working and frees the name.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import { api, type Repo, type RepoRedirect } from "$lib/api";
  import { Resource, listOf } from "$lib/load.svelte";
  import { auth } from "$lib/auth.svelte";
  import { formatRelative } from "$lib/format";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import Section from "$lib/components/Section.svelte";
  import ItemList from "$lib/components/ItemList.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import ShortFormDialog from "$lib/components/ShortFormDialog.svelte";
  import FormField from "$lib/components/FormField.svelte";
  import SignpostIcon from "@lucide/svelte/icons/signpost";
  import PlusIcon from "@lucide/svelte/icons/plus";

  let { repo }: { repo: Repo } = $props();

  /* svelte-ignore state_referenced_locally */
  const redirects = new Resource(() => api.repos.redirects(repo.name));
  const canEdit = $derived(auth.can("repositories.all"));

  const DAY = 86_400_000;
  // "Used 3 minutes ago" for a name in use; "Not used for 40 days" after a week without a request.
  function used(rd: RepoRedirect, now = Date.now()): string {
    const since = rd.last_used_at ?? rd.created_at;
    const days = Math.floor((now - Date.parse(since)) / DAY);
    if (days >= 7) return `Not used for ${days.toLocaleString()} days`;
    return rd.last_used_at ? `Used ${formatRelative(rd.last_used_at, now)}` : "Not used yet";
  }

  let addOpen = $state(false);
  let newName = $state("");
  function startAdd() {
    newName = "";
    addOpen = true;
  }
  async function add() {
    const rd = await api.repos.addRedirect(repo.name, newName.trim());
    toast.success(`${rd.name} now answers for ${repo.name}`);
    await redirects.reload();
  }

  let removing = $state<RepoRedirect | null>(null);
  let confirmOpen = $state(false);
  const changes = $derived(
    removing
      ? [
          `Builds and projects that still use ${removing.name} fail until they change to ${repo.name}`,
          used(removing),
          `Frees the name ${removing.name} for a new repository`,
        ]
      : [],
  );

  async function remove() {
    if (!removing) return;
    await api.repos.removeRedirect(repo.name, removing.name);
    toast.success(`Removed the redirect ${removing.name}`);
    await redirects.reload();
  }
</script>

<Section
  title="Redirects"
  description="Other names of {repo.name}. Use one anywhere you would use {repo.name}: to download, to publish, in a build."
>
  {#snippet actions()}
    {#if canEdit && redirects.data?.length}
      <Button variant="outline" size="sm" onclick={startAdd}><PlusIcon />Add redirect</Button>
    {/if}
  {/snippet}
  <ItemList source={listOf(redirects)} caption="Redirects to {repo.name}" key={(rd) => rd.name}>
    {#snippet leading()}<SignpostIcon class="size-5" />{/snippet}
    {#snippet title(rd)}{rd.name}{/snippet}
    {#snippet meta(rd)}{used(rd)} · redirect since {formatRelative(rd.created_at)}{/snippet}
    {#snippet trailing(rd)}
      {#if canEdit}
        <Button
          variant="ghost"
          size="sm"
          class="hover:bg-destructive/10 hover:text-destructive"
          onclick={() => {
            removing = rd;
            confirmOpen = true;
          }}>Remove</Button
        >
      {/if}
    {/snippet}
    {#snippet empty()}
      <CompactEmpty icon={SignpostIcon} text="None yet. When you rename the repository, its old name stays here as a redirect.">
        {#if canEdit}<Button size="sm" variant="outline" onclick={startAdd}><PlusIcon />Add redirect</Button>{/if}
      </CompactEmpty>
    {/snippet}
  </ItemList>
</Section>

<ShortFormDialog
  bind:open={addOpen}
  title="Add a redirect to {repo.name}"
  submitLabel="Add redirect"
  canSubmit={!!newName.trim()}
  onSubmit={add}
>
  <FormField label="Name" for="redirect-name" hint="Enter a name that is not a repository or a redirect yet.">
    <Input id="redirect-name" bind:value={newName} autocomplete="off" />
  </FormField>
</ShortFormDialog>

<ConfirmDialog
  bind:open={confirmOpen}
  title="Remove the redirect {removing?.name ?? ''}?"
  confirmLabel="Remove redirect"
  destructive
  {changes}
  onConfirm={remove}
/>
