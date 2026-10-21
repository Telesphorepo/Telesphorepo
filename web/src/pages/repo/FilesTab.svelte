<!--
  The Files tab: one panel (a card) whose toolbar holds the folder path and the choice of view, and
  under it the open folder in that view; on the right the details column. The folder path is a
  path (slashes, body text) in the panel, so it never reads as the page's breadcrumbs. The column
  shows the selected file, else the open folder: the repository at the top folder, what a plain
  folder holds, or the artifact and its snippets at an artifact or a version folder. It stays while the reader moves between files and folders, so the
  view never changes width. `path` is a folder ("" or ending in "/") or a file; ?version= names a
  version of the artifact folder (a Debian package keeps every version in one folder).

  Views (lib/view.svelte.ts, remembered per user): List, Tree and Columns everywhere, and Cover Flow
  where time is the axis: the versions of an artifact and the builds of a snapshot. A plain folder
  falls back to the last List, Tree or Columns choice. Every view fills the same area (files/area.ts),
  so a switch moves nothing around it.

  A group shows the merged folders of the members the reader can download from, in the List view
  only and read-only: each file names its member and opens in it.

  People with Delete files delete files and folders here, with the UI session. In the List view they pick rows (files and folders mix): while rows are picked,
  the details column shows how many and the size of the picked files, and Delete removes them all
  after one confirm.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { toast } from "svelte-sonner";
  import { api, errorMessage, type Repo } from "$lib/api";
  import { count, selectedText, someNames } from "$lib/words";
  import FileSize from "$lib/components/FileSize.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import type { TreeEntry } from "$lib/api";
  import { Resource, type TableSource } from "$lib/load.svelte";
  import { filesPath, href, navigate } from "$lib/router.svelte";
  import { ViewChoice, type ViewKind } from "$lib/view.svelte";
  import type { Crumb } from "$lib/crumbs";
  import { Button } from "$lib/components/ui/button";
  import * as Card from "$lib/components/ui/card";
  import Breadcrumbs from "$lib/components/Breadcrumbs.svelte";
  import ViewSwitcher from "$lib/components/ViewSwitcher.svelte";
  import DataTable from "$lib/components/DataTable.svelte";
  import DetailsColumn from "$lib/components/DetailsColumn.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import FolderList, { FOLDER_PAGE, type DeleteTarget } from "./files/FolderList.svelte";
  import VersionsView from "./files/VersionsView.svelte";
  import BuildsView from "./files/BuildsView.svelte";
  import ColumnsView from "./files/ColumnsView.svelte";
  import TreeView from "./files/TreeView.svelte";
  import FileDetails from "./files/FileDetails.svelte";
  import ArtifactDetails from "./files/ArtifactDetails.svelte";
  import FolderDetails from "./files/FolderDetails.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import DeleteDialog from "./delete/DeleteDialog.svelte";
  import { flowsHere, placeOf, versionTarget, type Place } from "./files/place";
  import { viewMin } from "./files/area";
  import { baseName, parentOf } from "./browse";
  import DownloadIcon from "@lucide/svelte/icons/download";
  import Trash2Icon from "@lucide/svelte/icons/trash-2";
  import PanelRightOpenIcon from "@lucide/svelte/icons/panel-right-open";

  let {
    repo,
    path,
    query,
    base,
    canWrite,
    canUpload,
  }: {
    repo: Repo;
    path: string;
    query: URLSearchParams;
    base: string;
    /** The person holds Delete files here (and the repository is not paused). */
    canWrite: boolean;
    canUpload: boolean;
  } = $props();

  const isFile = $derived(path !== "" && !path.endsWith("/"));
  const folder = $derived(isFile ? parentOf(path) : path);
  const file = $derived(isFile ? path : "");
  const build = $derived(query.get("build") ?? "");
  // A version of the artifact folder (?version=), for a format that keeps every version in one folder.
  const versionQ = $derived(query.get("version") ?? "");

  // The folder path, from the top of the repository.
  const crumbs = $derived.by((): Crumb[] => {
    const segs = folder.split("/").filter(Boolean);
    const last = versionQ ? segs.length : segs.length - 1;
    return [
      { label: repo.name, href: segs.length ? href(filesPath(repo.name)) : undefined },
      ...segs.map((s, i) => ({
        label: s,
        href: i < last ? href(filesPath(repo.name, segs.slice(0, i + 1).join("/") + "/")) : undefined,
      })),
      ...(versionQ ? [{ label: versionQ }] : []),
    ];
  });

  // A group lists the merged folders of its members, read-only, in the List view.
  const group = $derived(repo.kind === "group");
  // What the open folder is: a plain folder, an artifact or a version. A group's folders are plain:
  // an artifact and its versions open in the member that holds them.
  const place = $derived(new Resource<Place>(() => (group ? Promise.resolve<Place>({ kind: "folder" }) : placeOf(repo.name, folder, versionQ))));
  // The last place that loaded, so the view choice does not jump while the next one loads.
  let known = $state<Place | undefined>(undefined);
  $effect(() => {
    if (place.data) known = place.data;
  });
  const here = $derived(place.data ?? known);

  // --- the view ---

  const allViews = new ViewChoice("files", ["list", "tree", "columns", "coverflow"]);
  const plainViews = new ViewChoice("files-plain", ["list", "tree", "columns"]);
  const flow = $derived(flowsHere(here));
  const view = {
    get value(): ViewKind {
      if (group) return "list";
      const v = allViews.value;
      return flow || v !== "coverflow" ? v : plainViews.value;
    },
    set value(v: ViewKind) {
      allViews.value = v;
      if (v !== "coverflow") plainViews.value = v;
    },
  };
  const views = $derived<ViewKind[]>(flow ? allViews.views : plainViews.views);

  // Bumped after a delete, so the folder loads again.
  let version = $state(0);

  // The first page of the open folder, asked for together with its place (not after it), so a
  // plain folder's list does not wait for the place. FolderList and FolderDetails share it. None
  // with ?version= (it names a version, whose builds show) or where a plain folder shows as a
  // Tree or Columns (they load their own pages).
  const firstPage = $derived.by(() => {
    void version;
    const v = allViews.value === "coverflow" ? plainViews.value : allViews.value;
    if (!group && (versionQ || v !== "list")) return undefined;
    const name = repo.name;
    const f = folder;
    const p = untrack(() => api.repos.tree(name, f, { limit: FOLDER_PAGE }));
    // An artifact or a version folder shows no list, so its page may never be read.
    p.catch(() => {});
    return p;
  });
  // Read it at once, so its request starts with the place's.
  $effect.pre(() => {
    void firstPage;
  });

  // The details column is always there: it shows the selected file, else the open folder (a plain
  // folder, an artifact or a version), so it never comes and goes while the reader browses. Only
  // Close hides it, and it stays hidden until the reader selects a file or asks for it again.
  let closed = $state(false);
  $effect.pre(() => {
    if (file) closed = false;
  });
  const details = $derived(!!file || !closed);
  // While the next folder loads inside the same artifact, its details stay (no flash).
  const artifactHere = $derived.by(() => {
    const p = place.data ?? (known && known.kind !== "folder" && folder.startsWith(known.artifact.path) ? known : undefined);
    return p && p.kind !== "folder" ? p : null;
  });
  const folderTitle = $derived(folder === "" ? repo.name : baseName(folder));

  // --- the pick of the List view ---
  let picked = $state(new Set<string>());
  let pickedRows = $state<TreeEntry[]>([]);
  // A new folder, or the same folder after a delete, starts with nothing picked.
  $effect(() => {
    void folder;
    void version;
    untrack(() => (picked = new Set()));
  });
  const pickSummary = $derived.by(() => {
    const files = pickedRows.filter((e) => e.type === "file");
    return {
      files: files.length,
      folders: pickedRows.length - files.length,
      size: files.reduce((n, e) => n + (e.size ?? 0), 0),
    };
  });

  const loadingSource: TableSource<never> = { items: [], loading: true, error: "" };

  function showList() {
    view.value = "list";
  }

  function earlierBuilds(versionFolder: string) {
    view.value = "list";
    navigate(filesPath(repo.name, versionFolder), { query: { earlier: "1" } });
  }

  // --- delete ---

  let deleting = $state<DeleteTarget | null>(null);
  let deleteOpen = $state(false);

  function askDelete(t: DeleteTarget) {
    deleting = t;
    deleteOpen = true;
  }

  // Several picked rows of the List view; one picked row uses the dialog of a single delete.
  let many = $state<DeleteTarget[]>([]);
  let manyOpen = $state(false);

  function askDeleteMany(ts: DeleteTarget[]) {
    if (ts.length === 0) return;
    if (ts.length === 1) return askDelete(ts[0]);
    many = ts;
    manyOpen = true;
  }

  const manyFiles = $derived(many.filter((t) => !t.dir).length);
  const manyFolders = $derived(many.length - manyFiles);
  const manyNoun = $derived(
    [manyFiles && count(manyFiles, "file", "files"), manyFolders && count(manyFolders, "folder", "folders")].filter(Boolean).join(" and "),
  );
  const manyChanges = $derived([
    ...(manyFolders ? [`Deletes every file under the ${manyFolders === 1 ? "folder" : "folders"}`] : []),
    "Clients that still need these files get “not found”",
  ]);

  async function deleteTargets(ts: DeleteTarget[]) {
    if (ts.length === 0) return;
    const paths = ts.flatMap((t) => t.files ?? (t.dir ? [] : [t.path]));
    const prefixes = ts.filter((t) => t.dir).map((t) => t.path);
    // The server takes at most 1000 paths and prefixes in one request.
    const batch = 1000;
    let n = 0;
    for (let i = 0; i < paths.length + prefixes.length; i += batch) {
      const ps = paths.slice(i, i + batch);
      const xs = prefixes.slice(Math.max(0, i - paths.length), Math.max(0, i + batch - paths.length));
      n += (await api.repos.deleteFiles(repo.name, { paths: ps.length ? ps : undefined, prefixes: xs.length ? xs : undefined })).deleted;
    }
    toast.success(`Deleted ${count(n, "file", "files")}.`);
    picked = new Set();
    version++;
    const gone = prefixes.filter((p) => folder.startsWith(p)).sort((a, b) => a.length - b.length)[0];
    if (gone !== undefined) {
      navigate(filesPath(repo.name, parentOf(gone)), { replace: true });
    } else if (versionQ && ts.some((t) => t.version === versionQ)) {
      navigate(filesPath(repo.name, folder), { replace: true });
    } else if (file && paths.includes(file)) {
      navigate(filesPath(repo.name, folder), { replace: true });
    } else {
      place.reload();
    }
  }

  async function askDeleteVersion() {
    const p = place.data;
    if (p?.kind !== "version") return;
    try {
      askDelete(await versionTarget(repo.name, p.artifact, p.version));
    } catch (e) {
      toast.error(errorMessage(e));
    }
  }

  async function runDelete() {
    if (deleting) await deleteTargets([deleting]);
  }
