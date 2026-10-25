<!--
  The speed test of Edge delivery: download one file through the Worker from the bucket the
  administrator picks, timed in this browser. The server issues a pass (POST /api/edge/speedtest)
  that lets the Worker take the choice from the headers Telesphorepo-Edge-Region and
  Telesphorepo-Edge-Pass; the answer says which bucket served (Telesphorepo-Edge) and whether the
  Worker took the choice (Telesphorepo-Edge-Override). Run tests one bucket, Run all every bucket one
  after another. The results are a list with one row per bucket, fastest first, and mark Nearest, the bucket the Worker picks for
  this browser when nothing is chosen (from a HEAD without a choice), and Fastest. A row says more
  only when the Worker did not read the bucket asked for. The browser downloads from the address it
  runs on, so the test runs only when the page is open at the base URL, where the Worker's routes are.
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
  import ItemList from "$lib/components/ItemList.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import StatusBadge, { type Tone } from "$lib/components/StatusBadge.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import FormatIcon from "$lib/components/FormatIcon.svelte";

  let { status }: { status: EdgeStatus } = $props();

  const files = new Resource(() => api.edge.speedFiles());

  // A bucket to read from: key is its location hint, header the value of Telesphorepo-Edge-Region.
  type Target = { key: string; label: string; header: string };
  const homeLabel = $derived(status.home.hint ? `Home bucket (${status.home.hint})` : "Home bucket");
  const areaOf = (hint: string) => {
    if (hint === status.home.hint) return homeLabel;
    const r = status.regions.find((x) => x.hint === hint);
    return r ? `${r.area} (${hint})` : hint;
  };
  const targets = $derived<Target[]>([
    { key: status.home.hint, label: homeLabel, header: "home" },
    ...status.regions
      .filter((r) => r.state === "ready" && r.hint !== status.home.hint)
      .map((r) => ({ key: r.hint, label: areaOf(r.hint), header: r.hint })),
  ]);
  let targetKey = $state("");
  const target = $derived(targets.find((t) => t.key === targetKey) ?? targets[0]);

  const OTHER = "other";
  let fileChoice = $state("");
  let otherPath = $state("");
  $effect(() => {
    if (fileChoice === "" && files.data) fileChoice = files.data.files[0]?.url ?? OTHER;
  });
  const chosenFile = $derived(files.data?.files.find((f) => f.url === fileChoice));
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
    // Set when the Worker did not read the bucket that was asked for: what happened instead.
    other?: { word: string; tone: Tone; title: string };
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
  // The hint of the bucket that the Worker picks for this browser when nothing is chosen.
  let nearest = $state("");

  const fastest = $derived.by(() => {
    const done = results.filter((r) => !r.running && !r.error && !r.other && r.total !== undefined);
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

  // otherOf says what happened when the Worker did not read the bucket that was asked for;
  // undefined when it did.
  const otherOf = (override: string | null, servedBy: string): Result["other"] => {
    if (override?.startsWith("applied")) return undefined;
    if (override === null) {
      return { word: "Went to the server", tone: "warning", title: "No Worker of this server's version answered the request." };
    }
    if (override.includes("reason=pass")) {
      return { word: `Pass refused: served from ${servedBy}`, tone: "danger", title: "The Worker did not accept the pass, so it read from the nearest bucket." };
    }
    if (override.includes("reason=region")) {
      return { word: `Served from ${servedBy}`, tone: "warning", title: "The Worker does not read this region yet (not ready, or no binding), so it read from the nearest bucket." };
    }
    return { word: "Went to the server", tone: "neutral", title: "The Worker does not answer this file from a bucket." };
  };

  const ms = (v: number) => (v >= 10_000 ? `${(v / 1000).toFixed(1)} s` : `${Math.round(v)} ms`);
  const hintOf = (res: Response) => res.headers.get("Telesphorepo-Edge")?.match(/region=([a-z]+)/)?.[1] ?? "";

  // measure downloads path once from the bucket of t, reading the whole body, and times it with
  // the browser's resource timing of the request (its own clock when the entry is missing).
  async function measure(path: string, t: Target): Promise<Omit<Result, "key" | "label" | "running">> {
    const headers = { "Telesphorepo-Edge-Region": t.header, "Telesphorepo-Edge-Pass": await passToken() };
    const url = new URL(path, location.origin).href;
    // Room for the entry of this request in the browser's resource timing buffer.
    performance.clearResourceTimings();
    const start = performance.now();
    const res = await fetch(url, { headers, cache: "no-store", credentials: "omit", redirect: "manual" });
    const headersAt = performance.now();
    if (res.type === "opaqueredirect") {
      return { error: "The server answered, not the Worker: deploy the Worker (Deploy, below)." };
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
    const hit = hintOf(res);
    const servedBy = hit ? areaOf(hit) : res.headers.get("CF-Cache-Status") === "HIT" ? "Cloudflare's cache" : "the server";
    const timing = (entry?.serverTiming ?? []).map((s) => `${s.name} ${Math.round(s.duration)} ms${s.description ? ` (${s.description})` : ""}`).join(", ");
    return {
      other: otherOf(res.headers.get("Telesphorepo-Edge-Override"), servedBy),
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
      // The bucket the Worker picks here when nothing is chosen, from a HEAD without a choice.
      try {
        const head = await fetch(new URL(p.path, location.origin).href, { method: "HEAD", cache: "no-store", credentials: "omit", redirect: "manual" });
        nearest = hintOf(head);
      } catch {
        nearest = "";
      }
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

  // The results ranked: the buckets that the Worker read, fastest total first; then the runs that
  // went elsewhere, failed or still run, in the order they ran.
  const timed = (r: Result) => !r.running && !r.error && !r.other && r.total !== undefined;
  const ranked = $derived([...results.filter(timed).sort((a, b) => a.total! - b.total!), ...results.filter((r) => !timed(r))]);
  const rankOf = (r: Result) => (timed(r) ? ranked.indexOf(r) + 1 : 0);
  const source = {
    get items() {
      return ranked;
    },
    loading: false,
    error: "",
  };

  const workerNote = $derived.by(() => {
    if (!status.worker_version) return "No Worker has reported yet. Deploy it (Deploy, below) to test the regions.";
    if (status.worker_outdated) return `${status.worker_outdated} Deploy it again (Deploy, below) to test the regions.`;
    if (status.mode !== "on") return "Edge delivery is off, so the Worker sends every download to the server.";
    return "";
  });
</script>

{#snippet fileIcon()}<FormatIcon format={chosenFile?.format ?? ""} />{/snippet}

{#snippet runButtons()}
  <Button disabled={running || !target} onclick={() => target && run([target])}>{running ? "Running" : "Run"}</Button>
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
        <!-- The reason shows under Status when it is the same one. -->
        <p class="text-body">The speed test needs edge delivery set up{data.unfit === status.check_unfit ? "." : `: ${data.unfit}.`}</p>
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
              <Select.Trigger id="speed-file" class="w-full" aria-describedby="speed-file-help" icon={chosenFile ? fileIcon : undefined}>
                <span class="truncate">{fileChoice ? fileLabel(fileChoice) : "Choose a file"}</span>
              </Select.Trigger>
              <Select.Content>
                {#each data.files as f (f.url)}
                  <Select.Item value={f.url} label={fileLabel(f.url)}>
                    {#snippet icon()}<FormatIcon format={f.format} />{/snippet}
                  </Select.Item>
                {/each}
                <Select.Item value={OTHER} label="Another file" inset />
              </Select.Content>
            </Select.Root>
          </FormRow>
          <FormRow label="Bucket" for="speed-target" help="Run reads the file from this bucket. Run all reads it from every bucket in turn.">
            <Select.Root type="single" value={target?.key ?? ""} onValueChange={(v) => (targetKey = v)} disabled={running}>
              <Select.Trigger id="speed-target" class="w-full" aria-describedby="speed-target-help">
                <span class="truncate">{target?.label ?? "Choose a bucket"}</span>
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
          <ItemList {source} caption="Speed test results, fastest first" key={(r) => r.key}>
            {#snippet leading(r)}
              <span class="w-5 text-center text-label tabular-nums">{rankOf(r) || ""}</span>
            {/snippet}
            {#snippet title(r)}{r.label}{/snippet}
            {#snippet badges(r)}
              {#if r.key === nearest}<StatusBadge tone="neutral" title="The bucket the Worker picks for this browser when nothing is chosen.">Nearest</StatusBadge>{/if}
              {#if r.key === fastest}<StatusBadge tone="success">Fastest</StatusBadge>{/if}
              {#if r.other}<StatusBadge tone={r.other.tone} title={r.other.title}>{r.other.word}</StatusBadge>{/if}
            {/snippet}
            {#snippet meta(r)}
              {#if r.running}
                <span class="inline-flex items-center gap-2"><Spinner class="size-3.5" />Downloading</span>
              {:else if r.error}
                <span class="text-destructive" title={r.error}>{r.error}</span>
              {:else if r.ttfb !== undefined && r.total !== undefined}
                <span class="tabular-nums" title={r.timing}>First byte {ms(r.ttfb)} · total {ms(r.total)}</span>
              {/if}
            {/snippet}
            {#snippet trailing(r)}
              {#if r.speed !== undefined}
                <span class={r.key === fastest ? "text-label text-success tabular-nums" : "text-body tabular-nums"}>{formatSize(Math.round(r.speed))}/s</span>
              {/if}
            {/snippet}
            {#snippet empty()}
              <CompactEmpty icon={GaugeIcon} title="Speed test" state="Not run yet" text="Run reads the file from one bucket; Run all reads it from every bucket in turn." />
            {/snippet}
          </ItemList>
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
