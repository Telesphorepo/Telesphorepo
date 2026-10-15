<!-- Repositories (admins): every repository; a row opens its settings. -->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import { href, repoSettingsPath } from "$lib/router.svelte";
  import { api } from "$lib/api";
  import { Resource, listOf } from "$lib/load.svelte";
  import { Button } from "$lib/components/ui/button";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import RepoTable from "$lib/components/RepoTable.svelte";
  import Section from "$lib/components/Section.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import DatabaseIcon from "@lucide/svelte/icons/database";
  import PlusIcon from "@lucide/svelte/icons/plus";

  let {}: PageProps = $props();

  const repos = new Resource(() => api.repos.list());
  const formats = new Resource(() => api.formats.list());
  const titles = $derived(Object.fromEntries((formats.data ?? []).map((f) => [f.name, f.title])));
  const source = listOf(repos, (items) => [...items].sort((a, b) => a.name.localeCompare(b.name)));
  const newRepo = href("/settings/repositories/new");
</script>

<PageHeader title="All repositories">
  {#snippet actions()}<Button href={newRepo}><PlusIcon />New repository</Button>{/snippet}
</PageHeader>

<Section>
  <RepoTable {source} {titles} href={(r) => href(repoSettingsPath(r.name))}>
    {#snippet empty()}
      <CompactEmpty icon={DatabaseIcon} text="None yet. Create one, then publish to it from your build or upload files.">
        <Button variant="outline" href={newRepo}><PlusIcon />New repository</Button>
      </CompactEmpty>
    {/snippet}
  </RepoTable>
</Section>
