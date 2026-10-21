<!--
  Storage (Storage permission): where the server keeps the files of every repository (a folder on
  this server or an S3 bucket), a connection test, and the storage move. While a move copies, its
  card comes first with the progress. Saving a new location while files exist starts a move; the
  setting changes when the move ends.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { toast } from "svelte-sonner";
  import type { PageProps } from "$lib/router.svelte";
  import { api, errorMessage, type Move, type MoveFailure, type Setting, type Settings, type SettingsMoveStarted } from "$lib/api";
  import { Keyset, Resource } from "$lib/load.svelte";
  import { formatCount } from "$lib/format";
  import { Button } from "$lib/components/ui/button";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import FormPage from "$lib/components/FormPage.svelte";
  import Section from "$lib/components/Section.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import ConnectionTest, { type TestResult as ConnectionResult } from "$lib/components/ConnectionTest.svelte";
  import DataTable from "$lib/components/DataTable.svelte";
  import Cell from "$lib/components/Cell.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import SettingsGroupForm, { type Draft } from "$lib/components/SettingsGroupForm.svelte";
  import { cn } from "$lib/utils";

  let {}: PageProps = $props();

  const settings = new Resource(() => api.settings.get());
  const current = new Resource(() => api.moves.current());
  const group = $derived(settings.data?.groups.find((g) => g.id === "storage"));
  const value = (key: string) => group?.settings.find((s) => s.key === key)?.value;

  let move = $state<Move | null>(null);
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
        // The next tick asks again; a lasting failure shows when the page loads again.
      }
    }, 2000);
    return () => clearInterval(timer);
  });

  // The files that failed to copy, once an ended move has any.
  let failures = $state<Keyset<MoveFailure> | null>(null);
  let failuresFor = 0;
  $effect(() => {
    const id = move?.id ?? 0;
    const ended = move !== null && move.state !== "copying";
    if (!id || !ended || (move?.failed ?? 0) === 0) {
      failures = null;
      failuresFor = 0;
      return;
    }
    if (failuresFor === id && failures) return;
    failuresFor = id;
    failures = untrack(() => new Keyset((p) => api.moves.failures(id, p)));
  });

  const kindNow = $derived(String(value("storage") ?? "fs"));

  const sections = [
    { title: "Location", keys: ["storage", "fs.root"] },
    { title: "S3 bucket", keys: ["s3.endpoint", "s3.region", "s3.bucket", "s3.prefix", "s3.access_key_id", "s3.secret_access_key", "s3.path_style", "s3.redirect", "presign_ttl"] },
  ];
  const fields = {
    storage: { label: "Storage type", help: "When you change it, the server moves the existing files in the background." },
    "fs.root": { label: "Folder", help: "A relative path starts in the data folder." },
    "s3.endpoint": { label: "Address", help: "The address of the S3 API. For Cloudflare R2: https://<account id>.r2.cloudflarestorage.com, from the S3 API part of the bucket's settings, not a custom domain of the bucket. Leave it empty for AWS: the server then uses the AWS address of the region." },
    "s3.region": { label: "Region", help: "Required for AWS, for example us-east-1. With an address and no region, the server uses auto." },
    "s3.bucket": { label: "Bucket" },
    "s3.access_key_id": { label: "Access key ID" },
    "s3.secret_access_key": { label: "Secret access key" },
    "s3.prefix": { label: "Folder in the bucket", help: "Leave it empty to keep the files at the top of the bucket." },
    "s3.path_style": { label: "Path-style addresses", help: "MinIO and SeaweedFS need this. AWS and R2 do not." },
    "s3.redirect": { label: "Download from the bucket", help: "Builds get files directly from the bucket, not through this server." },
    presign_ttl: { label: "Download link lifetime", help: "A signed link to a file in the bucket works for this time." },
  };

  const kind = (draft: Record<string, Draft>) => String(draft["storage"] ?? kindNow);
  const visible = (s: Setting, draft: Record<string, Draft>) => {
    if (s.key === "storage") return true;
    if (s.key === "fs.root") return kind(draft) === "fs";
    return kind(draft) !== "fs";
  };

  const moveEffects = (patch: Record<string, unknown>) =>
    Object.keys(patch).some((k) => k === "storage" || k === "fs.root" || k.startsWith("s3."))
      ? ["When files exist and the location changes, copies every file to the new location in the background", "Keeps uploads working: each upload goes to both locations until the copy ends"]
      : [];

  function saved(result: Settings | SettingsMoveStarted) {
    if ("move" in result) {
      move = result.move;
      toast.info("Started a storage move");
    } else settings.data = result;
  }

  let testing = $state(false);
  let testResult = $state<ConnectionResult | null>(null);

  async function test(patch: Record<string, unknown>) {
    testing = true;
    testResult = null;
    try {
      const r = await api.settings.test("storage", patch);
      testResult = r.ok
        ? { ok: true, text: kind(patch as Record<string, string>) === "fs" ? "The server can write to the folder." : "The server can read and write the bucket." }
        : { ok: false, text: r.error ?? "The connection test failed." };
    } catch (e) {
      testResult = { ok: false, text: errorMessage(e) };
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

  const moveTone = { copying: "neutral", done: "success", failed: "danger", cancelled: "neutral" } as const;
  const moveWord: Record<Move["state"], string> = { copying: "Copying", done: "Done", failed: "Failed", cancelled: "Cancelled" };
  const percent = (m: Move) => (m.total > 0 ? Math.min(100, Math.round(((m.copied + m.skipped + m.failed) / m.total) * 100)) : 0);
  const targetText = (m: Move) => {
    const t = m.target;
    if (t.storage === "fs") return `To the folder ${t.fs_root ?? ""}`;
    return `To the bucket ${t.bucket ?? ""}${t.prefix ? `/${t.prefix}` : ""}${t.endpoint ? ` at ${t.endpoint}` : ""}`;
  };
</script>

<PageHeader title="Storage" description="Where the server keeps the files of every repository." />

{#if move && move.state === "copying"}
  <Section class="mb-6" title="Moving files" description={targetText(move)}>
    {#snippet actions()}<Button variant="outline" onclick={() => (confirmCancel = true)}>Cancel the move</Button>{/snippet}
    <div class="flex items-baseline justify-between gap-4 text-meta text-muted-foreground tabular-nums">
      <span>{formatCount(move.copied + move.skipped + move.failed)} of {formatCount(move.total)} files</span>
      <span>{percent(move)}%</span>
    </div>
    <div class="mt-2 h-2 w-full overflow-hidden rounded-full bg-muted" role="progressbar" aria-label="Files moved" aria-valuemin={0} aria-valuemax={100} aria-valuenow={percent(move)}>
      <div class="h-full rounded-full bg-primary transition-[width]" style="width: {percent(move)}%"></div>
    </div>
    <p class="mt-3 text-meta text-muted-foreground">Uploads keep working and go to both locations. Downloads come from the old location until the move ends.</p>
  </Section>
{:else if move && (move.state !== "done" || move.failed > 0)}
  <Section class="mb-6" title="Last storage move" description={targetText(move)}>
    {#snippet actions()}<StatusBadge tone={moveTone[move!.state]}>{moveWord[move!.state]}</StatusBadge>{/snippet}
    <dl class="grid grid-cols-2 gap-x-6 gap-y-4 sm:grid-cols-4">
      {#each [["Copied", move.copied], ["Already there", move.skipped], ["Failed", move.failed]] as [label, n] (label)}
        <div>
          <dt class="text-meta text-muted-foreground">{label}</dt>
          <dd class={cn("text-label tabular-nums", label === "Failed" && Number(n) > 0 && "text-destructive")}>{formatCount(Number(n))}</dd>
        </div>
      {/each}
      <div>
        <dt class="text-meta text-muted-foreground">Ended</dt>
        <dd class="text-label">{#if move.finished_at}<RelativeTime at={move.finished_at} />{:else}<RelativeTime at={move.started_at} />{/if}</dd>
      </div>
    </dl>
    {#if move.error}<p class="mt-4 text-meta text-destructive">{move.error}</p>{/if}
    {#if move.state !== "done"}<p class="mt-4 text-meta text-muted-foreground">The location did not change. Save the settings again to start a new move.</p>{/if}
    {#if failures}
      <div class="mt-6">
        <DataTable source={failures} caption="Files that failed to copy" key={(f) => f.blob_id} columns={[{ label: "File", grow: true }, { label: "Reason" }]}>
          {#snippet row(f)}
            <Cell grow text={f.key} copy copyLabel="Copy the path" />
            <Cell muted text={f.reason} />
          {/snippet}
        </DataTable>
      </div>
    {/if}
  </Section>
{:else if current.error}
  <Section class="mb-6"><ErrorState error={current.error} title="The storage move did not load" onRetry={current.reload} /></Section>
{/if}

{#if settings.loading && !settings.data}
  <Loading rows={6} />
{:else if settings.error && !settings.data}
  <ErrorState error={settings.error} onRetry={settings.reload} />
{:else if group}
  <FormPage>
    <SettingsGroupForm settings={group.settings} label="Storage settings" {sections} {fields} {visible} effects={moveEffects} onSaved={saved}>
      {#snippet extra(patch)}
        <ConnectionTest {testing} result={testResult} onTest={() => test(patch())} />
      {/snippet}
    </SettingsGroupForm>
  </FormPage>
{:else}
  <ErrorState error="The server sent no storage settings." onRetry={settings.reload} />
{/if}

<ConfirmDialog
  bind:open={confirmCancel}
  title="Cancel the storage move?"
  confirmLabel="Cancel the move"
  destructive
  changes={["Stops copying files", "Keeps the files already copied in the new location", "Keeps the storage location unchanged", "Sends uploads to the old location only"]}
  onConfirm={cancelMove}
/>
