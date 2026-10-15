<!--
  The Files tab: the folder path and the choice of view, then the open folder in that view, and on
  the right the details column. The column shows the selected file, else the open folder: the
  repository at the top folder, what a plain folder holds, or the artifact and its snippets at an
  artifact or a version folder. It stays while the reader moves between files and folders, so the
  view never changes width. `path` is a folder ("" or ending in "/") or a file.

  Views (lib/view.svelte.ts, remembered per user): List, Tree and Columns everywhere, and Cover Flow
  where time is the axis: the versions of an artifact and the builds of a snapshot. A plain folder
  falls back to the last List, Tree or Columns choice. Every view fills the same area (files/area.ts),
  so a switch moves nothing around it.

  People who can publish delete files and folders here; a non-admin first confirms with a fresh
  second factor, which creates a publish token kept in memory (session.ts). An admin deletes with
  the UI session. In the List view they pick rows (files and folders mix): while rows are picked,
  the details column shows how many and the size of the picked files, and Delete removes them all
  after one confirm.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { toast } from "svelte-sonner";
  import { api, ApiError, type Proof, type Repo } from "$lib/api";
  import { count, selectedText, someNames } from "$lib/words";
  import FileSize from "$lib/components/FileSize.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import type { TreeNode } from "./browse";
  import { Resource, type TableSource } from "$lib/load.svelte";
  import { filesPath, href, navigate } from "$lib/router.svelte";
  import { ViewChoice, type ViewKind } from "$lib/view.svelte";
  import type { Crumb } from "$lib/crumbs";
  import { Button } from "$lib/components/ui/button";
  import Toolbar from "$lib/components/Toolbar.svelte";
  import Breadcrumbs from "$lib/components/Breadcrumbs.svelte";
  import ViewSwitcher from "$lib/components/ViewSwitcher.svelte";
  import DataTable from "$lib/components/DataTable.svelte";
  import DetailsColumn from "$lib/components/DetailsColumn.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import SecondFactorPrompt from "$lib/components/SecondFactorPrompt.svelte";
  import FolderList, { type DeleteTarget } from "./files/FolderList.svelte";
  import VersionsView from "./files/VersionsView.svelte";
  import BuildsView from "./files/BuildsView.svelte";
  import ColumnsView from "./files/ColumnsView.svelte";
  import TreeView from "./files/TreeView.svelte";
  import FileDetails from "./files/FileDetails.svelte";
  import ArtifactDetails from "./files/ArtifactDetails.svelte";
  import FolderDetails from "./files/FolderDetails.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import DeleteDialog from "./delete/DeleteDialog.svelte";
  import { flowsHere, placeOf, type Place } from "./files/place";
  import { viewMin } from "./files/area";
  import { dropSession, keepSession, sessionToken } from "./session";
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
    isAdmin,
  }: { repo: Repo; path: string; query: URLSearchParams; base: string; canWrite: boolean; canUpload: boolean; isAdmin: boolean } = $props();

  const isFile = $derived(path !== "" && !path.endsWith("/"));
  const folder = $derived(isFile ? parentOf(path) : path);
  const file = $derived(isFile ? path : "");
  const build = $derived(query.get("build") ?? "");

  // The folder path, from the top of the repository.
  const crumbs = $derived.by((): Crumb[] => {
    const segs = folder.split("/").filter(Boolean);
    return [
      { label: repo.name, href: segs.length ? href(filesPath(repo.name)) : undefined },
      ...segs.map((s, i) => ({
        label: s,
        href: i < segs.length - 1 ? href(filesPath(repo.name, segs.slice(0, i + 1).join("/") + "/")) : undefined,
      })),
    ];
  });

  // What the open folder is: a plain folder, an artifact or a version.
  const place = $derived(new Resource<Place>(() => placeOf(repo.name, folder)));
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

  // Bumped after a delete, so the folder loads again.
  let version = $state(0);

  // --- the pick of the List view ---
  let picked = $state(new Set<string>());
  let pickedRows = $state<TreeNode[]>([]);
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

  // --- the publish token for deletes ---

  let factorOpen = $state(false);
  let afterSession: (() => void) | null = null;

  function withSession(then: () => void) {
    if (sessionToken(repo.name)) return then();
    afterSession = then;
    factorOpen = true;
  }

  async function createSession(proof: Proof) {
    keepSession(repo.name, await api.publish.manualSession(repo.name, proof));
    const then = afterSession;
    afterSession = null;
    // Open the next dialog after this one has closed.
    if (then) setTimeout(then, 0);
  }

  // --- delete ---

  let deleting = $state<DeleteTarget | null>(null);
  let deleteOpen = $state(false);

  function askDelete(t: DeleteTarget) {
    const open = () => {
      deleting = t;
      deleteOpen = true;
    };
    if (isAdmin) open();
    else withSession(open);
  }

  // Several picked rows of the List view; one picked row uses the dialog of a single delete.
  let many = $state<DeleteTarget[]>([]);
  let manyOpen = $state(false);

  function askDeleteMany(ts: DeleteTarget[]) {
    if (ts.length === 0) return;
    if (ts.length === 1) return askDelete(ts[0]);
    const open = () => {
      many = ts;
      manyOpen = true;
    };
    if (isAdmin) open();
    else withSession(open);
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
    let tk: string | undefined;
    if (!isAdmin) {
      tk = sessionToken(repo.name);
      if (!tk) throw new Error("Your confirmation has expired. Close this dialog and try again.");
    }
    const paths = ts.filter((t) => !t.dir).map((t) => t.path);
    const prefixes = ts.filter((t) => t.dir).map((t) => t.path);
    let n: number;
    try {
      n = (
        await api.repos.deleteFiles(
          repo.name,
          { paths: paths.length ? paths : undefined, prefixes: prefixes.length ? prefixes : undefined },
          tk,
        )
      ).deleted;
    } catch (e) {
      if (tk && e instanceof ApiError && e.status === 401) dropSession(repo.name);
      throw e;
    }
    toast.success(`Deleted ${count(n, "file", "files")}.`);
    picked = new Set();
    version++;
    const gone = prefixes.filter((p) => folder.startsWith(p)).sort((a, b) => a.length - b.length)[0];
    if (gone !== undefined) {
      navigate(filesPath(repo.name, parentOf(gone)), { replace: true });
    } else if (file && paths.includes(file)) {
      navigate(filesPath(repo.name, folder), { replace: true });
    } else {
      place.reload();
    }
  }

  async function runDelete() {
    if (deleting) await deleteTargets([deleting]);
  }
