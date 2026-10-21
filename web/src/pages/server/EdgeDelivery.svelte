<!--
  Edge delivery (Manage edge delivery permission): which Cloudflare R2 regions serve downloads, the
  settings of the edge, and the steps that deploy the Worker: the download of the Worker folder
  (with the generated wrangler.jsonc in it) and the commands to run there; the wrangler.jsonc for a
  checkout of the repository behind a disclosure.
  One Draft holds the home region, the regions with their buckets, the on switch and the access
  lifetime; Save checks every bucket first (the Test of the settings group), asks before the server
  stops writing to a bucket, and then saves. While a region fills, the page asks for progress and
  says what the region waits for, with the cadence of the count and of the Worker's gap check (from
  the status). Check now makes the deployed Worker run its gap check at once (POST
  /api/edge/check); the page runs it by itself when it opens and every 30 seconds while the Worker
  has not reported this server's version. One Live does both, with its LiveStatus in the header.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import DownloadIcon from "@lucide/svelte/icons/download";
  import RefreshCwIcon from "@lucide/svelte/icons/refresh-cw";
  import type { PageProps } from "$lib/router.svelte";
  import {
    api,
    ApiError,
    errorMessage,
    type EdgeCheck,
    type EdgeHint,
    type EdgeRegionState,
    type EdgeRegionStatus,
    type EdgeStatus,
    type Setting,
    type SettingsPatch,
  } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import { Draft } from "$lib/unsaved.svelte";
  import { formatCount, formatEvery } from "$lib/format";
  import { Live } from "$lib/live.svelte";
  import LiveStatus from "$lib/components/LiveStatus.svelte";
  import LiveValue from "$lib/components/LiveValue.svelte";
  import Cadence from "$lib/components/Cadence.svelte";
  import { DURATION_UNITS, splitDuration } from "$lib/units";
  import { Button } from "$lib/components/ui/button";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import { Input } from "$lib/components/ui/input";
  import * as Select from "$lib/components/ui/select";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import FormPage from "$lib/components/FormPage.svelte";
  import FormSection from "$lib/components/FormSection.svelte";
  import FormGrid from "$lib/components/FormGrid.svelte";
  import FormRow from "$lib/components/FormRow.svelte";
  import SwitchRow from "$lib/components/SwitchRow.svelte";
  import UnitInput from "$lib/components/UnitInput.svelte";
  import Section from "$lib/components/Section.svelte";
  import SaveBar from "$lib/components/SaveBar.svelte";
  import StatusBadge, { type Tone } from "$lib/components/StatusBadge.svelte";
  import DataTable from "$lib/components/DataTable.svelte";
  import Cell from "$lib/components/Cell.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import CodeBlock from "$lib/components/CodeBlock.svelte";
  import CodeSnippet from "$lib/components/CodeSnippet.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import LearnMore from "$lib/components/LearnMore.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import SpeedTest from "./edge/SpeedTest.svelte";

  let {}: PageProps = $props();

  // The six R2 location hints, with the area each one names.
  const AREAS: { hint: EdgeHint; area: string }[] = [
    { hint: "wnam", area: "Western North America" },
    { hint: "enam", area: "Eastern North America" },
    { hint: "weur", area: "Western Europe" },
    { hint: "eeur", area: "Eastern Europe" },
    { hint: "apac", area: "Asia-Pacific" },
    { hint: "oc", area: "Oceania" },
  ];
  const areaText = (hint: string) => {
    const a = AREAS.find((x) => x.hint === hint);
    return a ? `${a.area} (${a.hint})` : hint;
  };

  const status = new Resource(() => api.edge.status());
  const settings = new Resource(() => api.settings.get());
  const wrangler = new Resource(() => api.edge.wranglerConfig());

  const group = $derived(settings.data?.groups.find((g) => g.id === "edge"));
  const setting = (key: string): Setting | undefined => group?.settings.find((s) => s.key === key);
  const homeBucket = $derived(
    status.data?.home.bucket || String(settings.data?.groups.find((g) => g.id === "storage")?.settings.find((s) => s.key === "s3.bucket")?.value ?? ""),
  );
  const bucketFor = (hint: string) => (homeBucket ? `${homeBucket}-${hint}` : "");

  type RegionEdit = { on: boolean; bucket: string };
  type Form = {
    enabled: boolean;
    home: string;
    ttl: { num: string; unit: string };
    regions: Record<string, RegionEdit>;
  };

  // The form that the saved settings describe.
  function formOf(): Form {
    const regions: Record<string, RegionEdit> = {};
    for (const { hint } of AREAS) regions[hint] = { on: false, bucket: bucketFor(hint) };
    const saved = setting("edge.regions")?.value;
    for (const entry of Array.isArray(saved) ? (saved as string[]) : []) {
      const i = entry.indexOf("=");
      if (i > 0) regions[entry.slice(0, i)] = { on: true, bucket: entry.slice(i + 1) };
    }
    const ttl = splitDuration(String(setting("edge.access_ttl")?.value ?? "30s"));
    return {
      enabled: setting("edge.enabled")?.value === true,
      home: String(setting("edge.home_hint")?.value ?? ""),
      ttl: ttl.num === "" ? { num: "30", unit: "s" } : ttl,
      regions,
    };
  }

  let draft = $state<Draft<Form> | null>(null);
  $effect(() => {
    if (group && !draft && homeBucket !== undefined) draft = new Draft(formOf());
  });

  const locked = (key: string) => setting(key)?.locked === true;
  const lockHelp = (key: string) => {
    const s = setting(key);
    return s?.locked ? `The environment variable ${s.env} sets this value. Remove it to change the setting here.` : undefined;
  };

  const statusOf = (hint: string): EdgeRegionStatus | undefined => status.data?.regions.find((r) => r.hint === hint);
  const stateWord: Record<EdgeRegionState, string> = { not_used: "Not used", filling: "Filling", ready: "Ready", waiting: "Waiting for a deploy" };
  const stateTone: Record<EdgeRegionState, Tone> = { not_used: "neutral", filling: "info", ready: "success", waiting: "warning" };

  // Chosen regions are listed as "hint=bucket", in the order of the table.
  const regionList = (f: Form) =>
    AREAS.filter((a) => a.hint !== f.home && f.regions[a.hint].on).map((a) => `${a.hint}=${f.regions[a.hint].bucket.trim()}`);

  function patchOf(d: Draft<Form>): SettingsPatch {
    const patch: SettingsPatch = {};
    for (const key of d.changed) {
      if (key === "enabled") patch["edge.enabled"] = d.value.enabled;
      else if (key === "home") patch["edge.home_hint"] = d.value.home;
      else if (key === "ttl") patch["edge.access_ttl"] = `${d.value.ttl.num.trim() || "0"}${d.value.ttl.unit}`;
      else if (key === "regions") patch["edge.regions"] = regionList(d.value);
    }
    // The home region is not a regional bucket, so a change of it changes the list too.
    if ("edge.home_hint" in patch) patch["edge.regions"] = regionList(d.value);
    return patch;
  }

  // Buckets whose name the failed check mentions get its text on their row.
  let rowErrors = $state<Record<string, string>>({});
  let confirming = $state(false);
  let provisionNeeded = $state(false);

  const savedHints = $derived(
    (Array.isArray(setting("edge.regions")?.value) ? (setting("edge.regions")!.value as string[]) : []).map((e) => e.split("=")[0]),
  );
  const stopped = $derived.by(() => {
    if (!draft) return [];
    const d = draft;
    const keep = new Set(regionList(d.value).map((e) => e.split("=")[0]));
    return savedHints
      .filter((h) => !keep.has(h))
      .map((h) => ({ hint: h, bucket: statusOf(h)?.bucket || d.saved.regions[h]?.bucket || bucketFor(h) }));
  });

  async function save() {
    if (!draft) return;
    rowErrors = {};
    const patch = patchOf(draft);
    if (Object.keys(patch).length === 0) return draft.reset();
    const t = await api.settings.test("edge", patch);
    if (!t.ok) {
      const message = t.error ?? "The check of the buckets failed.";
      for (const { hint } of AREAS) {
        const bucket = draft.value.regions[hint].bucket.trim();
        if (draft.value.regions[hint].on && bucket && message.includes(bucket)) rowErrors[hint] = message;
      }
      throw new Error(message);
    }
    if (stopped.length > 0) {
      confirming = true;
      return;
    }
    await apply();
  }

  async function apply() {
    if (!draft) return;
    const patch = patchOf(draft);
    try {
      await api.settings.patch(patch);
    } catch (e) {
      throw new Error(errorMessage(e));
    }
    if ("edge.regions" in patch || "edge.home_hint" in patch) provisionNeeded = true;
    toast.success("Saved the edge delivery settings");
    await Promise.all([settings.reload(), status.reload(), wrangler.reload()]);
    draft = new Draft(formOf());
  }

  function discard() {
    rowErrors = {};
    draft?.reset();
  }

  const filling = $derived((status.data?.regions ?? []).filter((r) => r.state === "filling"));

  const fillingSource = {
    get items() {
      return filling;
    },
    loading: false,
    error: "",
    reload: () => status.reload(),
  };
  const regionsSource = {
    get items() {
      return AREAS;
    },
    loading: false,
    error: "",
    reload: () => status.reload(),
  };

  const needsDeploy = $derived(provisionNeeded || status.data?.deploy_needed === true);
  const mismatch = $derived(status.data?.version_mismatch === true);
  // Edge delivery turns on only once a deployed Worker has reported its version; turning it off
  // always works.
  const noWorker = $derived(!!status.data && !status.data.worker_version && !draft?.saved.enabled);

  // Check now: the deployed Worker runs its gap check at once and reports its version. The server
  // runs one check at a time, at most one every 10 seconds, and keeps what the last one found.
  let checking = $state(false);
  let checkError = $state("");
  async function checkNow(manual: boolean) {
    if (checking) return;
    checking = true;
    try {
      await api.edge.check();
      checkError = "";
    } catch (e) {
      // A check of another tab or person runs or ran just now: its result shows below.
      if (!(e instanceof ApiError && e.status === 429)) checkError = errorMessage(e);
      else if (manual) toast.info(errorMessage(e));
    } finally {
      checking = false;
      await status.reload();
    }
  }

  // One Live keeps the page current. While a region fills, it asks for the status every 3 seconds.
  // While the Worker has not reported this server's version, it checks the Worker when the page
  // opens and every 30 seconds, so the version shows within seconds of a deploy.
  const FILL_EVERY = 3000;
  const CHECK_EVERY = 30_000;
  const autoCheck = $derived(!!status.data && status.data.check_unfit === "" && (!status.data.worker_version || status.data.version_mismatch));
  let autoAt = 0;
  const live = new Live({
    now: true,
    while: () => filling.length > 0 || autoCheck,
    every: () => (filling.length > 0 ? FILL_EVERY : CHECK_EVERY),
    run: async () => {
      if (autoCheck && Date.now() - autoAt >= CHECK_EVERY - FILL_EVERY) {
        autoAt = Date.now();
        await checkNow(false);
        if (checkError || status.error) throw new Error(checkError || status.error);
      } else await status.fetch();
    },
  });
  const liveCadence = $derived(
    filling.length > 0
      ? `Refreshes every ${formatEvery(FILL_EVERY / 1000)} while a region fills`
      : `Checks the Worker every ${formatEvery(CHECK_EVERY / 1000)} until it reports this server's version`,
  );
  // The regions whose files the server counts, and the oldest of their last counts (null while one
  // has none yet).
  const counting = $derived(filling.filter((r) => r.pointers_done));
  const countedAt = $derived.by(() => {
    const times = counting.map((r) => r.blobs_checked_at);
    if (times.length === 0 || times.some((t) => t === null)) return null;
    return (times as string[]).reduce((a, b) => (Date.parse(a) < Date.parse(b) ? a : b));
  });
  const copying = $derived(!!status.data && filling.some((r) => waitsFor(status.data!, r).copying));

  const checkWord: Record<EdgeCheck["outcome"], string> = {
    worker: "Worker answered",
    refused: "Worker refused",
    server: "Reached the server",
    other: "Blocked",
    unreachable: "No answer",
    error: "Failed",
  };
  const checkTone = (c: EdgeCheck): Tone => (c.ok ? "success" : c.outcome === "unreachable" || c.outcome === "error" ? "danger" : "warning");

  // What a filling region waits for, in words. "copying" is the state where the Worker's gap check
  // copies the missing files; the row then shows the count, when it last changed and the pass.
  function waitsFor(st: EdgeStatus, r: EdgeRegionStatus): { text: string; copying: boolean } {
    const say = (text: string) => ({ text, copying: false });
    if (st.mode !== "on") {
      return say("The Worker copies files only while edge delivery is on, so the region stays at its files missing until you turn it on.");
    }
    if (!r.pointers_done) {
      return say(r.outbox > 0 ? `Writing pointers: ${formatCount(r.outbox)} waiting.` : "Queueing pointers.");
    }
    if (!st.worker_version) return say("Waiting for the Worker's first gap check, which copies the files.");
    if (st.version_mismatch) return say("Waiting for the Worker of this server version. Deploy it to copy the files.");
    if (r.blobs_missing === null) return say("Counting the files in the region.");
    if (r.blobs_missing > 0) {
      return { text: "Copying files: the Worker copies the missing files from the home bucket at each gap check, or at Check now.", copying: true };
    }
    return say("Every file is there. The region turns ready at the next count.");
  }
