<!-- Home: the repositories the visitor can read (name, format, description, visibility). -->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import { href } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { api } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import * as Table from "$lib/components/ui/table";
  import { Badge } from "$lib/components/ui/badge";
  import { Button } from "$lib/components/ui/button";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import LoadingRows from "$lib/components/LoadingRows.svelte";
  import FormatBadge from "$lib/components/FormatBadge.svelte";
  import DatabaseIcon from "@lucide/svelte/icons/database";
  import GlobeIcon from "@lucide/svelte/icons/globe";
  import LockIcon from "@lucide/svelte/icons/lock";

  let {}: PageProps = $props();

  const repos = new Resource(() => api.repos.list());
  // Format titles for the badges; the format name stands in until they load or if they fail.
  const formats = new Resource(() => api.formats.list());
  const titles = $derived(Object.fromEntries((formats.data ?? []).map((f) => [f.name, f.title])));
  const sorted = $derived([...(repos.data ?? [])].sort((a, b) => a.name.localeCompare(b.name)));

  const repoHref = (name: string) => href("/repo/" + encodeURIComponent(name) + "/");
</script>

<PageHeader
  title="Repositories"
  description={auth.user ? "The repositories you can read." : "Public repositories. Sign in to see the private ones you can read."}
>
  {#snippet actions()}
    {#if auth.isAdmin}
      <Button variant="outline" href={href("/settings/repos")}>Manage repositories</Button>
    {/if}
  {/snippet}
</PageHeader>

{#if repos.error}
  <ErrorState error={repos.error} onRetry={repos.reload} />
{:else if !repos.loading && sorted.length === 0}
  {#if auth.isAdmin}
    <EmptyState icon={DatabaseIcon} title="No repositories yet" description="Create a repository, then publish to it or upload files.">
      <Button href={href("/settings/repos")}>Create a repository</Button>
    </EmptyState>
  {:else if auth.user}
    <EmptyState
      icon={DatabaseIcon}
      title="No repositories to show"
      description="You cannot read any repository yet. Ask an administrator for access."
    />
  {:else}
    <EmptyState icon={DatabaseIcon} title="No public repositories" description="Sign in to see the repositories you can read.">
      <Button href={href("/login", { next: "/" })}>Sign in</Button>
    </EmptyState>
  {/if}
{:else}
  <div class="w-full overflow-x-auto rounded-2xl border">
    <Table.Root>
      <Table.Caption class="sr-only">Repositories</Table.Caption>
      <Table.Header>
        <Table.Row class="hover:bg-transparent">
          <Table.Head>Name</Table.Head>
          <Table.Head>Format</Table.Head>
          <Table.Head class="w-full">Description</Table.Head>
          <Table.Head>Access</Table.Head>
        </Table.Row>
      </Table.Header>
      <Table.Body>
        {#if repos.loading}
          <LoadingRows rows={4} columns={4} />
        {:else}
          {#each sorted as r (r.name)}
            <Table.Row>
              <Table.Cell class="font-medium">
                <a href={repoHref(r.name)} class="font-mono text-link underline-offset-4 hover:underline">{r.name}</a>
              </Table.Cell>
              <Table.Cell><FormatBadge format={r.format} title={titles[r.format]} /></Table.Cell>
              <Table.Cell class="max-w-md whitespace-normal text-muted-foreground">
                {#if r.description}
                  <span class="line-clamp-2 break-words">{r.description}</span>
                {:else}
                  <span class="sr-only">No description</span>
                {/if}
              </Table.Cell>
              <Table.Cell>
                {#if r.anonymous_read}
                  <Badge variant="secondary" title="Anyone can read this repository without signing in">
                    <GlobeIcon />Public
                  </Badge>
                {:else}
                  <Badge variant="outline" title="Only users with access can read this repository">
                    <LockIcon />Private
                  </Badge>
                {/if}
              </Table.Cell>
            </Table.Row>
          {/each}
        {/if}
      </Table.Body>
    </Table.Root>
  </div>
{/if}
