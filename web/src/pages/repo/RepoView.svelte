<!--
  One repository, loaded once: the header (name, a lock when it is private, format, description;
  Settings and Upload for the people who may use them), then the tabs named by the task. Files
  browses the repository. Use this repository adds it to a build (every developer). Publish to this
  repository is for the people who can publish: from a computer, from CI, or by hand. The upload
  page is a level below. The parent keys it by name.

  A proxy has the Files tab (its cached files; people with Delete files delete them) and the Use tab, and no
  publish or upload. A group has the tabs too: Files is the Files tab of a hosted repository of its
  format over the files of its members (read-only, where the header says so; each file and version
  names the member that serves it), Use this repository lists its members first, and
  Publish to this repository shows to a person who publishes to one of its repositories: they pick
  one (?to=) and get that repository's Publish tab.

  While the repository has an operation (a copy from Nexus that waits for the switch-over, a
  delete), the people who could upload or delete see its sentence as one muted line under the
  header. A failed operation is an alert with Resume and Stop for the holders of All repositories.
-->
<script lang="ts">
  import { api, ApiError, type Repo } from "$lib/api";
  import { auth } from "$lib/auth.svelte";
  import { waitForDeletes } from "$lib/deletes";
  import { toast } from "svelte-sonner";
  import { Resource } from "$lib/load.svelte";
  import { href, navigate, repoPath, repoSettingsPath } from "$lib/router.svelte";
  import * as Alert from "$lib/components/ui/alert";
  import { Button } from "$lib/components/ui/button";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import TabNav from "$lib/components/TabNav.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import FormatBadge from "$lib/components/FormatBadge.svelte";
  import FilesTab from "./FilesTab.svelte";
  import UseTab from "./UseTab.svelte";
  import GroupMembersList from "./GroupMembersList.svelte";
  import RepoKindIcon from "$lib/components/RepoKindIcon.svelte";
  import PublishTab from "./PublishTab.svelte";
  import GroupPublish from "./GroupPublish.svelte";
  import UploadView from "./UploadView.svelte";
  import { formats } from "$lib/formats/registry.svelte";
  import { parentOf } from "./browse";
  import LockIcon from "@lucide/svelte/icons/lock";
  import UploadIcon from "@lucide/svelte/icons/upload";
  import SettingsIcon from "@lucide/svelte/icons/settings";
  import FolderIcon from "@lucide/svelte/icons/folder";
  import DownloadIcon from "@lucide/svelte/icons/hard-drive-download";
  import SendIcon from "@lucide/svelte/icons/send";
  import PauseIcon from "@lucide/svelte/icons/circle-pause";
  import SearchXIcon from "@lucide/svelte/icons/search-x";
  import CompassIcon from "@lucide/svelte/icons/compass";

  let { name, rest, query }: { name: string; rest: string; query: URLSearchParams } = $props();

  // null: the repo does not exist or the visitor cannot read it (the server does not say which).
  const repo = new Resource<Repo | null>(async () => {
    try {
      return await api.repos.get(name);
    } catch (e) {
      if (e instanceof ApiError && [401, 403, 404].includes(e.status)) return null;
      throw e;
    }
  });
  // The format plugin's snippets for both setup tabs, and the public base URL for addresses.
  /* svelte-ignore state_referenced_locally */
  const usage = new Resource(() => api.repos.usage(name));
  const base = $derived((usage.data?.base_url || location.origin).replace(/\/+$/, ""));

  type View = { kind: "files"; path: string } | { kind: "use" } | { kind: "publish" } | { kind: "upload" } | { kind: "unknown" };

  const view = $derived.by((): View => {
    const r = rest.replace(/\/$/, "");
    if (r === "") return { kind: "files", path: "" };
    if (rest === "files" || rest.startsWith("files/")) return { kind: "files", path: rest.slice("files/".length) };
    if (r === "use") return { kind: "use" };
    if (r === "publish") return { kind: "publish" };
    if (r === "upload") return { kind: "upload" };
    return { kind: "unknown" };
  });

  // The address of the tab before it had a name for its task.
  $effect(() => {
    if (rest.replace(/\/$/, "") === "setup") navigate(repoPath(name, "use"), { replace: true });
  });

  // A single sign-on account returns from its identity provider to the repo page, with a setup grant or an error code.
  $effect(() => {
    if (rest === "" && (query.has("setup_grant") || query.has("error"))) {
      navigate(repoPath(name, "publish"), { query: { setup_grant: query.get("setup_grant") ?? undefined, error: query.get("error") ?? undefined }, replace: true });
    }
  });

  const r = $derived(repo.data ?? null);
  const group = $derived(r?.kind === "group");
  const proxy = $derived(r?.kind === "proxy");
  // The caller's permissions on the repository; a setup-only session uses none of them.
  const perms = $derived(r && auth.user && !auth.setupOnly ? (r.permissions ?? []) : []);
  // Settings shows to anyone who can open one of its tabs.
  const canSettings = $derived(perms.includes("repo.settings") || perms.includes("repo.access") || perms.includes("ci.pipelines"));
  // Nobody publishes to a group or a proxy.
  const canWrite = $derived(!!r && r.kind === "hosted" && perms.includes("repo.publish"));
  // A group's Publish tab: its repositories that the person publishes to, in read order.
  const publishTargets = $derived(
    group && auth.user && !auth.setupOnly ? (r?.members ?? []).filter((m) => m.kind === "hosted" && (m.permissions ?? []).includes("repo.publish")) : [],
  );
  const canPublish = $derived(canWrite || publishTargets.length > 0);
  const locked = $derived(r?.operation?.message ?? "");
  // The line under the header for the people who could upload or delete: the operation's own
  // sentence (a failed one shows as an alert to the holders of All repositories instead).
  const failedForAdmin = $derived(r?.operation?.state === "failed" && auth.allRepos());
  const paused = $derived(!!locked && !failedForAdmin && (canWrite || perms.includes("repo.delete")));
  let resumeOpen = $state(false);
  let clearOpen = $state(false);
  async function resumeOperation() {
    const deleting = r?.operation?.type === "delete";
    await api.repos.resumeOperation(name);
    if (deleting) {
      await waitForDeletes([name]);
      toast.success(`Deleted the repository ${name}`);
      navigate("/");
    }
    else await repo.reload();
  }
  async function clearOperation() {
    await api.repos.clearOperation(name);
    await repo.reload();
  }
  // An upload needs a manual publish token, which only a password account gets.
  const canUpload = $derived(canWrite && !!r && !!formats.get(r.format)?.upload && auth.user?.kind === "local");
  // Who deletes files on the Files tab: people with Delete files, with the UI session; a hosted
  // repository not while it is paused, a proxy its cached files.
  const canDelete = $derived(!!r && !group && perms.includes("repo.delete") && (proxy || !locked));

  // On a phone the tab rail scrolls: keep the open tab in view.
  let tabRail = $state<HTMLDivElement | null>(null);
  $effect(() => {
    void view.kind;
    const nav = tabRail?.querySelector("nav");
    const tab = nav?.querySelector<HTMLElement>("[aria-current=page]");
    if (!nav || !tab) return;
    // The tab's place in the rail; the rail starts at its beginning whenever the tab fits there.
    const x = tab.getBoundingClientRect().left - nav.getBoundingClientRect().left + nav.scrollLeft;
    nav.scrollLeft = x + tab.offsetWidth <= nav.clientWidth ? 0 : x - 4;
  });

  // The folder that Upload starts in: the open folder of the Files tab.
  const uploadFolder = $derived(view.kind !== "files" ? "" : view.path === "" || view.path.endsWith("/") ? view.path : parentOf(view.path));
