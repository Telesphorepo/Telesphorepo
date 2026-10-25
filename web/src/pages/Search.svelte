<!--
  Search results for query.get("q") (3+ characters), optionally in one repository
  (query.get("repo")). The top bar's search field holds the query; under 640px, where the top bar
  has no field, the page shows one.

  The hits come a repository at a time, in the order the repository stored them, so a card is
  whole before the next one starts; the next page loads on its own as the end of the results
  comes into view. They are grouped
  (search/hits.ts): a card per repository with its count of matches, and in it a row per folder
  that holds hits (an artifact folder takes in its version folders), with its folders and files as
  links; the checksums and signatures of a file fold into it. The matched text is marked in every
  path. The first page counts the matches of every repository (up to 1,000 each, "1,000+"): the toolbar shows the total, and
  with several repositories a line under it names each with its count, a link to search only it.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import type { PageProps } from "$lib/router.svelte";
  import { filesPath, href, navigate } from "$lib/router.svelte";
  import { api, type SearchHit, type SearchRepo } from "$lib/api";
  import { Keyset } from "$lib/load.svelte";
  import { openRow } from "$lib/pick.svelte";
  import { formatCount, formatSize } from "$lib/format";
  import { count } from "$lib/words";
  import * as InputGroup from "$lib/components/ui/input-group";
  import { Button } from "$lib/components/ui/button";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Toolbar from "$lib/components/Toolbar.svelte";
  import Section from "$lib/components/Section.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import SourceError from "$lib/components/SourceError.svelte";
  import SourceMore from "$lib/components/SourceMore.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import FormatIcon from "$lib/components/FormatIcon.svelte";
  import RepoPicker from "$lib/components/RepoPicker.svelte";
  import Highlight from "./search/Highlight.svelte";
  import { groupHits, type HitEntry, type HitFile, type HitRepo } from "./search/hits";
  import SearchIcon from "@lucide/svelte/icons/search";
  import FileIcon from "@lucide/svelte/icons/file";
  import FolderIcon from "@lucide/svelte/icons/folder";

  let { query }: PageProps = $props();

  const minLength = 3;
  /** The files and the folders a row shows before "N more". */
  const shownFiles = 6;
  const shownFolders = 16;
  const q = $derived((query.get("q") ?? "").trim());
  const repo = $derived(query.get("repo") ?? "");


  let results = $state<Keyset<SearchHit> | null>(null);
  // The matches of each repository, from the first page.
  let counts = $state<SearchRepo[]>([]);
  $effect(() => {
    const term = q;
    const inRepo = repo;
    counts = [];
    results =
      term.length < minLength
        ? null
        : untrack(
            () =>
              new Keyset(async (p) => {
                const page = await api.search(term, { repo: inRepo || undefined, ...p });
                if (page.repos && term === q && inRepo === repo) counts = page.repos;
                return page;
              }, 200),
          );
  });
  const groups = $derived(groupHits(results?.items ?? []));
  const total = $derived(counts.reduce((n, c) => n + c.matches, 0));
  const totalMore = $derived(counts.some((c) => c.more));
  /** "208 files", "1,000+ files": a count that stopped is at least that many. */
  const files = (n: number, more: boolean) => (more ? `${formatCount(n)}+ files` : count(n, "file", "files"));
  const formatOf = (name: string) => counts.find((c) => c.name === name)?.format ?? "";
  const filesOf = (name: string, loaded: number) => {
    const c = counts.find((x) => x.name === name);
    return c ? files(c.matches, c.more) : count(loaded, "file", "files");
  };

  /** "5,444 files", "11,008+ files in 2 repositories". */
  const countText = $derived(
    total === 0 ? "" : `${files(total, totalMore)}${counts.length > 1 ? ` in ${count(counts.length, "repository", "repositories")}` : ""}`,
  );

  let draft = $state("");
  $effect(() => {
    draft = q;
  });

  const go = (term: string, inRepo: string) => navigate("/search", { query: { q: term, repo: inRepo || undefined } });
  function submit(e: SubmitEvent) {
    e.preventDefault();
    go(draft.trim(), repo);
  }

  /** "com/contoso/widget/" and "2.1.0": the folder's parents, muted, and its own name. */
  function splitFolder(folder: string): [string, string] {
    const f = folder.replace(/\/$/, "");
    const i = f.lastIndexOf("/");
    return [f.slice(0, i + 1), f.slice(i + 1)];
  }
</script>

<PageHeader title="Search" />

<form role="search" class="mb-4 sm:hidden" onsubmit={submit}>
  <InputGroup.Root>
    <InputGroup.Addon><SearchIcon /></InputGroup.Addon>
    <InputGroup.Input bind:value={draft} type="search" placeholder="Search files" aria-label="Search files" />
  </InputGroup.Root>
</form>

