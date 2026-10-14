<!--
  Home: every repository the visitor can download from, as one list (name, format, description,
  who can download, the visitor's own access, last publish, size and file count).
  A filter box and format buttons narrow the list in the browser; the list is never paged.
-->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import { href, navigate } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { api, type Repo } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import * as InputGroup from "$lib/components/ui/input-group";
  import * as ToggleGroup from "$lib/components/ui/toggle-group";
  import { Button } from "$lib/components/ui/button";
  import { Skeleton } from "$lib/components/ui/skeleton";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import FormatIcon from "$lib/components/FormatIcon.svelte";
  import AccessBadge from "$lib/components/AccessBadge.svelte";
  import FileSize from "$lib/components/FileSize.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import { formatCount } from "$lib/format";
  import CreateRepoDialog from "../settings/repos/CreateRepoDialog.svelte";
  import DatabaseIcon from "@lucide/svelte/icons/database";
  import SearchIcon from "@lucide/svelte/icons/search";
  import SearchXIcon from "@lucide/svelte/icons/search-x";
  import PlusIcon from "@lucide/svelte/icons/plus";

  let {}: PageProps = $props();

  const repos = new Resource(() => api.repos.list());
  // Format titles; the format name stands in until they load or if they fail.
  const formats = new Resource(() => api.formats.list());
  const titles = $derived(Object.fromEntries((formats.data ?? []).map((f) => [f.name, f.title])));
  const titleOf = (format: string) => titles[format] ?? format;

  const all = $derived([...(repos.data ?? [])].sort((a, b) => a.name.localeCompare(b.name)));

  let filter = $state("");
  let format = $state("");

  // The formats that exist on this server, with their number of repositories.
  const counts = $derived.by(() => {
    const m = new Map<string, number>();
    for (const r of all) m.set(r.format, (m.get(r.format) ?? 0) + 1);
    return [...m].sort((a, b) => titleOf(a[0]).localeCompare(titleOf(b[0])));
  });

  const shown = $derived.by(() => {
    const f = filter.trim().toLowerCase();
    return all.filter(
      (r) => (!format || r.format === format) && (!f || r.name.toLowerCase().includes(f) || r.description.toLowerCase().includes(f)),
    );
  });
  const filtered = $derived(filter.trim() !== "" || format !== "");

  const cols = $derived(["minmax(0,1fr)", "9rem", "11rem", ...(auth.user ? ["8rem"] : []), "8rem", "9rem"].join(" "));

  const yourAccess = (r: Repo) =>
    r.permission === "admin" ? "Admin" : r.permission === "write" ? "Can publish" : "Can download";

  const repoHref = (name: string) => href("/repo/" + encodeURIComponent(name) + "/");

  let creating = $state(false);
  const clear = () => {
    filter = "";
    format = "";
  };
</script>

<PageHeader
  title="Repositories"
  description={auth.user
    ? "Everything you can download from. Open a repository to see its files and how to use it."
    : "Repositories that anyone can download from. Sign in to see the private ones you have access to."}
>
  {#snippet actions()}
    {#if auth.isAdmin && all.length > 0}
      <Button onclick={() => (creating = true)}><PlusIcon />New repository</Button>
    {:else if !auth.user}
      <Button variant="outline" href={href("/login", { next: "/" })}>Sign in</Button>
    {/if}
  {/snippet}
</PageHeader>

{#if repos.error}
  <ErrorState error={repos.error} title="The repositories did not load" onRetry={repos.reload} />
{:else if !repos.loading && all.length === 0}
  {#if auth.isAdmin}
    <EmptyState
      icon={DatabaseIcon}
      title="Create your first repository"
      description="A repository holds one kind of package: Maven and Gradle libraries, Debian packages, or plain files. Create one, then publish to it from your build or upload files here."
    >
      <Button onclick={() => (creating = true)}><PlusIcon />Create a repository</Button>
    </EmptyState>
  {:else if auth.user}
    <EmptyState
      icon={DatabaseIcon}
      title="No repositories to show"
      description="You do not have access to any repository yet. Ask an administrator to give you access."
    />
  {:else}
    <EmptyState
      icon={DatabaseIcon}
      title="No public repositories"
      description="This server has no repository that anyone can download from. Sign in to see the ones you have access to."
    >
      <Button href={href("/login", { next: "/" })}>Sign in</Button>
    </EmptyState>
  {/if}
{:else}
  <div class="mb-4 flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
    <InputGroup.Root class="md:max-w-sm">
      <InputGroup.Addon><SearchIcon /></InputGroup.Addon>
      <InputGroup.Input bind:value={filter} type="search" placeholder="Filter by name or description" aria-label="Filter repositories" />
    </InputGroup.Root>
    {#if counts.length > 1}
      <ToggleGroup.Root
        type="single"
        size="sm"
        spacing={1}
        class="flex-wrap"
        aria-label="Show one format"
        value={format}
        onValueChange={(v: string) => (format = v)}
      >
        <!-- Pressing the open format again also shows all formats (value ""). -->
        <ToggleGroup.Item value="" aria-label="All formats">All</ToggleGroup.Item>
        {#each counts as [f, n] (f)}
          <ToggleGroup.Item value={f}>
            {titleOf(f)}
            <span class="tabular-nums opacity-60">{n}</span>
          </ToggleGroup.Item>
        {/each}
      </ToggleGroup.Root>
    {/if}
  </div>

  {#if !repos.loading && shown.length === 0}
    <EmptyState
      icon={SearchXIcon}
      title="No repository matches"
      description={filter.trim() ? `No ${format ? titleOf(format) + " " : ""}repository has “${filter.trim()}” in its name or description.` : "No repository has this format."}
    >
      <Button variant="outline" onclick={clear}>Show all repositories</Button>
    </EmptyState>
  {:else}
    <div class="rounded-2xl border p-1.5" style="--cols: {cols}">
      <div aria-hidden="true" class="hidden gap-x-4 px-3 pt-1.5 pb-2 text-xs font-medium text-muted-foreground md:grid md:grid-cols-(--cols)">
        <span>Repository</span>
        <span>Format</span>
        <span>Who can download</span>
        {#if auth.user}<span>Your access</span>{/if}
        <span>Last publish</span>
        <span class="text-right">Size</span>
      </div>
      {#if repos.loading}
        <div role="status" aria-label="Loading repositories" class="flex flex-col">
          {#each { length: 4 }, i (i)}
            <div class="flex items-center gap-3 px-3 py-3">
              <Skeleton class="size-10 rounded-xl" />
              <div class="flex flex-1 flex-col gap-2">
                <Skeleton class="h-4 w-40" />
                <Skeleton class="h-3.5 w-2/3" />
              </div>
            </div>
          {/each}
        </div>
      {:else}
        <ul class="flex flex-col" aria-label={filtered ? `${shown.length} of ${all.length} repositories` : "Repositories"}>
          {#each shown as r (r.name)}
            <li>
              <a
                href={repoHref(r.name)}
                class="flex flex-col gap-2 rounded-xl px-3 py-3 outline-none transition-colors hover:bg-hover focus-visible:bg-hover focus-visible:ring-[3px] focus-visible:ring-ring/50 md:grid md:grid-cols-(--cols) md:items-center md:gap-x-4"
              >
                <span class="flex min-w-0 items-center gap-3">
                  <FormatIcon format={r.format} />
                  <span class="flex min-w-0 flex-col">
                    <span class="truncate font-mono text-[0.9375rem] font-medium">{r.name}</span>
                    {#if r.description}
                      <span class="line-clamp-2 text-sm break-words text-muted-foreground md:line-clamp-1">{r.description}</span>
                    {:else}
                      <span class="text-sm text-muted-foreground/70">No description</span>
                    {/if}
                  </span>
                </span>
                <span class="flex flex-wrap items-center gap-x-3 gap-y-1.5 pl-[3.25rem] text-sm md:contents">
                  <span class="text-sm">{titleOf(r.format)}</span>
                  <span><AccessBadge anyone={r.anonymous_read} /></span>
                  {#if auth.user}<span class="text-muted-foreground">{yourAccess(r)}</span>{/if}
                  <span class="text-muted-foreground">
                    <span class="md:hidden">Last publish </span><RelativeTime at={r.last_published_at} empty="Nothing yet" />
                  </span>
                  <span class="text-muted-foreground md:text-right">
                    <FileSize bytes={r.total_size} />
                    <span class="tabular-nums">· {formatCount(r.file_count)} {r.file_count === 1 ? "file" : "files"}</span>
                  </span>
                </span>
              </a>
            </li>
          {/each}
        </ul>
      {/if}
    </div>
  {/if}
{/if}

{#if auth.isAdmin}
  <CreateRepoDialog bind:open={creating} onCreated={(r) => navigate("/repo/" + encodeURIComponent(r.name) + "/")} />
{/if}
