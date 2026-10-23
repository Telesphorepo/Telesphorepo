<!--
  One Nexus sync: its state, a row per repository with its progress, the log, and the files that
  failed. It asks for progress every two seconds while the sync runs (a Live, with its LiveStatus
  in the section header; the log reads its new lines at each refresh), gives each answer to
  onProgress, and calls onChanged when the sync ends. A click on a repository row opens
  `filterHref(repo)`, which shows the log of that repository; `filter` is the repository the log
  shows ("" for every repository). The parent keys it by id.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { api, type NexusSync, type NexusSyncRepo } from "$lib/api";
  import { Keyset, Resource, type TableSource } from "$lib/load.svelte";
  import { clock, Live } from "$lib/live.svelte";
  import { formatCount, formatDateTime, formatDuration, formatRelative, formatSize } from "$lib/format";
  import LiveStatus from "$lib/components/LiveStatus.svelte";
  import LiveValue from "$lib/components/LiveValue.svelte";
  import { Button } from "$lib/components/ui/button";
  import Cell from "$lib/components/Cell.svelte";
  import DataTable from "$lib/components/DataTable.svelte";
  import ItemList from "$lib/components/ItemList.svelte";
  import CopyButton from "$lib/components/CopyButton.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import Section from "$lib/components/Section.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import SyncLog from "./SyncLog.svelte";
  import { howText, mergeSyncText, syncState } from "./words";
  import XIcon from "@lucide/svelte/icons/x";

  let {
    id,
    filter,
    filterHref,
    onClose,
    onChanged,
    onProgress,
  }: {
    id: number;
    filter: string;
    /** The address that shows the log of one repository (null: every repository). */
    filterHref: (repo: string | null) => string;
    /** Without it, the section has no Close button. */
    onClose?: () => void;
    onChanged: () => void;
    onProgress?: (s: NexusSync) => void;
  } = $props();

  const syncId = untrack(() => id);
  const sync = new Resource(() => api.nexus.getSync(syncId));
  const failures = new Keyset((p) => api.nexus.syncFailures(syncId, p));

  const running = $derived(sync.data?.state === "running");

  // When the sync ends, the source and the history change too.
  let previous: NexusSync["state"] | undefined;
  $effect(() => {
    const state = sync.data?.state;
    if (previous === "running" && state && state !== "running") untrack(onChanged);
    previous = state;
  });

  $effect(() => {
    const s = sync.data;
    if (s) untrack(() => onProgress?.(s));
  });

  // The failed files reload when the count of failed files changes, also while the sync runs.
  let failedSeen = -1;
  $effect(() => {
    const n = sync.data?.failed;
    if (n === undefined) return;
    if (failedSeen >= 0 && n !== failedSeen) untrack(failures.reload);
    failedSeen = n;
  });

  // Progress every two seconds while the sync runs; the log follows the same Live.
  const live = new Live({
    every: 2000,
    while: () => running,
    run: async () => {
      sync.data = await api.nexus.getSync(syncId);
    },
  });
  const now = $derived(clock.now);

  const repos: TableSource<NexusSyncRepo> = {
    get items() {
      return sync.data?.repos ?? [];
    },
    loading: false,
    error: "",
  };

  /** "about 40%": the part of the files that the last finished sync checked. Never 100 before the end. */
  const percent = (r: NexusSyncRepo) => Math.min(99, Math.floor((r.checked / r.previous_checked) * 100));
  /** A running repository with no finished page for two minutes. */
  const stalled = (r: NexusSyncRepo) => r.state === "running" && !!r.active_at && now - new Date(r.active_at).getTime() > 120_000;
  const progressText = (r: { imported: number; bytes: number; checked: number }) =>
    `${formatCount(r.imported)} copied (${formatSize(r.bytes)}) of ${formatCount(r.checked)} checked`;
  const tookText = (s: NexusSync) => (s.finished_at ? formatDuration(new Date(s.finished_at).getTime() - new Date(s.started_at).getTime()) : "");

  const rowHref = (r: NexusSyncRepo) => filterHref(r.source_repo === filter ? null : r.source_repo);
</script>

