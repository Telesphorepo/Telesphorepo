<!--
  Your account: Overview. First the one identity card: your avatar, your name and your role, as plain text.
  Then the repositories you can use and what you can do in each, your devices and your recent
  publishes, each a Section whose table links to its full page. The "You can" column shows only
  when the rows differ. No other element repeats the role.
-->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import { href, repoPath } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { api, type Device, type SessionInfo, type UserAccess, type UserRepoAccess } from "$lib/api";
  import { Keyset, Resource, listOf } from "$lib/load.svelte";

  import * as Table from "$lib/components/ui/table";
  import { Button } from "$lib/components/ui/button";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import DataTable from "$lib/components/DataTable.svelte";
  import Cell from "$lib/components/Cell.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import FormatBadge from "$lib/components/FormatBadge.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import UserAvatar from "$lib/components/UserAvatar.svelte";
  import DatabaseIcon from "@lucide/svelte/icons/database";
  import LaptopIcon from "@lucide/svelte/icons/laptop";
  import UploadIcon from "@lucide/svelte/icons/upload";
  import { clientName, deviceState, noDevices, tokenSource, tokenState } from "./words";

  let {}: PageProps = $props();

  const me = $derived(auth.user);

  // The repositories you can use, 200 to a page.
  let access = $state<UserAccess | null>(null);
  const repos = new Keyset<UserRepoAccess>(async (p) => {
    const a = await api.myAccess(p);
    if (!p.after) access = a;
    return a;
  }, 200);

  const canPublish = $derived(access?.can_publish ?? me?.kind === "local");
  const publishes = (r: UserRepoAccess) => r.level === "write" && canPublish;
  const publishCount = $derived(repos.items.filter(publishes).length);

  // Your own devices (an administrator's list holds everyone's) and your recent publish tokens.
  const devices = new Resource<Device[]>(async () => {
    const page = await api.devices.list({ limit: 200 });
    return page.items.filter((d) => !d.username || d.username === me?.username);
  });
  const recent = new Resource<SessionInfo[]>(async () => (await api.publish.sessions({ limit: 5 })).items);

  const youCan = (r: UserRepoAccess) => (publishes(r) ? "Download and publish" : "Download");
  // Say it once: the column shows only when the rows differ.
  const mixed = $derived(new Set(repos.items.map(publishes)).size > 1);
  const through: Record<UserRepoAccess["via"], string> = {
    admin: "Administrator",
    permission: "Access given to you",
    public: "Public repository",
  };
  const openRepo = (r: UserRepoAccess) => href(repoPath(r.repo));

</script>

<PageHeader title="Overview" />

{#if me}
  <Section>
    <div class="flex min-w-0 items-center gap-4">
      <UserAvatar name={me.username} size="lg" />
      <div class="min-w-0">
        <p class="truncate text-section">{me.username}</p>
        <p class="mt-1 text-meta text-muted-foreground">{me.is_admin ? "Administrator" : "Member"}</p>
      </div>
    </div>
  </Section>
{/if}

<Section title="Your repositories">
  <DataTable
    source={repos}
    caption="Your repositories"
    key={(r) => r.repo}
    href={openRepo}
    columns={[
      { label: "Repository", grow: true },
      { label: "Format", hideOnPhone: true },
      ...(mixed ? [{ label: "You can" }] : []),
      // An administrator reaches every repository as one: the identity card says it once.
      ...(me?.is_admin ? [] : [{ label: "Through", hideOnPhone: true }]),
    ]}
  >
    {#snippet row(r)}
      <Table.Cell class="w-full max-w-0 min-w-40">
        <a href={openRepo(r)} class="block truncate rounded-sm font-medium outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50">{r.repo}</a>
      </Table.Cell>
      <Table.Cell class="max-sm:hidden"><FormatBadge format={r.format} /></Table.Cell>
      {#if mixed}<Cell class={publishes(r) ? "text-success" : "text-muted-foreground"}><span class="sm:hidden">{publishes(r) ? "Publish" : "Download"}</span><span class="max-sm:hidden">{youCan(r)}</span></Cell>{/if}
      {#if !me?.is_admin}<Cell hideOnPhone muted>{through[r.via]}</Cell>{/if}
    {/snippet}
    {#snippet empty()}
      <CompactEmpty icon={DatabaseIcon} text="No repository is public, and nobody gave you access yet. Ask an administrator." />
    {/snippet}
  </DataTable>
</Section>

<Section title="Your devices" description="Computers that publish as you with the Gradle plugin or the Maven extension.">
  {#snippet actions()}
    {#if devices.data?.length}<Button variant="outline" size="sm" href={href("/settings/devices")}>All devices</Button>{/if}
  {/snippet}
  <DataTable
    source={listOf(devices, (d) => d.slice(0, 5))}
    caption="Your devices"
    key={(d) => d.id}
    href={() => href("/settings/devices")}
    columns={[{ label: "Device", grow: true }, { label: "Client", hideOnPhone: true }, { label: "Last used", align: "right" }, { label: "Status" }]}
  >
    {#snippet row(d)}
      {@const state = deviceState(d)}
      <Cell grow strong text={d.name} />
      <Cell hideOnPhone muted>{clientName(d)}</Cell>
      <Cell num muted><RelativeTime at={d.last_used_at} empty="Never" /></Cell>
      <Cell><StatusBadge tone={state.tone}>{state.word}</StatusBadge></Cell>
    {/snippet}
    {#snippet empty()}
      <CompactEmpty icon={LaptopIcon} text={noDevices}>
        <Button variant="outline" size="sm" href={href("/device")}>Approve a device</Button>
      </CompactEmpty>
    {/snippet}
  </DataTable>
</Section>

<Section title="Recent publishes" description="The publish tokens that you and your build tools used last.">
  {#snippet actions()}
    {#if recent.data?.length}<Button variant="outline" size="sm" href={href("/settings/tokens")}>All publish tokens</Button>{/if}
  {/snippet}
  <DataTable
    source={listOf(recent)}
    caption="Recent publishes"
    key={(s) => s.id}
    href={(s) => href(repoPath(s.repo))}
    columns={[{ label: "Repository", grow: true }, { label: "From", hideOnPhone: true }, { label: "Started", align: "right" }, { label: "Status", hideOnPhone: true }]}
  >
    {#snippet row(s)}
      <Table.Cell class="w-full max-w-0 min-w-40">
        <a href={href(repoPath(s.repo))} class="block truncate rounded-sm font-medium outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50">{s.repo}</a>
      </Table.Cell>
      <Cell hideOnPhone muted text={tokenSource(s)} />
      <Cell num muted><RelativeTime at={s.created_at} /></Cell>
      <Cell hideOnPhone>{@const st = tokenState(s)}<StatusBadge tone={st.tone}>{st.word}</StatusBadge></Cell>
    {/snippet}
    {#snippet empty()}
      <CompactEmpty
        icon={UploadIcon}
        text={canPublish && publishCount
          ? "Each publish from your computer or from this site shows here."
          : "You cannot publish to a repository yet. Ask an administrator for access."}
      />
    {/snippet}
  </DataTable>
</Section>
