<!--
  One merge (Nexus migration permission), the body of /settings/nexus/merges/:id. While it runs, its steps
  with the copy's progress, refreshed every two seconds (a Live with its LiveStatus in the
  header). A failed merge names the step and why, with Resume and Stop for the holders of All
  repositories. A done merge says what changed, and gives the setup line that points a project at
  the merged repository.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import { api, type NexusMerge } from "$lib/api";
  import { auth } from "$lib/auth.svelte";
  import { Resource, type TableSource } from "$lib/load.svelte";
  import { Live } from "$lib/live.svelte";
  import { href, repoPath, repoSettingsPath } from "$lib/router.svelte";
  import { formatCount } from "$lib/format";
  import { count } from "$lib/words";
  import { cn } from "$lib/utils";
  import { Button } from "$lib/components/ui/button";
  import { Spinner } from "$lib/components/ui/spinner";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import ItemList from "$lib/components/ItemList.svelte";
  import StatusBadge, { type Tone } from "$lib/components/StatusBadge.svelte";
  import LiveStatus from "$lib/components/LiveStatus.svelte";
  import LiveValue from "$lib/components/LiveValue.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import MergeSetup from "./MergeSetup.svelte";
  import CircleCheckIcon from "@lucide/svelte/icons/circle-check";
  import CircleIcon from "@lucide/svelte/icons/circle";
  import CircleXIcon from "@lucide/svelte/icons/circle-x";

  let { id, query }: { id: number; query: URLSearchParams } = $props();

  const merge = new Resource<NexusMerge>(() => api.nexus.merge(id));
  const m = $derived(merge.data);

  const live = new Live({
    every: 2000,
    while: () => m?.state === "running",
    run: async () => {
      await merge.fetch();
      if (merge.data?.state === "done") toast.success(`Merged ${merge.data.from} into ${merge.data.name || merge.data.into}`);
    },
  });

  const states: Record<NexusMerge["state"], { word: string; tone: Tone }> = {
    running: { word: "Running", tone: "info" },
    failed: { word: "Failed", tone: "danger" },
    done: { word: "Done", tone: "success" },
    stopped: { word: "Stopped", tone: "neutral" },
  };

  type Step = { key: NexusMerge["step"]; title: string };
  const order: NexusMerge["step"][] = ["sync", "copy", "access", "switch", "retire", "rename", "done"];
  const steps = $derived.by((): Step[] => {
    if (!m) return [];
    const out: Step[] = [
      { key: "sync", title: `Final sync of ${m.into} from Nexus` },
      { key: "copy", title: `Put the files of ${m.from} into ${m.into}` },
      { key: "access", title: `Give ${m.into} the access, CI pipelines and groups of ${m.from}` },
      { key: "switch", title: `Switch ${m.into} over` },
      { key: "retire", title: `Let ${m.from} answer for ${m.into}, and delete ${m.from}` },
    ];
    if (m.keep === "source") out.push({ key: "rename", title: `Rename ${m.into} to ${m.from}` });
    return out;
  });
  const stepSource: TableSource<Step> = {
    get items() {
      return steps;
    },
    loading: false,
    error: "",
  };
  /** done, now, failed or later, for one step of the merge. */
  function stateOf(s: Step): "done" | "now" | "failed" | "later" {
    if (!m) return "later";
    const at = order.indexOf(m.step);
    const i = order.indexOf(s.key);
    if (m.state === "done" || i < at) return "done";
    if (i > at) return "later";
    return m.state === "failed" ? "failed" : m.state === "running" ? "now" : "later";
  }
  const percent = $derived(m && m.files > 0 ? Math.min(100, Math.round((m.done / m.files) * 100)) : 0);
  const copyLine = (x: NexusMerge) =>
    [
      `${formatCount(x.done)} of ${count(x.files, "file", "files")}`,
      `${formatCount(x.copied)} copied`,
      `${formatCount(x.skipped)} skipped`,
      x.rebuilt ? `${formatCount(x.rebuilt)} rebuilt` : "",
      x.kept ? `${formatCount(x.kept)} kept as they were` : "",
      x.taken ? `${formatCount(x.taken)} taken from ${x.from}` : "",
    ]
      .filter(Boolean)
      .join(" · ");

  // Resume and Stop are the repository operation's (the holders of All repositories).
  const opRepo = $derived(m ? m.name || m.into : "");
  let resumeOpen = $state(false);
  let stopOpen = $state(false);
  async function resume() {
    await api.repos.resumeOperation(opRepo);
    toast.success("Resumed the merge");
    await merge.reload();
  }
  async function stop() {
    await api.repos.clearOperation(opRepo);
    toast.success("Stopped the merge");
    await merge.reload();
  }

  const link = "rounded-sm text-link underline-offset-4 hover:underline";
  const crumbs = $derived([
    { label: "Nexus migration", href: href("/settings/nexus") },
    ...(m ? [{ label: m.source_name || "Nexus server", href: href(`/settings/nexus/${m.source_id}`) }, { label: `Merge ${m.from}` }] : [{ label: "Merge" }]),
  ]);
</script>

