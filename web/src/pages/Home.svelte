<!--
  Browse home: every repository the visitor can download from, as one calm list sorted by name
  (RepoList): the format icon, the name, the description, the format and the kind in plain words.
  A filter box and a format filter narrow it in the browser (the list is never paged). People with
  All repositories create a repository from the toolbar.
-->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import { href, repoPath } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { api, type Repo } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import { formats } from "$lib/formats/registry.svelte";
  import * as InputGroup from "$lib/components/ui/input-group";
  import * as Select from "$lib/components/ui/select";
  import { Button } from "$lib/components/ui/button";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Toolbar from "$lib/components/Toolbar.svelte";
  import RepoList from "$lib/components/RepoList.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import DatabaseIcon from "@lucide/svelte/icons/database";
  import SearchIcon from "@lucide/svelte/icons/search";
  import SearchXIcon from "@lucide/svelte/icons/search-x";
  import PlusIcon from "@lucide/svelte/icons/plus";

  let {}: PageProps = $props();

  const repos = new Resource(() => api.repos.list());
  formats.load();

  let filter = $state("");
  let format = $state("");
  const anyFormat = "*";

  const all = $derived([...(repos.data ?? [])].sort((a, b) => a.name.localeCompare(b.name)));
  const present = $derived([...new Set(all.map((r) => r.format))].sort((a, b) => formats.title(a).localeCompare(formats.title(b))));
  const filtered = $derived(filter.trim() !== "" || format !== "");

  const matches = (r: Repo) => {
    const f = filter.trim().toLowerCase();
    return (!format || r.format === format) && (!f || r.name.toLowerCase().includes(f) || r.description.toLowerCase().includes(f));
  };
  const shown = $derived(all.filter(matches));

  const clear = () => {
    filter = "";
    format = "";
  };
  const newRepo = href("/settings/repositories/new");
</script>

<PageHeader title="Repositories" />

{#if all.length > 0}
  <Toolbar>
    <InputGroup.Root class="sm:w-72">
      <InputGroup.Addon><SearchIcon /></InputGroup.Addon>
      <InputGroup.Input bind:value={filter} type="search" placeholder="Filter by name or description" aria-label="Filter repositories" />
    </InputGroup.Root>
    {#if present.length > 1}
      <Select.Root type="single" value={format || anyFormat} onValueChange={(v) => (format = v === anyFormat ? "" : v)}>
        <Select.Trigger class="w-full sm:w-56" aria-label="Format">
          <span class="truncate">{format ? formats.title(format) : "All formats"}</span>
        </Select.Trigger>
        <Select.Content>
          <Select.Item value={anyFormat} label="All formats" />
          {#each present as f (f)}
            <Select.Item value={f} label={formats.title(f)} />
          {/each}
        </Select.Content>
      </Select.Root>
    {/if}
    {#snippet end()}
      {#if auth.can("repositories.all")}<Button href={newRepo}><PlusIcon />New repository</Button>{/if}
    {/snippet}
  </Toolbar>
{/if}

{#if repos.loading && !repos.data}
  <Loading rows={5} />
{:else if repos.error && !repos.data}
  <ErrorState error={repos.error} title="The repositories did not load" onRetry={repos.reload} />
{:else if shown.length > 0}
  <RepoList repos={shown} href={(r) => href(repoPath(r.name))} />
{:else if filtered && all.length > 0}
  <CompactEmpty icon={SearchXIcon} title="No repository matches" text="Change the filter, or clear it to see every repository.">
    <Button variant="outline" onclick={clear}>Clear the filter</Button>
  </CompactEmpty>
{:else if auth.can("repositories.all")}
  <EmptyState icon={DatabaseIcon} title="No repositories yet" description="A repository holds the packages of one format.">
    <Button href={newRepo}><PlusIcon />New repository</Button>
  </EmptyState>
{:else if auth.user}
  <EmptyState icon={DatabaseIcon} title="No repositories to show" description="You do not have access to a repository yet. Ask an administrator for access." />
{:else}
  <EmptyState icon={DatabaseIcon} title="No public repositories" description="Sign in to see the private repositories that you have access to." />
{/if}
