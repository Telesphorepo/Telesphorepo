<!--
  Publish, the first step of publishing: the hosted repositories the person can publish to
  (GET /api/repos?perm=repo.publish&kind=hosted, 200 a page with Load more) as RepoCards that open their Publish
  tab, a section per format when more than one is present. A group is not listed: publishing to a
  group is publishing to one of its hosted members. The first answer decides the page: with exactly
  one repository it replaces its own history entry with that Publish tab; with none it says whom to
  ask; with more than a handful (or a next page) a search field shows over the cards, which keeps the
  repositories whose name holds what is typed (q, 250 ms after the last key).
-->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import { href, navigate, repoPath } from "$lib/router.svelte";
  import { api } from "$lib/api";
  import { Keyset, Searched } from "$lib/load.svelte";
  import { formats } from "$lib/formats/registry.svelte";
  import * as InputGroup from "$lib/components/ui/input-group";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Toolbar from "$lib/components/Toolbar.svelte";
  import Section from "$lib/components/Section.svelte";
  import CardGrid from "$lib/components/CardGrid.svelte";
  import RepoCard from "$lib/components/RepoCard.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import SourceMore from "$lib/components/SourceMore.svelte";
  import UploadIcon from "@lucide/svelte/icons/upload";
  import SearchIcon from "@lucide/svelte/icons/search";
  import SearchXIcon from "@lucide/svelte/icons/search-x";

  let {}: PageProps = $props();

  /** The search shows past this many repositories: two rows of three cards. */
  const handful = 6;

  const found = new Searched((q) => new Keyset((p) => api.repos.list({ perm: ["repo.publish"], kind: "hosted", q }, p), 200));
  formats.load();

  const targets = $derived(found.items.filter((r) => r.permissions.includes("repo.publish")));
  const byFormat = $derived(
    [...new Set(targets.map((r) => r.format))]
      .sort((a, b) => formats.title(a).localeCompare(formats.title(b)))
      .map((f) => ({ format: f, repos: targets.filter((r) => r.format === f) })),
  );
  const publishPath = (name: string) => repoPath(name, "publish");

  // The first answer without a search decides the page: one repository opens its Publish tab, none
  // says whom to ask, more than a handful gets the search field. A search never decides it again.
  let first = $state<"waiting" | "none" | "one" | "few" | "many">("waiting");
  $effect(() => {
    if (first !== "waiting" || found.pending || found.query || found.error) return;
    if (targets.length === 0) first = "none";
    else if (targets.length === 1 && !found.hasMore) {
      first = "one";
      navigate(publishPath(targets[0].name), { replace: true });
    } else first = targets.length > handful || found.hasMore ? "many" : "few";
  });
</script>

{#snippet grid(list: typeof targets, label: string)}
  <CardGrid {label} max={3}>
    {#each list as r (r.name)}<RepoCard repo={r} href={href(publishPath(r.name))} />{/each}
  </CardGrid>
{/snippet}

{#if first === "waiting"}
  {#if found.error}
    <ErrorState error={found.error} title="The repositories did not load" onRetry={found.reload} />
  {:else}
    <Loading rows={4} />
  {/if}
{:else if first === "none"}
  <EmptyState
    icon={UploadIcon}
    title="No repository to publish to"
    description="You cannot publish to any repository yet. Ask an administrator for publish access."
  />
{:else if first !== "one"}
  <PageHeader title="Publish" />
  {#if first === "many"}
    <Toolbar>
      <InputGroup.Root class="sm:w-72">
        <InputGroup.Addon><SearchIcon /></InputGroup.Addon>
        <InputGroup.Input
          bind:value={found.text}
          type="search"
          autocomplete="off"
          spellcheck={false}
          placeholder="Search by name"
          aria-label="Search the repositories you publish to by name"
          onkeydown={(e: KeyboardEvent) => e.key === "Enter" && found.flush()}
        />
      </InputGroup.Root>
    </Toolbar>
  {/if}
  {#if found.loading && found.items.length === 0}
    <Loading rows={4} />
  {:else if found.error && found.items.length === 0}
    <ErrorState error={found.error} title="The repositories did not load" onRetry={found.reload} />
  {:else if targets.length === 0}
    <CompactEmpty icon={SearchXIcon} text="No repository's name holds “{found.query}”." />
  {:else}
    {#if byFormat.length > 1}
      {#each byFormat as g (g.format)}
        <Section plain title={formats.title(g.format)}>{@render grid(g.repos, formats.title(g.format))}</Section>
      {/each}
    {:else}
      {@render grid(targets, "Repositories")}
    {/if}
    <SourceMore source={found} />
  {/if}
{/if}
