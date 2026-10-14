<!-- Search results for query.get("q") (3+ characters), optionally in one repo (query.get("repo")). -->
<script lang="ts">
  import { untrack } from "svelte";
  import type { PageProps } from "$lib/router.svelte";
  import { encodePath, href, navigate } from "$lib/router.svelte";
  import { api, type SearchHit } from "$lib/api";
  import { Keyset, Resource } from "$lib/load.svelte";
  import * as Table from "$lib/components/ui/table";
  import * as Select from "$lib/components/ui/select";
  import * as InputGroup from "$lib/components/ui/input-group";
  import { Button } from "$lib/components/ui/button";
  import { Label } from "$lib/components/ui/label";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import LoadingRows from "$lib/components/LoadingRows.svelte";
  import LoadMore from "$lib/components/LoadMore.svelte";
  import FileSize from "$lib/components/FileSize.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import SearchIcon from "@lucide/svelte/icons/search";
  import FileIcon from "@lucide/svelte/icons/file";

  let { query }: PageProps = $props();

  const minLength = 3;
  const allRepos = "*";
  const q = $derived((query.get("q") ?? "").trim());
  const repo = $derived(query.get("repo") ?? "");

  // The form edits a copy; submitting opens the new search URL.
  let draft = $state("");
  let draftRepo = $state(allRepos);
  $effect(() => {
    draft = q;
    draftRepo = repo || allRepos;
  });

  const repos = new Resource(() => api.repos.list());
  const repoNames = $derived((repos.data ?? []).map((r) => r.name).sort((a, b) => a.localeCompare(b)));

  let results = $state<Keyset<SearchHit> | null>(null);
  $effect(() => {
    const term = q;
    const inRepo = repo;
    results = term.length < minLength ? null : untrack(() => new Keyset((p) => api.search(term, { repo: inRepo || undefined, ...p })));
  });

  function submit(e: SubmitEvent) {
    e.preventDefault();
    navigate("/search", { query: { q: draft.trim(), repo: draftRepo === allRepos ? undefined : draftRepo } });
  }

  const fileHref = (h: SearchHit) => href("/repo/" + encodeURIComponent(h.repo) + "/" + encodePath(h.path));
  const id = $props.id();
</script>

<PageHeader title="Search" description="Find files by any part of their path." />

<form role="search" class="mb-8 flex max-w-form flex-col gap-4 sm:flex-row sm:items-end" onsubmit={submit}>
  <div class="flex min-w-0 flex-1 flex-col gap-2">
    <Label for="{id}-q">Path contains</Label>
    <InputGroup.Root>
      <InputGroup.Addon><SearchIcon /></InputGroup.Addon>
      <InputGroup.Input id="{id}-q" bind:value={draft} type="search" placeholder="e.g. acme-core or 1.4.2" class="font-mono" />
    </InputGroup.Root>
  </div>
  <div class="flex flex-col gap-2">
    <Label for="{id}-repo">Repository</Label>
    <Select.Root type="single" bind:value={draftRepo}>
      <Select.Trigger id="{id}-repo" class="w-48">
        <span class="truncate">{draftRepo === allRepos ? "All repositories" : draftRepo}</span>
      </Select.Trigger>
      <Select.Content>
        <Select.Item value={allRepos} label="All repositories" />
        {#if repo && !repoNames.includes(repo)}
          <Select.Item value={repo} label={repo} />
        {/if}
        {#each repoNames as name (name)}
          <Select.Item value={name} label={name} />
        {/each}
      </Select.Content>
    </Select.Root>
  </div>
  <Button type="submit" disabled={draft.trim().length < minLength}>Search</Button>
</form>

{#if !results}
  <EmptyState
    icon={SearchIcon}
    title={q ? "Type a longer search" : "Search all repositories"}
    description="Enter at least {minLength} characters of a file path."
  />
{:else if results.error && results.items.length === 0 && !results.loading}
  <ErrorState error={results.error} onRetry={results.reload} />
{:else if !results.loading && results.items.length === 0}
  <EmptyState
    icon={SearchIcon}
    title="No files match"
    description={repo ? `No path in ${repo} contains “${q}”. Search all repositories, or try a shorter part of the path.` : `No path contains “${q}”. Try a shorter part of the path.`}
  >
    {#if repo}
      <Button variant="outline" href={href("/search", { q })}>Search all repositories</Button>
    {/if}
  </EmptyState>
{:else}
  <div class="w-full overflow-x-auto rounded-2xl border">
    <Table.Root>
      <Table.Caption class="sr-only">Search results</Table.Caption>
      <Table.Header>
        <Table.Row class="hover:bg-transparent">
          <Table.Head class="w-full">Path</Table.Head>
          <Table.Head>Repository</Table.Head>
          <Table.Head class="text-right">Size</Table.Head>
          <Table.Head>Modified</Table.Head>
        </Table.Row>
      </Table.Header>
      <Table.Body>
        {#if results.loading}
          <LoadingRows rows={4} columns={4} />
        {:else}
          {#each results.items as h (h.repo + "\u0000" + h.path)}
            <Table.Row>
              <Table.Cell class="max-w-xl whitespace-normal">
                <a href={fileHref(h)} class="flex items-start gap-2 font-mono text-[0.8125rem] break-all text-link underline-offset-4 hover:underline">
                  <FileIcon class="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                  {h.path}
                </a>
              </Table.Cell>
              <Table.Cell>
                <a href={href("/repo/" + encodeURIComponent(h.repo) + "/")} class="font-mono text-[0.8125rem] underline-offset-4 hover:underline">{h.repo}</a>
              </Table.Cell>
              <Table.Cell class="text-right"><FileSize bytes={h.size} /></Table.Cell>
              <Table.Cell class="text-muted-foreground"><RelativeTime at={h.updated_at} /></Table.Cell>
            </Table.Row>
          {/each}
        {/if}
      </Table.Body>
    </Table.Root>
  </div>
  <LoadMore list={results} />
{/if}
