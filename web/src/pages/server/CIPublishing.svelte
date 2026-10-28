<!--
  CI pipelines (All repositories, or CI pipelines on a repository): the CI jobs that publish without
  a stored password (trust policies in the API). A person sees the pipelines whose repositories all
  hold CI pipelines for them. One table by name: the name (Off after a turned-off one; the row
  opens the pipeline), the CI system with the GitHub repository or Jenkins host (when the name does
  not say it), the conditions as TicketBadges, and up to three of the repositories it publishes to.
  The search field keeps the pipelines whose name starts with what is typed (case-sensitive): the
  server answers it (GET /api/publish/trusts?q=), 250 ms after the last key. New CI pipeline opens
  an empty one.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import type { PageProps } from "$lib/router.svelte";
  import { href } from "$lib/router.svelte";
  import { api, type Trust } from "$lib/api";
  import { Keyset } from "$lib/load.svelte";
  import { someNames } from "$lib/words";
  import { Button } from "$lib/components/ui/button";
  import * as InputGroup from "$lib/components/ui/input-group";
  import * as Table from "$lib/components/ui/table";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Toolbar from "$lib/components/Toolbar.svelte";
  import Section from "$lib/components/Section.svelte";
  import DataTable, { type Column } from "$lib/components/DataTable.svelte";
  import Cell from "$lib/components/Cell.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import ConditionBadges from "./ci/ConditionBadges.svelte";
  import { admitsOf, ciSystemTitle } from "./ci/presets";
  import WorkflowIcon from "@lucide/svelte/icons/workflow";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import SearchIcon from "@lucide/svelte/icons/search";
  import SearchXIcon from "@lucide/svelte/icons/search-x";

  let {}: PageProps = $props();

  // The search asks the server 250 ms after the last key; a new prefix starts a new list.
  let filter = $state("");
  let prefix = $state("");
  $effect(() => {
    const f = filter.trim();
    const t = setTimeout(() => (prefix = f), 250);
    return () => clearTimeout(t);
  });
  let pipelines = $state<Keyset<Trust>>(untrack(() => new Keyset((p) => api.publish.trusts.list(p))));
  let first = true;
  $effect(() => {
    const q = prefix;
    if (first) {
      first = false;
      return;
    }
    pipelines = untrack(() => new Keyset((p) => api.publish.trusts.list(p, q)));
  });
  // The search and New CI pipeline show once there is a pipeline, also while the search finds none.
  let any = $state(false);
  $effect(() => {
    if (pipelines.items.length > 0) any = true;
  });

  const open = (t: Trust) => href(`/settings/ci-pipelines/${t.id}`);
  const newPipeline = href("/settings/ci-pipelines/new");
  const columns: Column[] = [
    { label: "Name" },
    { label: "CI system" },
    { label: "Conditions", class: "w-full", hideOnPhone: true },
    { label: "Repositories", hideOnPhone: true },
  ];
</script>

<PageHeader title="CI pipelines" description="CI jobs that publish without a stored password.">
  {#snippet actions()}{#if any}<Button href={newPipeline}><PlusIcon />New CI pipeline</Button>{/if}{/snippet}
</PageHeader>

{#if any || prefix}
  <Toolbar>
    <InputGroup.Root class="sm:w-72">
      <InputGroup.Addon><SearchIcon /></InputGroup.Addon>
      <InputGroup.Input bind:value={filter} type="search" placeholder="Search by name" aria-label="Search CI pipelines by the start of the name" />
    </InputGroup.Root>
  </Toolbar>
{/if}

<Section>
  <DataTable source={pipelines} {columns} caption="CI pipelines" key={(t) => t.id} href={open}>
    {#snippet row(t)}
      {@const a = admitsOf(t)}
      {@const head = [ciSystemTitle(t.issuer), a.who && a.who !== t.name ? a.who : ""].filter(Boolean).join(" · ")}
      <!-- The name never truncates; on a phone it is the row's first line (data-grow). -->
      <Table.Cell data-grow>
        <span class="flex items-center gap-2">
          <a href={open(t)} class="rounded-sm font-medium whitespace-nowrap outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50">{t.name}</a>
          {#if t.disabled}<StatusBadge>Off</StatusBadge>{/if}
        </span>
      </Table.Cell>
      <Cell muted text={head} />
      <Table.Cell class="w-full max-w-0 max-sm:hidden">
        <ConditionBadges items={a.items} every={a.every} max={4} nowrap />
      </Table.Cell>
      <Cell muted hideOnPhone text={t.repos.length ? someNames(t.repos, 3) : "None"} />
    {/snippet}
    {#snippet empty()}
      {#if prefix}
        <CompactEmpty icon={SearchXIcon} text="No pipeline's name starts with “{prefix}”." />
      {:else}
        <CompactEmpty icon={WorkflowIcon} text="None yet. Each file that a pipeline publishes links to the build that made it.">
          <Button variant="outline" href={newPipeline}><PlusIcon />New CI pipeline</Button>
        </CompactEmpty>
      {/if}
    {/snippet}
  </DataTable>
</Section>
