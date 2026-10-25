<!--
  Publish, the first step of publishing: the hosted repositories the person can publish to
  (GET /api/repos?perm=repo.publish&kind=hosted, 200 a page with Load more) as RepoCards that open their Publish
  tab, a section per format when more than one is present. A group is not listed: publishing to a
  group is publishing to one of its hosted members. With exactly one repository the page replaces
  its own history entry with that Publish tab; with none it says whom to ask.
-->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import { href, navigate, repoPath } from "$lib/router.svelte";
  import { api } from "$lib/api";
  import { Keyset } from "$lib/load.svelte";
  import { formats } from "$lib/formats/registry.svelte";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import CardGrid from "$lib/components/CardGrid.svelte";
  import RepoCard from "$lib/components/RepoCard.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import SourceMore from "$lib/components/SourceMore.svelte";
  import UploadIcon from "@lucide/svelte/icons/upload";

  let {}: PageProps = $props();

  const repos = new Keyset((p) => api.repos.list({ perm: ["repo.publish"], kind: "hosted" }, p), 200);
  formats.load();

  const targets = $derived(
    repos.items.filter((r) => r.permissions.includes("repo.publish")),
  );
  const byFormat = $derived(
    [...new Set(targets.map((r) => r.format))]
      .sort((a, b) => formats.title(a).localeCompare(formats.title(b)))
      .map((f) => ({ format: f, repos: targets.filter((r) => r.format === f) })),
  );
  const publishPath = (name: string) => repoPath(name, "publish");

  $effect(() => {
    if (!repos.loading && !repos.hasMore && targets.length === 1) navigate(publishPath(targets[0].name), { replace: true });
  });
</script>

{#snippet grid(list: typeof targets, label: string)}
  <CardGrid {label} max={3}>
    {#each list as r (r.name)}<RepoCard repo={r} href={href(publishPath(r.name))} />{/each}
  </CardGrid>
{/snippet}

{#if repos.loading && repos.items.length === 0}
  <Loading rows={4} />
{:else if repos.error && repos.items.length === 0}
  <ErrorState error={repos.error} title="The repositories did not load" onRetry={repos.reload} />
{:else if targets.length === 0}
  <EmptyState
    icon={UploadIcon}
    title="No repository to publish to"
    description="You cannot publish to any repository yet. Ask an administrator for publish access."
  />
{:else if targets.length > 1 || repos.hasMore}
  <PageHeader title="Publish" />
  {#if byFormat.length > 1}
    {#each byFormat as g (g.format)}
      <Section plain title={formats.title(g.format)}>{@render grid(g.repos, formats.title(g.format))}</Section>
    {/each}
  {:else}
    {@render grid(targets, "Repositories")}
  {/if}
  <SourceMore source={repos} />
{/if}
