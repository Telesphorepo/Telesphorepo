<!--
  One Nexus server: the header (its state, Sync now, Switch over, Delete), the repositories to
  copy and their names here, the syncs (a row opens its details below the table; a running sync
  opens by itself) and the connection. The new repositories to copy and the connection
  save together from the SaveBar. The parent keys it by id.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { toast } from "svelte-sonner";
  import { api, errorMessage, type NexusMapping, type NexusSourceDetail, type NexusSourceRepo, type NexusSync } from "$lib/api";
  import { Keyset, Resource, type TableSource } from "$lib/load.svelte";
  import { Draft } from "$lib/unsaved.svelte";
  import { href, navigate } from "$lib/router.svelte";
  import { formatCount } from "$lib/format";
  import { Button } from "$lib/components/ui/button";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import { Input } from "$lib/components/ui/input";
  import { Spinner } from "$lib/components/ui/spinner";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import FormPage from "$lib/components/FormPage.svelte";
  import FormSection from "$lib/components/FormSection.svelte";
  import FormGrid from "$lib/components/FormGrid.svelte";
  import FormRow from "$lib/components/FormRow.svelte";
  import DataTable from "$lib/components/DataTable.svelte";
  import Cell from "$lib/components/Cell.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import SaveBar from "$lib/components/SaveBar.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import SyncDetail from "./SyncDetail.svelte";
  import { syncState } from "./words";
  import ArrowRightLeftIcon from "@lucide/svelte/icons/arrow-right-left";
  import RefreshIcon from "@lucide/svelte/icons/refresh-cw";
  import HistoryIcon from "@lucide/svelte/icons/history";

  let { id, sync }: { id: number; sync: number | null } = $props();

  const fid = $props.id();
  const path = untrack(() => `/settings/nexus/${id}`);

  const source = new Resource(() => api.nexus.get(id));
  const syncs = new Keyset((p) => api.nexus.syncs(id, p));

  const d = $derived(source.data);
  const copying = $derived(d?.state === "mirroring");
  const running = $derived(syncs.items.some((s) => s.state === "running"));
  const shown = $derived(sync ?? syncs.items.find((s) => s.state === "running")?.id ?? null);

  function reloadAll() {
    source.reload();
    syncs.reload();
  }

  // --- the edits: new repositories to copy, and the connection ---
  type Pick = { on: boolean; target: string };
  type Value = { picks: Record<string, Pick>; base_url: string; username: string; password: string };
  const fromSource = (s: NexusSourceDetail): Value => ({
    picks: Object.fromEntries(s.repos.map((r) => [r.name, r.target ? { on: true, target: r.target } : { on: false, target: r.name }])),
    base_url: s.base_url,
    username: "",
    password: "",
  });
  let draft = $state<Draft<Value> | null>(null);
  $effect(() => {
    const data = source.data;
    if (data) untrack(() => (draft = new Draft(fromSource(data))));
  });

  const repoSource: TableSource<NexusSourceRepo> = {
    get items() {
      return source.data?.repos ?? [];
    },
    get loading() {
      return source.loading && !source.data;
    },
    error: "",
  };
  const chosen = $derived((d?.repos ?? []).filter((r) => r.target).length);

  async function save() {
    if (!draft || !d) return;
    const v = draft.value;
    const was = draft.saved;
    if ((v.username === "") !== (v.password === "")) throw new Error("Enter both the username and the password, or neither to keep the saved ones.");
    let next = d;
    if (JSON.stringify(v.picks) !== JSON.stringify(was.picks)) {
      const maps: NexusMapping[] = d.repos
        .filter((r) => v.picks[r.name]?.on)
        .map((r) => ({ source_repo: r.name, target: v.picks[r.name].target.trim(), create: !r.target }));
      next = await api.nexus.setRepos(id, maps);
    }
    if (v.base_url.trim() !== was.base_url || v.username) {
      await api.nexus.update(id, {
        base_url: v.base_url.trim() !== was.base_url ? v.base_url.trim() : undefined,
        username: v.username || undefined,
        password: v.password || undefined,
      });
      next = await api.nexus.get(id);
    }
    source.data = next;
    toast.success(`Saved ${next.name}`);
  }

  // --- syncs ---
  let syncBusy = $state(false);
  let actionError = $state("");
  async function syncNow() {
    syncBusy = true;
    actionError = "";
    try {
      const s = await api.nexus.sync(id);
      toast.success("Started a sync");
      navigate(path, { query: { sync: String(s.id) }, replace: true });
      reloadAll();
    } catch (err) {
      actionError = errorMessage(err);
    } finally {
      syncBusy = false;
    }
  }

  let switchOpen = $state(false);
  async function switchOver() {
    const s = await api.nexus.cutOver(id);
    toast.success("Started the switch-over");
    navigate(path, { query: { sync: String(s.id) }, replace: true });
    reloadAll();
  }

  let deleteOpen = $state(false);
  async function remove() {
    await api.nexus.delete(id);
    toast.success(`Deleted the Nexus server ${d?.name ?? ""}`);
    navigate("/settings/nexus", { force: true });
  }

  const lastFailed = $derived(d?.last_sync?.failed ?? 0);
  const typeText = (r: NexusSourceRepo) => [r.format, r.type].filter(Boolean).join(", ");
  const syncHref = (s: NexusSync) => href(path, { sync: String(s.id) });

  const quietDanger = "hover:bg-destructive/10 hover:text-destructive";
