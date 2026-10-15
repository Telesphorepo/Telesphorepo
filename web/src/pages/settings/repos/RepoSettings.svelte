<!--
  A repository's settings inside the Settings shell (route /settings/repositories/:name/:tab):
  the sidebar stays, "All repositories" stays open in it, and the breadcrumbs say where the page
  sits (Settings › Repositories › maven › Access). The header names the repository; the tabs are
  pages with their own address, so the back button returns to the previous tab. The tab bodies are
  the other files of this folder. The tabs depend on the kind: a hosted repository has Overview,
  Access, Format settings (and Signing for Debian); a proxy has Overview, Access and Remote; a group
  has Overview and Access.
-->
<script lang="ts">
  import { api, ApiError, type Repo } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import { filesPath, href, repoSettingsPath, type PageProps, type RepoSettingsTab } from "$lib/router.svelte";
  import { Button } from "$lib/components/ui/button";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import TabNav from "$lib/components/TabNav.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import FormatBadge from "$lib/components/FormatBadge.svelte";
  import OverviewTab from "./OverviewTab.svelte";
  import AccessTab from "./AccessTab.svelte";
  import SigningTab from "./SigningTab.svelte";
  import FormatTab from "./FormatTab.svelte";
  import DeleteRepo from "./DeleteRepo.svelte";
  import InfoIcon from "@lucide/svelte/icons/info";
  import UsersIcon from "@lucide/svelte/icons/users-round";
  import FileKeyIcon from "@lucide/svelte/icons/file-key";
  import SlidersIcon from "@lucide/svelte/icons/sliders-horizontal";
  import FolderOpenIcon from "@lucide/svelte/icons/folder-open";
  import LockIcon from "@lucide/svelte/icons/lock";
  import SearchXIcon from "@lucide/svelte/icons/search-x";
  import CompassIcon from "@lucide/svelte/icons/compass";
  import GlobeIcon from "@lucide/svelte/icons/globe";
  import RepoKindIcon from "$lib/components/RepoKindIcon.svelte";
  import RemoteTab from "./RemoteTab.svelte";

  let { params }: PageProps = $props();

  // null: no repository of that name.
  const repo = new Resource<Repo | null>(
    async () => {
      try {
        return await api.repos.get(params.name);
      } catch (e) {
        if (e instanceof ApiError && e.status === 404) return null;
        throw e;
      }
    },
    { lazy: true },
  );
  $effect(() => {
    void params.name;
    repo.reload();
  });

  const r = $derived(repo.data?.name === params.name ? repo.data : repo.data === null ? null : undefined);

  // A group and a proxy have no format settings and sign nothing; a proxy has its Remote tab.
  const tabs = $derived.by(() => {
    const t: { id: RepoSettingsTab; label: string; icon: typeof InfoIcon }[] = [
      { id: "overview", label: "Overview", icon: InfoIcon },
      { id: "access", label: "Access", icon: UsersIcon },
    ];
    if (r?.kind === "proxy") t.push({ id: "remote", label: "Remote", icon: GlobeIcon });
    if (r?.kind === "hosted") {
      t.push({ id: "format", label: "Format settings", icon: SlidersIcon });
      if (r.format === "debian") t.push({ id: "signing", label: "Signing", icon: FileKeyIcon });
    }
    return t;
  });
  const open = $derived(tabs.find((t) => t.id === params.tab));
  let deleteOpen = $state(false);
</script>

{#if r === undefined && repo.error}
  <PageHeader title={params.name} crumbs={[{ label: params.name }]} />
  <ErrorState error={repo.error} title="The repository did not load" onRetry={repo.reload} />
{:else if r === undefined}
  <PageHeader title={params.name} crumbs={[{ label: params.name }]} />
  <Loading rows={5} />
{:else if r === null}
  <PageHeader title={params.name} crumbs={[{ label: params.name }]} />
  <EmptyState icon={SearchXIcon} title="Repository not found" description="No repository named {params.name} exists. It may have been deleted.">
    <Button variant="outline" href={href("/settings/repositories")}>Show all repositories</Button>
  </EmptyState>
{:else}
  <PageHeader
    title={r.name}
    crumbs={[{ label: r.name, href: href(repoSettingsPath(r.name)) }, { label: open?.label ?? "Not found" }]}
  >
    {#snippet meta()}
      {#if !r.anonymous_read}<LockIcon class="size-4 text-muted-foreground" aria-label="Private" />{/if}
      <span class="text-muted-foreground"><FormatBadge format={r.format} /></span>
      <RepoKindIcon kind={r.kind} iconClass="size-4" />
    {/snippet}
    {#snippet actions()}
      <!-- A group has no files. -->
      {#if r.kind !== "group"}
        <Button variant="outline" href={href(filesPath(r.name))}><FolderOpenIcon />Open files</Button>
      {/if}
      <Button variant="ghost" class="hover:bg-destructive/10 hover:text-destructive" onclick={() => (deleteOpen = true)}>Delete</Button>
    {/snippet}
  </PageHeader>

  <TabNav
    label="Settings of {r.name}"
    items={tabs.map((t) => ({ label: t.label, icon: t.icon, href: href(repoSettingsPath(r.name, t.id)), active: t.id === open?.id }))}
  />

  {#key r.name}
    {#if !open}
      <EmptyState icon={CompassIcon} title="This page does not exist" description="Choose one of the tabs above." />
    {:else if open.id === "overview"}
      <OverviewTab repo={r} onChanged={repo.reload} />
    {:else if open.id === "access"}
      <AccessTab repo={r} onChanged={repo.reload} />
    {:else if open.id === "format"}
      <FormatTab repo={r} onChanged={repo.reload} />
    {:else if open.id === "signing"}
      <SigningTab repo={r} onChanged={repo.reload} />
    {:else if open.id === "remote"}
      <RemoteTab repo={r} onChanged={repo.reload} />
    {/if}
    <DeleteRepo repo={r} bind:open={deleteOpen} />
  {/key}
{/if}
