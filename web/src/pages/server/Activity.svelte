<!--
  Activity (Activity permission): what happened on the server, newest first, in one page with two
  views. Events (the audit log) filter by what happened and by repository (?action=&repo=); this
  is the only page that shows IP addresses. Publish tokens (?view=publishing) lists every publish
  token that a device, a CI job or a person got (the publish log).
-->
<script lang="ts">
  import { untrack } from "svelte";
  import type { PageProps } from "$lib/router.svelte";
  import { href, navigate, router } from "$lib/router.svelte";
  import { api, type SessionInfo } from "$lib/api";
  import { Keyset, Resource } from "$lib/load.svelte";
  import { formatDateTime } from "$lib/format";
  import { repoPhrase, serverPhrase } from "$lib/permissions";
  import FunnelXIcon from "@lucide/svelte/icons/funnel-x";
  import * as Select from "$lib/components/ui/select";
  import { Button } from "$lib/components/ui/button";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import TabNav from "$lib/components/TabNav.svelte";
  import Section from "$lib/components/Section.svelte";
  import Toolbar from "$lib/components/Toolbar.svelte";
  import DataTable from "$lib/components/DataTable.svelte";
  import Cell from "$lib/components/Cell.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import HistoryIcon from "@lucide/svelte/icons/history";
  import UploadIcon from "@lucide/svelte/icons/upload";

  let { query }: PageProps = $props();

  /** The actions the server records, grouped, with the words this page shows. */
  const actionGroups: { label: string; actions: Record<string, string> }[] = [
    { label: "Signing in", actions: { signin: "Signed in", "signin.failed": "Sign-in failed" } },
    {
      label: "Publishing",
      actions: { "session.create": "Got a publish token", "session.close": "Ended a publish token", upload: "Uploaded files", delete: "Deleted files" },
    },
    {
      label: "Repositories",
      actions: {
        "repo.create": "Created a repository",
        "repo.update": "Changed a repository",
        "repo.delete": "Deleted a repository",
        "permission.set": "Changed permissions",
        "repo.ip_rules": "Changed where from",
        "ip.denied": "Refused an address",
      },
    },
    {
      label: "Server",
      actions: {
        "user.create": "Created an account",
        "user.update": "Changed an account",
        "user.permissions": "Changed server permissions",
        "settings.update": "Changed settings",
        "ip_list.create": "Created a network list",
        "ip_list.update": "Changed a network list",
        "ip_list.delete": "Deleted a network list",
        "signing_key.create": "Added a signing key",
        "signing_key.delete": "Deleted a signing key",
      },
    },
  ];
  const actionWords: Record<string, string> = Object.assign({}, ...actionGroups.map((g) => g.actions));
  const actionText = (a: string) => actionWords[a] ?? a;

  const view = $derived(query.get("view") === "publishing" ? "publishing" : "events");
  const action = $derived(query.get("action") ?? "");
  const repo = $derived(query.get("repo") ?? "");
  const filtered = $derived(action !== "" || repo !== "");

  const repos = new Resource(() => api.repos.list());
  const repoNames = $derived((repos.data ?? []).map((r) => r.name).sort());

  const events = new Keyset((p) => api.audit({ action: action || undefined, repo: repo || undefined, ...p }));
  const sessions = new Keyset((p) => api.publish.log(p));

  // A filter change loads the first page again; the first run is the page's own load.
  let first = true;
  $effect(() => {
    void action;
    void repo;
    if (first) {
      first = false;
      return;
    }
    untrack(() => events.reload());
  });

  function setFilter(next: { action?: string; repo?: string }) {
    const a = next.action ?? action;
    const r = next.repo ?? repo;
    navigate(router.path, { query: { action: a || undefined, repo: r || undefined }, replace: true });
  }

  /** A permission set of a permission event in words: "Download, publish", "Administrator", or "none". */
  function permsText(action: string, v: unknown): string {
    const set = Array.isArray(v) ? v : [];
    const words = action === "user.permissions" ? serverPhrase(set) : repoPhrase(set);
    return words || "none";
  }

  /** The target and the facts of an event on one line: "1 · via manual", "alice · old Download · new Everything". */
  function detailText(action: string, target: string, d: Record<string, unknown> | null): string {
    const sets = action === "permission.set" || action === "user.permissions";
    const facts = Object.entries(d ?? {})
      .filter(([k]) => k !== "repo")
      .map(([k, v]) =>
        sets && (k === "old" || k === "new")
          ? `${k} ${permsText(action, v)}`
          : `${k.replace(/_/g, " ")} ${typeof v === "string" ? v : JSON.stringify(v)}`,
      );
    return [target, ...facts].filter(Boolean).join(" · ");
  }

  const viaWords: Record<SessionInfo["via"], string> = { device: "Device", oidc: "CI pipeline", manual: "Browser" };
  function who(s: SessionInfo): string {
    if (s.via === "oidc") return `${s.trust_name ?? "a deleted pipeline"}: ${s.subject}`;
    if (s.via === "device") return `${s.username ?? "a deleted account"} on ${s.device_name ?? "a deleted device"}`;
    return s.username ?? "a deleted account";
  }
