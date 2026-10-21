<!--
  Storage (Storage permission): where the server keeps the files of every repository (a folder on
  this server or an S3 bucket), a connection test, and the storage move. The page is in one state
  at a time:
  - no move: the storage form;
  - copying: where the files go and where storage is now, the progress and Cancel; no form;
  - failed or cancelled: storage is still where it was, why the move ended, and Try again, Change
    the target (the form filled with the target; saving starts a new move) or Keep (put the move away);
  - done: one success line over the form.
  Saving a new location while files exist starts a move; the setting changes when the move ends.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { toast } from "svelte-sonner";
  import CircleCheckIcon from "@lucide/svelte/icons/circle-check";
  import ArrowLeftIcon from "@lucide/svelte/icons/arrow-left";
  import type { PageProps } from "$lib/router.svelte";
  import { api, errorMessage, type Move, type MoveFailure, type Setting, type Settings, type SettingsMoveStarted } from "$lib/api";
  import { Keyset, Resource } from "$lib/load.svelte";
  import { formatCount } from "$lib/format";
  import { Button } from "$lib/components/ui/button";
  import * as Alert from "$lib/components/ui/alert";
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

  let move = $state<Move | null>(null);
  $effect(() => {
    if (current.data !== undefined) move = current.data;
  });
  // The form filled with the target of an ended move, to try it again with changes.
  let changing = $state(false);

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
          if (next.state === "done") toast.success("Every file is moved");
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

  // --- where files are: the words of a location ---

  type Place = { storage: string; fs_root?: string; endpoint?: string; bucket?: string; prefix?: string };
  const folderOf = (p?: string) => (p ?? "").trim().replace(/^\/+|\/+$/g, "");
  const bucketWord = (endpoint?: string) =>
    !endpoint ? "AWS S3 bucket" : /\.r2\.cloudflarestorage\.com/i.test(endpoint) ? "Cloudflare R2 bucket" : "S3 bucket";
  const bucketName = (p: Place) => `the ${bucketWord(p.endpoint)} ${p.bucket ?? ""}${p.endpoint && bucketWord(p.endpoint) === "S3 bucket" ? ` at ${p.endpoint}` : ""}`;
  /** The store: "the folder /data/blobs on this server", "the Cloudflare R2 bucket builds". */
  const storeText = (p: Place) => (p.storage === "fs" ? `the folder ${p.fs_root ?? ""} on this server` : bucketName(p));
  /** Exactly where the files are: the store and, in a bucket, its folder or its root. */
  const placeText = (p: Place) => {
    if (p.storage === "fs") return storeText(p);
    const folder = folderOf(p.prefix);
    return folder ? `the folder ${folder}/ in ${bucketName(p)}` : `the root of ${bucketName(p)}`;
  };
  const samePlace = (a: Place, b: Place) =>
    a.storage === b.storage &&
    (a.storage === "fs"
      ? (a.fs_root ?? "") === (b.fs_root ?? "")
      : (a.endpoint ?? "") === (b.endpoint ?? "") && (a.bucket ?? "") === (b.bucket ?? "") && folderOf(a.prefix) === folderOf(b.prefix));

  // The place that settings values (by key) select.
  const placeOf = (v: (key: string) => unknown): Place => ({
    storage: String(v("storage") ?? "fs"),
    fs_root: String(v("fs.root") ?? ""),
    endpoint: String(v("s3.endpoint") ?? ""),
    bucket: String(v("s3.bucket") ?? ""),
    prefix: String(v("s3.prefix") ?? ""),
  });
  const settingOf = (key: string) => group?.settings.find((s) => s.key === key);
  const here = $derived(placeOf((k) => settingOf(k)?.value));
  /** The button that keeps storage where it is. */
  const keepLabel = $derived(here.storage === "fs" ? "Keep the folder on this server" : `Keep the bucket ${here.bucket}`);

  // --- the form ---

  const sections = [
    { title: "Location", keys: ["storage", "fs.root"] },
    { title: "Bucket", keys: ["s3.endpoint", "s3.region", "s3.bucket", "s3.prefix", "s3.access_key_id", "s3.secret_access_key", "s3.path_style", "s3.redirect", "presign_ttl"] },
  ];
  const fields = {
    storage: { label: "Storage type", help: "When you change it, the server moves the existing files in the background." },
    "fs.root": { label: "Folder", help: "A relative path starts in the data folder." },
    "s3.endpoint": { label: "Address", help: "The address of the S3 API. For Cloudflare R2: https://<account id>.r2.cloudflarestorage.com, from the S3 API part of the bucket's settings, not a custom domain of the bucket. Leave it empty for AWS: the server then uses the AWS address of the region." },
    "s3.region": { label: "Region", help: "Leave it empty for R2: the server then uses auto. AWS needs its region, for example us-east-1." },
    "s3.bucket": { label: "Bucket" },
    "s3.access_key_id": { label: "Access key ID", help: "For R2, make an API token with Object Read & Write on the bucket: Cloudflare shows its access key ID and secret access key once." },
    "s3.secret_access_key": { label: "Secret access key" },
    "s3.prefix": { label: "Folder in the bucket", help: "Leave it empty to keep the files at the root of the bucket. A server that already has files in the bucket keeps this as it is: a new folder copies every file again." },
    "s3.path_style": { label: "Path-style addresses", help: "Leave it off for R2 and AWS. MinIO and SeaweedFS need it." },
    "s3.redirect": { label: "Download from the bucket", help: "This server checks each download, then sends the build to a signed link of the bucket: the build gets the bytes from R2, not through this server. With R2, that egress costs nothing. To answer downloads near each build without this server, set up Edge delivery." },
    presign_ttl: { label: "Download link lifetime", help: "A signed link to a file in the bucket works for this time." },
  };

  // The settings of the form: the current ones, or those of the move's target to try it again.
  const formSettings = $derived.by((): Setting[] => {
    const all = group?.settings ?? [];
    const t = move?.target_settings;
    if (!changing || !t) return all;
    return all.map((s) => (s.key in t ? { ...s, value: t[s.key] } : s));
  });
  const valueIn = (list: Setting[], key: string) => list.find((s) => s.key === key)?.value;

  const kind = (draft: Record<string, Draft>) => String(draft["storage"] ?? valueIn(formSettings, "storage") ?? "fs");
  const visible = (s: Setting, draft: Record<string, Draft>) => {
    if (s.key === "storage") return true;
    if (s.key === "fs.root") return kind(draft) === "fs";
    return kind(draft) !== "fs";
  };

  // The place a PATCH body of the form would select: the form's values with the body on top
  // (null is the default).
  const nextPlace = (patch: Record<string, unknown>) =>
    placeOf((k) => {
      if (!(k in patch)) return valueIn(formSettings, k);
      const v = patch[k];
      return v === null ? formSettings.find((s) => s.key === k)?.default : v;
    });

  // A save that changes where the files are starts a move: the dialog says where they go.
  const moveEffects = (patch: Record<string, unknown>) => {
    const next = nextPlace(patch);
    if (samePlace(next, here)) return [];
    const out = [
      `Copies the files to ${placeText(next)}`,
      `Keeps storage on ${storeText(here)} until every file is copied, then switches`,
      "Keeps uploads working: each upload goes to both places until the copy ends",
    ];
    if (next.storage === "s3" && here.storage === "s3" && next.bucket === here.bucket && (next.endpoint ?? "") === (here.endpoint ?? "")) {
      out.push(`Leaves the files at ${placeText(here)} where they are: the server then uses only ${placeText(next)}`);
    }
    return out;
  };
  // A retry always asks first: it starts a move, or saves at once when storage is already there.
  const retryEffects = (patch: Record<string, unknown>) => {
    const out = moveEffects(patch);
    return out.length ? [...out, "Skips the files that are already there"] : [`Saves the settings: storage is already on ${placeText(here)}`];
  };
  const confirmText = { title: "Move the files?", label: "Start the move" };

  function saved(result: Settings | SettingsMoveStarted) {
    changing = false;
    testResult = null;
    if ("move" in result) {
      move = result.move;
      toast.info("Started moving the files");
    } else {
      move = null;
      settings.data = result;
    }
  }

  // A retry of the move with the edits of its target: a new move, or the settings when they
  // saved at once.
  async function sendRetry(patch: Record<string, unknown>): Promise<Settings | SettingsMoveStarted> {
    const r = await api.moves.retry(move!.id, patch);
    return r.move ? { move: r.move } : await api.settings.get();
  }

  let testing = $state(false);
  let testResult = $state<ConnectionResult | null>(null);

  async function test(patch: Record<string, unknown>) {
    testing = true;
    testResult = null;
    try {
      const r = changing && move ? await api.moves.test(move.id, patch) : await api.settings.test("storage", patch);
      testResult = r.ok
        ? { ok: true, text: nextPlace(patch).storage === "fs" ? "The server can write to the folder." : "The server can read and write the bucket." }
        : { ok: false, text: r.error ?? "The connection test failed." };
    } catch (e) {
      testResult = { ok: false, text: errorMessage(e) };
    } finally {
      testing = false;
    }
  }

  // --- the actions of a move ---

  let confirmCancel = $state(false);
  async function cancelMove() {
    if (!move) return;
    move = await api.moves.cancel(move.id);
    toast.success("Cancelled the move");
    settings.reload();
  }

  let confirmRetry = $state(false);
  async function retry() {
    if (!move) return;
    const r = await api.moves.retry(move.id);
    if (r.move) {
      move = r.move;
      toast.info("Started the move again");
    } else {
      move = null;
      toast.success("Saved storage settings");
    }
    settings.reload();
  }

  let keeping = $state(false);
  async function keep() {
    if (!move) return;
    keeping = true;
    try {
      await api.moves.dismiss(move.id);
      move = null;
      changing = false;
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      keeping = false;
    }
  }

  function changeTarget() {
    testResult = null;
    changing = true;
  }

  function back() {
    testResult = null;
    changing = false;
  }

  const percent = (m: Move) => (m.total > 0 ? Math.min(100, Math.round(((m.copied + m.skipped + m.failed) / m.total) * 100)) : 0);
  const files = (n: number) => `${formatCount(n)} ${n === 1 ? "file" : "files"}`;
  // Why an ended move ended, in words.
  const endText = (m: Move) => {
    if (m.state === "cancelled") return "The move was cancelled before every file was copied.";
    if (m.failed > 0) return `${files(m.failed)} could not be copied. The list is below.`;
    return m.error ? `The move stopped: ${m.error}.` : "The move stopped.";
  };
