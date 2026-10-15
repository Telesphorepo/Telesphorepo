<!--
  CI pipelines (All repositories, or CI pipelines on a repository): the CI jobs that publish without
  a stored password (trust policies in the API). A person sees the pipelines whose repositories all
  hold CI pipelines for them. A row opens the pipeline; its name is followed by what the pipeline
  accepts, without the part that the name already says. New CI pipeline opens an empty one.
-->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import { href } from "$lib/router.svelte";
  import { api, type Trust } from "$lib/api";
  import { Keyset } from "$lib/load.svelte";
  import { Button } from "$lib/components/ui/button";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import DataTable from "$lib/components/DataTable.svelte";
  import Cell from "$lib/components/Cell.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import RepoLinks from "./RepoLinks.svelte";
  import { acceptsText, ciSystemTitle } from "./ci/presets";
  import WorkflowIcon from "@lucide/svelte/icons/workflow";
  import PlusIcon from "@lucide/svelte/icons/plus";

  let {}: PageProps = $props();

  const pipelines = new Keyset((p) => api.publish.trusts.list(p));
  const open = (t: Trust) => href(`/settings/ci-pipelines/${t.id}`);
  const newPipeline = href("/settings/ci-pipelines/new");

  /** What the pipeline accepts, less the name when the name says it: "branch main, release.yml". */
  function rest(t: Trust): string {
    const text = acceptsText(t);
    if (text === t.name) return "";
    return text.startsWith(`${t.name}, `) ? text.slice(t.name.length + 2) : text;
  }
</script>

<PageHeader title="CI pipelines" description="CI jobs that publish without a stored password.">
  {#snippet actions()}{#if pipelines.items.length}<Button href={newPipeline}><PlusIcon />New CI pipeline</Button>{/if}{/snippet}
</PageHeader>

<Section>
<DataTable
  source={pipelines}
  caption="CI pipelines"
  key={(t) => t.id}
  href={open}
  columns={[
    { label: "Pipeline", grow: true },
    { label: "CI system", hideOnPhone: true },
    { label: "Can publish to", hideOnPhone: true },
  ]}
>
  {#snippet row(t)}
    {@const more = rest(t)}
    <Cell grow>
      <span class="flex min-w-0 items-baseline gap-2">
        <a href={open(t)} class="shrink-0 rounded-sm font-medium outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50">{t.name}</a>
        {#if t.disabled}<span class="self-center"><StatusBadge>Off</StatusBadge></span>{/if}
        {#if more}<span class="min-w-0 truncate text-muted-foreground max-sm:hidden" title={more}>{more}</span>{/if}
      </span>
    </Cell>
    <Cell hideOnPhone muted text={ciSystemTitle(t.issuer)} />
    <Cell hideOnPhone class="max-w-56"><RepoLinks repos={t.repos} /></Cell>
  {/snippet}
  {#snippet empty()}
    <CompactEmpty icon={WorkflowIcon} text="Let GitHub Actions or Jenkins publish with no stored password, and see the build behind each file.">
      <Button variant="outline" href={newPipeline}><PlusIcon />New CI pipeline</Button>
    </CompactEmpty>
  {/snippet}
</DataTable>
</Section>
