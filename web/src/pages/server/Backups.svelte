<!--
  Backups (Manage backups permission): the backups in the backup folder, newest first, with Back up
  now; then the folder, the interval and how many to keep (the settings group "backup"), with a
  test that the server can write to the folder. While a backup runs, the list header shows its
  progress and the page asks for it every two seconds (a Live, with its LiveStatus in that header).
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import type { PageProps } from "$lib/router.svelte";
  import { api, errorMessage, type Backup, type BackupJob, type Settings, type SettingsMoveStarted } from "$lib/api";
  import { Resource, type TableSource } from "$lib/load.svelte";
  import { Live } from "$lib/live.svelte";
  import LiveStatus from "$lib/components/LiveStatus.svelte";
  import LiveValue from "$lib/components/LiveValue.svelte";
  import { formatCount, formatDateTime, formatSize } from "$lib/format";
  import { Button } from "$lib/components/ui/button";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import FormPage from "$lib/components/FormPage.svelte";
  import Section from "$lib/components/Section.svelte";
  import DataTable from "$lib/components/DataTable.svelte";
  import Cell from "$lib/components/Cell.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import ConnectionTest, { type TestResult } from "$lib/components/ConnectionTest.svelte";
  import SettingsGroupForm from "$lib/components/SettingsGroupForm.svelte";
  import ArchiveIcon from "@lucide/svelte/icons/archive";

  let {}: PageProps = $props();

  const settings = new Resource(() => api.settings.get());
  const overview = new Resource(() => api.backups.get());
  const group = $derived(settings.data?.groups.find((g) => g.id === "backup"));
  const dir = $derived(String(group?.settings.find((s) => s.key === "backup.dir")?.value ?? ""));
  const job = $derived<BackupJob | undefined>(overview.data?.job);

  const source: TableSource<Backup> = {
    get items() {
      return overview.data?.backups ?? [];
    },
    get loading() {
      return overview.loading && !overview.data;
    },
    // While a backup runs, LiveStatus says when a refresh fails.
    get error() {
      return live.active && overview.data ? "" : overview.error;
    },
    reload: overview.reload,
  };

  // While a backup runs, ask for its progress every two seconds; say how it ended.
  const live = new Live({
    every: 2000,
    while: () => !!job?.running,
    run: async () => {
      await overview.fetch();
      const now = overview.data?.job;
      if (now && !now.running) {
        if (now.last_error) toast.error(`The backup failed: ${now.last_error}`);
        else toast.success("Backed up the server");
      }
    },
  });

  let starting = $state(false);
  async function backUpNow() {
    starting = true;
    try {
      await api.backups.run();
      toast.info("Started a backup");
      await overview.reload();
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      starting = false;
    }
  }

  const stepWord = { files: "Copying files", database: "Copying the database", cleanup: "Removing old backups" } as const;

  const fields = {
    "backup.dir": { label: "Folder", help: "An absolute path, best on another disk. Leave it empty to turn backups off.", placeholder: "/mnt/backups" },
    "backup.interval": { label: "Back up every" },
    "backup.keep": { label: "Backups to keep", help: "Older backups and the files only they use are deleted." },
  };

  function saved(result: Settings | SettingsMoveStarted) {
    if (!("move" in result)) settings.data = result;
    overview.reload();
  }

  let testing = $state(false);
  let testResult = $state<TestResult | null>(null);
  async function test(patch: Record<string, unknown>) {
    testing = true;
    testResult = null;
    try {
      const r = await api.settings.test("backup", patch);
      testResult = r.ok ? { ok: true, text: "The server can write to the folder." } : { ok: false, text: r.error ?? "The test failed." };
    } catch (e) {
      testResult = { ok: false, text: errorMessage(e) };
    } finally {
      testing = false;
    }
  }
</script>

<PageHeader title="Backups" description="Copies of this server in a folder, on a schedule.">
  {#snippet actions()}
    {#if dir}
      <Button onclick={backUpNow} disabled={starting || job?.running}>Back up now</Button>
    {/if}
  {/snippet}
</PageHeader>

<Section
  title="Backups"
  description="A backup holds the database, the master key and the stored files of a folder store; files in an S3 bucket stay in the bucket, and a key set in TR_MASTER_KEY is not written."
>
  {#snippet actions()}
    {#if job?.running}
      <LiveStatus {live} />
      <StatusBadge tone="info">
        <span class="tabular-nums"
          >{stepWord[job.progress?.step ?? "files"]}{#if job.progress?.copied}
            · <LiveValue value={job.progress.copied}>{formatCount(job.progress.copied)} copied</LiveValue>{/if}</span
        >
      </StatusBadge>
    {:else if overview.data?.next_at}
      <span class="text-meta text-muted-foreground">Next backup <RelativeTime at={overview.data.next_at} /></span>
    {/if}
  {/snippet}
  {#if !dir && !settings.loading}
    <CompactEmpty icon={ArchiveIcon} title="Backups" state="Off" text="Set a backup folder below to turn them on." />
  {:else}
    <DataTable
      {source}
      caption="Backups"
      key={(b) => b.name}
      columns={[
        { label: "Backup", grow: true },
        { label: "Version", hideOnPhone: true },
        { label: "Files", align: "right", hideOnPhone: true },
        { label: "Size", align: "right" },
      ]}
    >
      {#snippet row(b)}
        <Cell grow>
          <span class="flex min-w-0 items-center gap-2">
            <span class="truncate font-medium" title={b.name}>{formatDateTime(b.created_at)}</span>
            {#if !b.complete}
              <StatusBadge tone="warning" title="This backup did not finish. The next backup removes it.">Incomplete</StatusBadge>
            {:else}
              {#if !b.blobs_included}<StatusBadge title="The files of this backup stay in the S3 bucket.">Files in the bucket</StatusBadge>{/if}
              {#if !b.master_key_included}<StatusBadge tone="warning" title="The server took its master key from TR_MASTER_KEY. Keep that key with the backup.">No master key</StatusBadge>{/if}
            {/if}
          </span>
        </Cell>
        <Cell hideOnPhone muted text={b.version} />
        <Cell num hideOnPhone muted>{b.complete ? formatCount(b.files) : ""}</Cell>
        <Cell num>{b.complete ? formatSize(b.size) : ""}</Cell>
      {/snippet}
      {#snippet empty()}
        <CompactEmpty icon={ArchiveIcon} title="Backups" state="None yet" text="The first backup runs within a minute, or now with Back up now." />
      {/snippet}
    </DataTable>
    {#if overview.data?.error}
      <p class="mt-4 text-meta text-destructive text-pretty">{overview.data.error}</p>
    {:else if job && !job.running && job.last_error}
      <p class="mt-4 text-meta text-destructive text-pretty">The last backup failed{#if job.last_at} (<RelativeTime at={job.last_at} />){/if}: {job.last_error}</p>
    {/if}
  {/if}
</Section>

{#if settings.loading && !settings.data}
  <Loading rows={4} />
{:else if settings.error && !settings.data}
  <ErrorState error={settings.error} onRetry={settings.reload} />
{:else if group}
  <FormPage>
    <SettingsGroupForm settings={group.settings} label="Backup settings" sections={[{ title: "Schedule", keys: ["backup.dir", "backup.interval", "backup.keep"] }]} {fields} onSaved={saved}>
      {#snippet extra(patch)}
        <ConnectionTest label="Test the folder" {testing} result={testResult} onTest={() => test(patch())} />
      {/snippet}
    </SettingsGroupForm>
  </FormPage>
{:else}
  <ErrorState error="The server sent no backup settings." onRetry={settings.reload} />
{/if}
