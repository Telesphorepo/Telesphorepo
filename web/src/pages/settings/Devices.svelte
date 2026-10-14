<!-- Account: enrolled devices (Gradle plugin, Maven extension). Admins see every user's devices. -->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import type { PageProps } from "$lib/router.svelte";
  import { href } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { api, type Device } from "$lib/api";
  import { Keyset } from "$lib/load.svelte";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import KeysetTable from "$lib/components/KeysetTable.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import * as Table from "$lib/components/ui/table";
  import { Badge } from "$lib/components/ui/badge";
  import { Button } from "$lib/components/ui/button";
  import MonitorIcon from "@lucide/svelte/icons/monitor-smartphone";

  let { params, query }: PageProps = $props();

  const devices = new Keyset((p) => api.devices.list(p));
  const admin = $derived(auth.isAdmin);

  let target = $state<Device | null>(null);
  let open = $state(false);

  function askRevoke(d: Device) {
    target = d;
    open = true;
  }

  async function revoke() {
    if (!target) return;
    await api.devices.revoke(target.id);
    toast.success(`Revoked the device ${target.name}`);
    await devices.reload();
  }

  const stateOf = (d: Device): "active" | "expired" | "revoked" =>
    d.revoked_at ? "revoked" : new Date(d.expires_at) < new Date() ? "expired" : "active";
</script>

<PageHeader
  title="Devices"
  description={"A device is a machine that enrolled with the Gradle plugin or the Maven extension. It publishes with its own key and needs no password." +
    (admin ? " As an administrator you see the devices of all users." : "")}
>
  {#snippet actions()}
    <Button variant="outline" href={href("/device")}>Approve a device</Button>
  {/snippet}
</PageHeader>

<KeysetTable list={devices} columns={admin ? 8 : 7} caption="Devices">
  {#snippet head()}
    <Table.Head>Name</Table.Head>
    {#if admin}<Table.Head>User</Table.Head>{/if}
    <Table.Head>Client</Table.Head>
    <Table.Head>Enrolled</Table.Head>
    <Table.Head>Valid until</Table.Head>
    <Table.Head>Last used</Table.Head>
    <Table.Head>State</Table.Head>
    <Table.Head class="w-0"><span class="sr-only">Actions</span></Table.Head>
  {/snippet}
  {#snippet row(d)}
    {@const state = stateOf(d)}
    <Table.Row>
      <Table.Cell class="font-medium">{d.name}</Table.Cell>
      {#if admin}<Table.Cell>{d.username ?? ""}</Table.Cell>{/if}
      <Table.Cell>{d.kind === "gradle" ? "Gradle plugin" : "Maven extension"}</Table.Cell>
      <Table.Cell><RelativeTime at={d.created_at} /></Table.Cell>
      <Table.Cell><RelativeTime at={d.expires_at} /></Table.Cell>
      <Table.Cell><RelativeTime at={d.last_used_at} empty="never" /></Table.Cell>
      <Table.Cell>
        <Badge variant={state === "active" ? "default" : state === "revoked" ? "destructive" : "secondary"} class="capitalize">{state}</Badge>
      </Table.Cell>
      <Table.Cell class="text-right">
        {#if !d.revoked_at}
          <Button variant="destructive" size="sm" onclick={() => askRevoke(d)}>Revoke</Button>
        {/if}
      </Table.Cell>
    </Table.Row>
  {/snippet}
  {#snippet empty()}
    <EmptyState
      icon={MonitorIcon}
      title="No devices yet"
      description="Run the login command of the Gradle plugin or the Maven extension on a machine. It prints a code to approve."
    >
      <Button variant="outline" href={href("/device")}>Approve a device</Button>
    </EmptyState>
  {/snippet}
</KeysetTable>

<ConfirmDialog
  bind:open
  title="Revoke device"
  confirmLabel="Revoke device"
  destructive
  changes={[
    `Stops the device ${target?.name ?? ""} at once`,
    "Ends its open publish sessions",
    "The machine must enroll again to publish",
  ]}
  onConfirm={revoke}
/>
