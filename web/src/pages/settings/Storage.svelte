<!--
  Admin: the storage settings group, a connection test, and the storage move (progress, failures,
  cancel). Saving a new location while files exist starts a move; the settings change when it ends.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import type { PageProps } from "$lib/router.svelte";
  import { toast } from "svelte-sonner";
  import { api, errorMessage, type Move, type MoveFailure, type Setting, type Settings, type SettingsMoveStarted, type TestResult } from "$lib/api";
  import { Keyset, Resource } from "$lib/load.svelte";
  import { formatCount } from "$lib/format";
  import { Button } from "$lib/components/ui/button";
  import { Badge } from "$lib/components/ui/badge";
  import { Spinner } from "$lib/components/ui/spinner";
  import * as Table from "$lib/components/ui/table";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import LoadingRows from "$lib/components/LoadingRows.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import KeysetTable from "$lib/components/KeysetTable.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import SettingsGroupForm from "$lib/components/SettingsGroupForm.svelte";
  import CircleCheckIcon from "@lucide/svelte/icons/circle-check";

  let { params, query }: PageProps = $props();

  const settings = new Resource(() => api.settings.get());
  const current = new Resource(() => api.moves.current());

  const group = $derived(settings.data?.groups.find((g) => g.name === "storage"));
  let move = $state<Move | null>(null);

  // The newest move, running or ended, starts the panel.
  $effect(() => {
    if (current.data !== undefined) move = current.data;
  });

  // While files copy, ask for progress every two seconds.
  $effect(() => {
    const m = move;
    if (!m || m.state !== "copying") return;
    const id = m.id;
    const timer = setInterval(async () => {
      try {
        const next = await api.moves.get(id);
        move = next;
        if (next.state !== "copying") {
          clearInterval(timer);
          if (next.state === "done") toast.success("The storage move is done");
          settings.reload();
        }
      } catch {
        // The next tick tries again; a persistent failure shows when the page reloads.
      }
    }, 2000);
    return () => clearInterval(timer);
  });

  // Failed copies are listed once the move has any.
  let failures = $state<Keyset<MoveFailure> | null>(null);
  let failuresFor = 0;
  $effect(() => {
    const id = move?.id ?? 0;
    const failed = move?.failed ?? 0;
    const ended = move !== null && move.state !== "copying";
    if (!id || failed === 0 || !ended) {
      failures = null;
      failuresFor = 0;
      return;
    }
    if (failuresFor === id && failures) return;
    failuresFor = id;
    failures = untrack(() => new Keyset((p) => api.moves.failures(id, p)));
  });

  const storageValue = (s: Setting[], draft: Record<string, string | boolean>) => {
    const st = s.find((x) => x.key === "storage");
    return String(draft["storage"] ?? st?.value ?? "fs");
  };
  const visible = (s: Setting, draft: Record<string, string | boolean>) => {
    if (!group) return true;
    const kind = storageValue(group.settings, draft);
    if (s.key === "storage") return true;
    if (s.key === "fs.root") return kind === "fs";
    return kind !== "fs";
  };

  const moveEffects = (patch: Record<string, unknown>) =>
    Object.keys(patch).length
      ? [
          "Copies every file to the new location in the background, when the location changes and files exist",
          "Refuses uploads to every repository until the copy ends",
        ]
      : [];

  function saved(result: Settings | SettingsMoveStarted) {
    if ("move" in result) {
      move = result.move;
      toast.info("Started a storage move");
    } else settings.data = result;
  }

  let testing = $state(false);
  let testResult = $state<TestResult | null>(null);
  let testError = $state("");

  async function test(patch: Record<string, unknown>) {
    testing = true;
    testResult = null;
    testError = "";
    try {
      testResult = await api.settings.testStorage(patch);
    } catch (e) {
      testError = errorMessage(e);
    } finally {
      testing = false;
    }
  }

  let confirmCancel = $state(false);
  async function cancelMove() {
    if (!move) return;
    move = await api.moves.cancel(move.id);
    toast.success("Cancelled the storage move");
    settings.reload();
  }

  const stateLabel: Record<Move["state"], string> = {
    copying: "Copying",
    done: "Done",
    failed: "Failed",
    cancelled: "Cancelled",
  };
  const stateVariant = (s: Move["state"]) =>
    s === "failed" ? "destructive" : s === "copying" ? "default" : "secondary";
  const percent = (m: Move) => (m.total > 0 ? Math.min(100, Math.round(((m.copied + m.skipped + m.failed) / m.total) * 100)) : 0);
  const targetText = (m: Move) => {
    const t = m.target;
    if (t.storage === "fs") return `File store at ${t.fs_root ?? ""}`;
    return `S3 bucket ${t.bucket ?? ""}${t.prefix ? ` (prefix ${t.prefix})` : ""}${t.endpoint ? ` at ${t.endpoint}` : ""}`;
  };
