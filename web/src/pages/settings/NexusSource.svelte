<!--
  Admin: one Nexus source. Connection, repository mapping, sync history with live progress, and
  the cut-over. params.id is the source id.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import type { PageProps } from "$lib/router.svelte";
  import { href, navigate } from "$lib/router.svelte";
  import { api, errorMessage, type NexusMapping, type NexusSourceDetail } from "$lib/api";
  import { Keyset, Resource } from "$lib/load.svelte";
  import { formatCount } from "$lib/format";
  import { Button } from "$lib/components/ui/button";
  import { Badge } from "$lib/components/ui/badge";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import { Spinner } from "$lib/components/ui/spinner";
  import * as Alert from "$lib/components/ui/alert";
  import * as Breadcrumb from "$lib/components/ui/breadcrumb";
  import * as Table from "$lib/components/ui/table";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import LoadingRows from "$lib/components/LoadingRows.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import KeysetTable from "$lib/components/KeysetTable.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import NexusSyncDetail from "$lib/components/NexusSyncDetail.svelte";
  import TriangleAlertIcon from "@lucide/svelte/icons/triangle-alert";

  let { params, query }: PageProps = $props();

  const id = $derived(Number(params.id));
  const source = new Resource(() => api.nexus.get(id));
  const syncs = new Keyset((p) => api.nexus.syncs(id, p));

  const mirroring = $derived(source.data?.state === "mirroring");
  const running = $derived(syncs.items.some((s) => s.state === "running"));

  // The sync whose detail is open. A running sync opens by itself.
  let shown = $state<number | null>(null);
  $effect(() => {
    if (shown === null) {
      const r = syncs.items.find((s) => s.state === "running");
      if (r) shown = r.id;
    }
  });

  function reloadAll() {
    source.reload();
    syncs.reload();
  }

  // --- connection ---
  let baseURL = $state("");
  let username = $state("");
  let password = $state("");
  let connBusy = $state(false);
  let connError = $state("");
  $effect(() => {
    if (source.data) baseURL = source.data.base_url;
  });

  async function saveConnection(e: SubmitEvent) {
    e.preventDefault();
    connError = "";
    if ((username === "") !== (password === "")) {
      connError = "Enter both the username and the password, or leave both empty to keep the saved credentials.";
      return;
    }
    connBusy = true;
    try {
      await api.nexus.update(id, {
        base_url: baseURL.trim() !== source.data?.base_url ? baseURL.trim() : undefined,
        username: username || undefined,
        password: password || undefined,
      });
      username = "";
      toast.success("Saved the connection");
      source.reload();
    } catch (err) {
      connError = errorMessage(err);
    } finally {
      password = "";
      connBusy = false;
    }
  }

  // --- mapping ---
  type Pick = { on: boolean; target: string; locked: boolean };
  let picks = $state<Record<string, Pick>>({});
  let mapBusy = $state(false);
  let mapError = $state("");
  $effect(() => {
    const d = source.data;
    if (!d) return;
    picks = Object.fromEntries(
      d.repos.map((r) => [
        r.name,
        r.target ? { on: true, target: r.target, locked: true } : { on: false, target: r.name, locked: false },
      ]),
    );
  });
  const mappingDirty = $derived(!!source.data && source.data.repos.some((r) => picks[r.name]?.on && !picks[r.name].locked));

  async function saveMapping(e: SubmitEvent) {
    e.preventDefault();
    if (!source.data) return;
    mapBusy = true;
    mapError = "";
    try {
      const maps: NexusMapping[] = source.data.repos
        .filter((r) => picks[r.name]?.on)
        .map((r) => ({ source_repo: r.name, target: picks[r.name].target.trim(), create: !picks[r.name].locked }));
      source.data = await api.nexus.setRepos(id, maps);
      toast.success("Saved the repository mapping");
    } catch (err) {
      mapError = errorMessage(err);
    } finally {
      mapBusy = false;
    }
  }

  // --- actions ---
  let actionError = $state("");
  let syncBusy = $state(false);
  async function syncNow() {
    syncBusy = true;
    actionError = "";
    try {
      const s = await api.nexus.sync(id);
      shown = s.id;
      toast.success("Started a sync");
      reloadAll();
    } catch (err) {
      actionError = errorMessage(err);
    } finally {
      syncBusy = false;
    }
  }

  let confirmCut = $state(false);
  async function cutOver() {
    const s = await api.nexus.cutOver(id);
    shown = s.id;
    toast.success("Started the cut-over");
    reloadAll();
  }

  let confirmDelete = $state(false);
  async function remove() {
    await api.nexus.delete(id);
    toast.success(`Deleted ${source.data?.name ?? "the source"}`);
    navigate("/settings/nexus");
  }

  const lastFailed = $derived(source.data?.last_sync?.failed ?? 0);
  const typeText = (r: NexusSourceDetail["repos"][number]) => [r.type, r.format].filter(Boolean).join(" · ");
