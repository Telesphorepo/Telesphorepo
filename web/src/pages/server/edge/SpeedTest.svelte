<!--
  The speed test of Edge delivery: download one file through the Worker from the bucket the
  administrator picks, timed in this browser. The server issues a pass (POST /api/edge/speedtest)
  that lets the Worker take the choice from the headers Telesphorepo-Edge-Region and
  Telesphorepo-Edge-Pass; the answer says which bucket served (Telesphorepo-Edge) and whether the
  Worker took the choice (Telesphorepo-Edge-Override). Run tests one bucket, Run all every bucket one
  after another. The browser downloads from the address it runs on, so the test runs only when the
  page is open at the base URL, where the Worker's routes are.
-->
<script lang="ts">
  import GaugeIcon from "@lucide/svelte/icons/gauge";
  import { api, errorMessage, type EdgeStatus } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import { formatSize } from "$lib/format";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Spinner } from "$lib/components/ui/spinner";
  import * as Select from "$lib/components/ui/select";
  import Section from "$lib/components/Section.svelte";
  import FormGrid from "$lib/components/FormGrid.svelte";
  import FormRow from "$lib/components/FormRow.svelte";
  import DataTable from "$lib/components/DataTable.svelte";
  import Cell from "$lib/components/Cell.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import StatusBadge, { type Tone } from "$lib/components/StatusBadge.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";

  let { status }: { status: EdgeStatus } = $props();

  const files = new Resource(() => api.edge.speedFiles());

  // A bucket to read from. header is the value of Telesphorepo-Edge-Region; null sends no choice,
  // so the Worker reads the nearest bucket as it does for every client.
  type Target = { key: string; label: string; header: string | null };
  const homeLabel = $derived(status.home.hint ? `Home bucket (${status.home.hint})` : "Home bucket");
  const areaOf = (hint: string) => {
    if (hint === status.home.hint) return homeLabel;
    const r = status.regions.find((x) => x.hint === hint);
    return r ? `${r.area} (${hint})` : hint;
  };
  const targets = $derived<Target[]>([
    { key: "nearest", label: "Nearest bucket", header: null },
    ...status.regions.filter((r) => r.state === "ready").map((r) => ({ key: r.hint, label: areaOf(r.hint), header: r.hint })),
    { key: "home", label: homeLabel, header: "home" },
    { key: "origin", label: "Server, no Worker", header: "origin" },
  ]);
  let targetKey = $state("nearest");
  const target = $derived(targets.find((t) => t.key === targetKey) ?? targets[0]);

  const OTHER = "other";
  let fileChoice = $state("");
  let otherPath = $state("");
  $effect(() => {
    if (fileChoice === "" && files.data) fileChoice = files.data.files[0]?.url ?? OTHER;
  });
  const fileLabel = (url: string) => {
    const f = files.data?.files.find((x) => x.url === url);
    return f ? `${f.repo}/${f.path.split("/").pop()} · ${formatSize(f.size)}` : "Another file";
  };

  // The test downloads from the address this page runs on; the Worker runs on the base URL host.
  const baseURL = $derived(files.data?.base_url ?? "");
  const onBase = $derived.by(() => {
    try {
      return baseURL !== "" && new URL(baseURL).origin === location.origin;
    } catch {
      return false;
    }
  });

  // pathOf returns the download path of the chosen file, or why there is none.
  function pathOf(): { path: string } | { error: string } {
    if (fileChoice !== OTHER) return { path: fileChoice };
    const v = otherPath.trim();
    if (v === "") return { error: "Enter the path of a file to download." };
    if (/^https?:\/\//i.test(v)) {
      const u = new URL(v);
      if (u.origin !== location.origin) return { error: `Enter a path on ${location.origin}.` };
      return { path: u.pathname + u.search };
    }
    return { path: v.startsWith("/") ? v : "/" + v };
  }

  type Result = {
    key: string;
    label: string;
    running: boolean;
    error?: string;
    answeredBy?: string;
    choice?: { word: string; tone: Tone; title: string };
    ttfb?: number;
    total?: number;
    speed?: number;
    timing?: string;
  };
  let results = $state<Result[]>([]);
  let runError = $state("");
  let running = $state(false);
  // The data center of Cloudflare that answered the last run (from CF-Ray).
  let colo = $state("");

  const fastest = $derived.by(() => {
    const done = results.filter((r) => !r.running && !r.error && r.total !== undefined);
    if (done.length < 2) return "";
    return done.reduce((a, b) => (b.total! < a.total! ? b : a)).key;
  });

  // The pass is kept in memory while it has more than 30 seconds left.
  let pass: { token: string; until: number } | null = null;
  async function passToken(): Promise<string> {
    if (pass && pass.until - Date.now() > 30_000) return pass.token;
    const p = await api.edge.speedPass();
    pass = { token: p.token, until: new Date(p.expires_at).getTime() };
    return p.token;
  }

  const choiceOf = (h: string | null): Result["choice"] => {
    if (h === null) {
      return { word: "Not seen", tone: "warning", title: "No Worker of this server's version saw the request: it went to the server." };
    }
    if (h.startsWith("applied")) return { word: "Taken", tone: "success", title: "The Worker read from the bucket you picked." };
    if (h.includes("reason=pass")) return { word: "Pass refused", tone: "danger", title: "The Worker did not accept the pass, so it read from the nearest bucket." };
    if (h.includes("reason=region")) {
      return { word: "Region not read", tone: "warning", title: "The Worker does not read this region yet (not ready, or no binding), so it read from the nearest bucket." };
    }
    return { word: "Not used", tone: "neutral", title: "The Worker does not answer this file from a bucket, so it went to the server." };
  };

  const ms = (v: number) => (v >= 10_000 ? `${(v / 1000).toFixed(1)} s` : `${Math.round(v)} ms`);

  // measure downloads path once, reading the whole body, and times it with the browser's resource
  // timing of the request (its own clock when the entry is missing).
  async function measure(path: string, t: Target): Promise<Omit<Result, "key" | "label" | "running">> {
    const headers: Record<string, string> = {};
    if (t.header) {
      headers["Telesphorepo-Edge-Region"] = t.header;
      headers["Telesphorepo-Edge-Pass"] = await passToken();
    }
    const url = new URL(path, location.origin).href;
    // Room for the entry of this request in the browser's resource timing buffer.
    performance.clearResourceTimings();
    const start = performance.now();
    const res = await fetch(url, { headers, cache: "no-store", credentials: "omit", redirect: "manual" });
    const headersAt = performance.now();
    if (res.type === "opaqueredirect") {
      return { error: "The server answers with a redirect to the storage bucket, which this browser cannot time." };
    }
    let bytes = 0;
    const reader = res.body?.getReader();
    if (reader) {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        bytes += value.byteLength;
      }
    }
    const end = performance.now();
    colo = res.headers.get("CF-Ray")?.split("-").pop() ?? colo;
    if (!res.ok) {
      const why = res.status === 401 || res.status === 403 ? ": the file needs a sign-in. Pick a file of a public repository" : "";
      return { error: `HTTP ${res.status}${why}.` };
    }
    const entry = performance
      .getEntriesByName(url, "resource")
      .filter((e) => e.startTime >= start - 1)
      .at(-1) as PerformanceResourceTiming | undefined;
    const timed = entry && entry.responseStart > 0 && entry.responseEnd > 0;
    const ttfb = timed ? entry.responseStart - entry.startTime : headersAt - start;
    const total = timed ? entry.responseEnd - entry.startTime : end - start;
    const transfer = timed ? entry.responseEnd - entry.responseStart : end - headersAt;
    const hit = res.headers.get("Telesphorepo-Edge")?.match(/region=([a-z]+)/)?.[1];
    const cache = res.headers.get("CF-Cache-Status") === "HIT" ? "Cloudflare cache" : "Server";
    const timing = (entry?.serverTiming ?? []).map((s) => `${s.name} ${Math.round(s.duration)} ms${s.description ? ` (${s.description})` : ""}`).join(", ");
    return {
      answeredBy: hit ? areaOf(hit) : cache,
      choice: t.header ? choiceOf(res.headers.get("Telesphorepo-Edge-Override")) : undefined,
      ttfb,
      total,
      speed: transfer > 0 ? bytes / (transfer / 1000) : undefined,
      timing: timing ? `Bucket reads in the Worker: ${timing}` : undefined,
    };
  }

  async function run(list: Target[]) {
    if (running) return;
    const p = pathOf();
    if ("error" in p) {
      runError = p.error;
      return;
    }
    runError = "";
    running = true;
    const keys = new Set(list.map((t) => t.key));
    results = list.length > 1 ? [] : results.filter((r) => !keys.has(r.key));
    try {
      for (const t of list) {
        results.push({ key: t.key, label: t.label, running: true });
        const i = results.length - 1;
        try {
          results[i] = { ...results[i], ...(await measure(p.path, t)), running: false };
        } catch (e) {
          results[i] = { ...results[i], running: false, error: errorMessage(e) };
        }
      }
    } finally {
      running = false;
    }
  }

  const source = {
    get items() {
      return results;
    },
    loading: false,
    error: "",
  };

  const workerNote = $derived.by(() => {
    if (!status.worker_version) return "No Worker has reported yet. Deploy it (Deploy, below) to test the regions.";
    if (status.version_mismatch) {
      return `The deployed Worker is ${status.worker_version} and this server is ${status.server_version}. Until you download the Worker of this version and deploy it (Deploy, below), the Worker sends every download to the server.`;
    }
    if (status.mode !== "on") return "Edge delivery is off, so the Worker sends every download to the server.";
    return "";
  });