{#snippet fileLink(r: string, file: HitFile)}
  {@const also = file.sidecars.map((s) => s.label).join(", ")}
  <li class="flex min-w-0 items-center gap-1.5">
    <FileIcon class="size-3.5 shrink-0 text-muted-foreground" />
    <a
      href={href(filesPath(r, file.hit.path))}
      class="min-w-0 [overflow-wrap:anywhere] rounded-sm outline-none hover:underline focus-visible:ring-[3px] focus-visible:ring-ring/50"
      title="{formatSize(file.hit.size)}{also ? ` · with ${also}` : ''}"
    ><Highlight text={file.name} term={q} /></a>
    {#if also}<span class="sr-only">, with {also}</span>{/if}
  </li>
{/snippet}

{#snippet entryRow(e: HitEntry)}
  {@const to = href(filesPath(e.repo, e.folder))}
  {@const [parents, own] = splitFolder(e.folder)}
  <!-- The folder is the row; the folders under it and its files are links on the line under it (a file's size and checksums in its tooltip). -->
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
  <li
    class="relative flex cursor-pointer items-start gap-3 rounded-lg px-3 py-3 before:absolute before:inset-x-3 before:top-0 before:h-px before:bg-border first:before:hidden hover:bg-hover"
    onclick={(ev) => openRow(ev, to)}
  >
    <FolderIcon class="mt-px size-5 shrink-0 text-muted-foreground" />
    <div class="min-w-0 flex-1">
      <div class="flex flex-col gap-x-4 sm:flex-row sm:items-baseline sm:justify-between">
        <a href={to} class="min-w-0 text-label [overflow-wrap:anywhere] rounded-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50">
          {#if e.folder}<span class="font-normal text-muted-foreground"><Highlight text={parents} term={q} /></span><Highlight text={own} term={q} />{:else}Top folder{/if}
        </a>
        <p class="shrink-0 text-meta text-muted-foreground">
          {#if e.children.length}{count(e.children.length, "folder", "folders")}{" · "}{/if}{count(e.fileCount, "file", "files")} · modified <RelativeTime at={e.updated} />
        </p>
      </div>
      <ul aria-label="In {e.folder || 'the top folder'}" class="mt-1.5 flex flex-wrap gap-x-5 gap-y-1 text-meta">
        {#each e.children.slice(0, shownFolders) as c (c.folder)}
          <li class="flex min-w-0 items-center gap-1.5">
            <FolderIcon class="size-3.5 shrink-0 text-muted-foreground" />
            <a
              href={href(filesPath(e.repo, c.folder))}
              class="min-w-0 [overflow-wrap:anywhere] rounded-sm outline-none hover:underline focus-visible:ring-[3px] focus-visible:ring-ring/50"
              title={count(c.files.length, "file", "files")}
            ><Highlight text={c.name} term={q} /></a>
          </li>
        {/each}
        {#if e.children.length > shownFolders}
          <li class="text-muted-foreground">{count(e.children.length - shownFolders, "more folder", "more folders")}</li>
        {/if}
        {#each e.files.slice(0, shownFiles) as file (file.hit.path)}{@render fileLink(e.repo, file)}{/each}
        {#if e.files.length > shownFiles}
          <li class="text-muted-foreground">{count(e.files.length - shownFiles, "more file", "more files")}</li>
        {/if}
      </ul>
    </div>
  </li>
{/snippet}

{#snippet repoCard(r: HitRepo)}
  {@const format = formatOf(r.repo)}
  <Section>
    <div class="mb-4 flex flex-wrap items-center justify-between gap-x-6 gap-y-2">
      <div class="flex min-w-0 items-center gap-3">
        {#if format}<FormatIcon {format} class="size-5" />{/if}
        <h2 class="min-w-0 truncate text-section"><a href={href(filesPath(r.repo))} class="hover:underline">{r.repo}</a></h2>
        <!-- With one repository the count over the results says it. -->
        {#if counts.length > 1}<span class="shrink-0 text-meta text-muted-foreground">{filesOf(r.repo, r.files)}</span>{/if}
      </div>
      {#if !repo && counts.length > 1}
        <div data-slot="header-actions" class="flex shrink-0">
          <Button variant="ghost" size="sm" onclick={() => go(q, r.repo)}>Search only {r.repo}</Button>
        </div>
      {/if}
    </div>
    <ul aria-label="Folders in {r.repo}" class="-mx-3 text-body">
      {#each r.entries as e (e.key)}{@render entryRow(e)}{/each}
    </ul>
  </Section>
{/snippet}

{#if !results}
  <EmptyState
    icon={SearchIcon}
    title={q ? "Type a longer search" : "Search every repository"}
    description="Type at least {minLength} characters of a file path in the search field."
  />
{:else}
  <Toolbar>
    <!-- A group has no files of its own, so search does not offer it. -->
    <RepoPicker value={repo} format={formatOf(repo) || undefined} keep={(r) => r.kind !== "group"} onchange={(r) => go(q, r)} />
    {#snippet end()}
      {#if countText}<p class="text-meta text-muted-foreground" aria-live="polite">{countText}</p>{/if}
    {/snippet}
  </Toolbar>

  {#if results.loading && results.items.length === 0}
    <Section><Loading rows={4} /></Section>
  {:else if results.error && results.items.length === 0}
    <Section><SourceError source={results} /></Section>
  {:else if results.items.length === 0}
    <EmptyState
      icon={SearchIcon}
      title="No files match"
      description={repo ? `No path in ${repo} contains “${q}”.` : `No path contains “${q}”. Try a shorter part of the path.`}
    >
      {#if repo}<Button variant="outline" onclick={() => go(q, "")}>Search all repositories</Button>{/if}
    </EmptyState>
  {:else}
    {#if !repo && counts.length > 1}
      <nav aria-label="Repositories with matches" class="mb-6 flex flex-wrap items-center gap-x-4 gap-y-1 text-meta text-muted-foreground">
        {#each counts as c (c.name)}
          <button type="button" class="flex items-center gap-1.5 rounded-sm outline-none hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50" title="Search only {c.name}" onclick={() => go(q, c.name)}>
            <FormatIcon format={c.format} class="size-3.5" /><span class="text-foreground">{c.name}</span><span class="tabular-nums">{formatCount(c.matches)}{c.more ? "+" : ""}</span>
          </button>
        {/each}
      </nav>
    {/if}
    {#each groups.repos as r (r.repo)}{@render repoCard(r)}{/each}
    <SourceMore source={results} auto />
  {/if}
{/if}
