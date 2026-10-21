<!--
  The log of one Nexus sync: a line per event, oldest first, in a box that scrolls on its own. It
  loads every line (of one Nexus repository with `repo`), then, while the sync runs, asks for the
  new lines at each refresh of the sync (`refreshed`). The box stays at the newest line unless the reader scrolled up.
  The parent keys it by sync and repository.
-->
<script lang="ts">
  import { tick, untrack } from "svelte";
  import { api, errorMessage, type NexusLogLine } from "$lib/api";
  import type { TableSource } from "$lib/load.svelte";
  import { formatDateTime } from "$lib/format";
  import DataTable from "$lib/components/DataTable.svelte";
  import Cell from "$lib/components/Cell.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import ScrollTextIcon from "@lucide/svelte/icons/scroll-text";

  let {
    id,
    repo,
    running,
    refreshed,
  }: {
    id: number;
    repo: string;
    running: boolean;
    /** The time of the sync's last refresh (`Live.lastUpdated`): each change reads the new lines. */
    refreshed: number | null;
  } = $props();

  const syncId = untrack(() => id);
  const only = untrack(() => repo) || undefined;

  let lines = $state<NexusLogLine[]>([]);
  let loading = $state(true);
  let error = $state("");
  let loaded = false;
  let busy = false;

  let box = $state<HTMLDivElement>();
  let stick = true;
  function onScroll() {
    if (box) stick = box.scrollHeight - box.scrollTop - box.clientHeight < 24;
  }
  async function toNewest() {
    await tick();
    if (stick && box) box.scrollTop = box.scrollHeight;
  }

  // Reads pages from `after` until the last one; at most 50 pages (10,000 lines) per call.
  async function readFrom(after: string | undefined) {
    for (let i = 0; i < 50; i++) {
      const p = await api.nexus.syncLog(syncId, { after, limit: 200 }, only);
      if (p.items.length) {
        lines = [...lines, ...p.items];
        await toNewest();
      }
      if (!p.next) return;
      after = p.next;
    }
  }

  async function load() {
    if (busy) return;
    busy = true;
    loading = true;
    error = "";
    lines = [];
    try {
      await readFrom(undefined);
      loaded = true;
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
      busy = false;
      if (again && loaded) {
        again = false;
        readNew();
      }
    }
  }

  // A read asked for while another one runs happens after it, so the end of the sync is never lost.
  let again = false;
  async function readNew() {
    if (busy || !loaded) {
      again = true;
      return;
    }
    busy = true;
    try {
      const last = lines.at(-1);
      await readFrom(last ? String(last.id) : undefined);
      error = "";
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
      if (again) {
        again = false;
        readNew();
      }
    }
  }

  load();

  // At each refresh of the sync (its Live), read the new lines.
  $effect(() => {
    if (refreshed === null || !untrack(() => running)) return;
    untrack(readNew);
  });

  // The last lines (the finish line) come after the last refresh: read them once when the sync ends.
  let was = untrack(() => running);
  $effect(() => {
    const now = running;
    if (was && !now) untrack(readNew);
    was = now;
  });

  const source: TableSource<NexusLogLine> = {
    get items() {
      return lines;
    },
    get loading() {
      return loading && lines.length === 0;
    },
    get error() {
      return error;
    },
    reload: load,
  };

  const timeOf = new Intl.DateTimeFormat(undefined, { timeStyle: "medium" });
  const tone = (l: NexusLogLine) => (l.level === "error" ? "text-destructive" : l.level === "warn" ? "text-warning" : "");
</script>

<div bind:this={box} onscroll={onScroll} class="max-h-120 overflow-y-auto">
  <DataTable
    {source}
    caption="Log of this sync"
    key={(l) => l.id}
    columns={[
      { label: "Time", align: "right" },
      { label: "Repository", hideOnPhone: true },
      { label: "Message", grow: true },
    ]}
  >
    {#snippet row(l)}
      <Cell num muted><time datetime={l.at} title={formatDateTime(l.at)}>{timeOf.format(new Date(l.at))}</time></Cell>
      <Cell hideOnPhone muted text={l.source_repo} />
      <Cell grow class={tone(l)} text={l.message} />
    {/snippet}
    {#snippet empty()}
      <CompactEmpty icon={ScrollTextIcon} text={only ? "The log has no lines about this repository yet." : "The log has no lines yet."} />
    {/snippet}
  </DataTable>
</div>