</script>

{#snippet deleteVersion()}
  <Button variant="outline" class="hover:text-destructive" onclick={askDeleteVersion}><Trash2Icon />Delete version</Button>
{/snippet}

{#snippet deleteFolder()}
  <Button variant="outline" class="hover:text-destructive" onclick={() => askDelete({ path: folder, dir: true })}><Trash2Icon />Delete folder</Button>
{/snippet}

<div class="flex flex-col gap-4 lg:flex-row lg:items-start">
  <Card.Root class="min-w-0 flex-1 gap-0 py-0">
    <!-- The panel's toolbar: where in the repository, then how to show it. -->
    <div class="flex flex-wrap items-center gap-x-4 gap-y-2 border-b px-4 py-2 sm:px-5">
      <div class="min-w-0 flex-1 basis-48 py-1.5"><Breadcrumbs path label="Folder" items={crumbs} /></div>
      <div class="ml-auto flex items-center gap-2">
        {#if !details}
          <Button variant="ghost" size="sm" onclick={() => (closed = false)}><PanelRightOpenIcon />Show details</Button>
        {/if}
        {#if !group}<ViewSwitcher bind:value={view.value} {views} label="Show files as" />{/if}
      </div>
    </div>
    <div class="min-w-0 px-2 pt-2 pb-3 sm:px-3 {viewMin}">
      {#if view.value === "tree"}
        {#key version}<TreeView {repo} {folder} {file} onEarlier={earlierBuilds} />{/key}
      {:else if !place.data && place.error}
        <div class="flex flex-wrap items-center gap-3 py-3">
          <ErrorState inline error={place.error} />
          <Button variant="outline" size="sm" onclick={place.reload}>Try again</Button>
        </div>
      {:else if view.value === "columns"}
        {#key version}<ColumnsView {repo} {folder} {file} onEarlier={earlierBuilds} />{/key}
      {:else if !place.data}
        <DataTable source={loadingSource} caption="Loading" key={() => 0} columns={[{ label: "Name", grow: true }, { label: "Size", align: "right" }]}>
          {#snippet row()}{/snippet}
        </DataTable>
      {:else}
        {@const p = place.data}
        {#key folder + "\n" + versionQ + "\n" + version}
          {#if p.kind === "artifact"}
            <VersionsView {repo} artifact={p.artifact} flow={view.value === "coverflow"} canWrite={canWrite} onDelete={askDelete} />
          {:else if p.kind === "version"}
            <BuildsView
              {repo}
              artifact={p.artifact}
              {folder}
              version={p.version}
              {build}
              earlier={query.get("earlier") === "1"}
              flow={view.value === "coverflow"}
              selected={file}
              compact={details}
              {canWrite}
              onDelete={askDelete}
              onList={showList}
            />
          {:else}
            <FolderList
              {repo}
              {folder}
              first={firstPage}
              selected={file}
              {canWrite}
              {canUpload}
              compact={details || pickedRows.length > 0}
              bind:picked
              onPicked={(rows) => (pickedRows = rows)}
              onDelete={askDeleteMany}
            />
          {/if}
        {/key}
      {/if}
    </div>
  </Card.Root>

  {#if pickedRows.length}
    {@const s = pickSummary}
    <DetailsColumn title={selectedText(pickedRows.length)} onClose={() => (picked = new Set())}>
      <dl class="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-x-4 gap-y-2 text-body">
        <dt class="text-muted-foreground">Holds</dt>
        <dd>{[s.folders && count(s.folders, "folder", "folders"), s.files && count(s.files, "file", "files")].filter(Boolean).join(", ")}</dd>
        {#if s.files}
          <dt class="text-muted-foreground">Size of files</dt>
          <dd class="tabular-nums"><FileSize bytes={s.size} /></dd>
        {/if}
      </dl>
      {#snippet actions()}
        <Button
          variant="outline"
          class="hover:text-destructive"
          onclick={() => askDeleteMany(pickedRows.map((e) => ({ path: e.path, dir: e.type === "dir" })))}><Trash2Icon />Delete</Button
        >
      {/snippet}
    </DetailsColumn>
  {:else if file}
    <DetailsColumn
      title={baseName(file)}
      onClose={() => {
        closed = true;
        navigate(filesPath(repo.name, folder), { query: { version: versionQ || undefined, build: build || undefined } });
      }}
    >
      {#key file}<FileDetails {repo} path={file} {base} />{/key}
      {#snippet actions()}
        <Button href={api.repos.fileURL(repo.format, repo.name, file)} download={baseName(file)}><DownloadIcon />Download</Button>
        {#if canWrite}
          <Button variant="outline" class="hover:text-destructive" onclick={() => askDelete({ path: file, dir: false })}><Trash2Icon />Delete</Button>
        {/if}
      {/snippet}
    </DetailsColumn>
  {:else if details}
    <!-- On a phone a folder's details follow the list; a selected file's come first. -->
    <div class="order-last lg:contents">
      {#if artifactHere}
        <DetailsColumn
          title={artifactHere.artifact.name || baseName(artifactHere.artifact.path)}
          onClose={() => (closed = true)}
          actions={canWrite && folder ? (artifactHere.kind === "version" ? deleteVersion : deleteFolder) : undefined}
        >
          <ArtifactDetails {repo} artifact={artifactHere.artifact} version={artifactHere.kind === "version" ? artifactHere.version : null} />
        </DetailsColumn>
      {:else}
        <DetailsColumn title={folderTitle} onClose={() => (closed = true)} actions={canWrite && folder ? deleteFolder : undefined}>
          {#if place.data || place.error}
            {#key folder + "\n" + version}<FolderDetails {repo} {folder} first={firstPage} />{/key}
          {:else}
            <Loading rows={2} />
          {/if}
        </DetailsColumn>
      {/if}
    </div>
  {/if}
</div>

<DeleteDialog bind:open={deleteOpen} {repo} target={deleting} onConfirm={runDelete} />

<ConfirmDialog
  bind:open={manyOpen}
  title="Delete {manyNoun}?"
  description="{someNames(many.map((t) => baseName(t.path)))}. You cannot undo this."
  changes={manyChanges}
  confirmLabel="Delete {manyNoun}"
  destructive
  onConfirm={() => deleteTargets(many)}
/>