</script>

<PageHeader title="Edge delivery" description="Serve downloads from Cloudflare R2 buckets near your clients.">
  <!-- Before the first answer the page's own error state speaks; LiveStatus reports later refreshes. -->
  {#snippet actions()}{#if status.data}<LiveStatus {live} cadence={liveCadence} />{/if}{/snippet}
</PageHeader>

{#if (status.loading && !status.data) || (settings.loading && !settings.data) || !draft}
  {#if status.error && !status.data}
    <ErrorState error={status.error} title="The edge status did not load" onRetry={status.reload} />
  {:else if settings.error && !settings.data}
    <ErrorState error={settings.error} onRetry={settings.reload} />
  {:else}
    <Loading rows={8} />
  {/if}
{:else}
  {@const st = status.data}
  <FormPage>
    {#if st}
      <Section title="Status">
        {#snippet actions()}
          <Button
            variant="outline"
            size="sm"
            disabled={checking || st.check_unfit !== ""}
            title={st.check_unfit || "Make the deployed Worker run its gap check now"}
            onclick={() => checkNow(true)}
          >
            <RefreshCwIcon aria-hidden="true" class={checking ? "animate-spin" : ""} />{checking ? "Checking" : "Check now"}
          </Button>
        {/snippet}
        <dl class="grid grid-cols-2 gap-x-6 gap-y-4 sm:grid-cols-3">
          <div>
            <dt class="text-meta text-muted-foreground">Edge delivery</dt>
            <dd class="text-label">
              {#if st.mode === "on"}
                <StatusBadge tone="success">On</StatusBadge>
              {:else if st.mode === "draining"}
                <StatusBadge tone="warning">Stopping</StatusBadge>
              {:else}
                <StatusBadge tone="neutral">Off</StatusBadge>
              {/if}
            </dd>
            {#if st.unfit}<p class="mt-1 text-meta text-muted-foreground">{st.unfit}</p>{/if}
          </div>
          <div>
            <dt class="text-meta text-muted-foreground">Worker version</dt>
            <dd class="flex flex-wrap items-center gap-2 text-label">
              {st.worker_version ?? "Not seen yet"}
              {#if mismatch}<StatusBadge tone="warning">Server is {st.server_version}</StatusBadge>{/if}
            </dd>
            {#if !st.worker_version}
              <p class="mt-1 text-meta text-muted-foreground">
                The Worker reports at its first gap check.{st.check_unfit === ""
                  ? ` This page checks it every ${formatEvery(CHECK_EVERY / 1000)} until then; select Check now to check at once.`
                  : ""}
              </p>
            {/if}
          </div>
          <div>
            <dt class="text-meta text-muted-foreground">Last gap check</dt>
            <dd class="text-label"><RelativeTime at={st.gap_checked_at} empty="Not yet" /></dd>
            <Cadence class="mt-1" what="The Worker checks" noun="check" every={st.gap_check_every} last={st.gap_checked_at} />
          </div>
          <div>
            <dt class="text-meta text-muted-foreground">Home bucket</dt>
            <dd class="text-label">{st.home.bucket || "Not set"}</dd>
          </div>
          <div class="col-span-2">
            <dt class="text-meta text-muted-foreground">Last check of the Worker</dt>
            {#if checkError}
              <dd class="text-body text-destructive">{checkError}</dd>
            {:else if st.last_check}
              {@const c = st.last_check}
              <dd class="flex flex-col gap-1">
                <span class="flex flex-wrap items-center gap-2 text-label">
                  <StatusBadge tone={checkTone(c)}>{checkWord[c.outcome]}</StatusBadge>
                  <span class="text-meta text-muted-foreground"><RelativeTime at={c.at} /></span>
                </span>
                <span class="text-body">{c.message}</span>
              </dd>
            {:else}
              <dd class="text-label">{checking ? "Checking" : "Not yet"}</dd>
              {#if st.check_unfit}<p class="mt-1 text-meta text-muted-foreground">{st.check_unfit}</p>{/if}
            {/if}
          </div>
        </dl>
      </Section>
    {/if}

    <Section title="Regions" description="The home bucket is the S3 storage of the server. Each region you add holds a copy of every file.">
      <div class="flex flex-col gap-6">
        <FormGrid>
          <FormRow label="Home region" for="edge-home" help="Where the home bucket lives. Files that can change (metadata, tags, raw files) are read from here.">
            <Select.Root
              type="single"
              disabled={locked("edge.home_hint")}
              value={draft.value.home}
              onValueChange={(v) => (draft!.value.home = v)}
            >
              <Select.Trigger id="edge-home" class="w-full" aria-describedby="edge-home-help">
                <span class="truncate">{draft.value.home ? areaText(draft.value.home) : "Choose a region"}</span>
              </Select.Trigger>
              <Select.Content>
                {#each AREAS as a (a.hint)}
                  <Select.Item value={a.hint} label={areaText(a.hint)}>{areaText(a.hint)}</Select.Item>
                {/each}
              </Select.Content>
            </Select.Root>
          </FormRow>
        </FormGrid>
        {#if lockHelp("edge.home_hint") || lockHelp("edge.regions")}
          <p class="text-meta text-muted-foreground">{lockHelp("edge.home_hint") ?? lockHelp("edge.regions")}</p>
        {/if}

        <DataTable
          source={regionsSource}
          caption="Regions"
          key={(a) => a.hint}
          columns={[
            { label: "Region", grow: true },
            { label: "Serve from it", srLabel: "Serve from this region" },
            { label: "Bucket", grow: true },
            { label: "State" },
            { label: "Pointers waiting", align: "right", hideOnPhone: true },
            { label: "Last error", grow: true, hideOnPhone: true },
          ]}
        >
          {#snippet row(a)}
            {@const isHome = a.hint === draft!.value.home}
            {@const r = statusOf(a.hint)}
            {@const edit = draft!.value.regions[a.hint]}
            <Cell grow strong text={areaText(a.hint)} />
            <Cell>
              {#if isHome}
                <span class="text-muted-foreground">Home bucket</span>
              {:else}
                <Checkbox
                  id="edge-region-{a.hint}"
                  aria-label="Serve from {areaText(a.hint)}"
                  disabled={locked("edge.regions")}
                  checked={edit.on}
                  onCheckedChange={(on) => {
                    edit.on = on === true;
                    if (edit.on && !edit.bucket.trim()) edit.bucket = bucketFor(a.hint);
                  }}
                />
              {/if}
            </Cell>
            <Cell grow>
              {#if isHome}
                <span class="block truncate">{st?.home.bucket || homeBucket}</span>
              {:else}
                <Input
                  id="edge-bucket-{a.hint}"
                  aria-label="Bucket for {areaText(a.hint)}"
                  class="h-8"
                  disabled={!edit.on || locked("edge.regions")}
                  bind:value={edit.bucket}
                  aria-invalid={rowErrors[a.hint] ? true : undefined}
                />
              {/if}
            </Cell>
            <Cell>
              {#if isHome}
                <StatusBadge tone={st?.home.last_error ? "danger" : "success"}>{st?.home.last_error ? "Error" : "Ready"}</StatusBadge>
              {:else if r}
                <StatusBadge tone={stateTone[r.state]}>{st && r.state === "filling" && waitsFor(st, r).copying ? "Copying files" : stateWord[r.state]}</StatusBadge>
              {:else}
                <span class="text-muted-foreground">{stateWord.not_used}</span>
              {/if}
            </Cell>
            <Cell num hideOnPhone>{formatCount(isHome ? (st?.home.pointers ?? 0) : (r?.outbox ?? 0))}</Cell>
            <Cell grow hideOnPhone>
              {#if rowErrors[a.hint]}
                <span class="block truncate text-destructive" title={rowErrors[a.hint]}>{rowErrors[a.hint]}</span>
              {:else}
                {@const err = isHome ? st?.home.last_error : r?.last_error}
                <span class="block truncate text-muted-foreground" title={err}>{err ?? ""}</span>
              {/if}
            </Cell>
          {/snippet}
        </DataTable>
      </div>
    </Section>

    {#if filling.length > 0}
      <Section
        title="Backfill"
        description="A region serves once the server has written its pointers and the Worker's gap check has copied its files."
      >
        <DataTable
          source={fillingSource}
          caption="Regions that fill"
          key={(r) => r.hint}
          columns={[
            { label: "Region", grow: true },
            { label: "Pointers written", align: "right" },
            { label: "Files missing", align: "right" },
            { label: "Waiting for", grow: true },
          ]}
        >
          {#snippet row(r)}
            <Cell grow strong text={areaText(r.hint)} />
            <Cell num><LiveValue value={r.pointers_written}>{formatCount(r.pointers_written)}</LiveValue> of {formatCount(r.pointers_total)}</Cell>
            <Cell num>{#if r.blobs_missing === null}Not counted yet{:else}<LiveValue value={r.blobs_missing}>{formatCount(r.blobs_missing)}</LiveValue>{/if}</Cell>
            <Cell grow>
              {#if st}
                {@const w = waitsFor(st, r)}
                <span class="flex flex-col gap-0.5">
                  <span>{w.text}</span>
                  {#if w.copying}
                    <span class="text-meta text-muted-foreground">
                      {formatCount(r.blobs_missing ?? 0)} missing{#if r.blobs_missing_since}, last changed <RelativeTime
                          at={r.blobs_missing_since}
                        />{/if}.
                      {#if st.gap_pass_started}Copy pass started <RelativeTime at={st.gap_pass_started} />.{:else if r.copies_queued !== null}Its last
                        copy pass queued {formatCount(r.copies_queued)} copies.{/if}
                    </span>
                  {/if}
                </span>
              {/if}
            </Cell>
          {/snippet}
          {#snippet empty()}<CompactEmpty title="Backfill" state="Done" text="Every region has its pointers." />{/snippet}
        </DataTable>
        {#if st && (counting.length > 0 || copying)}
          <div class="mt-4 flex flex-col gap-1">
            {#if counting.length > 0}
              <Cadence what="The server counts the missing files of a region" noun="count" every={st.blob_check_every} last={countedAt} />
            {/if}
            {#if copying}
              <Cadence what="The Worker copies them at its gap check" noun="check" every={st.gap_check_every} last={st.gap_checked_at} />
            {/if}
          </div>
        {/if}
      </Section>
    {/if}

    {#if st}<SpeedTest status={st} />{/if}

    <FormSection title="Settings">
      <FormGrid>
        <SwitchRow
          id="edge-enabled"
          label="Edge delivery"
          help={lockHelp("edge.enabled") ??
            (noWorker
              ? `Deploy the Worker first (Deploy, below). You can turn this on once the Worker has reported its version: this page checks every ${formatEvery(CHECK_EVERY / 1000)}.`
              : "The Worker answers downloads that it can prove the server would answer the same way.")}
          disabled={locked("edge.enabled") || noWorker}
          bind:checked={draft.value.enabled}
        />
        <FormRow label="Access lifetime" for="edge-ttl" help="How long a Worker keeps a token, a grant or a repository's visibility before it asks again. 0 asks every time.">
          <UnitInput
            id="edge-ttl"
            bind:num={draft.value.ttl.num}
            bind:unit={draft.value.ttl.unit}
            units={DURATION_UNITS}
            label="Access lifetime"
            disabled={locked("edge.access_ttl")}
            describedby="edge-ttl-help"
          />
        </FormRow>
      </FormGrid>
    </FormSection>

    <Section title="Deploy" description="Deploy the edge Worker to your Cloudflare account from a computer with bun.">
      {#snippet actions()}
        {#if needsDeploy || mismatch}<StatusBadge tone="warning">Deploy needed</StatusBadge>{/if}
      {/snippet}
      <div class="flex flex-col gap-6">
        {#if mismatch && st}
          <p class="text-body">
            Download the Worker of this server version ({st.server_version}) and deploy it. Until then the Worker sends every request to the server.
          </p>
        {:else if needsDeploy}
          <p class="text-body">The regions or the storage location changed. Download the Worker again, then run provision and deploy.</p>
        {/if}
        <ol class="flex flex-col gap-5">
          <li class="flex gap-3">
            <span class="w-4 shrink-0 text-section text-muted-foreground tabular-nums">1</span>
            <div class="flex min-w-0 grow flex-col gap-2">
              <p class="text-label">Download the edge Worker</p>
              {#if wrangler.error && !wrangler.data}
                <ErrorState error={wrangler.error} title="The Worker configuration did not load" onRetry={wrangler.reload} inline />
              {:else if st?.worker_missing}
                <p class="text-meta text-muted-foreground">{st.worker_missing}</p>
              {:else if wrangler.data}
                <div>
                  <Button variant="outline" size="sm" href={api.edge.workerURL} download>
                    <DownloadIcon aria-hidden="true" />Download the edge Worker
                  </Button>
                </div>
                <p class="text-meta text-muted-foreground">A zip of the Worker of this server version, with the wrangler.jsonc of these settings.</p>
              {:else}
                <Loading inline />
              {/if}
            </div>
          </li>
          <li class="flex gap-3">
            <span class="w-4 shrink-0 text-section text-muted-foreground tabular-nums">2</span>
            <div class="flex min-w-0 grow flex-col gap-2">
              <p class="text-label">Unpack it, and open a terminal in the telesphorepo-edge folder</p>
            </div>
          </li>
          <li class="flex gap-3">
            <span class="w-4 shrink-0 text-section text-muted-foreground tabular-nums">3</span>
            <div class="flex min-w-0 grow flex-col gap-2">
              <CodeSnippet label="Install wrangler" value="bun install" />
            </div>
          </li>
          <li class="flex gap-3">
            <span class="w-4 shrink-0 text-section text-muted-foreground tabular-nums">4</span>
            <div class="flex min-w-0 grow flex-col gap-2">
              <CodeSnippet label="Sign in to Cloudflare" value="bunx wrangler login" />
              <p class="text-meta text-muted-foreground">
                In CI, set CLOUDFLARE_API_TOKEN instead, with edit access to Workers Scripts, Workers Routes, R2 and Queues.
              </p>
            </div>
          </li>
          <li class="flex gap-3">
            <span class="w-4 shrink-0 text-section text-muted-foreground tabular-nums">5</span>
            <div class="flex min-w-0 grow flex-col gap-2">
              <CodeSnippet label="Create the regional buckets, the queue and the notification rule" value="bun run provision" />
              <p class="text-meta text-muted-foreground">If your login reaches several Cloudflare accounts, set CLOUDFLARE_ACCOUNT_ID to the account of the buckets first.</p>
            </div>
          </li>
          <li class="flex gap-3">
            <span class="w-4 shrink-0 text-section text-muted-foreground tabular-nums">6</span>
            <div class="flex min-w-0 grow flex-col gap-2">
              <CodeSnippet label="Deploy the Worker" value="bun run deploy" />
            </div>
          </li>
          <li class="flex gap-3">
            <span class="w-4 shrink-0 text-section text-muted-foreground tabular-nums">7</span>
            <div class="flex min-w-0 grow flex-col gap-2">
              <p class="text-label">Turn on Edge delivery under Settings above, and save</p>
              <p class="text-meta text-muted-foreground">
                Only after the deploy: the switch turns on once the Worker has reported its version. This page checks every
                {formatEvery(CHECK_EVERY / 1000)}; select Check now under Status to check at once.
              </p>
            </div>
          </li>
        </ol>
        <p class="text-meta text-muted-foreground">
          After a region change, download the Worker again and run provision and deploy. For each server release, download the Worker again and run deploy: the download holds the Worker of this server version.
        </p>
        {#if wrangler.data}
          <LearnMore label="Deploy from a checkout of the repository">
            <div class="flex flex-col gap-4">
              <p>Save this wrangler.jsonc in the edge folder of the checkout, then run the same commands there. Deploy bundles the Worker from its sources.</p>
              <CodeBlock title="wrangler.jsonc" code={wrangler.data} />
              <div>
                <Button variant="outline" size="sm" href={api.edge.wranglerURL} download="wrangler.jsonc">Download wrangler.jsonc</Button>
              </div>
            </div>
          </LearnMore>
        {/if}
      </div>
    </Section>
  </FormPage>

  <SaveBar count={draft.count} onSave={save} onDiscard={discard} />
{/if}

<ConfirmDialog
  bind:open={confirming}
  title="Stop serving from {stopped.length === 1 ? 'this region' : `${stopped.length} regions`}?"
  confirmLabel="Save"
  destructive
  changes={[
    ...stopped.map((s) => `The server stops writing to the bucket ${s.bucket}`),
    "Deploy the Worker again, then delete the bucket in Cloudflare",
  ]}
  onConfirm={apply}
/>