</script>

{#if source.loading && !source.data}
  <LoadingRows rows={5} />
{:else if source.error && !source.data}
  <ErrorState error={source.error} onRetry={source.reload} />
{:else if source.data}
  {@const d = source.data}
  <PageHeader title={d.name}>
    {#snippet breadcrumbs()}
      <Breadcrumb.Root>
        <Breadcrumb.List>
          <Breadcrumb.Item><Breadcrumb.Link href={href("/settings/nexus")}>Nexus sources</Breadcrumb.Link></Breadcrumb.Item>
          <Breadcrumb.Separator />
          <Breadcrumb.Item><Breadcrumb.Page>{d.name}</Breadcrumb.Page></Breadcrumb.Item>
        </Breadcrumb.List>
      </Breadcrumb.Root>
    {/snippet}
    {#snippet actions()}
      {#if mirroring}
        <Button variant="outline" disabled={syncBusy || running} onclick={syncNow}>
          {#if syncBusy}<Spinner />{/if}
          Sync now
        </Button>
        <Button disabled={running} onclick={() => (confirmCut = true)}>Cut over</Button>
      {/if}
    {/snippet}
    <div class="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
      <Badge variant={mirroring ? "default" : "secondary"}>{mirroring ? "Mirroring" : "Cut over"}</Badge>
      <span class="font-mono text-[0.8125rem] break-all">{d.base_url}</span>
      <span class="text-muted-foreground">Last sync: <RelativeTime at={d.last_sync?.finished_at ?? d.last_sync?.started_at} empty="none yet" /></span>
    </div>
  </PageHeader>

  {#if actionError}<div class="mb-6"><ErrorState inline error={actionError} /></div>{/if}
  {#if d.last_error}<div class="mb-6"><ErrorState error={d.last_error} title="The last sync failed" /></div>{/if}

  {#if !mirroring}
    <Alert.Root class="mb-10">
      <TriangleAlertIcon />
      <Alert.Title>Uploads go to this server</Alert.Title>
      <Alert.Description>
        <p>The server no longer syncs from Nexus and it deleted the stored Nexus credentials.</p>
        {#if d.cutover_failed > 0}
          <p class="mt-2 text-destructive">
            {formatCount(d.cutover_failed)} files failed to import in the cut-over sync and are not on this server.
            <button type="button" class="underline underline-offset-4" onclick={() => (shown = d.cutover_sync_id)}>Show the failed files</button>
          </p>
        {/if}
      </Alert.Description>
    </Alert.Root>
  {/if}

  {#if mirroring}
    <Section narrow title="Connection" description="Change the address, or enter a new username and password. The saved password is never shown.">
      <form class="flex flex-col gap-4" onsubmit={saveConnection} autocomplete="off">
        <div class="flex flex-col gap-2">
          <Label for="src-url">Base URL</Label>
          <Input id="src-url" class="font-mono" type="url" required bind:value={baseURL} />
        </div>
        <div class="flex flex-col gap-2">
          <Label for="src-user">Username</Label>
          <Input id="src-user" class="max-w-sm" autocomplete="off" placeholder="Unchanged" bind:value={username} />
        </div>
        <div class="flex flex-col gap-2">
          <Label for="src-pass">Password</Label>
          <Input id="src-pass" class="max-w-sm" type="password" autocomplete="new-password" placeholder="Unchanged" bind:value={password} />
        </div>
        {#if connError}<ErrorState inline error={connError} />{/if}
        <div>
          <Button type="submit" disabled={connBusy}>
            {#if connBusy}<Spinner />{/if}
            Save connection
          </Button>
        </div>
      </form>
    </Section>
  {/if}

  <Section title="Nexus repositories" description="Choose the repositories to mirror and the name each gets here. A new target is read-only for uploads while it mirrors Nexus.">
    {#if d.nexus_error}
      <div class="mb-4"><ErrorState error={d.nexus_error} title="The list shows only the mapped repositories" onRetry={source.reload} /></div>
    {/if}
    {#if d.repos.length === 0}
      <EmptyState title="Nexus lists no repositories" description="Check the account's permissions on the Nexus server." />
    {:else}
      <form onsubmit={saveMapping} autocomplete="off">
        <div class="w-full overflow-x-auto rounded-2xl border">
          <Table.Root>
            <Table.Caption class="sr-only">Nexus repositories and their targets</Table.Caption>
            <Table.Header>
              <Table.Row class="hover:bg-transparent">
                <Table.Head class="w-10"><span class="sr-only">Mirror</span></Table.Head>
                <Table.Head>Nexus repository</Table.Head>
                <Table.Head>Type</Table.Head>
                <Table.Head>Target name</Table.Head>
              </Table.Row>
            </Table.Header>
            <Table.Body>
              {#each d.repos as r (r.name)}
                {@const p = picks[r.name]}
                {@const editable = mirroring && r.supported && !!p && !p.locked}
                <Table.Row class={r.supported ? "" : "text-muted-foreground"}>
                  <Table.Cell>
                    {#if p}
                      <Checkbox
                        aria-label="Mirror {r.name}"
                        disabled={!editable}
                        checked={p.on}
                        onCheckedChange={(v) => (picks[r.name].on = v === true)}
                      />
                    {/if}
                  </Table.Cell>
                  <Table.Cell class="font-mono text-[0.8125rem]">{r.name}</Table.Cell>
                  <Table.Cell class="whitespace-normal">
                    {typeText(r)}
                    {#if !r.supported}<Badge variant="outline" class="ml-1" title={r.reason}>{r.reason || "Not supported"}</Badge>{/if}
                  </Table.Cell>
                  <Table.Cell>
                    {#if p}
                      <Input
                        class="max-w-64 font-mono"
                        aria-label="Target name for {r.name}"
                        disabled={!editable || !p.on}
                        required={p.on}
                        bind:value={picks[r.name].target}
                      />
                    {/if}
                  </Table.Cell>
                </Table.Row>
              {/each}
            </Table.Body>
          </Table.Root>
        </div>
        {#if mirroring}
          {#if mapError}<div class="mt-4"><ErrorState inline error={mapError} /></div>{/if}
          <div class="mt-4">
            <Button type="submit" disabled={mapBusy || !mappingDirty}>
              {#if mapBusy}<Spinner />{/if}
              Save mapping
            </Button>
          </div>
        {/if}
      </form>
    {/if}
  </Section>

  <Section title="Syncs">
    <KeysetTable list={syncs} columns={6} caption="Syncs">
      {#snippet head()}
        <Table.Head>Started</Table.Head>
        <Table.Head>Finished</Table.Head>
        <Table.Head>State</Table.Head>
        <Table.Head class="text-right">Imported</Table.Head>
        <Table.Head class="text-right">Failed</Table.Head>
        <Table.Head class="text-right"><span class="sr-only">Details</span></Table.Head>
      {/snippet}
      {#snippet row(s)}
        <Table.Row data-state={shown === s.id ? "selected" : undefined}>
          <Table.Cell><RelativeTime at={s.started_at} /></Table.Cell>
          <Table.Cell><RelativeTime at={s.finished_at} empty="" /></Table.Cell>
          <Table.Cell class="whitespace-normal">
            <span class="capitalize">{s.state}</span>{s.cutover ? " (cut-over)" : ""}
            {#if s.error}<div class="text-sm break-words text-destructive">{s.error}</div>{/if}
          </Table.Cell>
          <Table.Cell class="text-right tabular-nums">{formatCount(s.imported)}</Table.Cell>
          <Table.Cell class="text-right tabular-nums {s.failed > 0 ? 'text-destructive' : ''}">{formatCount(s.failed)}</Table.Cell>
          <Table.Cell class="text-right">
            <Button variant="outline" size="sm" onclick={() => (shown = s.id)}>Details</Button>
          </Table.Cell>
        </Table.Row>
      {/snippet}
      {#snippet empty()}
        <EmptyState title="No sync yet" description={mirroring ? "Map at least one repository, then sync." : "This source never synced."}>
          {#if mirroring}
            <Button variant="outline" disabled={syncBusy} onclick={syncNow}>Sync now</Button>
          {/if}
        </EmptyState>
      {/snippet}
    </KeysetTable>

    {#if shown !== null}
      <div class="mt-6 rounded-2xl border p-5">
        {#key shown}
          <NexusSyncDetail id={shown} onChanged={reloadAll} />
        {/key}
      </div>
    {/if}
  </Section>

  <Section narrow tone="danger" title="Delete source" description="Mirrored repositories and their files stay on this server.">
    <Button variant="destructive" disabled={running} onclick={() => (confirmDelete = true)}>Delete source</Button>
  </Section>

  <ConfirmDialog
    bind:open={confirmCut}
    title="Cut over to this server"
    confirmLabel="Run the final sync and cut over"
    destructive
    changes={[
      "Runs a final sync of every mapped repository",
      "Opens the mirrored repositories for uploads on this server",
      "Stops syncing from Nexus",
      "Deletes the stored Nexus credentials",
      "Does not delete files that were deleted in Nexus",
      ...(lastFailed > 0 ? [`Leaves ${formatCount(lastFailed)} files that failed in the last sync off this server`] : []),
    ]}
    description="Make sure every client uploads to this server before you continue."
    onConfirm={cutOver}
  />

  <ConfirmDialog
    bind:open={confirmDelete}
    title="Delete Nexus source"
    confirmLabel="Delete {d.name}"
    destructive
    changes={[
      "Deletes the source, its sync history and its stored credentials",
      "Keeps the mirrored repositories and their files",
    ]}
    onConfirm={remove}
  />
{/if}