{#if merge.loading && !m}
  <PageHeader title="Merge" {crumbs} />
  <Loading rows={6} />
{:else if merge.error && !m}
  <PageHeader title="Merge" {crumbs} />
  <ErrorState error={merge.error} title="The merge did not load" onRetry={merge.reload} />
{:else if m}
  <PageHeader title="Merge {m.from} into {m.into}" {crumbs}>
    {#snippet meta()}
      <StatusBadge tone={states[m.state].tone}>{states[m.state].word}</StatusBadge>
      <span class="text-meta text-muted-foreground">
        Started <RelativeTime at={m.started_at} />{m.started_by ? ` by ${m.started_by}` : ""}
      </span>
    {/snippet}
    {#snippet actions()}
      <LiveStatus {live} />
    {/snippet}
  </PageHeader>

  <Section title="Steps">
    {#snippet actions()}
      {#if m.state === "failed" && auth.allRepos()}
        <Button variant="outline" onclick={() => (resumeOpen = true)}>Resume</Button>
        <Button variant="ghost" class="hover:bg-destructive/10 hover:text-destructive" onclick={() => (stopOpen = true)}>Stop</Button>
      {/if}
    {/snippet}
    <ItemList source={stepSource} caption="Steps of the merge" key={(s) => s.key}>
      {#snippet leading(s)}
        {@const st = stateOf(s)}
        {#if st === "done"}<CircleCheckIcon class="size-5 text-success" aria-label="Done" />
        {:else if st === "now"}<Spinner class="size-5" aria-label="Running" />
        {:else if st === "failed"}<CircleXIcon class="size-5 text-destructive" aria-label="Failed" />
        {:else}<CircleIcon class="size-5 text-muted-foreground" aria-label="Later" />{/if}
      {/snippet}
      {#snippet title(s)}
        <span class={cn(stateOf(s) === "later" && "text-muted-foreground")}>{s.title}</span>
      {/snippet}
      {#snippet meta(s)}
        {@const st = stateOf(s)}
        {#if st === "failed"}
          <span class="text-destructive">{m.error}</span>
        {:else if s.key === "sync"}
          {#if m.sync_id}
            <a class={link} href={href(`/settings/nexus/${m.source_id}`, { sync: String(m.sync_id) })}>Open the sync</a>
          {:else if st === "done"}
            {m.into} copied nothing from Nexus any more
          {:else}
            Copies the newest files of {m.into} from Nexus
          {/if}
        {:else if s.key === "copy" && st !== "later"}
          <span class="tabular-nums"><LiveValue value={m.done}>{copyLine(m)}</LiveValue></span>
        {:else if s.key === "switch"}
          Uploads go to {m.into} on this server, and it stops copying from Nexus
        {:else if s.key === "retire"}
          {m.from} becomes a redirect, so its projects keep publishing
        {/if}
      {/snippet}
      {#snippet trailing(s)}
        {#if s.key === "copy" && stateOf(s) === "now"}
          <div class="h-2 w-24 overflow-hidden rounded-full bg-muted" role="progressbar" aria-label="Files put into {m.into}" aria-valuemin={0} aria-valuemax={100} aria-valuenow={percent}>
            <div class="h-full rounded-full bg-primary transition-[width]" style="width: {percent}%"></div>
          </div>
        {/if}
      {/snippet}
    </ItemList>
    {#if m.state === "failed" && !auth.allRepos()}
      <p class="mt-4 text-meta text-muted-foreground">An administrator with All repositories can resume or stop it.</p>
    {:else if m.state === "failed"}
      <p class="mt-4 text-meta text-muted-foreground">{m.into} keeps copying from Nexus until the merge switches it over, and {m.from} keeps its files.</p>
    {:else if m.state === "stopped"}
      <p class="mt-4 text-meta text-muted-foreground">Stopped before the switch-over: {m.into} keeps copying from Nexus, and {m.from} keeps its files and takes uploads again.</p>
    {/if}
  </Section>

  {#if m.state === "done" && m.name}
    <Section title="What changed">
      <ul class="space-y-2 text-body">
        <li><a class={link} href={href(repoPath(m.name))}>{m.name}</a> holds every file: {copyLine(m)}.</li>
        <li>
          <span class="font-medium">{m.redirect}</span> answers for {m.name} as a redirect, publishes included.
          <a class={link} href={href(repoSettingsPath(m.name))}>See its redirects</a>
        </li>
        {#each m.groups as g (g)}
          <li>The group <a class={link} href={href(repoSettingsPath(g))}>{g}</a> reads {m.name}.</li>
        {/each}
        <li>{m.name} takes uploads on this server and copies nothing from Nexus.</li>
      </ul>
    </Section>
    <Section title="Point a project at {m.name}" description="Projects that publish to {m.redirect} keep working through the redirect. Change one when you like: run the setup line in its folder.">
      <MergeSetup name={m.name} {query} />
    </Section>
  {/if}

  <ConfirmDialog
    bind:open={resumeOpen}
    title="Resume the merge?"
    confirmLabel="Resume"
    changes={["Runs a final sync from Nexus first when the repository still copies from it", "Copies the files that are not copied yet", "Then switches over and leaves the old name as a redirect"]}
    onConfirm={resume}
  />
  <ConfirmDialog
    bind:open={stopOpen}
    title="Stop the merge?"
    confirmLabel="Stop the merge"
    destructive
    changes={[`Keeps the files copied so far in ${m.into}`, `${m.into} keeps copying from Nexus`, `${m.from} keeps its files and takes uploads again`]}
    onConfirm={stop}
  />
{/if}
