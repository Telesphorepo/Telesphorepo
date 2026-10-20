<!--
  Edge delivery (Manage edge delivery permission): which Cloudflare R2 regions serve downloads, the
  settings of the edge, and the generated wrangler.jsonc with the commands that deploy the Worker.
  One Draft holds the home region, the regions with their buckets, the on switch and the access
  lifetime; Save checks every bucket first (the Test of the settings group), asks before the server
  stops writing to a bucket, and then saves. While a region fills, the page asks for progress.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import type { PageProps } from "$lib/router.svelte";
  import { api, errorMessage, type EdgeHint, type EdgeRegionState, type EdgeRegionStatus, type Setting, type SettingsPatch } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import { Draft } from "$lib/unsaved.svelte";
  import { formatCount } from "$lib/format";
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
  import Loading from "$lib/components/Loading.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";

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

  // While a region fills, ask for progress every three seconds.
  const filling = $derived((status.data?.regions ?? []).filter((r) => r.state === "filling"));
  $effect(() => {
    if (filling.length === 0) return;
    const timer = setInterval(() => status.reload(), 3000);
    return () => clearInterval(timer);
  });

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
</script>

<PageHeader title="Edge delivery" description="Serve downloads from Cloudflare R2 buckets near your clients." />

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
          </div>
          <div>
            <dt class="text-meta text-muted-foreground">Last gap check</dt>
            <dd class="text-label"><RelativeTime at={st.gap_checked_at} empty="Not yet" /></dd>
          </div>
          <div>
            <dt class="text-meta text-muted-foreground">Home bucket</dt>
            <dd class="text-label">{st.home.bucket || "Not set"}</dd>
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
            { label: "Waiting", align: "right", hideOnPhone: true },
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
                <StatusBadge tone={stateTone[r.state]}>{stateWord[r.state]}</StatusBadge>
              {:else}
                <span class="text-muted-foreground">{stateWord.not_used}</span>
              {/if}
            </Cell>
            <Cell num hideOnPhone>{formatCount(isHome ? (st?.home.outbox ?? 0) : (r?.outbox ?? 0))}</Cell>
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
      <Section title="Backfill" description="The pointers and files that each new region receives before the Worker reads it.">
        <DataTable
          source={fillingSource}
          caption="Regions that fill"
          key={(r) => r.hint}
          columns={[{ label: "Region", grow: true }, { label: "Pointers written", align: "right" }, { label: "Files missing", align: "right" }]}
        >
          {#snippet row(r)}
            <Cell grow strong text={areaText(r.hint)} />
            <Cell num>{formatCount(r.pointers_written)} of {formatCount(r.pointers_total)}</Cell>
            <Cell num>{r.blobs_missing === null ? "Counting" : formatCount(r.blobs_missing)}</Cell>
          {/snippet}
          {#snippet empty()}<CompactEmpty title="Backfill" state="Done" text="Every region has its pointers." />{/snippet}
        </DataTable>
      </Section>
    {/if}

    <FormSection title="Settings">
      <FormGrid>
        <SwitchRow
          id="edge-enabled"
          label="Edge delivery"
          help={lockHelp("edge.enabled") ?? "The Worker answers downloads that it can prove the server would answer the same way."}
          disabled={locked("edge.enabled")}
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

    <Section title="Deploy" description="Save the file in the edge folder of the repository (or of the release archive) and run the two commands.">
      {#snippet actions()}
        {#if needsDeploy || mismatch}<StatusBadge tone="warning">Deploy needed</StatusBadge>{/if}
      {/snippet}
      <div class="flex flex-col gap-6">
        {#if mismatch && st}
          <p class="text-body">Deploy the Worker of this server version ({st.server_version}). Until then the Worker sends every request to the server.</p>
        {:else if needsDeploy}
          <p class="text-body">The regions or the storage location changed. Run provision, then deploy.</p>
        {/if}
        {#if wrangler.error && !wrangler.data}
          <ErrorState error={wrangler.error} title="The wrangler.jsonc did not load" onRetry={wrangler.reload} inline />
        {:else if wrangler.data}
          <CodeBlock title="wrangler.jsonc" code={wrangler.data} />
          <div>
            <Button variant="outline" size="sm" href={api.edge.wranglerURL} download="wrangler.jsonc">Download wrangler.jsonc</Button>
          </div>
        {:else}
          <Loading inline />
        {/if}
        <CodeSnippet label="Create the buckets, the queue and the notification rule" value="CLOUDFLARE_API_TOKEN=... bun run provision" />
        <CodeSnippet label="Deploy the Worker" value="bun run deploy" />
        <p class="text-meta text-muted-foreground">
          The API token needs edit access to Workers Scripts, Workers Routes, R2 and Queues. Run provision again after a region change, and deploy for each release of the server.
        </p>
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
