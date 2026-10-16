<!--
  Your account: devices, the computers that publish with the publisher client of their build tool
  with their own key. People with the People permission see the devices of every user.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import type { PageProps } from "$lib/router.svelte";
  import { href } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { api, type Device } from "$lib/api";
  import { Keyset } from "$lib/load.svelte";
  import { Button } from "$lib/components/ui/button";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import DataTable, { type Column } from "$lib/components/DataTable.svelte";
  import Cell from "$lib/components/Cell.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import LaptopIcon from "@lucide/svelte/icons/laptop";
  import { clientName, deviceState, noDevices } from "./words";

  let {}: PageProps = $props();

  const devices = new Keyset((p) => api.devices.list(p));
  // A holder of People gets every person's devices.
  const admin = $derived(auth.can("people.manage"));

  let target = $state<Device | null>(null);
  let open = $state(false);

  async function revoke() {
    if (!target) return;
    await api.devices.revoke(target.id);
    toast.success(`Revoked the device ${target.name}`);
    await devices.reload();
  }

  const columns = $derived<Column[]>([
    { label: "Device", grow: true },
    ...(admin ? [{ label: "Person", hideOnPhone: true }] : []),
    { label: "Client", hideOnPhone: true },
    { label: "Last used", align: "right" as const },
    { label: "Works until", align: "right" as const, hideOnPhone: true },
    { label: "Status" },
    { label: "", srLabel: "Actions" },
  ]);
</script>

<PageHeader
  title="Devices"
  description={admin
    ? "Computers that publish with the publisher client of their build tool, for every person."
    : "Computers that publish as you with the publisher client of your build tool."}
>
  {#snippet actions()}
    {#if devices.items.length}<Button variant="outline" href={href("/device")}>Approve a device</Button>{/if}
  {/snippet}
</PageHeader>

<Section>
  <DataTable source={devices} {columns} caption="Devices" key={(d) => d.id}>
    {#snippet row(d)}
      {@const state = deviceState(d)}
      <Cell grow strong text={d.name} />
      {#if admin}<Cell hideOnPhone text={d.username ?? ""} />{/if}
      <Cell hideOnPhone muted>{clientName(d)}</Cell>
      <Cell num muted><RelativeTime at={d.last_used_at} empty="Never" /></Cell>
      <Cell num muted hideOnPhone>{#if d.revoked_at}<span>—</span>{:else}<RelativeTime at={d.expires_at} />{/if}</Cell>
      <Cell><StatusBadge tone={state.tone}>{state.word}</StatusBadge></Cell>
      <Cell actions>
        {#if !d.revoked_at}
          <Button
            variant="ghost"
            size="sm"
            class="text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
            aria-label="Revoke {d.name}"
            onclick={() => {
              target = d;
              open = true;
            }}>Revoke</Button
          >
        {/if}
      </Cell>
    {/snippet}
    {#snippet empty()}
      <CompactEmpty icon={LaptopIcon} text={noDevices}>
        <Button variant="outline" size="sm" href={href("/device")}>Approve a device</Button>
      </CompactEmpty>
    {/snippet}
  </DataTable>
</Section>

<ConfirmDialog
  bind:open
  title="Revoke this device?"
  confirmLabel="Revoke device"
  destructive
  changes={[
    `Stops the device ${target?.name ?? ""} at once`,
    "Ends its open publish tokens",
    "To publish from that computer again, someone signs in there again",
  ]}
  onConfirm={revoke}
/>
