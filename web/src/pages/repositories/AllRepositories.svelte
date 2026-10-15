<!--
  Repositories (admins): every repository; a row opens its settings. Pick rows to make them public
  or private at once, or to delete them (after a confirm; more than one asks you to type the count).
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import type { PageProps } from "$lib/router.svelte";
  import { href, repoSettingsPath } from "$lib/router.svelte";
  import { api, errorMessage } from "$lib/api";
  import { Resource, listOf } from "$lib/load.svelte";
  import { count, someNames } from "$lib/words";
  import { Button } from "$lib/components/ui/button";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import RepoTable from "$lib/components/RepoTable.svelte";
  import Section from "$lib/components/Section.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import DatabaseIcon from "@lucide/svelte/icons/database";
  import PlusIcon from "@lucide/svelte/icons/plus";

  let {}: PageProps = $props();

  const repos = new Resource(() => api.repos.list());
  const formats = new Resource(() => api.formats.list());
  const titles = $derived(Object.fromEntries((formats.data ?? []).map((f) => [f.name, f.title])));
  const source = listOf(repos, (items) => [...items].sort((a, b) => a.name.localeCompare(b.name)));
  const newRepo = href("/settings/repositories/new");

  // --- bulk actions ---
  let picked = $state(new Set<string>());
  const names = $derived([...picked].sort((a, b) => a.localeCompare(b)));
  const repoCount = (n: number) => count(n, "repository", "repositories");
  let busy = $state(false);

  /** Keeps only the picked names that still exist after a reload. */
  async function reloadKeepingPick() {
    await repos.reload();
    const left = new Set((repos.data ?? []).map((r) => r.name));
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
    (repos.data ?? []).filter((r) => picked.has(r.name)).reduce((n, r) => n + r.file_count, 0),
  );

  async function remove() {
    const list = names;
    try {
      const r = await api.repos.delete(list);
      toast.success(`Deleted ${repoCount(r.deleted.length)}.`);
      picked = new Set();
    } finally {
      await reloadKeepingPick();
    }
  }
</script>

<PageHeader title="All repositories">
  {#snippet actions()}<Button href={newRepo}><PlusIcon />New repository</Button>{/snippet}
</PageHeader>

<Section>
  <RepoTable {source} {titles} href={(r) => href(repoSettingsPath(r.name))} bind:picked>
    {#snippet bulk()}
      <Button variant="outline" size="sm" disabled={busy} onclick={() => setPublic(true)}>Make public</Button>
      <Button variant="outline" size="sm" disabled={busy} onclick={() => setPublic(false)}>Make private</Button>
      <Button variant="ghost" size="sm" class="hover:bg-destructive/10 hover:text-destructive" disabled={busy} onclick={() => (deleteOpen = true)}>Delete</Button>
    {/snippet}
    {#snippet empty()}
      <CompactEmpty icon={DatabaseIcon} text="None yet. Create one, then publish to it from your build or upload files.">
        <Button variant="outline" href={newRepo}><PlusIcon />New repository</Button>
      </CompactEmpty>
    {/snippet}
  </RepoTable>
</Section>

<ConfirmDialog
  bind:open={deleteOpen}
  title="Delete {repoCount(names.length)}?"
  description="{someNames(names)}."
  confirmLabel="Delete {repoCount(names.length)}"
  destructive
  typeToConfirm={names.length > 1 ? String(names.length) : undefined}
  changes={[
    `Deletes the ${count(deleteFiles, "file", "files")} of ${names.length === 1 ? "this repository" : "these repositories"} from the storage`,
    "Removes the access that people and CI pipelines have to them, and their network rules",
    "Builds that use them can no longer download from them",
  ]}
  onConfirm={remove}
/>
