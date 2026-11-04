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

  A group shows the merged files of the members the reader can download from, in the same views:
  the server answers the tree, the versions, the builds and the details from its members, and each
  file and version names the member that serves it.

  People with Delete files delete files and folders here, with the UI session. In the List view they pick rows (files and folders mix): while rows are picked,
  the details column shows how many and the size of the picked files, and Delete removes them all
  after one confirm. In a group the delete goes to its repositories (delete/GroupDeleteDialog): each
  repository decides with the person's own Delete files there.
-->
<script lang="ts">
  import { tick, untrack } from "svelte";
  import { toast } from "svelte-sonner";
  import { api, errorMessage, type Repo } from "$lib/api";
  import { deleteFiles, deleteGroupFiles, type GroupDelete } from "$lib/deletes";
  import { count, someNames } from "$lib/words";
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
  import ErrorState from "$lib/components/ErrorState.svelte";
  import FolderList, { FOLDER_PAGE, type DeleteTarget } from "./files/FolderList.svelte";
  import VersionsView from "./files/VersionsView.svelte";
  import BuildsView from "./files/BuildsView.svelte";
  import ColumnsView from "./files/ColumnsView.svelte";
  import TreeView from "./files/TreeView.svelte";
  import FileView from "./files/FileView.svelte";
  import ArtifactDetails from "./files/ArtifactDetails.svelte";
  import DeleteDialog from "./delete/DeleteDialog.svelte";
  import GroupDeleteDialog from "./delete/GroupDeleteDialog.svelte";
  import { flowsHere, placeOf, versionTarget, type Place } from "./files/place";
  import { paneBox, viewMin } from "./files/area";
  import { baseName, parentOf } from "./browse";
  import Trash2Icon from "@lucide/svelte/icons/trash-2";

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
    /** The person holds Delete files here (and the repository is not paused); in a group, in one of its repositories. */
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

  // What the open folder is: a plain folder, an artifact or a version.
  const place = $derived(new Resource<Place>(() => placeOf(repo.name, folder, versionQ)));
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
  // plain folder's list does not wait for the place. FolderList reads it. None
  // with ?version= (it names a version, whose builds show) or where a plain folder shows as a
  // Tree or Columns (they load their own pages).
  const firstPage = $derived.by(() => {
    void version;
    const v = allViews.value === "coverflow" ? plainViews.value : allViews.value;
    if (versionQ || v !== "list") return undefined;
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

  // A file shows in a pane beside the open folder, inside the panel, so the place the reader
  // came from stays on screen. On a narrow screen the pane follows the folder and comes into view.
  let pane = $state<HTMLElement | null>(null);
  $effect(() => {
    if (!file || !pane || window.matchMedia("(min-width: 1024px)").matches) return;
    pane.scrollIntoView({ block: "start", behavior: "smooth" });
  });
  function closeFile() {
    navigate(filesPath(repo.name, folder), { query: { version: versionQ || undefined, build: build || undefined } });
  }

  // While the next folder loads inside the same artifact, its details stay (no flash).
  const artifactHere = $derived.by(() => {
    const p = place.data ?? (known && known.kind !== "folder" && folder.startsWith(known.artifact.path) ? known : undefined);
    return p && p.kind !== "folder" ? p : null;
  });

  // --- the pick of the List view ---
  let picked = $state(new Set<string>());
  let pickedRows = $state<TreeEntry[]>([]);
  // A new folder, or the same folder after a delete, starts with nothing picked.
  $effect(() => {
    void folder;
    void version;
    untrack(() => (picked = new Set()));
  });
  const loadingSource: TableSource<never> = { items: [], loading: true, error: "" };

  function showList() {
    view.value = "list";
  }

  // --- delete ---

  let deleting = $state<DeleteTarget | null>(null);
  let deleteOpen = $state(false);

  // A group deletes in its repositories: one dialog for one item or several.
  const group = $derived(repo.kind === "group");
  let groupTargets = $state<DeleteTarget[]>([]);
  let groupOpen = $state(false);

  function askDelete(t: DeleteTarget) {
    if (group) {
      groupTargets = [t];
      groupOpen = true;
      return;
    }
    deleting = t;
    deleteOpen = true;
  }

  // Several picked rows of the List view; one picked row uses the dialog of a single delete.
  let many = $state<DeleteTarget[]>([]);
  let manyOpen = $state(false);

  function askDeleteMany(ts: DeleteTarget[]) {
    if (ts.length === 0) return;
    if (ts.length === 1) return askDelete(ts[0]);
    if (group) {
      groupTargets = ts;
      groupOpen = true;
      return;
    }
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

  // Whether the tab is still on screen when a delete in the background ends.
  let mounted = true;
  $effect(() => () => {
    mounted = false;
  });

  // The confirm closes at once; the delete goes on behind its toast, also after the page changes.
  function deleteTargets(ts: DeleteTarget[]) {
    if (ts.length === 0) return;
    const paths = ts.flatMap((t) => t.files ?? (t.dir ? [] : [t.path]));
    const prefixes = ts.filter((t) => t.dir).map((t) => t.path);
    picked = new Set();
    deleteFiles(repo.name, { paths, prefixes }).then(
      (n) => {
        toast.success(`Deleted ${count(n, "file", "files")}.`);
        if (mounted) afterDelete(ts, paths, prefixes);
      },
      (e) => {
        toast.error(errorMessage(e));
        if (mounted) place.reload();
      },
    );
  }

  function deleteInGroup(ts: DeleteTarget[], deletes: GroupDelete[]) {
    if (deletes.length === 0) return;
    const paths = ts.flatMap((t) => t.files ?? (t.dir ? [] : [t.path]));
    const prefixes = ts.filter((t) => t.dir).map((t) => t.path);
    picked = new Set();
    deleteGroupFiles(repo.name, deletes).then(
      ({ deleted, kept }) => {
        const names = kept.map((k) => k.repo);
        toast.success(`Deleted ${count(deleted, "file", "files")}.`, {
          description: names.length ? `${someNames(names)} kept ${names.length === 1 ? "its copy" : "their copies"}.` : undefined,
        });
        if (mounted) afterDelete(ts, paths, prefixes);
      },
      (e) => {
        toast.error(errorMessage(e));
        if (mounted) {
          version++;
          place.reload();
        }
      },
    );
  }

  function afterDelete(ts: DeleteTarget[], paths: string[], prefixes: string[]) {
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
    if (deleting) deleteTargets([deleting]);
  }
</script>

{#snippet deleteVersion()}
  <Button variant="outline" class="hover:text-destructive" onclick={askDeleteVersion}><Trash2Icon />Delete version</Button>
{/snippet}

{#snippet deleteFolder()}
  <Button variant="outline" class="hover:text-destructive" onclick={() => askDelete({ path: folder, dir: true })}><Trash2Icon />Delete folder</Button>
{/snippet}

<div class="flex flex-col gap-6">
  <Card.Root class="min-w-0 gap-0 py-0">
    <!-- The panel's toolbar: where in the repository, then how to show it. -->
    <div class="flex flex-wrap items-center gap-x-4 gap-y-2 border-b px-5 py-2 sm:px-6">
      <div class="min-w-0 flex-1 basis-48 py-1.5"><Breadcrumbs label="Folder" items={crumbs} /></div>
      <div class="ml-auto flex items-center gap-2">
        <ViewSwitcher bind:value={view.value} {views} label="Show files as" />
      </div>
    </div>
    <div class="flex flex-col lg:flex-row lg:items-start">
    <div class="min-w-0 flex-1 px-2 pt-2 pb-3 sm:px-3 {viewMin}">
      {#if view.value === "tree"}
        {#key version}<TreeView {repo} {folder} {file} {base} />{/key}
      {:else if !place.data && place.error}
        <div class="flex flex-wrap items-center gap-3 py-3">
          <ErrorState inline error={place.error} />
          <Button variant="outline" size="sm" onclick={place.reload}>Try again</Button>
        </div>
      {:else if view.value === "columns"}
        {#key version}<ColumnsView {repo} {folder} {file} />{/key}
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
              {base}
              {canWrite}
              {canUpload}
              compact={false}
              bind:picked
              onPicked={(rows) => (pickedRows = rows)}
              onDelete={askDeleteMany}
            />
          {/if}
        {/key}
      {/if}
    </div>
    {#if file}
      <div bind:this={pane} class="min-w-0 scroll-mt-24 border-t lg:w-[min(48%,44rem)] lg:shrink-0 lg:border-t-0 lg:border-l {paneBox}">
        {#key file}
          <FileView {repo} path={file} {base} onClose={closeFile} {canWrite} onDelete={() => askDelete({ path: file, dir: false })} />
        {/key}
      </div>
    {/if}
    </div>
  </Card.Root>

  <!-- How to use the artifact or the version that is open: wide, under the panel. -->
  {#if !file && artifactHere}
    <Card.Root class="gap-0 py-0">
      <div class="flex flex-wrap items-center gap-3 border-b px-5 py-3 sm:px-6">
        <h2 class="min-w-0 flex-1 text-section font-semibold break-all">
          {artifactHere.kind === "version" ? `${artifactHere.artifact.name || baseName(artifactHere.artifact.path)} ${artifactHere.version.version}` : artifactHere.artifact.name || baseName(artifactHere.artifact.path)}
        </h2>
        {#if canWrite && folder}
          {#if artifactHere.kind === "version"}{@render deleteVersion()}{:else}{@render deleteFolder()}{/if}
        {/if}
      </div>
      <div class="grid gap-x-10 gap-y-6 px-5 py-5 sm:px-6 lg:grid-cols-2 [&>*]:min-w-0">
        <ArtifactDetails {repo} artifact={artifactHere.artifact} version={artifactHere.kind === "version" ? artifactHere.version : null} />
      </div>
    </Card.Root>
  {/if}
</div>

<DeleteDialog bind:open={deleteOpen} {repo} target={deleting} onConfirm={runDelete} />

{#if group}
  <GroupDeleteDialog bind:open={groupOpen} {repo} targets={groupTargets} onConfirm={(deletes) => deleteInGroup(groupTargets, deletes)} />
{/if}

<ConfirmDialog
  bind:open={manyOpen}
  title="Delete {manyNoun}?"
  description="{someNames(many.map((t) => baseName(t.path)))}. You cannot undo this."
  changes={manyChanges}
  confirmLabel="Delete {manyNoun}"
  destructive
  onConfirm={() => deleteTargets(many)}
/>
