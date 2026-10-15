<!--
  One version: the files of one build, without checksum and signature files (pom first, then the
  main file, the Gradle module metadata and the classified files). A release has one build. A
  snapshot shows only its latest build; "11 earlier builds" opens the list of the others, and a
  row of that list shows its files (?build= keeps the choice in the address). A file row opens its
  details. With `flow`, Cover Flow shows the builds as felt tickets in place of the files, in the
  same area; opening a ticket shows its files in the List view.
-->
<script lang="ts">
  import type { Repo } from "$lib/api";
  import { Keyset, type TableSource } from "$lib/load.svelte";
  import { filesPath, href, navigate } from "$lib/router.svelte";
  import { Button } from "$lib/components/ui/button";
  import DataTable from "$lib/components/DataTable.svelte";
  import Cell from "$lib/components/Cell.svelte";
  import Truncate from "$lib/components/Truncate.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import FileSize from "$lib/components/FileSize.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import CoverFlow, { type Ticket } from "./CoverFlow.svelte";
  import ProvenanceInline from "./ProvenanceInline.svelte";
  import type { DeleteTarget } from "./FolderList.svelte";
  import { browse, buildLabel, shortSha, type Build, type BuildFile, type Version } from "../browse";
  import { formatCount, formatDateTime } from "$lib/format";
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
    version,
    build,
    earlier: earlierAsked = false,
    flow,
    selected,
    compact,
    canWrite,
    onDelete,
    onList,
  }: {
    repo: Repo;
    version: Version;
    /** The build in the address (?build=); "" for the latest. */
    build: string;
    /** Open the list of earlier builds at once (?earlier=1). */
    earlier?: boolean;
    flow: boolean;
    /** The file whose details show. */
    selected: string;
    compact: boolean;
    canWrite: boolean;
    onDelete: (t: DeleteTarget) => void;
    /** Shows the chosen build's files: switches to the List view. */
    onList: () => void;
  } = $props();

  /* svelte-ignore state_referenced_locally */
  const builds = new Keyset<Build>((p) => browse.builds(repo.name, version.path, p), 20);

  const snapshot = $derived(version.kind === "snapshot");
  const newest = $derived(builds.items[0]);
  const shown = $derived(builds.items.find((b) => b.build === build) ?? newest);
  // A build named in the address may sit on a later page: load pages until it shows.
  $effect(() => {
    if (build && !builds.items.some((b) => b.build === build) && builds.hasMore && !builds.loadingMore && !builds.loading) builds.more();
  });

  /* svelte-ignore state_referenced_locally */
  let earlierOpen = $state(earlierAsked);
  const earlier = $derived(earlierOpen || (!!build && build !== newest?.build));
  // Closing the list goes back to the latest build.
  function toggleEarlier() {
    if (earlier && newest && shown !== newest) choose(newest.build);
    earlierOpen = !earlier;
  }

  const choose = (key: string) =>
    navigate(filesPath(repo.name, version.path), { query: { build: key === newest?.build ? undefined : key }, replace: true });
  const fileHref = (f: BuildFile) => href(filesPath(repo.name, f.path), { build: shown && shown !== newest ? shown.build : undefined });
  const buildHref = (b: Build) => href(filesPath(repo.name, version.path), { build: b === newest ? undefined : b.build });

  const day = new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short", year: "numeric" });
  const tickets = $derived<Ticket[]>(
    builds.items.map((b) => ({
      key: b.build,
      top: buildLabel(b.build),
      bottom: day.format(new Date(b.published_at)),
      serial: b.provenance ? shortSha(b.provenance.sha) : version.version,
      label: `${buildLabel(b.build)} of ${version.version}, published ${formatDateTime(b.published_at)}`,
    })),
  );
  const byKey = $derived(new Map(builds.items.map((b) => [b.build, b])));

  /** What a file of a build is, in words. */
  function kindOf(f: BuildFile): string {
    if (!f.classifier) {
      if (f.extension === "pom") return "Project description (POM)";
      if (f.extension === "module") return "Gradle module metadata";
      if (f.extension === "jar") return "Library (JAR)";
      return f.extension.toUpperCase();
    }
    if (f.classifier === "sources") return "Sources";
    if (f.classifier === "javadoc") return "Javadoc";
    return `${f.classifier} (${f.extension})`;
  }

  // The files of the shown build, as a table source with the builds' request state.
  const files: TableSource<BuildFile> = {
    get items() {
      return shown?.files ?? [];
    },
    get loading() {
      return builds.loading;
    },
    get error() {
      return builds.error;
    },
    reload: builds.reload,
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
    <span class="text-label">{buildLabel(b.build)}</span>
    {#if b === newest}<StatusBadge tone="success" title="The build a snapshot dependency resolves to">Latest</StatusBadge>{/if}
    <span class="flex flex-wrap items-center gap-x-3 gap-y-1 text-meta text-muted-foreground">
      <span>Published <RelativeTime at={b.published_at} /></span>
      <ProvenanceInline p={b.provenance} />
    </span>
  </p>
{/snippet}

{#if snapshot && flow && builds.items.length}
  <CoverFlow
    {tickets}
    selected={shown?.build ?? ""}
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
        <DataTable
          source={others}
          caption="Builds of {version.version}"
          key={(b) => b.build}
          href={buildHref}
          selected={(b) => b === shown}
          columns={[
            { label: "Build", grow: true },
            { label: "Commit", hideOnPhone: true },
            { label: "Files", align: "right", hideOnPhone: true },
            { label: "Published", align: "right" },
          ]}
        >
          {#snippet row(b)}
            <Cell grow>
              <span class="flex min-w-0 items-center gap-2">
                <PackageIcon aria-hidden="true" class="size-4 shrink-0 text-muted-foreground" />
                <a href={buildHref(b)} class="flex min-w-0 rounded-sm font-medium outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
                  ><Truncate text={buildLabel(b.build)} /></a
                >
                <span class="min-w-0 truncate text-muted-foreground max-sm:hidden">{b.build}</span>
                {#if b === newest}<StatusBadge tone="success">Latest</StatusBadge>{/if}
              </span>
            </Cell>
            <Cell hideOnPhone muted><ProvenanceInline p={b.provenance} /></Cell>
            <Cell num muted hideOnPhone>{b.files.length}</Cell>
            <Cell num muted><RelativeTime at={b.published_at} /></Cell>
          {/snippet}
        </DataTable>
      </div>
      <h3 class="mb-2 text-label">Files of {buildLabel(shown.build).toLowerCase()}</h3>
    {/if}
  {/if}

  <DataTable
    source={files}
    caption="Files of {version.version}{snapshot && shown ? `, ${buildLabel(shown.build)}` : ''}"
    key={(f) => f.path}
    href={fileHref}
    selected={(f) => f.path === selected}
    columns={[
      { label: "Name", grow: true },
      ...(compact ? [] : [{ label: "Kind", hideOnPhone: true }]),
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
        </a>
      </Cell>
      {#if !compact}<Cell hideOnPhone muted text={kindOf(f)} />{/if}
      <Cell num><FileSize bytes={f.size} /></Cell>
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
