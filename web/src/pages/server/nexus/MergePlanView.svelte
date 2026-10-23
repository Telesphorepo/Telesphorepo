<!--
  The plan of a merge (Nexus migration permission), the body of /settings/nexus/merge?from=<moving>&into=<copy>
  (into empty: the repository that from moves into). It reads what the merge would do from the
  files of both repositories now (GET /api/nexus/merges/plan): the files it copies, skips and
  leaves to the format, the conflicts with a choice each (keep the file of the copy, or take the
  new one), the files the format cannot take, which name stays with who changes what, and what
  else moves. Start asks first, then opens the merge's page.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import { api, type NexusMergeChoice, type NexusMergePlan } from "$lib/api";
  import { Resource, type TableSource } from "$lib/load.svelte";
  import { href, navigate, repoPath, repoSettingsPath } from "$lib/router.svelte";
  import { formatCount, formatRelative } from "$lib/format";
  import { count, someNames } from "$lib/words";
  import * as RadioGroup from "$lib/components/ui/radio-group";
  import { Button } from "$lib/components/ui/button";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import ItemList from "$lib/components/ItemList.svelte";
  import OptionRow from "$lib/components/OptionRow.svelte";
  import Segmented from "$lib/components/Segmented.svelte";
  import FileSize from "$lib/components/FileSize.svelte";
  import Truncate from "$lib/components/Truncate.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import FileWarningIcon from "@lucide/svelte/icons/file-warning";
  import FileIcon from "@lucide/svelte/icons/file";

  let { from, into }: { from: string; into: string } = $props();

  const fid = $props.id();
  const plan = new Resource(() => api.nexus.mergePlan(from, into || undefined));
  const p = $derived(plan.data);

  // The choices: one per listed conflict, and the choice of every other one.
  let choices = $state<Record<string, NexusMergeChoice>>({});
  let otherwise = $state<NexusMergeChoice | "">("");
  let keep = $state<"target" | "source">("target");

  const unlisted = $derived(p ? p.conflict_count - p.conflicts.length : 0);
  const open = $derived(p ? p.conflicts.filter((c) => !choices[c.path]).length + (otherwise ? 0 : unlisted) : 0);
  function chooseAll(c: NexusMergeChoice) {
    if (!p) return;
    choices = Object.fromEntries(p.conflicts.map((x) => [x.path, c]));
    otherwise = c;
  }
  const choiceOf = (path: string) => choices[path] ?? otherwise ?? "";

  const conflictSource: TableSource<NexusMergePlan["conflicts"][number]> = {
    get items() {
      return p?.conflicts ?? [];
    },
    loading: false,
    error: "",
  };
  const blockedSource: TableSource<NexusMergePlan["blocked"][number]> = {
    get items() {
      return p?.blocked ?? [];
    },
    loading: false,
    error: "",
  };

  const short = (sha: string) => sha.slice(0, 12);
  const kept = $derived(p ? (keep === "target" ? p.into : p.from) : "");
  const redirect = $derived(p ? (keep === "target" ? p.from : p.into) : "");
  const group = $derived(p?.groups.find((g) => g === `${p.into}-all`) ?? "");
  const otherGroups = $derived(p ? p.groups.filter((g) => g !== group) : []);
  const startProblem = $derived.by(() => {
    if (!p) return "";
    if (p.busy) return p.busy;
    if (p.blocked_count) return `${count(p.blocked_count, "file", "files")} cannot go into ${p.into}.`;
    if (open) return `Choose what happens to ${count(open, "conflict", "conflicts")}.`;
    return "";
  });

  let confirmOpen = $state(false);
  async function start() {
    if (!p) return;
    const m = await api.nexus.startMerge({ from: p.from, into: p.into, keep, choices, otherwise });
    toast.success(`Started the merge of ${p.from} into ${p.into}`);
    navigate(`/settings/nexus/merges/${m.id}`);
  }

  const link = "rounded-sm text-link underline-offset-4 hover:underline";
  const crumbs = $derived([
    { label: "Nexus migration", href: href("/settings/nexus") },
    ...(p ? [{ label: p.source_name || "Nexus server", href: href(`/settings/nexus/${p.source_id}`) }, { label: `Merge ${p.from}` }] : [{ label: `Merge ${from}` }]),
  ]);
</script>