</script>

{#snippet runButtons()}
  <Button disabled={running} onclick={() => run([target])}>{running ? "Running" : "Run"}</Button>
  <Button variant="outline" disabled={running} onclick={() => run(targets)}>Run all</Button>
{/snippet}

<Section
  title="Speed test"
  description="Download a file through the Worker from the bucket you pick, timed from this browser."
  footer={files.data && !files.data.unfit && onBase ? runButtons : undefined}
>
  {#if files.loading && !files.data}
    <Loading rows={3} />
  {:else if files.error && !files.data}
    <ErrorState error={files.error} title="The files to test did not load" onRetry={files.reload} />
  {:else if files.data}
    {@const data = files.data}
    <div class="flex flex-col gap-6">
      {#if data.unfit}
        <p class="text-body">The speed test needs edge delivery set up: {data.unfit}.</p>
      {:else if !onBase}
        <p class="text-body">
          Open this page at <a class="text-link underline-offset-4 hover:underline" href="{baseURL}/#/settings/edge">{baseURL}</a> to run the
          test. The browser downloads from the address the page runs on, and the Worker runs only on the base URL.
        </p>
      {:else}
        {#if workerNote}<p class="text-body">{workerNote}</p>{/if}
        <FormGrid>
          <FormRow
            label="File"
            for="speed-file"
            help={data.files.length > 0
              ? "The largest files of public repositories that the Worker serves. A file of a few megabytes gives a steady speed."
              : "No public repository has a file that the Worker serves from every region. Enter a path."}
          >
            <Select.Root type="single" value={fileChoice} onValueChange={(v) => (fileChoice = v)} disabled={running}>
              <Select.Trigger id="speed-file" class="w-full" aria-describedby="speed-file-help">
                <span class="truncate">{fileChoice ? fileLabel(fileChoice) : "Choose a file"}</span>
              </Select.Trigger>
              <Select.Content>
                {#each data.files as f (f.url)}
                  <Select.Item value={f.url} label={fileLabel(f.url)}>{fileLabel(f.url)}</Select.Item>
                {/each}
                <Select.Item value={OTHER} label="Another file">Another file</Select.Item>
              </Select.Content>
            </Select.Root>
          </FormRow>
          <FormRow label="Read from" for="speed-target" help="Nearest is the bucket that a client here gets; the others make the Worker read that bucket.">
            <Select.Root type="single" value={targetKey} onValueChange={(v) => (targetKey = v)} disabled={running}>
              <Select.Trigger id="speed-target" class="w-full" aria-describedby="speed-target-help">
                <span class="truncate">{target.label}</span>
              </Select.Trigger>
              <Select.Content>
                {#each targets as t (t.key)}
                  <Select.Item value={t.key} label={t.label}>{t.label}</Select.Item>
                {/each}
              </Select.Content>
            </Select.Root>
          </FormRow>
          {#if fileChoice === OTHER}
            <FormRow
              label="Path"
              for="speed-path"
              full
              help="The download path of a file of a public repository, such as /maven/releases/com/acme/app/1.0/app-1.0.jar, or its whole address."
            >
              <Input id="speed-path" bind:value={otherPath} disabled={running} aria-describedby="speed-path-help" />
            </FormRow>
          {/if}
        </FormGrid>

        <div class="flex flex-col gap-3">
          <DataTable
            {source}
            caption="Speed test results"
            key={(r) => r.key}
            columns={[
              { label: "Read from", grow: true },
              { label: "Answered by" },
              { label: "Choice", hideOnPhone: true },
              { label: "First byte", align: "right", hideOnPhone: true },
              { label: "Total", align: "right" },
              { label: "Speed", align: "right", hideOnPhone: true },
            ]}
          >
            {#snippet row(r)}
              <Cell grow>
                <span class="flex min-w-0 items-center gap-2">
                  <span class="truncate font-medium">{r.label}</span>
                  {#if r.key === fastest}<StatusBadge tone="success">Fastest</StatusBadge>{/if}
                </span>
              </Cell>
              {#if r.running}
                <Cell><span class="flex items-center gap-2 text-muted-foreground"><Spinner class="size-3.5" />Downloading</span></Cell>
                <Cell hideOnPhone />
                <Cell num hideOnPhone />
                <Cell num />
                <Cell num hideOnPhone />
              {:else if r.error}
                <Cell><span class="block max-w-80 truncate text-destructive" title={r.error}>{r.error}</span></Cell>
                <Cell hideOnPhone />
                <Cell num hideOnPhone />
                <Cell num />
                <Cell num hideOnPhone />
              {:else}
                <Cell text={r.answeredBy} />
                <Cell hideOnPhone>
                  {#if r.choice}<StatusBadge tone={r.choice.tone} title={r.choice.title}>{r.choice.word}</StatusBadge>{:else}<span class="text-muted-foreground">None</span>{/if}
                </Cell>
                <Cell num hideOnPhone><span title={r.timing}>{r.ttfb === undefined ? "" : ms(r.ttfb)}</span></Cell>
                <Cell num><span class={r.key === fastest ? "font-medium text-success" : ""}>{r.total === undefined ? "" : ms(r.total)}</span></Cell>
                <Cell num hideOnPhone>{r.speed === undefined ? "" : `${formatSize(Math.round(r.speed))}/s`}</Cell>
              {/if}
            {/snippet}
            {#snippet empty()}
              <CompactEmpty icon={GaugeIcon} title="Speed test" state="Not run yet" text="Run reads the file from one bucket; Run all reads it from each bucket in turn." />
            {/snippet}
          </DataTable>
          {#if results.length > 0}
            <p class="text-meta text-muted-foreground">
              Times are from this browser's location{colo ? `, through Cloudflare's ${colo} data center` : ""}. Each run downloads the whole file again,
              without the browser cache.
            </p>
          {/if}
        </div>
        {#if runError}<p class="text-body text-destructive">{runError}</p>{/if}
      {/if}
    </div>
  {/if}
</Section>
