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

  /** The actions the server records, with the words the page shows for them. */
  const actionLabels: Record<string, string> = {
    signin: "Signed in",
    "signin.failed": "Sign-in failed",
    "session.create": "Started publishing",
    "session.close": "Stopped publishing",
    upload: "Uploaded",
    delete: "Deleted files",
    "repo.create": "Created a repository",
    "repo.update": "Changed a repository",
    "repo.delete": "Deleted a repository",
    "permission.set": "Gave access",
    "permission.delete": "Removed access",
    "user.create": "Created a user",
    "user.update": "Changed a user",
    "settings.update": "Changed settings",
  };
  const actions = Object.keys(actionLabels);
  const actionText = (a: string) => actionLabels[a] ?? a;

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

<PageHeader title="Activity log" description="Sign-ins, publishing, uploads, deletes and setting changes, newest first. This is the only page that shows IP addresses." />

<form class="mb-8 flex flex-wrap items-end gap-4" onsubmit={applyRepo}>
  <div class="flex flex-col gap-2">
    <Label for="audit-action">What happened</Label>
    <Select.Root type="single" value={action || "all"} onValueChange={(v) => setFilter({ action: v === "all" ? "" : v })}>
      <Select.Trigger id="audit-action" class="w-48">
        <span class="truncate">{action ? actionText(action) : "Everything"}</span>
      </Select.Trigger>
      <Select.Content>
        <Select.Item value="all" label="Everything">Everything</Select.Item>
        {#each actions as a (a)}
          <Select.Item value={a} label={actionText(a)}>{actionText(a)}</Select.Item>
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

<KeysetTable {list} columns={7} caption="Activity log">
  {#snippet head()}
    <Table.Head>When</Table.Head>
    <Table.Head>Who</Table.Head>
    <Table.Head>What</Table.Head>
    <Table.Head>Repository</Table.Head>
    <Table.Head>Item</Table.Head>
    <Table.Head>Details</Table.Head>
    <Table.Head>IP address</Table.Head>
  {/snippet}
  {#snippet row(e)}
    <Table.Row>
      <Table.Cell class="text-[0.8125rem]">{formatDateTime(e.at)}</Table.Cell>
      <Table.Cell class="break-words whitespace-normal">{e.actor}</Table.Cell>
      <Table.Cell title={e.action}>{actionText(e.action)}</Table.Cell>
      <Table.Cell class="font-mono text-[0.8125rem]">{e.repo}</Table.Cell>
      <Table.Cell class="max-w-56 font-mono text-[0.8125rem] break-all whitespace-normal">{e.target}</Table.Cell>
      <Table.Cell class="max-w-64 text-[0.8125rem] break-words whitespace-normal text-muted-foreground">{detailText(e.detail)}</Table.Cell>
      <Table.Cell class="font-mono text-[0.8125rem]">{e.ip}</Table.Cell>
    </Table.Row>
  {/snippet}
  {#snippet empty()}
    <EmptyState
      icon={ScrollTextIcon}
      title={filtered ? "Nothing matches these filters" : "No activity yet"}
      description={filtered ? "Change or clear the filters to see more." : "Sign-ins, uploads and changes appear here as people use the server."}
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
