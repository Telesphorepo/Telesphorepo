<!--
  One repo: header, breadcrumbs, the folder list, and the file panel for an open file. Users with
  write access upload and delete here. A non-admin needs a publish session for that (created
  with a fresh second factor and kept in memory); an admin deletes with the UI session.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import { api, ApiError, type Proof, type Repo } from "$lib/api";
  import { auth } from "$lib/auth.svelte";
  import { Resource } from "$lib/load.svelte";
  import { encodePath, href, navigate, router } from "$lib/router.svelte";
  import * as Breadcrumb from "$lib/components/ui/breadcrumb";
  import * as Alert from "$lib/components/ui/alert";
  import { Button } from "$lib/components/ui/button";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import LoadingRows from "$lib/components/LoadingRows.svelte";
  import FormatBadge from "$lib/components/FormatBadge.svelte";
  import AccessBadge from "$lib/components/AccessBadge.svelte";
  import SecondFactorPrompt from "$lib/components/SecondFactorPrompt.svelte";
  import FolderList, { type DeleteTarget } from "./FolderList.svelte";
  import FilePanel from "./FilePanel.svelte";
  import UsageSheet from "./UsageSheet.svelte";
  import UploadDialog from "$lib/browse/upload/UploadDialog.svelte";
  import FolderUploadDialog from "$lib/browse/upload/FolderUploadDialog.svelte";
  import type { FolderDrop } from "$lib/browse/upload/folder";
  import DeleteDialog from "$lib/browse/delete/DeleteDialog.svelte";
  import { dropSession, keepSession, sessionToken } from "./session";
  import BookOpenIcon from "@lucide/svelte/icons/book-open";
  import UploadIcon from "@lucide/svelte/icons/upload";
  import FolderUpIcon from "@lucide/svelte/icons/folder-up";
  import SettingsIcon from "@lucide/svelte/icons/settings";
  import PauseIcon from "@lucide/svelte/icons/circle-pause";
  import SearchXIcon from "@lucide/svelte/icons/search-x";

  let { name, folder, file }: { name: string; folder: string; file: string } = $props();

  /** Formats whose plugin takes uploads from the web UI (format.ManualUploader). */
  const uploadFormats = ["maven", "debian", "raw"];

  // null: the repo does not exist or the visitor cannot read it (the server does not say which).
  const repo = new Resource<Repo | null>(async () => {
    try {
      return await api.repos.get(name);
    } catch (e) {
      if (e instanceof ApiError && [401, 403, 404].includes(e.status)) return null;
      throw e;
    }
  });
  // The public base URL for client snippets; the page's own origin until it loads.
  const usage = new Resource(() => api.repos.usage(name));
  const base = $derived((usage.data?.base_url || location.origin).replace(/\/+$/, ""));

  const r = $derived(repo.data ?? null);
  const isAdmin = $derived(r?.permission === "admin");
  const canWrite = $derived(
    !!r && !!auth.user && !auth.setupOnly && (r.permission === "admin" || r.permission === "write"),
  );
  const locked = $derived(r?.locked_by ?? "");
  const canUpload = $derived(canWrite && !!r && uploadFormats.includes(r.format));

  const repoPath = (p: string) => "/repo/" + encodeURIComponent(name) + "/" + encodePath(p);
  const crumbs = $derived.by(() => {
    const segs = folder.split("/").filter(Boolean);
    return segs.map((s, i) => ({ name: s, path: segs.slice(0, i + 1).join("/") + "/" }));
  });

  // Bumped after an upload or a delete, so the folder list loads again.
  let version = $state(0);

  // --- publish session for uploads and deletes ---

  let factorOpen = $state(false);
  let afterSession: (() => void) | null = null;

  /** Runs then() once a publish session for the repo exists, asking for a second factor first if needed. */
  function withSession(then: () => void) {
    if (sessionToken(name)) return then();
    afterSession = then;
    factorOpen = true;
  }

  async function createSession(proof: Proof) {
    keepSession(name, await api.publish.manualSession(name, proof));
    const then = afterSession;
    afterSession = null;
    // Open the next dialog after this one has closed.
    if (then) setTimeout(then, 0);
  }

  /** The session token for a request, or an error that tells the user to start over. */
  function token(): string {
    const t = sessionToken(name);
    if (!t) throw new Error("Your confirmation has expired. Close this dialog and try again.");
    return t;
  }

  /** Forgets a session the server no longer accepts. */
  function checkSession(e: unknown, t: string | undefined) {
    if (t && e instanceof ApiError && e.status === 401) dropSession(name);
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

  async function runDelete() {
    const t = deleting;
    if (!t) return;
    const tk = isAdmin ? undefined : token();
    let n: number;
    try {
      n = (await api.repos.deleteFiles(name, t.dir ? { prefix: t.path } : { path: t.path }, tk)).deleted;
    } catch (e) {
      checkSession(e, tk);
      throw e;
    }
    toast.success(`Deleted ${n} ${n === 1 ? "file" : "files"}`);
    version++;
    if (t.dir && folder.startsWith(t.path)) {
      const parent = t.path.slice(0, t.path.slice(0, -1).lastIndexOf("/") + 1);
      navigate(repoPath(parent), { replace: true });
    } else if (!t.dir && file === t.path) {
      navigate(repoPath(folder), { replace: true });
    }
  }

  // --- upload ---

  let uploadOpen = $state(false);
  const startUpload = () => withSession(() => (uploadOpen = true));
  let folderUploadOpen = $state(false);
  // A folder dropped on "Upload files"; "Upload a folder" reads it when it opens.
  let droppedFolder = $state<FolderDrop | null>(null);
  const startFolderUpload = (drop: FolderDrop | null = null) =>
    withSession(() => {
      droppedFolder = drop;
      folderUploadOpen = true;
    });
  function openFolderUpload(drop: FolderDrop) {
    uploadOpen = false;
    startFolderUpload(drop);
  }

  let usageOpen = $state(false);

  // --- file panel ---

  const panelOpen = $derived(file !== "");
  const closePanel = () => {
    if (router.path.startsWith("/repo/")) navigate(repoPath(folder));
  };
</script>

{#if repo.loading}
  <LoadingRows rows={5} />
{:else if repo.error}
  <ErrorState error={repo.error} onRetry={repo.reload} />
{:else if !r}
  <EmptyState
    icon={SearchXIcon}
    title="Repository not found"
    description={auth.user
      ? `No repository named ${name} exists, or you do not have access to it. Ask an administrator for access.`
      : `No repository named ${name} is open to everyone. Sign in if it is private.`}
  >
    {#if auth.user}
      <Button variant="outline" href={href("/")}>Show all repositories</Button>
    {:else}
      <Button href={href("/login", { next: router.path })}>Sign in</Button>
    {/if}
  </EmptyState>
{:else}
  <PageHeader title={r.name} description={r.description || undefined}>
    {#snippet breadcrumbs()}
      <Breadcrumb.Root>
        <Breadcrumb.List class="break-all">
          <Breadcrumb.Item><Breadcrumb.Link href={href("/")}>Repositories</Breadcrumb.Link></Breadcrumb.Item>
          <Breadcrumb.Separator />
          <Breadcrumb.Item>
            {#if crumbs.length === 0}
              <Breadcrumb.Page class="font-mono">{name}</Breadcrumb.Page>
            {:else}
              <Breadcrumb.Link href={href(repoPath(""))} class="font-mono">{name}</Breadcrumb.Link>
            {/if}
          </Breadcrumb.Item>
          {#each crumbs as c, i (c.path)}
            <Breadcrumb.Separator />
            <Breadcrumb.Item>
              {#if i === crumbs.length - 1}
                <Breadcrumb.Page class="font-mono">{c.name}</Breadcrumb.Page>
              {:else}
                <Breadcrumb.Link href={href(repoPath(c.path))} class="font-mono">{c.name}</Breadcrumb.Link>
              {/if}
            </Breadcrumb.Item>
          {/each}
        </Breadcrumb.List>
      </Breadcrumb.Root>
    {/snippet}
    {#snippet actions()}
      <Button variant="outline" onclick={() => (usageOpen = true)}><BookOpenIcon />How to use</Button>
      {#if isAdmin}
        <Button variant="outline" href={href("/settings/repos/" + encodeURIComponent(name))}>
          <SettingsIcon />Repository settings
        </Button>
      {/if}
      {#if canUpload}
        <Button variant="outline" disabled={!!locked} onclick={() => startFolderUpload()}><FolderUpIcon />Upload a folder</Button>
        <Button disabled={!!locked} onclick={startUpload}><UploadIcon />Upload files</Button>
      {/if}
    {/snippet}
    <div class="flex flex-wrap items-center gap-2">
      <FormatBadge format={r.format} />
      <AccessBadge anyone={r.anonymous_read} />
      {#if canWrite}
        <span class="text-sm text-muted-foreground">{isAdmin ? "You are an admin" : "You can publish here"}</span>
      {/if}
    </div>
  </PageHeader>

  {#if canWrite && locked}
    <Alert.Root class="mb-8">
      <PauseIcon />
      <Alert.Title>Uploads and deletes are paused</Alert.Title>
      <Alert.Description>{locked}. Try again when it has finished. Downloads keep working.</Alert.Description>
    </Alert.Root>
  {/if}

  {#key folder + "\n" + version}
    <FolderList
      repo={r}
      {folder}
      selected={file}
      canWrite={canWrite && !locked}
      onDelete={askDelete}
      onUpload={canUpload && !locked ? startUpload : undefined}
    />
  {/key}

  <FilePanel
    open={panelOpen}
    onClose={closePanel}
    repo={r}
    path={file}
    {base}
    canDelete={canWrite && !locked}
    onDelete={askDelete}
  />

  <SecondFactorPrompt
    bind:open={factorOpen}
    title="Confirm it is you"
    description="To upload or delete files in {name}, confirm it is you with your authenticator app or a passkey. The confirmation lasts a short time, so you can make several changes in a row."
    actionLabel="Continue"
    onProof={createSession}
  />

  <UsageSheet bind:open={usageOpen} repo={r} {usage} />

  <DeleteDialog bind:open={deleteOpen} repo={r} target={deleting} onConfirm={runDelete} />

  {#if canUpload}
    <UploadDialog
      bind:open={uploadOpen}
      repo={r}
      {folder}
      {token}
      onError={(e, t) => checkSession(e, t)}
      onUploaded={() => version++}
      onFolder={openFolderUpload}
    />
    <FolderUploadDialog
      bind:open={folderUploadOpen}
      repo={r}
      {folder}
      {token}
      dropped={droppedFolder}
      onError={(e, t) => checkSession(e, t)}
      onUploaded={() => version++}
    />
  {/if}
{/if}