</script>

<PageHeader title="Storage" description="Where the server keeps the files of every repository." />

{#if current.loading && !move}
  <Section title="Storage move"><LoadingRows rows={2} /></Section>
{:else if current.error && !move}
  <Section title="Storage move"><ErrorState error={current.error} onRetry={current.reload} /></Section>
{:else if move}
  <Section title="Storage move">
    <div class="flex flex-col gap-4 rounded-2xl border p-5">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div class="flex flex-wrap items-center gap-2">
          <Badge variant={stateVariant(move.state)}>
            {#if move.state === "copying"}<Spinner class="size-3" />{/if}
            {stateLabel[move.state]}
          </Badge>
          <span class="text-sm text-muted-foreground">Started <RelativeTime at={move.started_at} /></span>
          {#if move.finished_at}<span class="text-sm text-muted-foreground">Ended <RelativeTime at={move.finished_at} /></span>{/if}
        </div>
        {#if move.state === "copying"}
          <Button variant="outline" size="sm" onclick={() => (confirmCancel = true)}>Cancel the move</Button>
        {/if}
      </div>

      <p class="text-sm">To: <span class="font-mono text-[0.8125rem] break-all">{targetText(move)}</span></p>

      <div>
        <div
          class="h-2 overflow-hidden rounded-full bg-muted"
          role="progressbar"
          aria-label="Files moved"
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={percent(move)}
        >
          <div class="h-full bg-primary transition-all" style="width: {percent(move)}%"></div>
        </div>
        <p class="mt-2 text-sm text-muted-foreground tabular-nums">
          {formatCount(move.copied)} copied · {formatCount(move.skipped)} already there · {formatCount(move.failed)} failed · {formatCount(move.total)} in total
        </p>
      </div>

      {#if move.error}<ErrorState inline error={move.error} />{/if}
      {#if move.state === "copying"}
        <p class="text-sm text-muted-foreground">Uploads to every repository are refused until the move ends. Downloads keep working from the old location.</p>
      {:else if move.state === "done"}
        <p class="text-sm text-muted-foreground">The server now reads and writes the new location.</p>
      {:else}
        <p class="text-sm text-muted-foreground">The storage location did not change. Save the settings again to start a new move.</p>
      {/if}
    </div>

    {#if failures}
      <h3 class="mt-6 mb-4 text-sm font-semibold">Files that failed to copy</h3>
      <KeysetTable list={failures} columns={2} caption="Files that failed to copy">
        {#snippet head()}
          <Table.Head>File</Table.Head>
          <Table.Head>Reason</Table.Head>
        {/snippet}
        {#snippet row(f)}
          <Table.Row>
            <Table.Cell class="font-mono text-[0.8125rem] break-all whitespace-normal">{f.key}</Table.Cell>
            <Table.Cell class="whitespace-normal">{f.reason}</Table.Cell>
          </Table.Row>
        {/snippet}
      </KeysetTable>
    {/if}
  </Section>
{/if}

<Section narrow title="Storage backend" description="Changing the location copies the existing files to the new one in the background.">
  {#if settings.loading && !settings.data}
    <LoadingRows rows={5} />
  {:else if settings.error && !settings.data}
    <ErrorState error={settings.error} onRetry={settings.reload} />
  {:else if group}
    <SettingsGroupForm settings={group.settings} label="Storage settings" {visible} effects={moveEffects} onSaved={saved}>
      {#snippet extra(patch)}
        <Button type="button" variant="outline" disabled={testing} onclick={() => test(patch())}>
          {#if testing}<Spinner />{/if}
          Test connection
        </Button>
      {/snippet}
    </SettingsGroupForm>
    <div class="mt-4" aria-live="polite">
      {#if testResult?.ok}
        <p class="flex items-center gap-2 text-sm">
          <CircleCheckIcon class="size-4 shrink-0 text-link" />
          The connection works.
        </p>
      {:else if testResult}
        <ErrorState inline error={testResult.error ?? "The connection test failed."} />
      {:else if testError}
        <ErrorState inline error={testError} />
      {/if}
    </div>
  {:else}
    <ErrorState error="The server sent no storage settings." onRetry={settings.reload} />
  {/if}
</Section>

<ConfirmDialog
  bind:open={confirmCancel}
  title="Cancel the storage move"
  confirmLabel="Cancel the move"
  destructive
  changes={[
    "Stops copying files",
    "Keeps the files already copied in the new location",
    "Keeps the storage location unchanged",
    "Allows uploads again",
  ]}
  onConfirm={cancelMove}
/>