{#if plan.loading && !p}
  <PageHeader title="Merge {from}{into ? ` into ${into}` : ''}" {crumbs} />
  <Loading rows={6} label="Reading the files of {from}" />
{:else if plan.error && !p}
  <PageHeader title="Merge {from}{into ? ` into ${into}` : ''}" {crumbs} />
  <ErrorState error={plan.error} title="The plan did not load" onRetry={plan.reload} />
{:else if p}
  <PageHeader title="Merge {p.from} into {p.into}" description="Every file of {p.from} goes into {p.into}, which then takes uploads, and one of the two names stays." {crumbs}>
    {#snippet actions()}
      {#if p.merge_id}
        <Button variant="outline" href={href(`/settings/nexus/merges/${p.merge_id}`)}>Open the merge</Button>
      {/if}
    {/snippet}
  </PageHeader>

  <Section title="Files" description="From the files of both repositories now. The merge reads them again when it runs.">
    <ul class="space-y-2 text-body tabular-nums">
      <li><span class="font-medium">{formatCount(p.copy)}</span> {p.copy === 1 ? "file goes" : "files go"} into {p.into}, by reference: no bytes are copied.</li>
      <li><span class="font-medium">{formatCount(p.skip)}</span> {p.skip === 1 ? "file is" : "files are"} in {p.into} with the same bytes, and {p.skip === 1 ? "is" : "are"} skipped.</li>
      <li><span class="font-medium">{formatCount(p.rebuild)}</span> metadata and index {p.rebuild === 1 ? "file is" : "files are"} rebuilt or merged by the format, not copied.</li>
      {#if p.labels}
        <li><span class="font-medium">{formatCount(p.labels)}</span> {p.labels === 1 ? "tag goes" : "tags go"} into {p.into} as {p.from} has {p.labels === 1 ? "it" : "them"}, where {p.into} has none or not the package.</li>
      {/if}
      {#if p.conflict_count}
        <li class="text-warning"><span class="font-medium">{formatCount(p.conflict_count)}</span> {p.conflict_count === 1 ? "path is" : "paths are"} in both with other bytes: choose below.</li>
      {/if}
      {#if p.blocked_count}
        <li class="text-destructive"><span class="font-medium">{formatCount(p.blocked_count)}</span> {p.blocked_count === 1 ? "file" : "files"} cannot go into {p.into}.</li>
      {/if}
    </ul>
  </Section>

  {#if p.blocked_count}
    <Section title="Files the merge cannot take" description="The format takes these only from its clients. Delete them from {p.from}, or publish them to {p.into}, then open the plan again.">
      <ItemList source={blockedSource} caption="Files the merge cannot take" key={(b) => b.path}>
        {#snippet leading()}<FileWarningIcon class="size-5" />{/snippet}
        {#snippet title(b)}<Truncate text={b.path} />{/snippet}
        {#snippet meta(b)}{b.reason}{/snippet}
      </ItemList>
      {#if p.blocked_count > p.blocked.length}
        <p class="mt-4 text-meta text-muted-foreground">And {formatCount(p.blocked_count - p.blocked.length)} more.</p>
      {/if}
    </Section>
  {/if}

  {#if p.conflict_count}
    <Section title="Conflicts" description="Both hold these with other contents, where the format keeps one fixed (a release, a tag). Keep what {p.into} has, or take what {p.from} has in its place.">
      {#snippet badges()}
        {#if open}<StatusBadge tone="warning">{formatCount(open)} to choose</StatusBadge>{/if}
      {/snippet}
      {#snippet actions()}
        <Button variant="outline" size="sm" onclick={() => chooseAll("keep")}>Keep all</Button>
        <Button variant="outline" size="sm" onclick={() => chooseAll("take")}>Take all</Button>
      {/snippet}
      <ItemList source={conflictSource} caption="Conflicts" key={(c) => c.path}>
        {#snippet leading()}<FileIcon class="size-5" />{/snippet}
        {#snippet title(c)}{#if c.kind === "label"}<Truncate text={c.title} />{:else}<Truncate text={c.path} copy />{/if}{/snippet}
        {#snippet meta(c)}
          {#if c.kind === "label"}
            <span class="tabular-nums">{p.into}: {c.target_value} · {p.from}: {c.value}</span>
          {:else}
          <span class="tabular-nums">
            {p.into}: <FileSize bytes={c.target_size} />, {formatRelative(c.target_updated_at)}, {short(c.target_sha256)} · {p.from}: <FileSize bytes={c.size} />, {formatRelative(c.updated_at)}, {short(c.sha256)}
          </span>
          {/if}
        {/snippet}
        {#snippet trailing(c)}
          <Segmented
              compact
              label="What happens to {c.path}"
              options={[
                { value: "keep", label: "Keep", title: `Keep the file of ${p.into}; the one of ${p.from} goes with it` },
                { value: "take", label: "Take", title: `Delete the file of ${p.into} and take the one of ${p.from}` },
              ]}
              bind:value={() => choiceOf(c.path) as NexusMergeChoice, (v) => (choices = { ...choices, [c.path]: v })}
            />
        {/snippet}
      </ItemList>
      {#if unlisted > 0}
        <p class="mt-4 text-meta text-muted-foreground">
          And {formatCount(unlisted)} more, which take the choice for every file{otherwise ? `: ${otherwise === "keep" ? `keep the file of ${p.into}` : `take the file of ${p.from}`}` : "; choose it with the buttons above"}.
        </p>
      {/if}
    </Section>
  {/if}

  <Section title="Which name stays" description="The other name becomes a redirect: every request to it, publishes included, is served by the merged repository.">
    <RadioGroup.Root class="gap-1" aria-label="Which name stays" bind:value={keep}>
      <OptionRow
        id="{fid}-keep-target"
        label="Keep {p.into}"
        description="{p.from} becomes a redirect to {p.into}. Projects that publish to {p.from} keep publishing, unchanged; change them to {p.into} when you like. Builds that read {p.into}{group ? ` or ${group}` : ''} change nothing."
      >
        {#snippet control()}<RadioGroup.Item id="{fid}-keep-target" value="target" />{/snippet}
      </OptionRow>
      <OptionRow
        id="{fid}-keep-source"
        label="Keep {p.from}"
        description="{p.into} is renamed to {p.from}, and {p.into} becomes a redirect. Projects that publish to {p.from} change nothing. Builds that read {p.into} keep working through the redirect; change them to {p.from} when you like."
      >
        {#snippet control()}<RadioGroup.Item id="{fid}-keep-source" value="source" />{/snippet}
      </OptionRow>
    </RadioGroup.Root>
    {#if p.from_redirects.length || p.into_redirects.length}
      <p class="mt-4 text-meta text-muted-foreground">
        {#if p.from_redirects.length}{someNames(p.from_redirects)} {p.from_redirects.length === 1 ? "answers" : "answer"} for {p.from} and will answer for {kept}.{/if}
        {#if p.into_redirects.length}{someNames(p.into_redirects)} {p.into_redirects.length === 1 ? "answers" : "answer"} for {p.into} and keep{p.into_redirects.length === 1 ? "s" : ""} doing so.{/if}
      </p>
    {/if}
  </Section>

  <Section title="What else changes">
    {#snippet footer()}
      <Button disabled={!!startProblem} onclick={() => (confirmOpen = true)}>Start the merge</Button>
      {#if startProblem}<p class="text-meta text-warning">{startProblem}</p>{/if}
    {/snippet}
    <ul class="space-y-2 text-body">
      {#if p.copying}
        <li>A final sync copies the newest files of {p.into} from {p.source_name || "Nexus"} first. Then {p.into} switches over: uploads go to it, and it stops copying from Nexus, for good.</li>
      {:else}
        <li>{p.into} switched over already: nothing copies from Nexus.</li>
      {/if}
      <li>
        {#if p.people.length}{someNames(p.people)} {p.people.length === 1 ? "gets" : "get"} the same access to {kept} as to {p.from}.{:else}Nobody has access to {p.from} of their own.{/if}
        {#if p.pipelines.length}
          The CI {p.pipelines.length === 1 ? "pipeline" : "pipelines"} {someNames(p.pipelines)} also {p.pipelines.length === 1 ? "publishes" : "publish"} to {kept}.
        {/if}
      </li>
      {#if group}
        <li>The group <a class={link} href={href(repoSettingsPath(group))}>{group}</a> keeps reading {kept}.</li>
      {/if}
      {#each otherGroups as g (g)}
        <li>The group <a class={link} href={href(repoSettingsPath(g))}>{g}</a> reads {kept} in place of {p.from}.</li>
      {/each}
      <li>Then {p.from} is deleted as a repository, and {redirect} answers for {kept} as a redirect.</li>
      <li class="text-muted-foreground">While the merge runs, uploads to <a class={link} href={href(repoPath(p.from))}>{p.from}</a> and <a class={link} href={href(repoPath(p.into))}>{p.into}</a> wait: they answer 409. Downloads work.</li>
    </ul>
  </Section>

  <ConfirmDialog
    bind:open={confirmOpen}
    title="Merge {p.from} into {p.into}?"
    confirmLabel="Merge {p.from} into {p.into}"
    destructive
    changes={[
      ...(p.copying ? [`Runs a final sync of ${p.into} from Nexus, then switches it over for good`] : []),
      `Puts ${count(p.copy, "file", "files")} of ${p.from} into ${p.into}`,
      ...(p.conflict_count ? [`Resolves ${count(p.conflict_count, "conflict", "conflicts")} as you chose`] : []),
      `Keeps the name ${kept}; ${redirect} becomes a redirect`,
      `Deletes the repository ${p.from}`,
    ]}
    onConfirm={start}
  />
{/if}