</script>

{#snippet pausedLine()}
  <PauseIcon aria-hidden="true" />
  <span>{locked}{r?.operation?.type === "delete" ? "" : " Downloads work."}</span>
{/snippet}

{#if repo.loading && repo.data === undefined}
  <Loading rows={5} />
{:else if repo.error && repo.data === undefined}
  <ErrorState error={repo.error} title="The repository did not load" onRetry={repo.reload} />
{:else if !r}
  <EmptyState
    icon={SearchXIcon}
    title="Repository not found"
    description={auth.user
      ? `No repository named ${name} exists, or you do not have access to it. Ask an administrator for access.`
      : `No public repository is named ${name}. Sign in if it is private.`}
  >
    <Button variant="outline" href={href("/")}>Show all repositories</Button>
  </EmptyState>
{:else if view.kind === "upload"}
  {#if canUpload}
    <UploadView repo={r} folder={query.get("folder") ?? ""} />
  {:else}
    <EmptyState icon={LockIcon} title="You cannot upload here" description="Uploads need permission to publish to {r.name}, in a format that takes uploads.">
      <Button variant="outline" href={href(repoPath(r.name))}>Open the files</Button>
    </EmptyState>
  {/if}
{:else if view.kind === "publish" && !canPublish}
  <EmptyState
    icon={LockIcon}
    title="You cannot publish to {r.name}"
    description={group
      ? "A group only reads its repositories. Publish to one of them."
      : proxy
        ? "A proxy only keeps the files of its remote repository."
        : "A person who manages its access gives the Publish permission."}
  >
    <Button variant="outline" href={href(repoPath(r.name))}>{group ? "Open the group" : "Open the files"}</Button>
  </EmptyState>
{:else if view.kind === "unknown"}
  <EmptyState icon={CompassIcon} title="This page does not exist" description="Check the address, or open the files of {r.name}.">
    <Button variant="outline" href={href(repoPath(r.name))}>Open the files</Button>
  </EmptyState>
{:else}
  <PageHeader
    title={r.name}
    description={r.description || undefined}
    crumbs={[{ label: "Repositories", href: href("/") }, { label: r.name }]}
    status={paused ? pausedLine : undefined}
  >
    {#snippet meta()}
      {#if !r.anonymous_read}
        <LockIcon role="img" aria-label="Private" class="size-5 shrink-0 text-muted-foreground" />
      {/if}
      <span class="text-body text-muted-foreground"><FormatBadge format={r.format} /></span>
      <RepoKindIcon kind={r.kind} iconClass="size-5" />
    {/snippet}
    {#snippet actions()}
      {#if canSettings}
        <Button variant="outline" href={href(repoSettingsPath(r.name))}><SettingsIcon />Settings</Button>
      {/if}
      {#if canUpload}
        <Button href={href(repoPath(r.name, "upload"), { folder: uploadFolder || undefined })} disabled={!!locked}>
          <UploadIcon />Upload
        </Button>
      {:else if group}
        <span class="text-meta text-muted-foreground">Read-only: this group serves its members' files</span>
      {/if}
    {/snippet}
  </PageHeader>

  {#if r.operation && failedForAdmin}
    <Alert.Root variant="destructive" class="mb-6">
      <PauseIcon />
      <Alert.Title>An operation on this repository failed</Alert.Title>
      <Alert.Description>
        <p>{r.operation.message}</p>
        <div class="mt-3 flex flex-wrap gap-2">
          <Button size="sm" variant="outline" onclick={() => (resumeOpen = true)}>Resume</Button>
          <Button size="sm" variant="outline" onclick={() => (clearOpen = true)}>Stop and keep the files</Button>
        </div>
      </Alert.Description>
    </Alert.Root>
    <ConfirmDialog bind:open={resumeOpen} title="Resume the {r.operation.type}?" confirmLabel="Resume" destructive
      changes={r.operation.type === "delete" ? ["Deletes the files that are left", "Deletes the repository"] : ["Runs the operation again"]}
      onConfirm={resumeOperation} />
    <ConfirmDialog bind:open={clearOpen} title="Stop the {r.operation.type}?" confirmLabel="Stop the operation"
      changes={["Ends the operation", "Keeps the repository with the files that are left"]}
      onConfirm={clearOperation} />
  {/if}

  <!-- On a phone the tabs scroll sideways; the cut-off last tab shows that, without a scrollbar. -->
  <div bind:this={tabRail} class="[&>nav]:[scrollbar-width:none] [&>nav::-webkit-scrollbar]:hidden">
  <TabNav
    label="Repository"
    items={[
      { label: "Files", href: href(repoPath(r.name)), active: view.kind === "files", icon: FolderIcon },
      { label: "Use this repository", href: href(repoPath(r.name, "use")), active: view.kind === "use", icon: DownloadIcon },
      ...(canPublish ? [{ label: "Publish to this repository", href: href(repoPath(r.name, "publish")), active: view.kind === "publish", icon: SendIcon }] : []),
    ]}
  />
  </div>

  {#if view.kind === "files"}
    <FilesTab repo={r} path={view.path} {query} {base} canWrite={canDelete} {canUpload} />
  {:else if view.kind === "use"}
    <!-- A group's page: its members first, then how to use it. -->
    {#if group}<GroupMembersList repo={r} />{/if}
    <UseTab repo={r} {usage} {base} />
  {:else if group}
    <GroupPublish group={r} targets={publishTargets} {query} />
  {:else}
    <PublishTab repo={r} {usage} {canUpload} {query} />
  {/if}
{/if}