{#snippet actions()}
  {#if sync.data}<LiveStatus {live} />{/if}
  {#if sync.data}<StatusBadge tone={syncState[sync.data.state].tone}>{syncState[sync.data.state].word}</StatusBadge>{/if}
  {#if onClose}<Button variant="ghost" size="icon-sm" aria-label="Close the sync" onclick={onClose}><XIcon /></Button>{/if}
{/snippet}

<Section title={sync.data ? (sync.data.merge_id ? `${mergeSyncText(sync.data)}, ${formatDateTime(sync.data.started_at)}` : `${sync.data.trigger === "cutover" ? "Final sync" : "Sync"} of ${formatDateTime(sync.data.started_at)}`) : "Sync"} {actions}>
{#if sync.loading && !sync.data}
  <Loading rows={3} />
{:else if sync.error && !sync.data}
  <ErrorState error={sync.error} onRetry={sync.reload} />
{:else if sync.data}
  {@const s = sync.data}
  <div class="flex flex-col gap-6">
    <div class="flex flex-col gap-2">
      {#if s.error}<ErrorState inline error={s.error} />{/if}
      <p class="text-meta text-muted-foreground tabular-nums">
        {howText(s)}. <LiveValue value={s.checked}>{progressText(s)}</LiveValue>{#if s.failed > 0}, <span class="text-destructive">{formatCount(s.failed)} failed</span>{/if}.
        {#if s.finished_at}It took {tookText(s)}.{/if}
      </p>
    </div>

    {#if s.repos && s.repos.length > 0}
      <DataTable
        source={repos}
        caption="Repositories of this sync. Click a row to show its lines in the log."
        key={(r) => r.source_repo}
        href={rowHref}
        selected={(r) => r.source_repo === filter}
        columns={[
          { label: "Nexus repository", grow: true },
          { label: "State" },
          { label: "Progress" },
          { label: "Last activity", align: "right", hideOnPhone: true },
          { label: "Failed", align: "right" },
        ]}
      >
        {#snippet row(r)}
          <Cell grow>
            <span class="flex min-w-0 items-baseline gap-2">
              <a href={rowHref(r)} class="shrink-0 rounded-sm font-medium outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50">{r.source_repo}</a>
              {#if r.error}<span class="min-w-0 truncate text-destructive" title={r.error}>{r.error}</span>{/if}
            </span>
          </Cell>
          <Cell><StatusBadge tone={syncState[r.state].tone}>{syncState[r.state].word}</StatusBadge></Cell>
          <Cell class="tabular-nums">
            {#if r.state !== "pending"}
              <span class="flex items-center gap-3">
                <span>{progressText(r)}</span>
                {#if r.state === "running" && r.previous_checked > 0}
                  <span
                    class="block h-1.5 w-20 shrink-0 overflow-hidden rounded-full bg-muted"
                    role="progressbar"
                    aria-label="Progress of {r.source_repo}"
                    aria-valuemin={0}
                    aria-valuemax={100}
                    aria-valuenow={percent(r)}
                  ><span class="block h-full rounded-full bg-primary transition-[width]" style="width: {percent(r)}%"></span></span>
                  <span class="text-muted-foreground">about {percent(r)}%</span>
                {/if}
              </span>
            {/if}
          </Cell>
          <Cell num muted hideOnPhone>
            {#if r.state !== "running"}
              <RelativeTime at={r.active_at} empty="" />
            {:else if !r.active_at}
              Nexus sent no page yet
            {:else if stalled(r)}
              <span class="text-warning">No progress for {formatDuration(now - new Date(r.active_at).getTime())}</span>
            {:else}
              <time datetime={r.active_at} title={formatDateTime(r.active_at)}>{formatRelative(r.active_at, now)}</time>
            {/if}
          </Cell>
          <Cell num class={r.failed > 0 ? "text-destructive" : "text-muted-foreground"}>{formatCount(r.failed)}</Cell>
        {/snippet}
      </DataTable>
    {/if}

    <div class="flex flex-col gap-2">
      <div class="flex min-h-8 items-center justify-between gap-2">
        <h3 class="text-label">{filter ? `Log of ${filter}` : "Log"}</h3>
        {#if filter}<Button variant="ghost" size="sm" href={filterHref(null)}>All repositories</Button>{/if}
      </div>
      {#key filter}
        <SyncLog id={syncId} repo={filter} {running} refreshed={live.lastUpdated} />
      {/key}
    </div>

    {#if s.failed > 0}
      <div class="flex flex-col gap-2">
        <h3 class="text-label">Files that failed to copy</h3>
        <ItemList source={failures} caption="Files that failed to copy" key={(f) => f.source_repo + "/" + f.path}>
          {#snippet title(f)}{f.path}{/snippet}
          {#snippet meta(f)}{f.source_repo} · {f.reason}{/snippet}
          {#snippet trailing(f)}<CopyButton value={f.path} label="Copy the path" />{/snippet}
        </ItemList>
      </div>
    {/if}
  </div>
{/if}
</Section>
