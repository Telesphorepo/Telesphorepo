<!--
  Admin: one repository. params.name is the repo; params.tab is general (default), permissions,
  config, status or danger.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import type { PageProps } from "$lib/router.svelte";
  import { href, navigate } from "$lib/router.svelte";
  import { api } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import * as Tabs from "$lib/components/ui/tabs";
  import * as Breadcrumb from "$lib/components/ui/breadcrumb";
  import * as Alert from "$lib/components/ui/alert";
  import { Button } from "$lib/components/ui/button";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import LoadingRows from "$lib/components/LoadingRows.svelte";
  import FormatBadge from "$lib/components/FormatBadge.svelte";
  import LockIcon from "@lucide/svelte/icons/lock";
  import FolderOpenIcon from "@lucide/svelte/icons/folder-open";
  import GeneralTab from "./repos/GeneralTab.svelte";
  import PermissionsTab from "./repos/PermissionsTab.svelte";
  import ConfigTab from "./repos/ConfigTab.svelte";
  import StatusTab from "./repos/StatusTab.svelte";
  import DangerTab from "./repos/DangerTab.svelte";

  let { params, query }: PageProps = $props();

  const tabs = [
    { id: "general", label: "General" },
    { id: "permissions", label: "Permissions" },
    { id: "config", label: "Config" },
    { id: "status", label: "Status" },
    { id: "danger", label: "Danger zone" },
  ];
  const tab = $derived(tabs.some((t) => t.id === params.tab) ? params.tab : "general");

  const repo = new Resource(() => api.repos.get(params.name), { lazy: true });
  const formats = new Resource(() => api.formats.list());
  // Load the repo again when the URL names another one.
  $effect(() => {
    params.name;
    untrack(() => repo.reload());
  });

  const current = $derived(repo.data?.name === params.name ? repo.data : undefined);
  const formatTitle = $derived(formats.data?.find((f) => f.name === current?.format)?.title);
  // A save changes the repo: reload quietly, so the tabs keep their state.
  const changed = () => repo.reload();
</script>

<PageHeader title={params.name} description={current?.description || undefined}>
  {#snippet breadcrumbs()}
    <Breadcrumb.Root>
      <Breadcrumb.List>
        <Breadcrumb.Item><Breadcrumb.Link href={href("/settings/repos")}>Repositories</Breadcrumb.Link></Breadcrumb.Item>
        <Breadcrumb.Separator />
        <Breadcrumb.Item><Breadcrumb.Page class="font-mono">{params.name}</Breadcrumb.Page></Breadcrumb.Item>
      </Breadcrumb.List>
    </Breadcrumb.Root>
  {/snippet}
  {#snippet actions()}
    <Button variant="outline" href={href("/repo/" + encodeURIComponent(params.name) + "/")}>
      <FolderOpenIcon />
      Browse files
    </Button>
  {/snippet}
  {#if current}<FormatBadge format={current.format} title={formatTitle} />{/if}
</PageHeader>

{#if current}
  {#if current.locked_by}
    <Alert.Root class="mb-8">
      <LockIcon />
      <Alert.Title>Uploads and deletes are paused</Alert.Title>
      <Alert.Description>{current.locked_by}.</Alert.Description>
    </Alert.Root>
  {/if}
  <Tabs.Root value={tab} onValueChange={(v) => navigate(`/settings/repos/${encodeURIComponent(params.name)}/${v}`, { replace: true })}>
    <Tabs.List class="mb-8 max-w-full justify-start self-start overflow-x-auto">
      {#each tabs as t (t.id)}<Tabs.Trigger value={t.id}>{t.label}</Tabs.Trigger>{/each}
    </Tabs.List>
    <!-- Only the open tab mounts, so a tab loads its data when it opens. -->
    <Tabs.Content value="general">{#if tab === "general"}<GeneralTab repo={current} onChanged={changed} />{/if}</Tabs.Content>
    <Tabs.Content value="permissions">{#if tab === "permissions"}<PermissionsTab repo={current} />{/if}</Tabs.Content>
    <Tabs.Content value="config">{#if tab === "config"}<ConfigTab repo={current} onChanged={changed} />{/if}</Tabs.Content>
    <Tabs.Content value="status">{#if tab === "status"}<StatusTab repo={current} />{/if}</Tabs.Content>
    <Tabs.Content value="danger">{#if tab === "danger"}<DangerTab repo={current} />{/if}</Tabs.Content>
  </Tabs.Root>
{:else if repo.error}
  <ErrorState error={repo.error} title="The repository did not load" onRetry={repo.reload} />
{:else}
  <LoadingRows rows={4} />
{/if}
