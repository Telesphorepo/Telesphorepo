<!--
  Home, the front door: one line of welcome, the search field large (the top bar's search: files by
  path), then, when at least two would show, a door for each of the people who open the app, each a
  large card that is a link: Publish (to /publish, for a person who can publish somewhere), Browse
  (to /browse) and Administer (to the Settings overview, for a person who would see a card there).
  Each door is one column of a three-column row, so two doors keep that width, centered; on a phone
  they stack. Under them the repositories (a heading over the cards, no card around them): Your
  repositories, the first six by name where the person holds more than download, or, when there
  are none, Repositories, the first six the person can see (three on a phone), with See all.
  Both read GET /api/repos (each Repo.permissions), so a holder of All repositories counts too.
-->
<script lang="ts">
  import type { Component } from "svelte";
  import type { PageProps } from "$lib/router.svelte";
  import { href, repoPath } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { api } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import { Button } from "$lib/components/ui/button";
  import Section from "$lib/components/Section.svelte";
  import CardGrid from "$lib/components/CardGrid.svelte";
  import RepoCard from "$lib/components/RepoCard.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import GlobalSearch from "$lib/layouts/GlobalSearch.svelte";
  import { brand } from "$lib/brand.svelte";
  import { adminNav } from "../modules";
  import UploadIcon from "@lucide/svelte/icons/upload";
  import CompassIcon from "@lucide/svelte/icons/compass";
  import SettingsIcon from "@lucide/svelte/icons/settings";
  import DatabaseIcon from "@lucide/svelte/icons/database";
  import PlusIcon from "@lucide/svelte/icons/plus";

  let {}: PageProps = $props();

  const repos = new Resource(() => api.repos.list());

  const byName = $derived([...(repos.data ?? [])].sort((a, b) => a.name.localeCompare(b.name)));
  const mine = $derived(byName.filter((r) => r.permissions.some((p) => p !== "repo.download")));
  const cards = $derived((mine.length > 0 ? mine : byName).slice(0, 6));
  const heading = $derived(mine.length > 0 ? "Your repositories" : "Repositories");
  // Until the list loads, the grants decide whether the Publish door shows.
  const publishes = $derived(
    repos.data ? byName.some((r) => r.kind === "hosted" && r.permissions.includes("repo.publish")) : auth.canSomeRepo("repo.publish"),
  );

  type Door = { title: string; text: string; href: string; icon: Component<{ class?: string }> };
  const doors = $derived.by(() => {
    const list: Door[] = [];
    if (auth.user && publishes) list.push({ title: "Publish", text: "Set up a project to publish to this server.", href: href("/publish"), icon: UploadIcon });
    list.push({ title: "Browse", text: "Find a package and see how to use it.", href: href("/browse"), icon: CompassIcon });
    if (adminNav().length > 0)
      list.push({
        title: "Administer",
        text: "People, repositories, storage and the rest of the server.",
        href: href("/settings/overview"),
        icon: SettingsIcon,
      });
    // A lone Browse door says no more than the repositories under it.
    return list.length > 1 ? list : [];
  });
</script>

<div class="mb-(--header-gap) flex flex-col items-center gap-6 text-center">
  <h1 class="text-title">Welcome to {brand.name}</h1>
  <GlobalSearch large />
</div>

{#if doors.length > 0}
  <!-- Each door is one column of a three-column row, so two doors keep that width, centered. -->
  <nav aria-label="Start here" class="mb-(--section-gap) flex flex-col gap-4 md:flex-row md:justify-center">
    {#each doors as d (d.title)}
      {@const Icon = d.icon}
      <a
        href={d.href}
        class="flex flex-col gap-3 rounded-lg bg-card p-6 text-card-foreground ring-1 ring-foreground/10 outline-none transition-colors hover:bg-hover focus-visible:ring-[3px] focus-visible:ring-ring/50 md:w-[calc((100%-2rem)/3)]"
      >
        <span aria-hidden="true" class="flex text-(--mark)"><Icon class="size-8" /></span>
        <span class="text-section">{d.title}</span>
        <span class="text-body text-muted-foreground">{d.text}</span>
      </a>
    {/each}
  </nav>
{/if}

<!-- Its own block, so the section sits one gap under the search or the doors. -->
<div>
  {#if repos.loading && !repos.data}
    <Loading rows={5} />
  {:else if repos.error && !repos.data}
    <ErrorState error={repos.error} title="The repositories did not load" onRetry={repos.reload} />
  {:else if cards.length > 0}
    <Section title={heading} plain>
      {#snippet actions()}<Button variant="ghost" href={href("/browse")}>See all</Button>{/snippet}
      <!-- Three cards on a phone, six from 640px. -->
      <CardGrid label={heading} max={3} class="max-sm:[&>:nth-child(n+4)]:hidden">
        {#each cards as r (r.name)}<RepoCard repo={r} href={href(repoPath(r.name))} />{/each}
      </CardGrid>
    </Section>
  {:else if auth.can("repositories.all")}
    <EmptyState icon={DatabaseIcon} title="No repositories yet" description="A repository holds the packages of one format.">
      <Button href={href("/settings/repositories/new")}><PlusIcon />New repository</Button>
    </EmptyState>
  {:else if auth.user}
    <EmptyState icon={DatabaseIcon} title="No repositories to show" description="You do not have access to a repository yet. Ask an administrator for access." />
  {:else}
    <EmptyState icon={DatabaseIcon} title="No public repositories" description="Sign in to see the private repositories that you have access to." />
  {/if}
</div>
