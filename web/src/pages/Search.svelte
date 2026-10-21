<!--
  Search results for query.get("q") (3+ characters), optionally in one repository
  (query.get("repo")). The top bar's search field holds the query; under 640px, where the top bar
  has no field, the page shows one.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import type { PageProps } from "$lib/router.svelte";
  import { filesPath, href, navigate, repoPath } from "$lib/router.svelte";
  import { api, type SearchHit } from "$lib/api";
  import { Keyset, Resource } from "$lib/load.svelte";
  import * as Table from "$lib/components/ui/table";
  import * as Select from "$lib/components/ui/select";
  import * as InputGroup from "$lib/components/ui/input-group";
  import { Button } from "$lib/components/ui/button";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Toolbar from "$lib/components/Toolbar.svelte";
  import DataTable from "$lib/components/DataTable.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import FileSize from "$lib/components/FileSize.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import SearchIcon from "@lucide/svelte/icons/search";
  import FileIcon from "@lucide/svelte/icons/file";

  let { query }: PageProps = $props();

  const minLength = 3;
  const allRepos = "*";
  const q = $derived((query.get("q") ?? "").trim());
  const repo = $derived(query.get("repo") ?? "");

  const repos = new Resource(() => api.repos.list());
  // A group has no files of its own, so search does not offer it.
  const repoNames = $derived((repos.data ?? []).filter((r) => r.kind !== "group").map((r) => r.name).sort((a, b) => a.localeCompare(b)));

  let results = $state<Keyset<SearchHit> | null>(null);
  $effect(() => {
    const term = q;
    const inRepo = repo;
    results = term.length < minLength ? null : untrack(() => new Keyset((p) => api.search(term, { repo: inRepo || undefined, ...p })));
  });

  let draft = $state("");
  $effect(() => {
    draft = q;
  });

  const go = (term: string, inRepo: string) => navigate("/search", { query: { q: term, repo: inRepo || undefined } });
  function submit(e: SubmitEvent) {
    e.preventDefault();
    go(draft.trim(), repo);
  }
</script>

<PageHeader title="Search" />

<form role="search" class="mb-4 sm:hidden" onsubmit={submit}>
  <InputGroup.Root>
    <InputGroup.Addon><SearchIcon /></InputGroup.Addon>
    <InputGroup.Input bind:value={draft} type="search" placeholder="Search files" aria-label="Search files" />
  </InputGroup.Root>
</form>

{#if !results}
  <EmptyState
    icon={SearchIcon}
    title={q ? "Type a longer search" : "Search every repository"}
    description="Type at least {minLength} characters of a file path in the search field."
  />
{:else}
  <Toolbar>
    <Select.Root type="single" value={repo || allRepos} onValueChange={(v) => go(q, v === allRepos ? "" : v)}>
      <Select.Trigger class="sm:w-56" aria-label="Repository">
        <span class="truncate">{repo || "All repositories"}</span>
      </Select.Trigger>
      <Select.Content>
        <Select.Item value={allRepos} label="All repositories" />
        {#if repo && !repoNames.includes(repo)}<Select.Item value={repo} label={repo} />{/if}
        {#each repoNames as name (name)}
          <Select.Item value={name} label={name} />
        {/each}
      </Select.Content>
    </Select.Root>
  </Toolbar>

  <DataTable
    source={results}
    caption="Search results"
    key={(h) => h.repo + "\u0000" + h.path}
    href={(h) => href(filesPath(h.repo, h.path))}
    columns={[{ label: "Path", class: "w-full" }, { label: "Repository" }, { label: "Size", align: "right" }, { label: "Modified" }]}
  >
    {#snippet row(h)}
      <Table.Cell class="max-w-0 min-w-64 whitespace-normal">
        <a href={href(filesPath(h.repo, h.path))} class="flex items-start gap-2 rounded-sm break-all outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50">
          <FileIcon class="mt-0.5 size-4 shrink-0 text-muted-foreground" />
          {h.path}
        </a>
      </Table.Cell>
      <Table.Cell><a href={href(repoPath(h.repo))} class="hover:underline">{h.repo}</a></Table.Cell>
      <Table.Cell class="text-right"><FileSize bytes={h.size} /></Table.Cell>
      <Table.Cell class="text-muted-foreground"><RelativeTime at={h.updated_at} /></Table.Cell>
    {/snippet}
    {#snippet empty()}
      {#if results?.hasMore}
        <EmptyState
          icon={SearchIcon}
          title="No match so far"
          description="The search read a part of the index and found no path with “{q}” in the repositories you can read."
        >
          <Button variant="outline" disabled={results.loadingMore} onclick={results.more}>Search further</Button>
        </EmptyState>
      {:else}
        <EmptyState
          icon={SearchIcon}
          title="No files match"
          description={repo ? `No path in ${repo} contains “${q}”.` : `No path contains “${q}”. Try a shorter part of the path.`}
        >
          {#if repo}<Button variant="outline" onclick={() => go(q, "")}>Search all repositories</Button>{/if}
        </EmptyState>
      {/if}
    {/snippet}
  </DataTable>
{/if}