</script>

{#if source.loading && !d}
  <PageHeader title="Nexus server" />
  <Loading rows={6} />
{:else if source.error && !d}
  <PageHeader title="Nexus server" />
  <ErrorState error={source.error} title="The Nexus server did not load" onRetry={source.reload} />
{:else if d && draft}
  {@const v = draft.value}
  <PageHeader title={d.name} description={d.base_url}>
    {#snippet meta()}
      {#if !copying}
        <StatusBadge tone="success">Switched over</StatusBadge>
      {:else if d.last_error}
        <StatusBadge tone="danger">Sync failed</StatusBadge>
      {:else if running}
        <StatusBadge tone="info">Syncing</StatusBadge>
      {:else}
        <StatusBadge tone="info">Copying</StatusBadge>
      {/if}
    {/snippet}
    {#snippet actions()}
      {#if copying}
        <Button variant="outline" disabled={syncBusy || running} onclick={syncNow}>
          {#if syncBusy}<Spinner />{:else}<RefreshIcon />{/if}
          Sync now
        </Button>
        <Button disabled={running || chosen === 0} onclick={() => (switchOpen = true)}>Switch over</Button>
      {/if}
      <Button variant="ghost" class={quietDanger} disabled={running} onclick={() => (deleteOpen = true)}>Delete</Button>
    {/snippet}
  </PageHeader>

  <Section title="Repositories to copy" description="Choose the Nexus repositories to copy and the name each one gets here.">
    {#if d.nexus_error}
      <div class="mb-4"><ErrorState inline error="Nexus did not list its repositories, so this shows only the chosen ones: {d.nexus_error}" /></div>
    {/if}
    <DataTable
      source={repoSource}
      caption="Nexus repositories and their names here"
      key={(r) => r.name}
      columns={[{ label: "", srLabel: "Copy" }, { label: "Nexus repository", grow: true }, { label: "Kind", hideOnPhone: true }, { label: "Name here" }]}
    >
      {#snippet row(r)}
        {@const p = v.picks[r.name]}
        {@const editable = copying && r.supported && !!p && !r.target}
        {@const box = `${fid}-copy-${r.name}`}
        <Cell class="w-0">
          {#if p}<Checkbox id={box} aria-label="Copy {r.name}" disabled={!editable} checked={p.on} onCheckedChange={(c) => (p.on = c === true)} />{/if}
        </Cell>
        {#if editable}
          <!-- The name and the kind label the checkbox too, so a click anywhere left of the name field ticks it. -->
          <Cell grow strong><label for={box} class="flex h-row cursor-pointer items-center"><span class="truncate" title={r.name}>{r.name}</span></label></Cell>
          <Cell hideOnPhone muted><label for={box} class="flex h-row cursor-pointer items-center">{typeText(r)}</label></Cell>
        {:else}
          <Cell grow strong muted={!r.supported} text={r.name} />
          <Cell hideOnPhone muted>
            {#if r.supported}{typeText(r)}{:else}<StatusBadge title={r.reason}>{r.reason || "Not supported"}</StatusBadge>{/if}
          </Cell>
        {/if}
        <Cell>
          {#if r.target}
            <a class="rounded-sm text-link underline-offset-4 hover:underline" href={href(`/settings/repositories/${encodeURIComponent(r.target)}/overview`)}>{r.target}</a>
          {:else if p && editable}
            <Input class="h-8 w-48 max-sm:w-36" aria-label="Name here for {r.name}" disabled={!p.on} bind:value={p.target} />
          {:else}
            <span class="text-muted-foreground">Not copied</span>
          {/if}
        </Cell>
      {/snippet}
      {#snippet empty()}
        <CompactEmpty icon={ArrowRightLeftIcon} text="Nexus lists no repositories. Check the permissions of the account on the Nexus server." />
      {/snippet}
    </DataTable>
  </Section>

  <Section title="Syncs" description="The server syncs on its own at the interval in General.">
    {#if actionError || d.last_error || (!copying && d.cutover_failed > 0)}
      <div class="mb-4 flex flex-col gap-2">
        {#if actionError}<ErrorState inline error="The sync did not start: {actionError}" />{/if}
        {#if d.last_error}<ErrorState inline error={d.last_error} />{/if}
        {#if !copying && d.cutover_failed > 0}
          <p class="text-meta text-muted-foreground">
            {formatCount(d.cutover_failed)} files failed in the final sync and are not on this server.
            <a class="text-link underline-offset-4 hover:underline" href={href(path, { sync: String(d.cutover_sync_id) })}>Show them</a>
          </p>
        {/if}
      </div>
    {/if}
    <DataTable
      source={syncs}
      caption="Syncs"
      key={(s) => s.id}
      href={syncHref}
      selected={(s) => s.id === shown}
      columns={[
        { label: "Started", grow: true },
        { label: "State" },
        { label: "Copied", align: "right" },
        { label: "Failed", align: "right" },
      ]}
    >
      {#snippet row(s)}
        <Cell grow>
          <a href={syncHref(s)} class="rounded-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"><RelativeTime at={s.started_at} /></a>
          {#if s.cutover}<span class="text-muted-foreground"> · final sync</span>{/if}
        </Cell>
        <Cell><StatusBadge tone={syncState[s.state].tone}>{syncState[s.state].word}</StatusBadge></Cell>
        <Cell num>{formatCount(s.imported)}</Cell>
        <Cell num class={s.failed > 0 ? "text-destructive" : "text-muted-foreground"}>{formatCount(s.failed)}</Cell>
      {/snippet}
      {#snippet empty()}
        <CompactEmpty icon={HistoryIcon} text={copying ? "Choose a repository above and save, then sync." : "This Nexus server never synced."}>
          {#if copying}<Button variant="outline" disabled={syncBusy || chosen === 0} onclick={syncNow}>Sync now</Button>{/if}
        </CompactEmpty>
      {/snippet}
    </DataTable>
  </Section>

  {#if shown !== null}
    {#key shown}
      <SyncDetail id={shown} onClose={() => navigate(path, { replace: true })} onChanged={reloadAll} />
    {/key}
  {/if}

  <FormPage>
    {#if copying}
      <FormSection title="Connection">
        <FormGrid>
          <FormRow label="Address" for="{fid}-url">
            <Input id="{fid}-url" type="url" bind:value={v.base_url} />
          </FormRow>
          <FormRow label="Username" for="{fid}-user" help="Leave it empty to keep the saved account.">
            <Input id="{fid}-user" autocomplete="off" placeholder="Unchanged" aria-describedby="{fid}-user-help" bind:value={v.username} />
          </FormRow>
          <FormRow label="Password" for="{fid}-pass" help="A new username needs its password.">
            <Input id="{fid}-pass" type="password" autocomplete="new-password" placeholder="Unchanged" aria-describedby="{fid}-pass-help" bind:value={v.password} />
          </FormRow>
        </FormGrid>
      </FormSection>
    {/if}

  </FormPage>

  <SaveBar count={draft.count} onDiscard={draft.reset} onSave={save} />

  <ConfirmDialog
    bind:open={switchOpen}
    title="Switch over from {d.name}?"
    confirmLabel="Run the final sync and switch over"
    destructive
    changes={[
      "Make sure every build uses this server before you continue",
      "Runs a final sync of every chosen repository",
      "Allows uploads to the copied repositories on this server",
      "Stops copying from Nexus, and deletes the saved Nexus password",
      "Keeps files here that were deleted in Nexus",
      ...(lastFailed > 0 ? [`Leaves out ${formatCount(lastFailed)} files that failed in the last sync`] : []),
    ]}
    onConfirm={switchOver}
  />

  <ConfirmDialog
    bind:open={deleteOpen}
    title="Delete the Nexus server {d.name}?"
    confirmLabel="Delete Nexus server"
    destructive
    changes={["Deletes the Nexus server, its sync history and its saved password", "Keeps the copied repositories and their files"]}
    onConfirm={remove}
  />
{/if}
