<!-- Admin: publish history, every publish session, newest first. -->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import { api, type SessionInfo } from "$lib/api";
  import { Keyset } from "$lib/load.svelte";
  import { Badge } from "$lib/components/ui/badge";
  import * as Table from "$lib/components/ui/table";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import KeysetTable from "$lib/components/KeysetTable.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import ScrollTextIcon from "@lucide/svelte/icons/scroll-text";

  let { params, query }: PageProps = $props();

  const list = new Keyset((p) => api.publish.log(p));

  const viaLabel: Record<SessionInfo["via"], string> = { device: "Build tool", oidc: "CI rule", manual: "Temporary password" };

  function who(s: SessionInfo): string {
    if (s.via === "oidc") return `${s.trust_name ?? "deleted rule"}: ${s.subject}`;
    if (s.via === "device") return `${s.username ?? "deleted user"} on ${s.device_name ?? "deleted device"}`;
    return s.username ?? "deleted user";
  }

  const stateVariant = (state: string) => (state === "open" ? "default" : "secondary");
</script>

<PageHeader title="Publish history" description="Each time a person, a build tool or a CI job got permission to publish, newest first. Ended entries stay for 30 days." />

<KeysetTable {list} columns={6} caption="Publish history">
  {#snippet head()}
    <Table.Head>Started</Table.Head>
    <Table.Head>Repository</Table.Head>
    <Table.Head>How</Table.Head>
    <Table.Head>Who</Table.Head>
    <Table.Head>Ended</Table.Head>
    <Table.Head>State</Table.Head>
  {/snippet}
  {#snippet row(s)}
    <Table.Row>
      <Table.Cell><RelativeTime at={s.created_at} /></Table.Cell>
      <Table.Cell class="font-mono text-[0.8125rem]">{s.repo}</Table.Cell>
      <Table.Cell>{viaLabel[s.via]}</Table.Cell>
      <Table.Cell class="break-words whitespace-normal">{who(s)}</Table.Cell>
      <Table.Cell><RelativeTime at={s.closed_at} empty="" /></Table.Cell>
      <Table.Cell><Badge variant={stateVariant(s.state)} class="capitalize">{s.state}</Badge></Table.Cell>
    </Table.Row>
  {/snippet}
  {#snippet empty()}
    <EmptyState
      icon={ScrollTextIcon}
      title="Nothing published yet"
      description="An entry appears here when a build tool, a CI job or a person starts to publish."
    />
  {/snippet}
</KeysetTable>
