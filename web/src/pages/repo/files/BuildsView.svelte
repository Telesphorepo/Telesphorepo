<!--
  One version: the files of one build, without checksum and signature files (Maven: pom first, then
  the main file, the Gradle module metadata and the classified files; Debian: one .deb for each
  architecture). A release has one build. A
  snapshot shows only its latest build; "11 earlier builds" opens the list of the others, and a
  row of that list shows its files (?build= keeps the choice in the address; a build named there
  that the loaded pages lack is asked for by its id, ?build= of GET /builds). A file row opens its
  details. With `flow`, Cover Flow shows the builds as felt tickets in place of the files, in the
  same area; opening a ticket shows its files in the List view.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import type { Repo } from "$lib/api";
  import { Keyset, Resource, type TableSource } from "$lib/load.svelte";
  import { filesPath, href, navigate } from "$lib/router.svelte";
  import { Button } from "$lib/components/ui/button";
  import DataTable from "$lib/components/DataTable.svelte";
  import ItemList from "$lib/components/ItemList.svelte";
  import Cell from "$lib/components/Cell.svelte";
  import Truncate from "$lib/components/Truncate.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import FileSize from "$lib/components/FileSize.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import CoverFlow, { type Ticket } from "./CoverFlow.svelte";
  import BuildInline from "./BuildInline.svelte";
  import MemberTag from "./MemberTag.svelte";
  import type { DeleteTarget } from "./FolderList.svelte";
  import { api, type Artifact, type Build, type BuildFile, type Version } from "$lib/api";
  import { formats } from "$lib/formats/registry.svelte";
  import { buildInline, buildLabel, parentOf, shortSha } from "../browse";
  import { formatCount, formatDateTime } from "$lib/format";
  import { count } from "$lib/words";
  import { cn } from "$lib/utils";
  import { viewBox } from "./area";
  import FileIcon from "@lucide/svelte/icons/file";
  import PackageIcon from "@lucide/svelte/icons/package";
  import Trash2Icon from "@lucide/svelte/icons/trash-2";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import FolderOpenIcon from "@lucide/svelte/icons/folder-open";
  import ArrowRightIcon from "@lucide/svelte/icons/arrow-right";

  let {
    repo,
    artifact,
    folder,
    version,
    build,
    earlier: earlierAsked = false,
    flow,
    selected,
    canWrite,
    onDelete,
    onList,
  }: {
    repo: Repo;
    artifact: Artifact;
    /** The open folder: the version folder (Maven), or the artifact folder with ?version= (Debian). */
    folder: string;
    version: Version;
    /** The build in the address (?build=); "" for the latest. */
    build: string;
    /** Open the list of earlier builds at once (?earlier=1). */
    earlier?: boolean;
    flow: boolean;
    /** The file whose details show. */
    selected: string;
    canWrite: boolean;
    onDelete: (t: DeleteTarget) => void;
    /** Shows the chosen build's files: switches to the List view. */
    onList: () => void;
  } = $props();

  /* svelte-ignore state_referenced_locally */
  const builds = new Keyset<Build>((p) => api.repos.builds(repo.name, artifact.path, version.version, p), 20);

  const snapshot = $derived(version.kind === "snapshot");
  const newest = $derived(builds.items[0]);
  // The id of the latest build: the version names it, so a named build is known as latest or not
  // before the first page loads.
  const newestId = $derived(version.latest_build || newest?.id);
  // A build named in the address that the loaded pages do not hold is asked for by its id, alongside
  // the first page, so it shows wherever it sits in the list.
  const named = $derived.by(() => {
    if (!build || build === version.latest_build) return null;
    const id = build;
    if (untrack(() => builds.items.some((b) => b.id === id))) return null;
    return new Resource(() => api.repos.build(repo.name, artifact.path, version.version, id));
  });
  const shown = $derived.by(() => {
    if (!build) return newest;
    // A build that is gone shows the latest.
    return builds.items.find((b) => b.id === build) ?? named?.data?.items[0] ?? (named?.loading ? undefined : newest);
  });
  const latest = (b: Build) => b.id === newestId;

  /* svelte-ignore state_referenced_locally */
  let earlierOpen = $state(earlierAsked);
  const earlier = $derived(earlierOpen || (!!build && build !== newestId));
  // Closing the list goes back to the latest build.
  function toggleEarlier() {
    if (earlier && newestId && shown && !latest(shown)) choose(newestId);
    earlierOpen = !earlier;
  }

  // The address of this version with a build: ?version= names the version when the open folder is
  // the artifact folder. A file in the artifact folder keeps it too.
  const at = (dir: string, build: string | undefined) => ({ version: dir === artifact.path ? version.version : undefined, build });
  const choose = (key: string) =>
    navigate(filesPath(repo.name, folder), { query: at(folder, key === newestId ? undefined : key), replace: true });
  const fileHref = (f: BuildFile) =>
    href(filesPath(repo.name, f.path), at(parentOf(f.path), shown && !latest(shown) ? shown.id : undefined));
  const buildHref = (b: Build) => href(filesPath(repo.name, folder), at(folder, latest(b) ? undefined : b.id));

  const day = new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short", year: "numeric" });
  const tickets = $derived<Ticket[]>(
    builds.items.map((b) => ({
      key: b.id,
      top: buildLabel(b.id, b.label),
      bottom: b.published_at ? day.format(new Date(b.published_at)) : "",
      serial: b.build?.revision ? shortSha(b.build.revision) : version.version,
      label: `${buildLabel(b.id, b.label)} of ${version.version}${b.published_at ? `, published ${formatDateTime(b.published_at)}` : ""}`,
    })),
  );
  const byKey = $derived(new Map(builds.items.map((b) => [b.id, b])));

  /** What a file of a build is, in words: the first build_files label of the format that matches it. */
  function kindOf(f: BuildFile): string {
    const l = formats
      .get(repo.format)
      ?.labels.build_files.find(
        (b) => (b.extension === "*" || b.extension === f.extension) && (b.classifier === "*" || b.classifier === f.classifier),
      );
    if (l) return l.label.replaceAll("{classifier}", f.classifier).replaceAll("{extension}", f.extension);
    return f.classifier ? `${f.classifier} (${f.extension})` : f.extension.toUpperCase();
  }

  // The files of the shown build, as a table source with the builds' request state.
  const files: TableSource<BuildFile> = {
    get items() {
      return shown?.files ?? [];
    },
    get loading() {
      return builds.loading || (!!named?.loading && !named.data);
    },
    get error() {
      return builds.error || named?.error || "";
    },
    reload: () => {
      builds.reload();
      named?.reload();
    },
  };
  // The other builds of a snapshot, newest first.
  const others: TableSource<Build> = {
    get items() {
      return builds.items;
    },
    get loading() {
      return builds.loading;
    },
    get error() {
      return "";
    },
    get hasMore() {
      return builds.hasMore;
    },
    get loadingMore() {
      return builds.loadingMore;
    },
    more: builds.more,
  };
