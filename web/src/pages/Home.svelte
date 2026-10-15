<!--
  Browse home: every repository the visitor can download from, in one table. A filter box and a
  format filter narrow it in the browser (the list is never paged). People with All repositories create a repository
  from the toolbar.
-->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import { href, repoPath } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { api, type Repo } from "$lib/api";
  import { Resource, listOf } from "$lib/load.svelte";
  import * as InputGroup from "$lib/components/ui/input-group";
  import * as Select from "$lib/components/ui/select";
  import { Button } from "$lib/components/ui/button";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Toolbar from "$lib/components/Toolbar.svelte";
  import RepoTable from "$lib/components/RepoTable.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import { formatLook } from "$lib/formats";
  import DatabaseIcon from "@lucide/svelte/icons/database";
  import SearchIcon from "@lucide/svelte/icons/search";
  import SearchXIcon from "@lucide/svelte/icons/search-x";
  import PlusIcon from "@lucide/svelte/icons/plus";

  let {}: PageProps = $props();

  const repos = new Resource(() => api.repos.list());
  // Format titles; the built-in title or the name stands in until they load or if they fail.
  const formats = new Resource(() => api.formats.list());
  const titles = $derived(Object.fromEntries((formats.data ?? []).map((f) => [f.name, f.title])));
  const titleOf = (format: string) => titles[format] ?? formatLook(format).title ?? format;

  let filter = $state("");
  let format = $state("");
  const anyFormat = "*";

  const all = $derived([...(repos.data ?? [])].sort((a, b) => a.name.localeCompare(b.name)));
  const present = $derived([...new Set(all.map((r) => r.format))].sort((a, b) => titleOf(a).localeCompare(titleOf(b))));
  const filtered = $derived(filter.trim() !== "" || format !== "");

  const matches = (r: Repo) => {
    const f = filter.trim().toLowerCase();
    return (!format || r.format === format) && (!f || r.name.toLowerCase().includes(f) || r.description.toLowerCase().includes(f));
  };
  const source = listOf(repos, (items) => [...items].sort((a, b) => a.name.localeCompare(b.name)).filter(matches));

  const clear = () => {
    filter = "";
    format = "";
  };
  const newRepo = href("/settings/repositories/new");
</script>

<PageHeader title="Repositories" />

{#if all.length > 0}
  <Toolbar>
    <InputGroup.Root class="sm:w-64">
      <InputGroup.Addon><SearchIcon /></InputGroup.Addon>
      <InputGroup.Input bind:value={filter} type="search" placeholder="Filter by name or description" aria-label="Filter repositories" />
    </InputGroup.Root>
    {#if present.length > 1}
      <Select.Root type="single" value={format || anyFormat} onValueChange={(v) => (format = v === anyFormat ? "" : v)}>
        <Select.Trigger class="w-full sm:w-64" aria-label="Format">
          <span class="truncate">{format ? titleOf(format) : "All formats"}</span>
        </Select.Trigger>
        <Select.Content>
          <Select.Item value={anyFormat} label="All formats" />
          {#each present as f (f)}
            <Select.Item value={f} label={titleOf(f)} />
          {/each}
        </Select.Content>
      </Select.Root>
    {/if}
    {#snippet end()}
      {#if auth.can("repos")}<Button href={newRepo}><PlusIcon />New repository</Button>{/if}
    {/snippet}
  </Toolbar>
{/if}

<RepoTable {source} {titles} href={(r) => href(repoPath(r.name))}>
  {#snippet empty()}
    {#if filtered && all.length > 0}
      <CompactEmpty icon={SearchXIcon} title="No repository matches" text="Change the filter, or clear it to see every repository.">
        <Button variant="outline" onclick={clear}>Clear the filter</Button>
      </CompactEmpty>
    {:else if auth.can("repos")}
      <EmptyState
        icon={DatabaseIcon}
        title="No repositories yet"
        description="A repository holds one kind of package: Maven and Gradle libraries, Debian packages, or plain files."
      >
        <Button href={newRepo}><PlusIcon />New repository</Button>
      </EmptyState>
    {:else if auth.user}
      <EmptyState icon={DatabaseIcon} title="No repositories to show" description="You do not have access to a repository yet. Ask an administrator for access." />
    {:else}
      <EmptyState icon={DatabaseIcon} title="No public repositories" description="Sign in to see the private repositories that you have access to." />
    {/if}
  {/snippet}
</RepoTable>
