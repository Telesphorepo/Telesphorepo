<!--
  One Nexus sync: state, a row per mirrored repository, the failed files, and Cancel while it
  runs. It asks for progress every two seconds while the sync runs and calls onChanged when the
  state changes. The parent wraps it in {#key id}.

    {#key shown}<NexusSyncDetail id={shown} onChanged={reload} />{/key}
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import { api, type NexusSync } from "$lib/api";
  import { Keyset, Resource } from "$lib/load.svelte";
  import { formatCount } from "$lib/format";
  import { Badge } from "$lib/components/ui/badge";
  import { Button } from "$lib/components/ui/button";
  import { Spinner } from "$lib/components/ui/spinner";
  import * as Table from "$lib/components/ui/table";
  import ErrorState from "./ErrorState.svelte";
  import LoadingRows from "./LoadingRows.svelte";
  import KeysetTable from "./KeysetTable.svelte";
  import ConfirmDialog from "./ConfirmDialog.svelte";
  import RelativeTime from "./RelativeTime.svelte";

  let { id, onChanged }: { id: number; onChanged: () => void } = $props();

  /* svelte-ignore state_referenced_locally */
  const sync = new Resource(() => api.nexus.getSync(id));
  /* svelte-ignore state_referenced_locally */
  const failures = new Keyset((p) => api.nexus.syncFailures(id, p));

  const running = $derived(sync.data?.state === "running");
  let previous: NexusSync["state"] | undefined;

  $effect(() => {
    const state = sync.data?.state;
    if (previous === "running" && state && state !== "running") {
      failures.reload();
      onChanged();
    }
    previous = state;
  });

  $effect(() => {
    if (!running) return;
    const timer = setInterval(() => {
      api.nexus.getSync(id).then(
        (s) => (sync.data = s),
        () => {},
      );
    }, 2000);
    return () => clearInterval(timer);
  });

  const totalFailed = $derived(sync.data?.repos?.reduce((n, r) => n + r.failed, 0) ?? sync.data?.failed ?? 0);

  let confirmCancel = $state(false);
  async function cancel() {
    sync.data = await api.nexus.cancelSync(id);
    toast.success("Cancelled the sync");
  }

  const variant = (s: string) => (s === "failed" ? "destructive" : s === "running" ? "default" : "secondary");
</script>

{#if sync.loading && !sync.data}
  <LoadingRows rows={3} />
{:else if sync.error && !sync.data}
  <ErrorState error={sync.error} onRetry={sync.reload} />
{:else if sync.data}
  {@const s = sync.data}
  <div class="flex flex-col gap-4">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div class="flex flex-wrap items-center gap-2">
        <h3 class="text-sm font-semibold">Sync {s.id}{s.cutover ? " (final sync)" : ""}</h3>
        <Badge variant={variant(s.state)} class="capitalize">
          {#if s.state === "running"}<Spinner class="size-3" />{/if}
          {s.state}
        </Badge>
        <span class="text-sm text-muted-foreground">Started <RelativeTime at={s.started_at} /></span>
      </div>
      {#if s.state === "running"}
        <Button variant="outline" size="sm" onclick={() => (confirmCancel = true)}>Cancel the sync</Button>
      {/if}
    </div>

    {#if s.error}<ErrorState inline error={s.error} />{/if}

    {#if s.repos && s.repos.length > 0}
      <div class="w-full overflow-x-auto rounded-2xl border">
        <Table.Root>
          <Table.Caption class="sr-only">Repositories of sync {s.id}</Table.Caption>
          <Table.Header>
            <Table.Row class="hover:bg-transparent">
              <Table.Head>Nexus repository</Table.Head>
              <Table.Head>Name here</Table.Head>
              <Table.Head>State</Table.Head>
              <Table.Head class="text-right">Imported</Table.Head>
              <Table.Head class="text-right">Skipped</Table.Head>
              <Table.Head class="text-right">Ignored</Table.Head>
              <Table.Head class="text-right">Failed</Table.Head>
            </Table.Row>
          </Table.Header>
          <Table.Body>
            {#each s.repos as r (r.source_repo)}
              <Table.Row>
                <Table.Cell class="font-mono text-[0.8125rem]">{r.source_repo}</Table.Cell>
                <Table.Cell class="font-mono text-[0.8125rem]">{r.target}</Table.Cell>
                <Table.Cell class="capitalize">
                  {r.state}
                  {#if r.error}<div class="text-sm break-words whitespace-normal text-destructive normal-case">{r.error}</div>{/if}
                </Table.Cell>
                <Table.Cell class="text-right tabular-nums">{formatCount(r.imported)}</Table.Cell>
                <Table.Cell class="text-right tabular-nums">{formatCount(r.skipped)}</Table.Cell>
                <Table.Cell class="text-right tabular-nums">{formatCount(r.ignored)}</Table.Cell>
                <Table.Cell class="text-right tabular-nums {r.failed > 0 ? 'text-destructive' : ''}">{formatCount(r.failed)}</Table.Cell>
              </Table.Row>
            {/each}
          </Table.Body>
        </Table.Root>
      </div>
    {:else}
      <p class="text-sm text-muted-foreground">
        Imported {formatCount(s.imported)} · skipped {formatCount(s.skipped)} · ignored {formatCount(s.ignored)} · failed {formatCount(s.failed)}
      </p>
    {/if}

    {#if totalFailed > 0 && s.state !== "running"}
      <div>
        <h4 class="mb-4 text-sm font-semibold">Files that failed to import</h4>
        <KeysetTable list={failures} columns={3} caption="Files that failed to import">
          {#snippet head()}
            <Table.Head>Nexus repository</Table.Head>
            <Table.Head>Path</Table.Head>
            <Table.Head>Reason</Table.Head>
          {/snippet}
          {#snippet row(f)}
            <Table.Row>
              <Table.Cell class="font-mono text-[0.8125rem]">{f.source_repo}</Table.Cell>
              <Table.Cell class="font-mono text-[0.8125rem] break-all whitespace-normal">{f.path}</Table.Cell>
              <Table.Cell class="whitespace-normal">{f.reason}</Table.Cell>
            </Table.Row>
          {/snippet}
        </KeysetTable>
      </div>
    {:else if totalFailed > 0}
      <p class="text-sm text-muted-foreground">The list of failed files shows when the sync ends.</p>
    {/if}
  </div>
{/if}

<ConfirmDialog
  bind:open={confirmCancel}
  title="Cancel the sync"
  confirmLabel="Cancel the sync"
  destructive
  changes={["Stops importing files", "Keeps the files already imported"]}
  onConfirm={cancel}
/>