</script>

<PageHeader title="Activity" description="What happened on the server, newest first." />

<TabNav
  label="Activity"
  items={[
    { label: "Events", href: href("/settings/activity"), active: view === "events", icon: HistoryIcon },
    { label: "Publish tokens", href: href("/settings/activity", { view: "publishing" }), active: view === "publishing", icon: UploadIcon },
  ]}
/>

<Section>
{#if view === "events"}
  <Toolbar>
    <Select.Root type="single" value={action || "all"} onValueChange={(v) => setFilter({ action: v === "all" ? "" : v })}>
      <Select.Trigger class="w-full sm:w-56" aria-label="What happened">
        <span class="truncate">{action ? actionText(action) : "Everything"}</span>
      </Select.Trigger>
      <Select.Content>
        <Select.Item value="all" label="Everything">Everything</Select.Item>
        {#each actionGroups as g (g.label)}
          <Select.Group>
            <Select.GroupHeading>{g.label}</Select.GroupHeading>
            {#each Object.entries(g.actions) as [a, w] (a)}
              <Select.Item value={a} label={w}>{w}</Select.Item>
            {/each}
          </Select.Group>
        {/each}
      </Select.Content>
    </Select.Root>
    <Select.Root type="single" value={repo || "all"} onValueChange={(v) => setFilter({ repo: v === "all" ? "" : v })}>
      <Select.Trigger class="w-full sm:w-56" aria-label="Repository">
        <span class="truncate">{repo || "All repositories"}</span>
      </Select.Trigger>
      <Select.Content>
        <Select.Item value="all" label="All repositories">All repositories</Select.Item>
        {#each repoNames as n (n)}
          <Select.Item value={n} label={n}>{n}</Select.Item>
        {/each}
      </Select.Content>
    </Select.Root>
    {#if filtered}
      <Button variant="ghost" onclick={() => setFilter({ action: "", repo: "" })}>Clear filters</Button>
    {/if}
  </Toolbar>

  <DataTable
    source={events}
    caption="Events"
    key={(e) => e.id}
    columns={[
      { label: "What" },
      { label: "Who" },
      { label: "Repository", hideOnPhone: true },
      { label: "Details", grow: true, hideOnPhone: true },
      { label: "Address", hideOnPhone: true },
      { label: "When", align: "right" },
    ]}
  >
    {#snippet row(e)}
      <Cell>
        {#if e.action === "signin.failed" || e.action === "ip.denied"}
          <StatusBadge tone="danger">{actionText(e.action)}</StatusBadge>
        {:else}
          <span title={e.action}>{actionText(e.action)}</span>
        {/if}
      </Cell>
      <Cell text={e.actor} />
      <Cell hideOnPhone muted={!e.repo} text={e.repo || "–"} />
      <Cell grow hideOnPhone muted text={detailText(e.action, e.target, e.detail)} />
      <Cell hideOnPhone muted text={e.ip} copy={!!e.ip} copyLabel="Copy the address" />
      <Cell num muted><span title={formatDateTime(e.at)}><RelativeTime at={e.at} /></span></Cell>
    {/snippet}
    {#snippet empty()}
      {#if filtered}
        <CompactEmpty icon={FunnelXIcon} text="No event matches. Change or clear the filters to see more.">
          <Button variant="outline" onclick={() => setFilter({ action: "", repo: "" })}>Clear filters</Button>
        </CompactEmpty>
      {:else}
        <CompactEmpty icon={HistoryIcon} text="None yet. Sign-ins, uploads and changes show here as people use the server." />
      {/if}
    {/snippet}
  </DataTable>
{:else}
  <DataTable
    source={sessions}
    caption="Publish tokens"
    key={(s) => s.id}
    columns={[
      { label: "Who", grow: true },
      { label: "Repository" },
      { label: "Through", hideOnPhone: true },
      { label: "State" },
      { label: "Started", align: "right" },
      { label: "Ended", align: "right", hideOnPhone: true },
    ]}
  >
    {#snippet row(s)}
      <Cell grow strong text={who(s)} />
      <Cell text={s.repo} />
      <Cell hideOnPhone muted text={viaWords[s.via]} />
      <Cell>
        {#if s.state === "open"}<StatusBadge tone="success">Open</StatusBadge>{:else}<StatusBadge>{s.state.charAt(0).toUpperCase() + s.state.slice(1)}</StatusBadge>{/if}
      </Cell>
      <Cell num muted><RelativeTime at={s.created_at} /></Cell>
      <Cell num muted hideOnPhone><RelativeTime at={s.closed_at} empty="–" /></Cell>
    {/snippet}
    {#snippet empty()}
      <CompactEmpty icon={UploadIcon} text="None yet. A row shows here each time a device, a CI job or a person gets a publish token." />
    {/snippet}
  </DataTable>
{/if}
</Section>