</script>

{#snippet deleteFolder()}
  <Button variant="outline" class="hover:text-destructive" onclick={() => askDelete({ path: folder, dir: true })}><Trash2Icon />Delete folder</Button>
{/snippet}

<Toolbar>
  <div class="min-w-0 flex-1"><Breadcrumbs items={crumbs} /></div>
  {#snippet end()}
    {#if !details}
      <Button variant="ghost" size="sm" onclick={() => (closed = false)}><PanelRightOpenIcon />Show details</Button>
    {/if}
    <ViewSwitcher bind:value={view.value} {views} label="Show files as" />
  {/snippet}
</Toolbar>

<div class="flex flex-col gap-6 lg:flex-row lg:items-start">
  <div class="min-w-0 flex-1 {viewMin}">
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
      {#key folder + "\n" + version}
        {#if p.kind === "artifact"}
          <VersionsView {repo} artifact={p.artifact} flow={view.value === "coverflow"} canWrite={canWrite} onDelete={askDelete} />
        {:else if p.kind === "version"}
          <BuildsView
            {repo}
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
        navigate(filesPath(repo.name, folder), { query: { build: build || undefined } });
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
          title={artifactHere.artifact.kind === "gradle-plugin" ? artifactHere.artifact.title : baseName(artifactHere.artifact.path)}
          onClose={() => (closed = true)}
          actions={canWrite && folder ? deleteFolder : undefined}
        >
          <ArtifactDetails
            {repo}
            artifact={artifactHere.artifact}
            version={artifactHere.kind === "version" ? artifactHere.version : null}
            path={folder}
          />
        </DetailsColumn>
      {:else}
        <DetailsColumn title={folderTitle} onClose={() => (closed = true)} actions={canWrite && folder ? deleteFolder : undefined}>
          {#if place.data || place.error}
            {#key folder + "\n" + version}<FolderDetails {repo} {folder} />{/key}
          {:else}
            <Loading rows={2} />
          {/if}
        </DetailsColumn>
      {/if}
    </div>
  {/if}
</div>

<SecondFactorPrompt
  bind:open={factorOpen}
  title="Confirm it is you"
  description="To delete files in {repo.name}, confirm it is you with your authenticator app or a passkey. The confirmation lasts a short time, so you can make several changes in a row."
  actionLabel="Continue"
  onProof={createSession}
/>

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
