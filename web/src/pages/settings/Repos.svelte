<!-- Admin: list and create repositories. A repository opens its settings page. -->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import { href, navigate } from "$lib/router.svelte";
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
  import PlusIcon from "@lucide/svelte/icons/plus";
  import PackageIcon from "@lucide/svelte/icons/package";
  import GlobeIcon from "@lucide/svelte/icons/globe";
  import LockIcon from "@lucide/svelte/icons/lock";
  import PauseIcon from "@lucide/svelte/icons/circle-pause";
  import CreateRepoDialog from "./repos/CreateRepoDialog.svelte";

  let { params, query }: PageProps = $props();

  const repos = new Resource(() => api.repos.list());
  const formats = new Resource(() => api.formats.list());
  const titles = $derived(new Map((formats.data ?? []).map((f) => [f.name, f.title])));

  let creating = $state(false);
</script>

<PageHeader title="Repositories" description="Create repositories, set who can read and write, and edit their config.">
  {#snippet actions()}
    <Button onclick={() => (creating = true)}>
      <PlusIcon />
      New repository
    </Button>
  {/snippet}
</PageHeader>

{#if repos.error && !repos.data}
  <ErrorState error={repos.error} onRetry={repos.reload} />
{:else if !repos.loading && repos.data?.length === 0}
  <EmptyState title="No repositories yet" description="A repository holds the files of one format, for example Maven artifacts." icon={PackageIcon}>
    <Button onclick={() => (creating = true)}>
      <PlusIcon />
      Create a repository
    </Button>
  </EmptyState>
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
          {#each repos.data ?? [] as r (r.name)}
            <Table.Row>
              <Table.Cell>
                <a class="font-mono font-medium text-link underline-offset-4 hover:underline" href={href("/settings/repos/" + encodeURIComponent(r.name))}>{r.name}</a>
              </Table.Cell>
              <Table.Cell><FormatBadge format={r.format} title={titles.get(r.format)} /></Table.Cell>
              <Table.Cell class="max-w-md whitespace-normal text-muted-foreground">
                {#if r.description}
                  <span class="line-clamp-2 break-words">{r.description}</span>
                {:else}
                  <span class="sr-only">No description</span>
                {/if}
              </Table.Cell>
              <Table.Cell>
                <div class="flex flex-wrap gap-1.5">
                  {#if r.anonymous_read}
                    <Badge variant="secondary" title="Anyone can read this repository without signing in"><GlobeIcon />Public</Badge>
                  {:else}
                    <Badge variant="outline" title="Only users with access can read this repository"><LockIcon />Private</Badge>
                  {/if}
                  {#if r.locked_by}<Badge variant="outline" title={r.locked_by}><PauseIcon />Uploads paused</Badge>{/if}
                </div>
              </Table.Cell>
            </Table.Row>
          {/each}
        {/if}
      </Table.Body>
    </Table.Root>
  </div>
{/if}

<CreateRepoDialog bind:open={creating} onCreated={(r) => navigate("/settings/repos/" + encodeURIComponent(r.name))} />
