<!--
  Backups (Manage backups permission): the backups in the backup folder, newest first (cards, or a
  list when there are more than 12), with Back up now; then the folder, the interval and how many to keep (the settings group "backup"), with a
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
  import ItemList from "$lib/components/ItemList.svelte";
  import SourceGrid from "$lib/components/SourceGrid.svelte";
  import ItemCard from "$lib/components/ItemCard.svelte";
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

  const files = (n: number) => `${formatCount(n)} ${n === 1 ? "file" : "files"}`;
  // What a backup holds: with a folder store the database and the files, with S3 the database.
  const holds = (b: Backup) => (b.blobs_included ? `${files(b.files)} · ${formatSize(b.size)}` : `Database · ${formatSize(b.database_size)}`);

  const stepWord ={ files: "Copying files", database: "Copying the database", cleanup: "Removing old backups" } as const;

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

<PageHeader title="Backups">
  {#snippet actions()}
    {#if dir}
      <Button onclick={backUpNow} disabled={starting || job?.running}>Back up now</Button>
    {/if}
  {/snippet}
</PageHeader>

<Section
  plain={!!dir && source.items.length <= 12}
  title="Kept backups"
  description="A backup holds the database and the master key, and with a folder store the stored files."
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
    <CompactEmpty icon={ArchiveIcon} text="Off until you set a backup folder below." />
  {:else}
    {#snippet marks(b: Backup)}
      {#if !b.complete}
        <StatusBadge tone="warning" title="This backup did not finish. The next backup removes it.">Incomplete</StatusBadge>
      {:else}
        {#if !b.master_key_included}<StatusBadge tone="warning" title="The server took its master key from TR_MASTER_KEY. Keep that key with the backup.">No master key</StatusBadge>{/if}
      {/if}
    {/snippet}
    {#snippet none()}
      <CompactEmpty icon={ArchiveIcon} text="None yet. The first backup runs within a minute, or now with Back up now." />
    {/snippet}
    <!-- A handful of backups are cards; the number kept can be large, and many are a list. -->
    {#if source.items.length > 12}
      <ItemList {source} caption="Backups" key={(b) => b.name} empty={none}>
        {#snippet leading()}<ArchiveIcon class="size-5" />{/snippet}
        {#snippet title(b)}<span title={b.name}>{formatDateTime(b.created_at)}</span>{/snippet}
        {#snippet badges(b)}{@render marks(b)}{/snippet}
        {#snippet meta(b)}<span class="tabular-nums">{b.complete ? `${holds(b)} · ` : ""}version {b.version}</span>{/snippet}
      </ItemList>
    {:else}
      <SourceGrid {source} label="Backups" key={(b) => b.name} empty={none}>
        {#snippet card(b)}
          <ItemCard title={formatDateTime(b.created_at)}>
            {#snippet icon()}<ArchiveIcon class="size-5" />{/snippet}
            {#snippet badges()}{@render marks(b)}{/snippet}
            {#if b.complete}<p class="tabular-nums">{holds(b)}</p>{/if}
            <p>Version {b.version}</p>
          </ItemCard>
        {/snippet}
      </SourceGrid>
    {/if}
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
