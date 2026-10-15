<!--
  One Nexus sync: its state, a row per copied repository, the files that failed, and Cancel while
  it runs. It asks for progress every two seconds while the sync runs, and calls onChanged when it
  ends. The parent keys it by id.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { toast } from "svelte-sonner";
  import { api, type NexusSync, type NexusSyncRepo } from "$lib/api";
  import { Keyset, Resource, type TableSource } from "$lib/load.svelte";
  import { formatCount, formatDateTime } from "$lib/format";
  import Cell from "$lib/components/Cell.svelte";
  import { Button } from "$lib/components/ui/button";
  import DataTable from "$lib/components/DataTable.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import Section from "$lib/components/Section.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import { syncState } from "./words";
  import XIcon from "@lucide/svelte/icons/x";

  let { id, onClose, onChanged }: { id: number; onClose: () => void; onChanged: () => void } = $props();

  const sync = new Resource(() => api.nexus.getSync(untrack(() => id)));
  const failures = new Keyset((p) => api.nexus.syncFailures(untrack(() => id), p));

  const running = $derived(sync.data?.state === "running");
  let previous: NexusSync["state"] | undefined;

  $effect(() => {
    const state = sync.data?.state;
    if (previous === "running" && state && state !== "running") {
      untrack(() => {
        failures.reload();
        onChanged();
      });
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

  const repos: TableSource<NexusSyncRepo> = {
    get items() {
      return sync.data?.repos ?? [];
    },
    loading: false,
    error: "",
  };
  const totalFailed = $derived(sync.data?.repos?.reduce((n, r) => n + r.failed, 0) ?? sync.data?.failed ?? 0);

  let cancelOpen = $state(false);
  async function cancel() {
    sync.data = await api.nexus.cancelSync(id);
    toast.success("Cancelled the sync");
  }
</script>

{#snippet actions()}
  {#if running}<Button variant="outline" size="sm" onclick={() => (cancelOpen = true)}>Cancel the sync</Button>{/if}
  <Button variant="ghost" size="icon-sm" aria-label="Close the sync" onclick={onClose}><XIcon /></Button>
{/snippet}

<Section title={sync.data ? `${sync.data.cutover ? "Final sync" : "Sync"} of ${formatDateTime(sync.data.started_at)}` : "Sync"} {actions}>
{#if sync.loading && !sync.data}
  <Loading rows={3} />
{:else if sync.error && !sync.data}
  <ErrorState error={sync.error} onRetry={sync.reload} />
{:else if sync.data}
  {@const s = sync.data}
  <div class="flex flex-col gap-5">
    {#if s.error}<ErrorState inline error={s.error} />{/if}

    {#if s.repos && s.repos.length > 0}
      <DataTable
        source={repos}
        caption="Repositories of this sync"
        key={(r) => r.source_repo}
        columns={[
          { label: "Nexus repository", grow: true },
          { label: "Name here", hideOnPhone: true },
          { label: "State" },
          { label: "Copied", align: "right" },
          { label: "Skipped", align: "right", hideOnPhone: true },
          { label: "Ignored", align: "right", hideOnPhone: true },
          { label: "Failed", align: "right" },
        ]}
      >
        {#snippet row(r)}
          <Cell grow>
            <span class="flex min-w-0 items-baseline gap-2">
              <span class="shrink-0 font-medium">{r.source_repo}</span>
              {#if r.error}<span class="min-w-0 truncate text-destructive" title={r.error}>{r.error}</span>{/if}
            </span>
          </Cell>
          <Cell hideOnPhone muted text={r.target} />
          <Cell><StatusBadge tone={syncState[r.state].tone}>{syncState[r.state].word}</StatusBadge></Cell>
          <Cell num>{formatCount(r.imported)}</Cell>
          <Cell num muted hideOnPhone>{formatCount(r.skipped)}</Cell>
          <Cell num muted hideOnPhone>{formatCount(r.ignored)}</Cell>
          <Cell num class={r.failed > 0 ? "text-destructive" : "text-muted-foreground"}>{formatCount(r.failed)}</Cell>
        {/snippet}
      </DataTable>
    {:else}
      <p class="text-meta text-muted-foreground tabular-nums">
        Copied {formatCount(s.imported)}, skipped {formatCount(s.skipped)}, ignored {formatCount(s.ignored)}, failed {formatCount(s.failed)}.
      </p>
    {/if}

    {#if totalFailed > 0 && !running}
      <div class="flex flex-col gap-2">
        <h4 class="text-label">Files that failed to copy</h4>
        <DataTable
          source={failures}
          caption="Files that failed to copy"
          key={(f) => f.source_repo + "/" + f.path}
          columns={[{ label: "Nexus repository", hideOnPhone: true }, { label: "Path", grow: true }, { label: "Reason" }]}
        >
          {#snippet row(f)}
            <Cell hideOnPhone muted text={f.source_repo} />
            <Cell grow text={f.path} copy copyLabel="Copy the path" />
            <Cell muted text={f.reason} />
          {/snippet}
        </DataTable>
      </div>
    {:else if totalFailed > 0}
      <p class="text-meta text-muted-foreground">The failed files show when the sync ends.</p>
    {/if}
  </div>
{/if}
</Section>

<ConfirmDialog
  bind:open={cancelOpen}
  title="Cancel this sync?"
  confirmLabel="Cancel the sync"
  destructive
  changes={["Stops copying files", "Keeps the files already copied"]}
  onConfirm={cancel}
/>
