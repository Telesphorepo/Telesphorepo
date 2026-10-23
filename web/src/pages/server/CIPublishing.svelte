<!--
  CI pipelines (All repositories, or CI pipelines on a repository): the CI jobs that publish without
  a stored password (trust policies in the API). A person sees the pipelines whose repositories all
  hold CI pipelines for them. Each pipeline is a card that opens it: its name, its CI system and
  the GitHub repository or Jenkins host (when the name does not say it), its conditions as
  TicketBadges, and up to three of the repositories it publishes to. New CI pipeline opens an empty one.
-->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import { href } from "$lib/router.svelte";
  import { api, type Trust } from "$lib/api";
  import { Keyset } from "$lib/load.svelte";
  import { someNames } from "$lib/words";
  import { Button } from "$lib/components/ui/button";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import SourceGrid from "$lib/components/SourceGrid.svelte";
  import ItemCard from "$lib/components/ItemCard.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import ConditionBadges from "./ci/ConditionBadges.svelte";
  import { admitsOf, ciSystemTitle } from "./ci/presets";
  import WorkflowIcon from "@lucide/svelte/icons/workflow";
  import PlusIcon from "@lucide/svelte/icons/plus";

  let {}: PageProps = $props();

  const pipelines = new Keyset((p) => api.publish.trusts.list(p));
  const open = (t: Trust) => href(`/settings/ci-pipelines/${t.id}`);
  const newPipeline = href("/settings/ci-pipelines/new");
</script>

<PageHeader title="CI pipelines" description="CI jobs that publish without a stored password.">
  {#snippet actions()}{#if pipelines.items.length}<Button href={newPipeline}><PlusIcon />New CI pipeline</Button>{/if}{/snippet}
</PageHeader>

<Section plain>
  <SourceGrid source={pipelines} label="CI pipelines" key={(t) => t.id}>
    {#snippet card(t)}
      {@const a = admitsOf(t)}
      {@const head = [ciSystemTitle(t.issuer), a.who && a.who !== t.name ? a.who : ''].filter(Boolean).join(' · ')}
      <ItemCard title={t.name} href={open(t)}>
        {#snippet icon()}<WorkflowIcon class="size-5" />{/snippet}
        {#snippet badges()}{#if t.disabled}<StatusBadge>Off</StatusBadge>{/if}{/snippet}
        <p class="truncate" title={head}>{head}</p>
        <ConditionBadges items={a.items} every={a.every} max={4} class="my-1.5" />
        <p class="truncate" title={t.repos.join(", ")}>{t.repos.length ? `Publishes to ${someNames(t.repos, 3)}` : "Publishes to no repository"}</p>
      </ItemCard>
    {/snippet}
    {#snippet empty()}
      <CompactEmpty icon={WorkflowIcon} text="None yet. Each file that a pipeline publishes links to the build that made it.">
        <Button variant="outline" href={newPipeline}><PlusIcon />New CI pipeline</Button>
      </CompactEmpty>
    {/snippet}
  </SourceGrid>
</Section>
