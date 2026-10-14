<!--
  Admin: the audit log, newest first. query: action, repo. This is the only page that shows IP
  addresses.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { navigate, router, type PageProps } from "$lib/router.svelte";
  import { api } from "$lib/api";
  import { Keyset } from "$lib/load.svelte";
  import { formatDateTime } from "$lib/format";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import * as Select from "$lib/components/ui/select";
  import * as Table from "$lib/components/ui/table";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import KeysetTable from "$lib/components/KeysetTable.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import ScrollTextIcon from "@lucide/svelte/icons/scroll-text";

  let { params, query }: PageProps = $props();

  const actions = [
    "signin",
    "signin.failed",
    "session.create",
    "session.close",
    "upload",
    "delete",
    "repo.create",
    "repo.update",
    "repo.delete",
    "permission.set",
    "permission.delete",
    "user.create",
    "user.update",
    "settings.update",
  ];

  const action = $derived(query.get("action") ?? "");
  const repo = $derived(query.get("repo") ?? "");
  let repoText = $state(untrack(() => query.get("repo") ?? ""));

  const list = new Keyset((p) => api.audit({ action: action || undefined, repo: repo || undefined, ...p }));

  // A filter change loads the first page again. The first run is the page's own load.
  let first = true;
  $effect(() => {
    action;
    repo;
    if (first) {
      first = false;
      return;
    }
    untrack(() => list.reload());
  });

  function setFilter(next: { action?: string; repo?: string }) {
    const q = { action: next.action ?? action, repo: next.repo ?? repo };
    navigate(router.path, { query: { action: q.action || undefined, repo: q.repo || undefined }, replace: true });
  }

  function applyRepo(e: SubmitEvent) {
    e.preventDefault();
    setFilter({ repo: repoText.trim() });
  }

  function detailText(d: Record<string, unknown> | null): string {
    if (!d) return "";
    return Object.entries(d)
      .map(([k, v]) => `${k}: ${typeof v === "string" ? v : JSON.stringify(v)}`)
      .join(", ");
  }

  const filtered = $derived(action !== "" || repo !== "");
</script>

<PageHeader title="Audit log" description="Sign-ins, sessions, uploads, deletes and configuration changes, newest first. This page is the only place the server shows IP addresses." />

<form class="mb-8 flex flex-wrap items-end gap-4" onsubmit={applyRepo}>
  <div class="flex flex-col gap-2">
    <Label for="audit-action">Action</Label>
    <Select.Root type="single" value={action || "all"} onValueChange={(v) => setFilter({ action: v === "all" ? "" : v })}>
      <Select.Trigger id="audit-action" class="w-48">
        <span class="truncate {action ? 'font-mono' : ''}">{action || "All actions"}</span>
      </Select.Trigger>
      <Select.Content>
        <Select.Item value="all" label="All actions">All actions</Select.Item>
        {#each actions as a (a)}
          <Select.Item value={a} label={a} class="font-mono">{a}</Select.Item>
        {/each}
      </Select.Content>
    </Select.Root>
  </div>
  <div class="flex flex-col gap-2">
    <Label for="audit-repo">Repository</Label>
    <div class="flex gap-2">
      <Input id="audit-repo" class="w-48 font-mono" placeholder="All repositories" autocomplete="off" spellcheck={false} bind:value={repoText} />
      <Button type="submit" variant="outline">Apply</Button>
    </div>
  </div>
  {#if filtered}
    <Button
      type="button"
      variant="ghost"
      onclick={() => {
        repoText = "";
        setFilter({ action: "", repo: "" });
      }}>Clear filters</Button
    >
  {/if}
</form>

<KeysetTable {list} columns={7} caption="Audit events">
  {#snippet head()}
    <Table.Head>When</Table.Head>
    <Table.Head>Actor</Table.Head>
    <Table.Head>Action</Table.Head>
    <Table.Head>Repository</Table.Head>
    <Table.Head>Target</Table.Head>
    <Table.Head>Detail</Table.Head>
    <Table.Head>IP address</Table.Head>
  {/snippet}
  {#snippet row(e)}
    <Table.Row>
      <Table.Cell class="text-[0.8125rem]">{formatDateTime(e.at)}</Table.Cell>
      <Table.Cell class="break-words whitespace-normal">{e.actor}</Table.Cell>
      <Table.Cell class="font-mono text-[0.8125rem]">{e.action}</Table.Cell>
      <Table.Cell class="font-mono text-[0.8125rem]">{e.repo}</Table.Cell>
      <Table.Cell class="max-w-56 font-mono text-[0.8125rem] break-all whitespace-normal">{e.target}</Table.Cell>
      <Table.Cell class="max-w-64 text-[0.8125rem] break-words whitespace-normal text-muted-foreground">{detailText(e.detail)}</Table.Cell>
      <Table.Cell class="font-mono text-[0.8125rem]">{e.ip}</Table.Cell>
    </Table.Row>
  {/snippet}
  {#snippet empty()}
    <EmptyState
      icon={ScrollTextIcon}
      title={filtered ? "No events match these filters" : "No audit events yet"}
      description={filtered ? "Change or clear the filters to see more events." : "The server records events here as people sign in and change things."}
    >
      {#if filtered}
        <Button
          variant="outline"
          onclick={() => {
            repoText = "";
            setFilter({ action: "", repo: "" });
          }}>Clear filters</Button
        >
      {/if}
    </EmptyState>
  {/snippet}
</KeysetTable>