</script>

<PageHeader title="Storage" description="Where the server keeps the files of every repository." />

{#snippet formFor(list: Setting[], retrying: boolean)}
  <FormPage>
    <SettingsGroupForm
      settings={list}
      label="Storage settings"
      {sections}
      {fields}
      {visible}
      effects={retrying ? retryEffects : moveEffects}
      {confirmText}
      send={retrying ? sendRetry : undefined}
      onSaved={saved}
    >
      {#snippet extra(patch)}
        <ConnectionTest {testing} result={testResult} onTest={() => test(patch())} />
      {/snippet}
    </SettingsGroupForm>
  </FormPage>
{/snippet}

{#if (settings.loading && !settings.data) || (current.loading && current.data === undefined && !current.error)}
  <Loading rows={6} />
{:else if settings.error && !settings.data}
  <ErrorState error={settings.error} onRetry={settings.reload} />
{:else if current.error && current.data === undefined}
  <ErrorState error={current.error} title="The storage move did not load" onRetry={current.reload} />
{:else if !group}
  <ErrorState error="The server sent no storage settings." onRetry={settings.reload} />
{:else if move && move.state === "copying"}
  <Section title="Moving files">
    {#snippet actions()}<Button variant="outline" onclick={() => (confirmCancel = true)}>Cancel the move</Button>{/snippet}
    <dl class="grid gap-x-6 gap-y-4 sm:grid-cols-2">
      <div class="min-w-0">
        <dt class="text-meta text-muted-foreground">Moving to</dt>
        <dd class="mt-0.5 text-label [overflow-wrap:anywhere]">{placeText(move.target)}</dd>
      </div>
      <div class="min-w-0">
        <dt class="text-meta text-muted-foreground">Storage now</dt>
        <dd class="mt-0.5 text-label [overflow-wrap:anywhere]">{storeText(here)}</dd>
        <dd class="mt-0.5 text-meta text-muted-foreground">It stays here until every file is copied.</dd>
      </div>
    </dl>
    <div class="mt-6 flex items-baseline justify-between gap-4 text-meta text-muted-foreground tabular-nums">
      <span>{formatCount(move.copied + move.skipped + move.failed)} of {files(move.total)}</span>
      <span>{percent(move)}%</span>
    </div>
    <div class="mt-2 h-2 w-full overflow-hidden rounded-full bg-muted" role="progressbar" aria-label="Files moved" aria-valuemin={0} aria-valuemax={100} aria-valuenow={percent(move)}>
      <div class="h-full rounded-full bg-primary transition-[width]" style="width: {percent(move)}%"></div>
    </div>
    <p class="mt-3 text-meta text-muted-foreground">Uploads keep working and go to both places. Downloads come from {storeText(here)} until the move ends.</p>
  </Section>
{:else if move && move.state !== "done" && changing}
  <Section class="mb-6" title="Change the target" description="Storage stays on {storeText(here)} until a new move copies every file.">
    {#snippet actions()}<Button variant="outline" onclick={back}><ArrowLeftIcon />Back</Button>{/snippet}
    <p class="text-meta text-muted-foreground">
      The form holds the target of the move. Save to start a new move to it: files it already holds are not copied again. The secret access key stays
      the target's until you replace it.
    </p>
  </Section>
  {@render formFor(formSettings, true)}
{:else if move && move.state !== "done"}
  {@const m = move}
  <Section title={m.state === "cancelled" ? "The move was cancelled" : "The move did not finish"}>
    {#snippet actions()}<StatusBadge tone={m.state === "cancelled" ? "neutral" : "danger"}>{m.state === "cancelled" ? "Cancelled" : "Failed"}</StatusBadge>{/snippet}
    <Alert.Root variant={m.state === "failed" ? "destructive" : "default"}>
      <Alert.Title>Storage is still on {storeText(here)}. Nothing was lost.</Alert.Title>
      <Alert.Description><p>{endText(m)}</p></Alert.Description>
    </Alert.Root>
    <dl class="mt-6 grid grid-cols-2 gap-x-6 gap-y-4 sm:grid-cols-4">
      <div class="col-span-2 min-w-0 sm:col-span-4">
        <dt class="text-meta text-muted-foreground">Target</dt>
        <dd class="mt-0.5 text-label [overflow-wrap:anywhere]">{placeText(m.target)}</dd>
      </div>
      {#each [["Copied", m.copied], ["Already there", m.skipped], ["Failed", m.failed]] as [label, n] (label)}
        <div>
          <dt class="text-meta text-muted-foreground">{label}</dt>
          <dd class={cn("mt-0.5 text-label tabular-nums", label === "Failed" && Number(n) > 0 && "text-destructive")}>{formatCount(Number(n))}</dd>
        </div>
      {/each}
      <div>
        <dt class="text-meta text-muted-foreground">Ended</dt>
        <dd class="mt-0.5 text-label"><RelativeTime at={m.finished_at ?? m.started_at} /></dd>
      </div>
    </dl>
    <div class="mt-6 flex flex-wrap items-center gap-2">
      <Button onclick={() => (confirmRetry = true)}>Try again</Button>
      <Button variant="outline" onclick={changeTarget}>Change the target</Button>
      <Button variant="ghost" disabled={keeping} onclick={keep}>{keepLabel}</Button>
    </div>
    <p class="mt-3 text-meta text-muted-foreground">Try again copies only the files the target does not hold yet.</p>
    {#if failures}
      <div class="mt-6">
        <DataTable source={failures} caption="Files that could not be copied" key={(f) => f.blob_id} columns={[{ label: "File", grow: true }, { label: "Reason" }]}>
          {#snippet row(f)}
            <Cell grow text={f.key} copy copyLabel="Copy the path" />
            <Cell muted text={f.reason} />
          {/snippet}
        </DataTable>
      </div>
    {/if}
  </Section>
{:else}
  {#if move && move.state === "done"}
    <Alert.Root class="mb-6">
      <CircleCheckIcon />
      <Alert.Title>The files moved to {placeText(move.target)}</Alert.Title>
      <Alert.Description>
        <p>Storage switched <RelativeTime at={move.finished_at ?? move.started_at} />.</p>
        <div class="mt-2"><Button size="sm" variant="outline" disabled={keeping} onclick={keep}>Dismiss</Button></div>
      </Alert.Description>
    </Alert.Root>
  {/if}
  {@render formFor(group.settings, false)}
{/if}

<ConfirmDialog
  bind:open={confirmCancel}
  title="Cancel the move?"
  confirmLabel="Cancel the move"
  destructive
  changes={["Stops copying files", "Keeps the files already copied in the target", `Keeps storage on ${storeText(here)}`, "Sends uploads to that place only"]}
  onConfirm={cancelMove}
/>

{#if move}
  <ConfirmDialog
    bind:open={confirmRetry}
    title="Try the move again?"
    confirmLabel="Try again"
    changes={[
      `Copies the files to ${placeText(move.target)}`,
      "Skips the files that are already there",
      `Keeps storage on ${storeText(here)} until every file is copied, then switches`,
    ]}
    onConfirm={retry}
  />
{/if}
