<!--
  The versions of one artifact, newest first: one dense line per version with its kind, its builds
  (a snapshot shows "latest build · 11 earlier"), the commit it was built from and when it was
  published. The newest release is marked. With `flow`, Cover Flow shows the same versions as felt
  tickets in place of the list, in the same area. A row opens its version.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import { errorMessage, type Repo } from "$lib/api";
  import { Keyset } from "$lib/load.svelte";
  import { filesPath, go, href } from "$lib/router.svelte";
  import { Button } from "$lib/components/ui/button";
  import DataTable, { type Column } from "$lib/components/DataTable.svelte";
  import Cell from "$lib/components/Cell.svelte";
  import Truncate from "$lib/components/Truncate.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import CoverFlow, { type Ticket } from "./CoverFlow.svelte";
  import BuildInline from "./BuildInline.svelte";
  import MemberTag from "./MemberTag.svelte";
  import type { DeleteTarget } from "./FolderList.svelte";
  import { api, type Artifact, type Version } from "$lib/api";
  import { shortSha } from "../browse";
  import { versionTarget } from "./place";
  import Loading from "$lib/components/Loading.svelte";
  import { formatDateTime } from "$lib/format";
  import { cn } from "$lib/utils";
  import { viewBox } from "./area";
  import TagIcon from "@lucide/svelte/icons/tag";
  import Trash2Icon from "@lucide/svelte/icons/trash-2";
  import FolderOpenIcon from "@lucide/svelte/icons/folder-open";
  import ArrowRightIcon from "@lucide/svelte/icons/arrow-right";

  let {
    repo,
    artifact,
    flow,
    canWrite,
    onDelete,
  }: { repo: Repo; artifact: Artifact; flow: boolean; canWrite: boolean; onDelete: (t: DeleteTarget) => void } = $props();

  /* svelte-ignore state_referenced_locally */
  const versions = new Keyset<Version>((p) => api.repos.versions(repo.name, artifact.path, p), 50);

  // The newest release: the version most builds should use.
  const latest = $derived(versions.items.find((v) => v.kind === "release")?.version ?? "");
  const anySnapshot = $derived(versions.items.some((v) => v.kind === "snapshot"));

  let chosen = $state("");
  const selected = $derived(chosen || versions.items[0]?.version || "");

  const day = new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short", year: "numeric" });
  const tickets = $derived<Ticket[]>(
    versions.items.map((v) => ({
      key: v.version,
      top: v.version,
      bottom: day.format(new Date(v.published_at)),
      serial: v.build?.revision ? shortSha(v.build.revision) : v.kind === "snapshot" ? "Snapshot" : "Release",
      label: `${v.version}, ${v.kind}, published ${formatDateTime(v.published_at)}`,
    })),
  );
  const byVersion = $derived(new Map(versions.items.map((v) => [v.version, v])));
  const open = (key: string) => {
    const v = byVersion.get(key);
    if (v) go(versionHref(v));
  };

  /** A version's place: the artifact folder and ?version=. */
  const versionHref = (v: Version) => href(filesPath(repo.name, artifact.path), { version: v.version });

  async function deleteVersion(v: Version) {
    try {
      onDelete(await versionTarget(repo.name, artifact, v));
    } catch (e) {
      toast.error(errorMessage(e));
    }
  }

  const builds = (v: Version) =>
    v.kind !== "snapshot" ? "" : v.builds_count > 1 ? `latest build · ${v.builds_count - 1} earlier` : "1 build";

  const columns = $derived<Column[]>([
    { label: "Version", grow: true },
    ...(anySnapshot ? [{ label: "Builds", hideOnPhone: true }] : []),
    { label: "Commit", hideOnPhone: true },
    { label: "Published", align: "right" as const },
    ...(canWrite ? [{ label: "", srLabel: "Actions" }] : []),
  ]);
</script>

{#snippet badges(v: Version)}
  <!-- On a phone the version name keeps its room: "Latest" alone, and no Snapshot badge (the name says it). -->
  {#if v.version === latest}
    <StatusBadge tone="success" title="The newest release">Latest<span class="max-sm:hidden">&nbsp;release</span></StatusBadge>
  {:else if v.kind === "snapshot"}
    <span class="max-sm:hidden"><StatusBadge title="A snapshot changes with each build">Snapshot</StatusBadge></span>
  {/if}
{/snippet}

{#if flow && versions.items.length}
  <CoverFlow
    {tickets}
    {selected}
    onSelect={(k) => (chosen = k)}
    onOpen={open}
    label="Versions of {artifact.title}"
    hasMore={versions.hasMore}
    loadingMore={versions.loadingMore}
    more={versions.more}
  >
    {#snippet caption(t)}
      {@const v = byVersion.get(t.key)}
      {#if v}
        <div class="flex flex-wrap items-center gap-x-4 gap-y-3">
          <div class="flex min-w-0 grow basis-64 flex-col gap-1">
            <p class="flex flex-wrap items-center gap-2">
              <span class="text-section">{v.version}</span>
              {@render badges(v)}
              <MemberTag repo={v.repo} />
            </p>
            <p class="flex flex-wrap items-center gap-x-3 gap-y-1 text-meta text-muted-foreground">
              <span>Published <RelativeTime at={v.published_at} /></span>
              {#if builds(v)}<span>{builds(v)}</span>{/if}
              <BuildInline p={v.build} />
            </p>
          </div>
          <div class="flex shrink-0 items-center gap-2">
            {#if canWrite}
              <Button
                variant="ghost"
                size="icon"
                class="text-muted-foreground hover:text-destructive"
                aria-label="Delete version {v.version}"
                title="Delete the version"
                onclick={() => deleteVersion(v)}
              >
                <Trash2Icon />
              </Button>
            {/if}
            <Button variant="outline" href={versionHref(v)}>Open {v.version}<ArrowRightIcon /></Button>
          </div>
        </div>
      {/if}
    {/snippet}
  </CoverFlow>
{:else if flow && versions.loading}
  <div class={cn("rounded-lg bg-panel ring-1 ring-border", viewBox)}><Loading rows={8} class="h-full" /></div>
{:else}
  <DataTable
    source={versions}
    {columns}
    caption="Versions of {artifact.title}"
    key={(v) => v.version}
    href={versionHref}
  >
    {#snippet row(v)}
      <Cell grow>
        <span class="flex min-w-0 items-center gap-2">
          <TagIcon aria-hidden="true" class="size-4 shrink-0 text-link" />
          <a
            href={versionHref(v)}
            class="flex min-w-0 rounded-sm font-medium outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
            ><Truncate text={v.version} /></a
          >
          {@render badges(v)}
          <MemberTag repo={v.repo} />
        </span>
      </Cell>
      {#if anySnapshot}<Cell hideOnPhone muted class="whitespace-nowrap">{builds(v)}</Cell>{/if}
      <Cell hideOnPhone muted><BuildInline p={v.build} /></Cell>
      <Cell num muted><RelativeTime at={v.published_at} /></Cell>
      {#if canWrite}
        <Cell actions>
          <Button
            variant="ghost"
            size="icon-sm"
            class="text-muted-foreground hover:text-destructive"
            aria-label="Delete version {v.version}"
            title="Delete the version"
            onclick={() => deleteVersion(v)}
          >
            <Trash2Icon />
          </Button>
        </Cell>
      {/if}
    {/snippet}
    {#snippet empty()}
      <CompactEmpty icon={FolderOpenIcon} title="Versions" state="None" text="Every version of this artifact was deleted." />
    {/snippet}
  </DataTable>
{/if}