</script>

{#snippet head(b: Build)}
  <p class="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
    <span class="text-label">{buildLabel(b.id, b.label)}</span>
    {#if latest(b)}<StatusBadge tone="success" title="The build a snapshot dependency resolves to">Latest</StatusBadge>{/if}
    <span class="flex flex-wrap items-center gap-x-3 gap-y-1 text-meta text-muted-foreground">
      {#if b.published_at}<span>Published <RelativeTime at={b.published_at} /></span>{/if}
      <BuildInline p={b.build} />
    </span>
  </p>
{/snippet}

{#if snapshot && flow && builds.items.length}
  <CoverFlow
    {tickets}
    selected={shown?.id ?? ""}
    onSelect={choose}
    onOpen={onList}
    label="Builds of {version.version}"
    hasMore={builds.hasMore}
    loadingMore={builds.loadingMore}
    more={builds.more}
  >
    {#snippet caption(t)}
      {@const b = byKey.get(t.key)}
      {#if b}
        <div class="flex flex-wrap items-center gap-x-4 gap-y-3">
          <div class="min-w-0 grow basis-64">{@render head(b)}</div>
          <Button variant="outline" class="shrink-0" onclick={onList}>
            Show {formatCount(b.files.length)} {b.files.length === 1 ? "file" : "files"}<ArrowRightIcon />
          </Button>
        </div>
      {/if}
    {/snippet}
  </CoverFlow>
{:else if snapshot && flow && builds.loading}
  <div class={cn("rounded-lg bg-panel ring-1 ring-border", viewBox)}><Loading rows={8} class="h-full" /></div>
{:else}
  {#if snapshot && shown}
    <div class="mb-4 flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
      {@render head(shown)}
      {#if version.builds_count > 1}
        <Button variant="ghost" size="sm" aria-expanded={earlier} onclick={toggleEarlier}>
          {formatCount(version.builds_count - 1)} earlier {version.builds_count === 2 ? "build" : "builds"}
          <ChevronDownIcon class="transition-transform {earlier ? 'rotate-180' : ''}" />
        </Button>
      {/if}
    </div>
    {#if earlier}
      <div class="mb-8">
        <ItemList source={others} caption="Builds of {version.version}" key={(b) => b.id} href={buildHref} selected={(b) => b.id === shown?.id}>
          {#snippet leading()}<PackageIcon class="size-5" />{/snippet}
          {#snippet title(b)}{buildLabel(b.id, b.label)}{/snippet}
          {#snippet badges(b)}{#if latest(b)}<StatusBadge tone="success">Latest</StatusBadge>{/if}{/snippet}
          {#snippet meta(b)}
            <span class="flex min-w-0 items-center gap-1.5">
              {#if b.published_at}<span class="shrink-0">Published <RelativeTime at={b.published_at} /></span><span aria-hidden="true">·</span>{/if}
              {#if b.build}<BuildInline p={b.build} /><span aria-hidden="true">·</span>{/if}
              <span class="shrink-0">{count(b.files.length, "file", "files")}</span>
              {#if b.label}<span aria-hidden="true" class="max-sm:hidden">·</span><span class="min-w-0 truncate max-sm:hidden">{b.id}</span>{/if}
            </span>
          {/snippet}
        </ItemList>
      </div>
      <h3 class="mb-2 text-label">Files of {buildInline(shown.id, shown.label)}</h3>
    {/if}
  {/if}

  <DataTable
    source={files}
    caption="Files of {version.version}{snapshot && shown ? `, ${buildLabel(shown.id, shown.label)}` : ''}"
    key={(f) => f.path}
    href={fileHref}
    selected={(f) => f.path === selected}
    columns={[
      { label: "Name", grow: true },
      { label: "Kind", hideOnPhone: true },
      { label: "Size", align: "right" as const },
      ...(canWrite ? [{ label: "", srLabel: "Actions" }] : []),
    ]}
  >
    {#snippet row(f)}
      <Cell grow>
        <a
          href={fileHref(f)}
          aria-current={f.path === selected ? "true" : undefined}
          class="flex min-w-0 items-center gap-2 rounded-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
        >
          <FileIcon aria-hidden="true" class="size-4 shrink-0 text-muted-foreground" />
          <Truncate text={f.name} />
          {#if f.remote}<StatusBadge title="The proxy fetches this file from its remote on the first download">Not cached</StatusBadge>{/if}
          <MemberTag repo={f.repo ?? version.repo} />
        </a>
      </Cell>
      <Cell hideOnPhone muted text={kindOf(f)} />
      <Cell num>{#if f.remote && !f.size}<span class="text-muted-foreground">Unknown</span>{:else}<FileSize bytes={f.size} />{/if}</Cell>
      {#if canWrite}
        <Cell actions>
          <Button
            variant="ghost"
            size="icon-sm"
            class="text-muted-foreground hover:text-destructive"
            aria-label="Delete {f.name}"
            title="Delete the file"
            onclick={() => onDelete({ path: f.path, dir: false })}
          >
            <Trash2Icon />
          </Button>
        </Cell>
      {/if}
    {/snippet}
    {#snippet empty()}
      <CompactEmpty icon={FolderOpenIcon} title="Files" state="None" text="This version holds no files of a build any more." />
    {/snippet}
  </DataTable>
{/if}
