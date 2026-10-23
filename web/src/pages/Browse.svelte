<!--
  Browse: every repository the visitor can download from, sorted by name, as one table to scan
  down: the format icon, the name with a lock after a private one, the format, the kind of a proxy
  or a group, and the description on one line. The Format column shows only with two or more
  formats, the Kind column only when a proxy or a group is listed. A click anywhere on a row opens
  the repository. A filter box and a format select ("All formats" first, then each format present
  with its icon) narrow it in the browser (the list is never paged). People with All repositories
  create a repository from the toolbar.
-->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import { href, repoPath } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { api, type Repo } from "$lib/api";
  import { Resource, type TableSource } from "$lib/load.svelte";
  import { formats } from "$lib/formats/registry.svelte";
  import * as Select from "$lib/components/ui/select";
  import * as InputGroup from "$lib/components/ui/input-group";
  import * as Table from "$lib/components/ui/table";
  import { Button } from "$lib/components/ui/button";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Toolbar from "$lib/components/Toolbar.svelte";
  import Section from "$lib/components/Section.svelte";
  import DataTable, { type Column } from "$lib/components/DataTable.svelte";
  import Cell from "$lib/components/Cell.svelte";
  import FormatIcon from "$lib/components/FormatIcon.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import DatabaseIcon from "@lucide/svelte/icons/database";
  import SearchIcon from "@lucide/svelte/icons/search";
  import SearchXIcon from "@lucide/svelte/icons/search-x";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import LockIcon from "@lucide/svelte/icons/lock";

  let {}: PageProps = $props();

  const repos = new Resource(() => api.repos.list());
  formats.load();

  let filter = $state("");
  let format = $state("");
  const allFormats = "*";

  const all = $derived([...(repos.data ?? [])].sort((a, b) => a.name.localeCompare(b.name)));
  const present = $derived([...new Set(all.map((r) => r.format))].sort((a, b) => formats.title(a).localeCompare(formats.title(b))));
  const filtered = $derived(filter.trim() !== "" || format !== "");

  const matches = (r: Repo) => {
    const f = filter.trim().toLowerCase();
    return (!format || r.format === format) && (!f || r.name.toLowerCase().includes(f) || r.description.toLowerCase().includes(f));
  };
  const shown = $derived(all.filter(matches));
  // The page shows loading and errors itself; the table only draws the rows.
  const source: TableSource<Repo> = {
    get items() {
      return shown;
    },
    loading: false,
    error: "",
  };

  // A column that would say the same thing on every row is left out.
  const formatColumn = $derived(present.length > 1);
  const kindColumn = $derived(all.some((r) => r.kind !== "hosted"));
  const columns: Column[] = $derived([
    { label: "Name" },
    ...(formatColumn ? [{ label: "Format", hideOnPhone: true }] : []),
    ...(kindColumn ? [{ label: "Kind" }] : []),
    { label: "Description", class: "w-full" },
  ]);
  const rowHref = (r: Repo) => href(repoPath(r.name));

  const clear = () => {
    filter = "";
    format = "";
  };
  const newRepo = href("/settings/repositories/new");
</script>

{#snippet formatIcon()}<FormatIcon {format} />{/snippet}

<PageHeader title="Repositories" />

{#if all.length > 0}
  <Toolbar>
    <InputGroup.Root class="sm:w-72">
      <InputGroup.Addon><SearchIcon /></InputGroup.Addon>
      <InputGroup.Input bind:value={filter} type="search" placeholder="Filter by name or description" aria-label="Filter repositories" />
    </InputGroup.Root>
    {#if present.length > 1}
      <Select.Root type="single" value={format || allFormats} onValueChange={(v) => (format = v === allFormats ? "" : v)}>
        <Select.Trigger class="w-full sm:w-56" aria-label="Format" icon={format ? formatIcon : undefined}>
          <span class="truncate">{format ? formats.title(format) : "All formats"}</span>
        </Select.Trigger>
        <Select.Content>
          <Select.Item value={allFormats} label="All formats" inset />
          {#each present as f (f)}
            <Select.Item value={f} label={formats.title(f)}>
              {#snippet icon()}<FormatIcon format={f} />{/snippet}
            </Select.Item>
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
  <Section>
    <DataTable {source} {columns} caption="Repositories" key={(r) => r.name} href={rowHref}>
      {#snippet row(r)}
        <!-- The name never truncates; on a phone it is the row's first line (data-grow) and the other cells its meta line. -->
        <Table.Cell data-grow>
          <span class="flex items-center gap-2.5">
            <FormatIcon format={r.format} class="text-muted-foreground" />
            <a href={rowHref(r)} class="rounded-sm font-medium outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50">{r.name}</a>
            {#if !r.anonymous_read}
              <span class="-ml-1 flex text-muted-foreground" title="Private: only people with access can download">
                <LockIcon aria-hidden="true" class="size-3.5" /><span class="sr-only">Private</span>
              </span>
            {/if}
          </span>
        </Table.Cell>
        {#if formatColumn}<Cell muted hideOnPhone>{formats.title(r.format)}</Cell>{/if}
        <!-- An empty cell stays empty, so a phone's meta line leaves it out. -->
        {#if kindColumn}{#if r.kind !== "hosted"}<Cell muted>{r.kind === "proxy" ? "Proxy" : "Group"}</Cell>{:else}<Table.Cell />{/if}{/if}
        {#if r.description}<Cell muted text={r.description} class="w-full max-w-0 max-sm:flex-[1_1_8rem]" />{:else}<Table.Cell class="w-full" />{/if}
      {/snippet}
    </DataTable>
  </Section>
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
