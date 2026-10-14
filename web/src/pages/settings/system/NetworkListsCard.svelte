<!--
  Server settings, "Network lists": the IP range presets the server downloads (GET
  /api/network/presets), with their size, last refresh and last error, and a Refresh button.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import { api, errorMessage, type NetworkPreset } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import { formatCount } from "$lib/format";
  import * as Table from "$lib/components/ui/table";
  import { Button } from "$lib/components/ui/button";
  import { Spinner } from "$lib/components/ui/spinner";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import LoadingRows from "$lib/components/LoadingRows.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";

  const presets = new Resource(() => api.network.presets());

  /** What each list is for, in plain words. */
  const uses: Record<string, string> = {
    cloudflare: "Cloudflare's own servers. Used by the Cloudflare reverse proxy option.",
    "github-actions": "The machines that run GitHub Actions jobs. Used by network access rules of repositories.",
  };

  let refreshing = $state<Record<string, boolean>>({});

  async function refresh(p: NetworkPreset) {
    refreshing = { ...refreshing, [p.name]: true };
    try {
      const fresh = await api.network.refreshPreset(p.name);
      if (presets.data) presets.data = presets.data.map((x) => (x.name === fresh.name ? fresh : x));
      toast.success(`Refreshed the ${p.title} list: ${formatCount(fresh.count)} ranges`);
    } catch (e) {
      toast.error(`The ${p.title} list did not refresh. The server keeps the last good copy.`, { description: errorMessage(e) });
      await presets.reload();
    } finally {
      refreshing = { ...refreshing, [p.name]: false };
    }
  }
</script>

{#if presets.error && !presets.data}
  <ErrorState error={presets.error} title="The network lists did not load" onRetry={presets.reload} />
{:else if !presets.loading && presets.data?.length === 0}
  <EmptyState title="No network lists" description="This server downloads no address lists." />
{:else}
  <div class="w-full overflow-x-auto rounded-2xl border">
    <Table.Root>
      <Table.Caption class="sr-only">Network lists that the server downloads</Table.Caption>
      <Table.Header>
        <Table.Row class="hover:bg-transparent">
          <Table.Head>List</Table.Head>
          <Table.Head class="text-right">Ranges</Table.Head>
          <Table.Head>Last refresh</Table.Head>
          <Table.Head class="w-px"><span class="sr-only">Actions</span></Table.Head>
        </Table.Row>
      </Table.Header>
      <Table.Body>
        {#if presets.loading && !presets.data}
          <LoadingRows rows={2} columns={4} />
        {:else}
          {#each presets.data ?? [] as p (p.name)}
            <Table.Row>
              <Table.Cell class="align-top whitespace-normal">
                <div class="flex min-w-48 flex-col gap-1">
                  <span class="font-medium">{p.title}</span>
                  {#if uses[p.name]}<span class="text-sm text-muted-foreground">{uses[p.name]}</span>{/if}
                  {#if p.error}
                    <ErrorState inline error={`The last refresh failed: ${p.error}`} />
                  {/if}
                </div>
              </Table.Cell>
              <Table.Cell class="text-right align-top tabular-nums">
                {#if p.count}{formatCount(p.count)}{:else}<span class="text-muted-foreground">none yet</span>{/if}
              </Table.Cell>
              <Table.Cell class="align-top"><RelativeTime at={p.fetched_at} /></Table.Cell>
              <Table.Cell class="align-top">
                <Button variant="outline" size="sm" disabled={refreshing[p.name]} onclick={() => refresh(p)}>
                  {#if refreshing[p.name]}<Spinner />{/if}
                  Refresh
                </Button>
              </Table.Cell>
            </Table.Row>
          {/each}
        {/if}
      </Table.Body>
    </Table.Root>
  </div>
  <p class="mt-4 max-w-prose text-sm text-muted-foreground">
    The server downloads each list every day and keeps the last good copy when a download fails. "Private networks" is
    built in and never changes.
  </p>
{/if}
