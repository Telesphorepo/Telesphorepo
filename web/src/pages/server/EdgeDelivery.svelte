<!--
  Edge delivery (Manage edge delivery permission): which Cloudflare R2 regions serve downloads, the
  settings of the edge, and the deploy of the Worker in one of two ways, picked with a segmented
  control: From this server, with a Cloudflare API token (Deploy now, POST /api/edge/deploy; the
  last deploy shows with its failed step, Cloudflare's error and what it changed; and on its own
  while "Deploy on its own" is on), or From a computer: the download of the Worker folder (with the
  generated wrangler.jsonc in it) and the commands to run there; the wrangler.jsonc for a checkout
  of the repository behind a disclosure. Under From this server, Token shows the server's last check
  of the token: each permission of the deploy and of the pointer cache present, missing, or not
  provable, and the read permission that a full check needs when the token lacks it.
  One Draft holds the home region, the regions with their buckets, the on switch, the access
  lifetime, the token and the auto-deploy switch; Save checks every bucket first (the Test of the settings group), asks before the server
  stops writing to a bucket, and then saves. Each region is a card with its state, its Serve from
  it switch and its bucket. While a region fills, the page asks for progress and its card shows
  the progress and what the region waits for, with the cadence of the count and of the Worker's gap check (from
  the status). Check now makes the deployed Worker run its gap check at once (POST
  /api/edge/check); the page runs it by itself when it opens and every 30 seconds while the Worker
  has not reported this server's version. One Live does both, with its LiveStatus in the header.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import DownloadIcon from "@lucide/svelte/icons/download";
  import RefreshCwIcon from "@lucide/svelte/icons/refresh-cw";
  import UploadCloudIcon from "@lucide/svelte/icons/cloud-upload";
  import type { PageProps } from "$lib/router.svelte";
  import {
    api,
    ApiError,
    errorMessage,
    type EdgeCheck,
    type EdgeDeployStep,
    type EdgeHint,
    type EdgeRegionState,
    type EdgeRegionStatus,
    type EdgeStatus,
    type EdgeToken,
    type EdgeTokenPermission,
    type Setting,
    type SettingsPatch,
  } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import { Draft } from "$lib/unsaved.svelte";
  import { formatCount, formatEvery } from "$lib/format";
  import { count } from "$lib/words";
  import { Live } from "$lib/live.svelte";
  import LiveStatus from "$lib/components/LiveStatus.svelte";
  import LiveValue from "$lib/components/LiveValue.svelte";
  import Cadence from "$lib/components/Cadence.svelte";
  import { DURATION_UNITS, splitDuration } from "$lib/units";
  import { Button } from "$lib/components/ui/button";
  import { Switch } from "$lib/components/ui/switch";
  import { Input } from "$lib/components/ui/input";
  import * as Select from "$lib/components/ui/select";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import FormPage from "$lib/components/FormPage.svelte";
  import FormSection from "$lib/components/FormSection.svelte";
  import FormGrid from "$lib/components/FormGrid.svelte";
  import FormRow from "$lib/components/FormRow.svelte";
  import SwitchRow from "$lib/components/SwitchRow.svelte";
  import UnitInput from "$lib/components/UnitInput.svelte";
  import SecretInput from "$lib/components/SecretInput.svelte";
  import Segmented from "$lib/components/Segmented.svelte";
  import Section from "$lib/components/Section.svelte";
  import SaveBar from "$lib/components/SaveBar.svelte";
  import StatusBadge, { type Tone } from "$lib/components/StatusBadge.svelte";
  import CardGrid from "$lib/components/CardGrid.svelte";
  import ItemCard from "$lib/components/ItemCard.svelte";
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
    /** The Cloudflare API token: undefined keeps it, a string replaces it, null clears it. */
    token: string | null | undefined;
    auto: boolean;
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
      token: undefined,
      auto: setting("edge.auto_deploy")?.value !== false,
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

  // Chosen regions are listed as "hint=bucket", in the order of the cards.
  const regionList = (f: Form) =>
    AREAS.filter((a) => a.hint !== f.home && f.regions[a.hint].on).map((a) => `${a.hint}=${f.regions[a.hint].bucket.trim()}`);

  function patchOf(d: Draft<Form>): SettingsPatch {
    const patch: SettingsPatch = {};
    for (const key of d.changed) {
      if (key === "enabled") patch["edge.enabled"] = d.value.enabled;
      else if (key === "home") patch["edge.home_hint"] = d.value.home;
      else if (key === "ttl") patch["edge.access_ttl"] = `${d.value.ttl.num.trim() || "0"}${d.value.ttl.unit}`;
      else if (key === "regions") patch["edge.regions"] = regionList(d.value);
      else if (key === "token" && d.value.token !== undefined) patch["edge.cloudflare_token"] = d.value.token;
      else if (key === "auto") patch["edge.auto_deploy"] = d.value.auto;
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

  const needsDeploy = $derived(provisionNeeded || status.data?.deploy_needed === true);
  const outdated = $derived(status.data?.worker_outdated ?? "");
  // How the Worker is deployed: from this server with a token, or from a computer with wrangler.
  // Until the person picks, the page shows the way in use: a token set is the server; a Worker
  // that reports without one was deployed from a computer.
  type DeployWay = "server" | "computer";
  let pickedWay = $state<DeployWay | null>(null);
  const deployWay = $derived<DeployWay>(
    pickedWay ?? (status.data?.deploy.token_set ? "server" : status.data?.worker_version ? "computer" : "server"),
  );
  const DEPLOY_WAYS = [
    { value: "server", label: "From this server", title: "With a Cloudflare API token" },
    { value: "computer", label: "From a computer", title: "Download the Worker and deploy it with wrangler" },
  ];
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

  // Deploy now: the server deploys the Worker to Cloudflare with the token of the settings. The
  // page follows its steps through the status while it runs.
  const STEP_WORDS: Record<EdgeDeployStep, string> = {
    verify: "Verify the token",
    zones: "Find the zone of each route host",
    buckets: "Check the buckets",
    queue: "Create the copy queue",
    notification: "Put the notification rule of the home bucket",
    upload: "Upload the Worker",
    consumer: "Set the queue consumer",
    triggers: "Set the cron and turn off workers.dev",
    routes: "Set the routes",
    check: "Check the Worker",
  };
  // The permission policies of the token, as Cloudflare's token form names them: the scope, then
  // each permission as its category › name: level.
  const TOKEN_POLICIES = [
    {
      scope: "Entire Account",
      permissions: [
        "Developer Platform › Workers: Admin (Editor cannot create the Worker)",
        "Developer Platform › Workers R2 Storage: Edit",
        "Developer Platform › Queues: Edit",
        "Account & Billing › Account API Tokens: Read (for the full check of the token)",
      ],
    },
    {
      scope: "All Domains, or the domains of the routes",
      permissions: [
        "Developer Platform › Workers Routes: Edit",
        "DNS & Zones › Zone: Read",
        "Cache & Performance › Cache: Purge (to cache pointers at the edge)",
      ],
    },
  ];
  // The check of the token: each permission present, missing, or not provable.
  const PERM_WORD: Record<EdgeTokenPermission["state"], string> = { present: "Present", missing: "Missing", unproven: "Not provable" };
  const PERM_TONE: Record<EdgeTokenPermission["state"], Tone> = { present: "success", missing: "danger", unproven: "warning" };
  function tokenVerdict(t: EdgeToken): { word: string; tone: Tone } {
    if (t.problem) return { word: "Not usable", tone: "danger" };
    const needed = t.permissions.filter((p) => !p.optional);
    if (needed.some((p) => p.state === "missing")) return { word: "Missing permissions", tone: "danger" };
    if (needed.some((p) => p.state === "unproven")) return { word: "Partly checked", tone: "warning" };
    return { word: "Ready to deploy", tone: "success" };
  }
  let deployError = $state("");
  let deployStarting = $state(false);
  const deploying = $derived(deployStarting || status.data?.deploy.running === true);
  async function deployNow() {
    if (deploying) return;
    deployStarting = true;
    try {
      await api.edge.deploy();
      deployError = "";
    } catch (e) {
      deployError = errorMessage(e);
    } finally {
      deployStarting = false;
      await status.reload();
    }
  }

  // One Live keeps the page current. While a deploy runs, it asks for the status every 2 seconds;
  // while a region fills, every 3 seconds.
  // While no Worker has reported, or the one that did is outdated, it checks the Worker when the
  // page opens and every 30 seconds, so a deploy shows within seconds.
  const DEPLOY_EVERY = 2000;
  const FILL_EVERY = 3000;
  const CHECK_EVERY = 30_000;
  const autoCheck = $derived(!!status.data && status.data.check_unfit === "" && (!status.data.worker_version || status.data.worker_outdated !== ""));
  let autoAt = 0;
  const live = new Live({
    now: true,
    while: () => deploying || filling.length > 0 || autoCheck,
    every: () => (deploying ? DEPLOY_EVERY : filling.length > 0 ? FILL_EVERY : CHECK_EVERY),
    run: async () => {
      if (deploying) await status.fetch();
      else if (autoCheck && Date.now() - autoAt >= CHECK_EVERY - FILL_EVERY) {
        autoAt = Date.now();
        await checkNow(false);
        if (checkError || status.error) throw new Error(checkError || status.error);
      } else await status.fetch();
    },
  });
  const liveCadence = $derived(
    deploying
      ? `Refreshes every ${formatEvery(DEPLOY_EVERY / 1000)} while the Worker deploys`
      : filling.length > 0
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
      return say("Turn on edge delivery to copy the files.");
    }
    if (!r.pointers_done) {
      return say(r.outbox > 0 ? `Writing pointers: ${formatCount(r.outbox)} waiting.` : "Queueing pointers.");
    }
    if (!st.worker_version) return say("Waiting for the Worker's first gap check.");
    if (st.worker_outdated) return say("Waiting for a Worker that this server can use. Deploy it to copy the files.");
    if (r.blobs_missing === null) return say("Counting the files in the region.");
    if (r.blobs_missing > 0) {
      return { text: "Copying the missing files from the home bucket at each gap check.", copying: true };
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
        {#snippet badges()}
          {#if st.mode === "on"}
            <StatusBadge tone="success">On</StatusBadge>
          {:else if st.mode === "draining"}
            <StatusBadge tone="warning">Stopping</StatusBadge>
          {:else}
            <StatusBadge tone="neutral">Off</StatusBadge>
          {/if}
        {/snippet}
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
        {#if st.unfit}<p class="mb-6 text-body text-warning">{st.unfit}</p>{/if}
        <dl class="grid grid-cols-2 gap-x-6 gap-y-5 sm:grid-cols-3">
          <div class="min-w-0">
            <dt class="text-meta text-muted-foreground">Worker build</dt>
            <dd class="flex flex-wrap items-center gap-2 text-label">
              {st.worker_version ?? "None reported"}
              {#if outdated}<StatusBadge tone="warning">Outdated</StatusBadge>{/if}
            </dd>
          </div>
          <div class="min-w-0">
            <dt class="text-meta text-muted-foreground">Last gap check</dt>
            <dd class="text-label"><RelativeTime at={st.gap_checked_at} empty="Not yet" /></dd>
            {#if st.gap_checked_at}<dd><Cadence noun="gap check" every={st.gap_check_every} last={st.gap_checked_at} /></dd>{/if}
          </div>
          <div class="col-span-2 min-w-0 sm:col-span-1">
            <dt class="text-meta text-muted-foreground">Home bucket</dt>
            <dd class="truncate text-label" title={st.home.bucket || undefined}>{st.home.bucket || "Not set"}</dd>
          </div>
          {#if st.mode === "on"}
            <div class="col-span-full min-w-0">
              <dt class="text-meta text-muted-foreground">Pointer cache</dt>
              {#if st.pointer_cache.on}
                <dd class="text-label">On</dd>
                <dd class="text-meta text-muted-foreground">The server purges a pointer's cached copies before its file is replaced or removed.</dd>
              {:else}
                <dd class="text-label">Off</dd>
                <dd class="text-meta text-muted-foreground">{st.pointer_cache.why || "Checking that a purge removes a cached pointer."}</dd>
              {/if}
            </div>
          {/if}
          <div class="col-span-full min-w-0">
            <dt class="text-meta text-muted-foreground">Last check of the Worker</dt>
            {#if checkError}
              <dd class="text-body text-destructive">{checkError}</dd>
            {:else if st.last_check}
              {@const c = st.last_check}
              <dd class="flex flex-wrap items-center gap-x-2 gap-y-1">
                <StatusBadge tone={checkTone(c)}>{checkWord[c.outcome]}</StatusBadge>
                <span class="text-meta text-muted-foreground"><RelativeTime at={c.at} /></span>
              </dd>
              <dd class="mt-1 text-body">{c.message}</dd>
            {:else}
              <dd class="text-label">{checking ? "Checking" : "Not yet"}</dd>
              {#if st.check_unfit}<dd class="text-meta text-muted-foreground">{st.check_unfit}</dd>{/if}
            {/if}
          </div>
        </dl>
      </Section>
    {/if}

    <Section plain title="Regions" description="The home bucket is the S3 storage of the server. Each region you add holds a copy of every file.">
      <div class="flex flex-col gap-4">
        <CardGrid label="Regions" max={3}>
          {#each AREAS as a (a.hint)}
            {@const isHome = a.hint === draft.value.home}
            {@const r = statusOf(a.hint)}
            {@const edit = draft.value.regions[a.hint]}
            {@const err = rowErrors[a.hint] || (isHome ? st?.home.last_error : r?.last_error)}
            {@const waiting = isHome ? (st?.home.pointers ?? 0) : (r?.outbox ?? 0)}
            <ItemCard title={areaText(a.hint)}>
              {#snippet badges()}
                {#if isHome}
                  <StatusBadge tone={st?.home.last_error ? "danger" : "success"}>{st?.home.last_error ? "Error" : "Ready"}</StatusBadge>
                {:else if r}
                  <StatusBadge tone={stateTone[r.state]}>{st && r.state === "filling" && waitsFor(st, r).copying ? "Copying files" : stateWord[r.state]}</StatusBadge>
                {:else}
                  <StatusBadge tone={stateTone.not_used}>{stateWord.not_used}</StatusBadge>
                {/if}
              {/snippet}
              {#if isHome}
                <p class="truncate">Home bucket {st?.home.bucket || homeBucket}</p>
              {:else if edit.on}
                <Input
                  id="edge-bucket-{a.hint}"
                  aria-label="Bucket for {areaText(a.hint)}"
                  class="my-1 h-8"
                  disabled={locked("edge.regions")}
                  bind:value={edit.bucket}
                  aria-invalid={err ? true : undefined}
                />
              {:else if edit.bucket || bucketFor(a.hint)}
                <p class="truncate">Bucket {edit.bucket || bucketFor(a.hint)}</p>
              {/if}
              <!-- A filling region that writes its pointers says how many wait in its progress. -->
              {#if waiting > 0 && !(r?.state === "filling" && !r.pointers_done)}<p class="tabular-nums">{count(waiting, "pointer", "pointers")} waiting</p>{/if}
              {#if err}<p class="line-clamp-2 text-destructive" title={err}>{err}</p>{/if}
              {#if st && r && r.state === "filling"}
                {@const w = waitsFor(st, r)}
                {@const pct = r.pointers_total > 0 ? Math.min(100, Math.round((r.pointers_written / r.pointers_total) * 100)) : 0}
                <div class="mt-2 flex flex-col gap-1.5">
                  <div class="flex items-baseline justify-between gap-3 tabular-nums">
                    <span>Pointers <LiveValue value={r.pointers_written}>{formatCount(r.pointers_written)}</LiveValue> of {formatCount(r.pointers_total)}</span>
                    <span>{#if r.blobs_missing === null}Not counted yet{:else}<LiveValue value={r.blobs_missing}>{formatCount(r.blobs_missing)}</LiveValue> missing{/if}</span>
                  </div>
                  <div class="h-1.5 w-full overflow-hidden rounded-full bg-muted" role="progressbar" aria-label="Pointers written to {areaText(a.hint)}" aria-valuemin={0} aria-valuemax={100} aria-valuenow={pct}>
                    <div class="h-full rounded-full bg-primary transition-[width]" style="width: {pct}%"></div>
                  </div>
                  <p class="text-pretty text-foreground">{w.text}</p>
                  {#if w.copying}
                    <p class="text-pretty">
                      {#if r.blobs_missing_since}Last changed <RelativeTime at={r.blobs_missing_since} />.{/if}
                      {#if st.gap_pass_started}Copy pass started <RelativeTime at={st.gap_pass_started} />.{:else if r.copies_queued !== null}Its last copy pass
                        queued {formatCount(r.copies_queued)} copies.{/if}
                    </p>
                  {/if}
                </div>
              {/if}
              {#snippet actions()}
                {#if !isHome}
                  <Switch
                    id="edge-region-{a.hint}"
                    aria-label="Serve from {areaText(a.hint)}"
                    disabled={locked("edge.regions")}
                    checked={edit.on}
                    onCheckedChange={(on) => {
                      edit.on = on;
                      if (edit.on && !edit.bucket.trim()) edit.bucket = bucketFor(a.hint);
                    }}
                  />
                  <label for="edge-region-{a.hint}" class="text-body {edit.on ? 'text-foreground' : ''}">Serve from it</label>
                {/if}
              {/snippet}
            </ItemCard>
          {/each}
        </CardGrid>
        {#if st && filling.length > 0}
          <div class="flex flex-col gap-1">
            <p class="text-meta text-muted-foreground">A region serves once it has its pointers and a copy of every file.</p>
            {#if counting.length > 0}
              <Cadence noun="count of missing files" every={st.blob_check_every} last={countedAt} />
            {/if}
            {#if copying}
              <Cadence noun="gap check" every={st.gap_check_every} last={st.gap_checked_at} />
            {/if}
          </div>
        {/if}
      </div>
    </Section>

    {#if st}<SpeedTest status={st} />{/if}

    <FormSection title="Settings">
      <FormGrid>
        <SwitchRow
          id="edge-enabled"
          label="Edge delivery"
          help={lockHelp("edge.enabled") ??
            (noWorker
              ? "Deploy the Worker first (Deploy, below). It turns on once the Worker reports its version."
              : "Clients download from the nearest region. What only the server can answer still goes to the server.")}
          disabled={locked("edge.enabled") || noWorker}
          bind:checked={draft.value.enabled}
        />
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
      {#if lockHelp("edge.home_hint") || lockHelp("edge.regions")}
        <p class="mt-6 text-meta text-muted-foreground">{lockHelp("edge.home_hint") ?? lockHelp("edge.regions")}</p>
      {/if}
    </FormSection>

    {@const dep = st?.deploy}
    {@const tokenSet = dep?.token_set === true && draft.value.token !== null}
    <Section title="Deploy" description="Deploy the edge Worker to your Cloudflare account in one of two ways.">
      {#snippet badges()}
        {#if deploying}
          <StatusBadge tone="info">Deploying</StatusBadge>
        {:else if needsDeploy || dep?.needed}
          <StatusBadge tone="warning">Deploy needed</StatusBadge>
        {/if}
      {/snippet}
      {#snippet actions()}
        {#if deployWay === "server" && dep?.token_set}
          <Button
            variant="outline"
            size="sm"
            disabled={deploying || dep.unfit !== "" || !!draft?.changed.includes("token")}
            title={draft?.changed.includes("token") ? "Save the token first" : dep.unfit || undefined}
            onclick={deployNow}
          >
            <UploadCloudIcon aria-hidden="true" class={deploying ? "animate-pulse" : ""} />{deploying ? "Deploying" : "Deploy now"}
          </Button>
        {/if}
      {/snippet}
      <div class="flex flex-col gap-6">
        <Segmented bind:value={() => deployWay, (v) => (pickedWay = v as DeployWay)} options={DEPLOY_WAYS} label="How to deploy" compact />

        {#if outdated}
          <p class="text-body">
            {outdated}
            {deployWay === "server" ? (tokenSet ? "Deploy it again." : "Set a token to deploy it.") : "Download the Worker and deploy it."}
          </p>
        {:else if needsDeploy && deployWay === "computer"}
          <p class="text-body">The regions or the storage location changed. Download the Worker again, then run provision and deploy.</p>
        {/if}

        {#if deployWay === "computer"}
          {#if dep?.token_set}
            <p class="text-body">
              This server has a Cloudflare API token, so it still deploys the Worker itself. To deploy only from a computer, remove the token under From this server.
            </p>
          {/if}
          {@render manualSteps()}
        {:else}
          {#if dep && dep.token_set}
            <div class="flex flex-col gap-1">
              <p class="text-meta text-muted-foreground">Last deploy from the server</p>
              {#if deploying}
                <p class="text-body">{dep.step ? `${STEP_WORDS[dep.step]}…` : "Starting…"}</p>
              {:else if deployError}
                <p class="text-body text-destructive">{deployError}</p>
              {:else if dep.last_at}
                <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
                  <StatusBadge tone={dep.last_ok ? "success" : "danger"}>{dep.last_ok ? "Deployed" : "Failed"}</StatusBadge>
                  <span class="text-meta text-muted-foreground"><RelativeTime at={dep.last_at} /></span>
                </div>
                {#if !dep.last_ok}
                  {#if dep.step}<p class="mt-1 text-label">Failed at: {STEP_WORDS[dep.step]}</p>{/if}
                  <p class="text-body break-words whitespace-pre-wrap text-destructive">{dep.last_error}</p>
                  {#if dep.last_call && dep.last_call.errors.some((e) => e.code)}
                    <p class="text-meta text-muted-foreground">
                      Cloudflare answered {dep.last_call.method} {dep.last_call.path} with HTTP {dep.last_call.status}, error {dep.last_call.errors
                        .filter((e) => e.code)
                        .map((e) => e.code)
                        .join(", ")}.
                    </p>
                  {/if}
                  {#if dep.next_at}<p class="text-meta text-muted-foreground">Tries again on its own <RelativeTime at={dep.next_at} />.</p>{/if}
                {/if}
                {#if dep.changed.length > 0}
                  <ul class="mt-1 list-disc pl-5 text-meta text-muted-foreground">
                    {#each dep.changed as line, i (i)}<li>{line}</li>{/each}
                  </ul>
                {/if}
              {:else}
                <p class="text-label">Not yet</p>
              {/if}
              {#if dep.unfit && !deploying}<p class="text-meta text-warning">{dep.unfit}</p>{/if}
            </div>
          {/if}

          {#if st?.token}{@render tokenCheck(st.token)}{/if}

          {#if dep?.token_set}
            {@render tokenFields()}
          {:else}
            <ol class="flex flex-col gap-5">
              <li class="flex gap-3">
                <span class="w-4 shrink-0 text-section text-muted-foreground tabular-nums">1</span>
                <div class="flex min-w-0 grow flex-col gap-2 text-body text-muted-foreground">
                  <p class="text-label text-foreground">Create an API token in Cloudflare</p>
                  <p>In the account of the R2 buckets: Manage account › Account API tokens › Create Token › Custom, with two policies.</p>
                  <div class="grid gap-x-6 gap-y-2 sm:grid-cols-2">{@render tokenPolicies()}</div>
                </div>
              </li>
              <li class="flex gap-3">
                <span class="w-4 shrink-0 text-section text-muted-foreground tabular-nums">2</span>
                <div class="flex min-w-0 grow flex-col gap-2">
                  <p class="text-label">Paste the token here and save</p>
                  {@render tokenFields()}
                </div>
              </li>
              <li class="flex gap-3">
                <span class="w-4 shrink-0 text-section text-muted-foreground tabular-nums">3</span>
                <div class="flex min-w-0 grow flex-col gap-2">
                  <p class="text-label">Deploy</p>
                  <div>
                    <Button variant="outline" size="sm" disabled title="Save the token first"><UploadCloudIcon aria-hidden="true" />Deploy now</Button>
                  </div>
                </div>
              </li>
            </ol>
          {/if}
        {/if}
      </div>
    </Section>
  </FormPage>

  <SaveBar count={draft.count} onSave={save} onDiscard={discard} />
{/if}

{#snippet tokenPolicies()}
  {#each TOKEN_POLICIES as policy (policy.scope)}
    <div>
      <p class="text-label text-foreground">{policy.scope}</p>
      <ul class="mt-1 list-disc pl-5">
        {#each policy.permissions as p (p)}<li>{p}</li>{/each}
      </ul>
    </div>
  {/each}
{/snippet}

{#snippet tokenCheck(t: EdgeToken)}
  <div class="flex flex-col gap-1">
    <p class="text-meta text-muted-foreground">Token</p>
    {#if !t.checked_at}
      <p class="text-label">{t.running ? "Checking" : "Not checked yet"}</p>
    {:else}
      {@const v = tokenVerdict(t)}
      <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
        <StatusBadge tone={v.tone}>{v.word}</StatusBadge>
        <span class="text-meta text-muted-foreground">Checked <RelativeTime at={t.checked_at} />{t.running ? ", checking again" : ""}</span>
      </div>
      {#if t.problem}<p class="text-body text-destructive">{t.problem}</p>{/if}
      {#if t.expires_on}<p class="text-meta text-muted-foreground">Expires <RelativeTime at={t.expires_on} />.</p>{/if}
      {#if t.permissions.length > 0}
        <ul class="mt-1 flex flex-col gap-2">
          {#each t.permissions as p (p.key)}
            <li class="flex min-w-0 flex-col gap-0.5">
              <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
                <StatusBadge tone={p.optional && p.state !== "present" ? "neutral" : PERM_TONE[p.state]}>{PERM_WORD[p.state]}</StatusBadge>
                <span class="text-label">{p.name}</span>
                {#if p.optional}<span class="text-meta text-muted-foreground">Optional, for the pointer cache</span>{/if}
              </div>
              {#if p.state !== "present"}<p class="text-meta break-words text-muted-foreground">{p.why}</p>{/if}
            </li>
          {/each}
        </ul>
      {/if}
      {#if t.read_missing}
        <p class="mt-1 text-body">Add {t.read_missing} for a full check.</p>
      {:else if t.exact_why && !t.problem}
        <p class="mt-1 text-meta break-words text-muted-foreground">{t.exact_why}</p>
      {/if}
    {/if}
  </div>
{/snippet}

{#snippet tokenFields()}
  {@const dep = status.data?.deploy}
  {@const tokenSet = dep?.token_set === true && draft?.value.token !== null}
  {#if draft}
    <FormGrid>
      <FormRow label="Cloudflare API token" for="edge-token" help={lockHelp("edge.cloudflare_token")}>
        <SecretInput
          id="edge-token"
          set={dep?.token_set === true}
          change={draft.value.token}
          onchange={(v) => (draft!.value.token = v)}
          disabled={locked("edge.cloudflare_token")}
          describedby="edge-token-help"
        />
      </FormRow>
      <SwitchRow
        id="edge-auto"
        label="Deploy on its own"
        help={lockHelp("edge.auto_deploy") ?? "After a change of the regions, the repository domains, the storage location or the server version."}
        disabled={locked("edge.auto_deploy") || !tokenSet}
        bind:checked={draft.value.auto}
      />
    </FormGrid>
  {/if}
{/snippet}

{#snippet manualSteps()}
  {@const st = status.data}
  <div class="flex flex-col gap-6">
    <ol class="flex flex-col gap-5">
      <li class="flex gap-3">
        <span class="w-4 shrink-0 text-section text-muted-foreground tabular-nums">1</span>
        <div class="flex min-w-0 grow flex-col gap-2">
          <p class="text-label">Download the edge Worker</p>
          {#if wrangler.error && !wrangler.data && wrangler.error === st?.check_unfit}
            <!-- The reason shows under Status. -->
            <div>
              <Button variant="outline" size="sm" disabled><DownloadIcon aria-hidden="true" />Download the edge Worker</Button>
            </div>
          {:else if wrangler.error && !wrangler.data}
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
          <p class="text-meta text-muted-foreground">If you cannot turn it on yet, select Check now under Status.</p>
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
{/snippet}

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
