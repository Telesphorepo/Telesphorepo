<!--
  Repositories: every repository for a holder of All repositories; for anyone else the repositories
  where they hold a permission that manages it (GET /api/repos?perm=, 100 a page with Load more). A row opens its settings, or its files when the person can
  open no settings tab. The search field keeps the repositories whose name starts with what is
  typed (GET /api/repos?q=, 250 ms after the last key). Pick rows (Edit settings) to make them
  public or private at once, or (All repositories) to delete them after a confirm; more than one
  asks you to type the count.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { toast } from "svelte-sonner";
  import type { PageProps } from "$lib/router.svelte";
  import { href, repoPath, repoSettingsPath } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { api, errorMessage, type Repo } from "$lib/api";
  import { Keyset, Resource } from "$lib/load.svelte";
  import { managingRepoPermissions } from "$lib/permissions";
  import { count, someNames } from "$lib/words";
  import { waitForDeletes } from "$lib/deletes";
  import { Button } from "$lib/components/ui/button";
  import * as InputGroup from "$lib/components/ui/input-group";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Toolbar from "$lib/components/Toolbar.svelte";
  import RepoTable from "$lib/components/RepoTable.svelte";
  import Section from "$lib/components/Section.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import DatabaseIcon from "@lucide/svelte/icons/database";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import SearchIcon from "@lucide/svelte/icons/search";
  import SearchXIcon from "@lucide/svelte/icons/search-x";

  let {}: PageProps = $props();

  // The search asks the server 250 ms after the last key; a new prefix starts a new list.
  let filter = $state("");
  let prefix = $state("");
  $effect(() => {
    const f = filter.trim().toLowerCase();
    const t = setTimeout(() => (prefix = f), 250);
    return () => clearTimeout(t);
  });
  const list = (q: string) => new Keyset((p) => api.repos.list({ q, perm: managingRepoPermissions() }, p), 100);
  let repos = $state<Keyset<Repo>>(untrack(() => list("")));
  let first = true;
  $effect(() => {
    const q = prefix;
    if (first) {
      first = false;
      return;
    }
    repos = untrack(() => list(q));
    picked = new Set();
  });
  // The search shows once there is a repository, also while it finds none.
  let any = $state(false);
  $effect(() => {
    if (repos.items.length > 0) any = true;
  });
  const formats = new Resource(() => api.formats.list());
  const titles = $derived(Object.fromEntries((formats.data ?? []).map((f) => [f.name, f.title])));
  const rowHref = (r: Repo) => href(auth.managesRepo(r.name) ? repoSettingsPath(r.name) : repoPath(r.name));
  const canCreate = $derived(auth.can("repositories.all"));
  const newRepo = href("/settings/repositories/new");

  // --- bulk actions ---
  let picked = $state(new Set<string>());
  const names = $derived([...picked].sort((a, b) => a.localeCompare(b)));
  const repoCount = (n: number) => count(n, "repository", "repositories");
  let busy = $state(false);

  /** Keeps only the picked names that still exist after a reload. */
  async function reloadKeepingPick() {
    await repos.reload();
    const left = new Set(repos.items.map((r) => r.name));
    picked = new Set([...picked].filter((n) => left.has(n)));
  }

  async function setPublic(pub: boolean) {
    const list = names;
    busy = true;
    try {
      await api.repos.setPublic(list, pub);
      toast.success(`${count(list.length, "repository is", "repositories are")} now ${pub ? "public" : "private"}.`);
      picked = new Set();
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      busy = false;
      await reloadKeepingPick();
    }
  }

  let deleteOpen = $state(false);
  const deleteFiles = $derived(
    // A group's files are its members': deleting the group deletes none of them.
    repos.items.filter((r) => picked.has(r.name) && r.kind !== "group").reduce((n, r) => n + r.file_count, 0),
  );

  async function remove() {
    const list = names;
    try {
      const r = await api.repos.delete(list);
      picked = new Set();
      await waitForDeletes(r.deleting.map((d) => d.name));
      toast.success(`Deleted ${repoCount(r.deleting.length)}.`);
    } finally {
      await reloadKeepingPick();
    }
  }
</script>

<PageHeader title="Repositories">
  {#snippet actions()}{#if canCreate}<Button href={newRepo}><PlusIcon />New repository</Button>{/if}{/snippet}
</PageHeader>

{#if any || prefix}
  <Toolbar>
    <InputGroup.Root class="sm:w-72">
      <InputGroup.Addon><SearchIcon /></InputGroup.Addon>
      <InputGroup.Input bind:value={filter} type="search" placeholder="Search by name" aria-label="Search repositories by the start of the name" />
    </InputGroup.Root>
  </Toolbar>
{/if}

<Section>
  <RepoTable source={repos} {titles} href={rowHref} bind:picked pickable={(r) => auth.canRepo(r.name, "repo.settings")}>
    {#snippet bulk()}
      <Button variant="outline" size="sm" disabled={busy} onclick={() => setPublic(true)}>Make public</Button>
      <Button variant="outline" size="sm" disabled={busy} onclick={() => setPublic(false)}>Make private</Button>
      {#if canCreate}<Button variant="ghost" size="sm" class="hover:bg-destructive/10 hover:text-destructive" disabled={busy} onclick={() => (deleteOpen = true)}>Delete</Button>{/if}
    {/snippet}
    {#snippet empty()}
      {#if prefix}
        <CompactEmpty icon={SearchXIcon} text="No repository's name starts with “{prefix}”." />
      {:else if canCreate}
        <CompactEmpty icon={DatabaseIcon} text="None yet. Create one, then publish to it from your build or upload files.">
          <Button variant="outline" href={newRepo}><PlusIcon />New repository</Button>
        </CompactEmpty>
      {:else}
        <CompactEmpty icon={DatabaseIcon} text="You manage no repository. Ask an administrator for a permission on one." />
      {/if}
    {/snippet}
  </RepoTable>
</Section>

<ConfirmDialog
  bind:open={deleteOpen}
  title="Delete {repoCount(names.length)}?"
  description="{someNames(names)}."
  confirmLabel="Delete {repoCount(names.length)}"
  destructive
  typeToConfirm={names.length > 1 ? `delete ${repoCount(names.length)}` : undefined}
  changes={[
    `Deletes the ${count(deleteFiles, "file", "files")} of ${names.length === 1 ? "this repository" : "these repositories"} from the storage`,
    "Removes the access that people and CI pipelines have to them, and their network rules",
    "Builds that use them can no longer download from them",
  ]}
  onConfirm={remove}
/>
