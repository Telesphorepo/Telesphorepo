<!--
  The Overview card of a repository with old names (redirects): every request to an old name
  reaches this repository, publishes included. Each row says when a request last used the name;
  Remove (All repositories) asks first, names what stops working and frees the name. The card
  shows only while the repository has redirects.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import { api, type Repo, type RepoRedirect } from "$lib/api";
  import { Resource, listOf } from "$lib/load.svelte";
  import { auth } from "$lib/auth.svelte";
  import { formatRelative } from "$lib/format";
  import { Button } from "$lib/components/ui/button";
  import Section from "$lib/components/Section.svelte";
  import ItemList from "$lib/components/ItemList.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import SignpostIcon from "@lucide/svelte/icons/signpost";

  let { repo }: { repo: Repo } = $props();

  /* svelte-ignore state_referenced_locally */
  const redirects = new Resource(() => api.repos.redirects(repo.name));

  const DAY = 86_400_000;
  // "Used 3 minutes ago" for a name in use; "Not used for 40 days" after a week without a request.
  function used(rd: RepoRedirect, now = Date.now()): string {
    const since = rd.last_used_at ?? rd.created_at;
    const days = Math.floor((now - Date.parse(since)) / DAY);
    if (days >= 7) return `Not used for ${days.toLocaleString()} days`;
    return rd.last_used_at ? `Used ${formatRelative(rd.last_used_at, now)}` : "Not used yet";
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

{#if redirects.data?.length || redirects.error}
  <Section title="Redirects" description="Old names of {repo.name}. Every request to one of them reaches this repository.">
    <ItemList source={listOf(redirects)} caption="Redirects to {repo.name}" key={(rd) => rd.name}>
      {#snippet leading()}<SignpostIcon class="size-5" />{/snippet}
      {#snippet title(rd)}{rd.name}{/snippet}
      {#snippet meta(rd)}{used(rd)} · redirect since {formatRelative(rd.created_at)}{/snippet}
      {#snippet trailing(rd)}
        {#if auth.can("repositories.all")}
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
    </ItemList>
  </Section>
{/if}

<ConfirmDialog
  bind:open={confirmOpen}
  title="Remove the redirect {removing?.name ?? ''}?"
  confirmLabel="Remove redirect"
  destructive
  {changes}
  onConfirm={remove}
/>
