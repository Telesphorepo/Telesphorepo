<!--
  Your account: devices, the computers that publish with the Telesphorepo CLI
  with their own key, as a list: the name, the client, when it was last used and when it expires,
  its state and Revoke. People with the People permission see the devices of every user, each
  with the person's avatar and name.
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
  import ItemList from "$lib/components/ItemList.svelte";
  import UserAvatar from "$lib/components/UserAvatar.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import LaptopIcon from "@lucide/svelte/icons/laptop";
  import { cliName, deviceState, noDevices } from "./words";

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
</script>

<PageHeader
  title="Devices"
  description={admin
    ? "Computers that publish with the Telesphorepo CLI, for every person."
    : "Computers that publish as you with the Telesphorepo CLI."}
>
  {#snippet actions()}
    {#if devices.items.length}<Button variant="outline" href={href("/device")}>Approve a device</Button>{/if}
  {/snippet}
</PageHeader>

<Section>
  <ItemList source={devices} caption="Devices" key={(d) => d.id}>
    {#snippet leading(d)}
      {#if admin && d.username}<UserAvatar name={d.username} size="md" />{:else if admin}<span class="flex size-8 items-center justify-center"><LaptopIcon class="size-5" /></span>{:else}<LaptopIcon class="size-5" />{/if}
    {/snippet}
    {#snippet title(d)}{d.name}{#if admin && d.username}<span class="text-muted-foreground"> · {d.username}</span>{/if}{/snippet}
    {#snippet meta(d)}
      {cliName} · {#if d.last_used_at}used <RelativeTime at={d.last_used_at} />{:else}never used{/if}{#if !d.revoked_at}
        · {new Date(d.expires_at) < new Date() ? "expired" : "expires"} <RelativeTime at={d.expires_at} />{/if}
    {/snippet}
    {#snippet trailing(d)}
      {@const state = deviceState(d)}
      <StatusBadge tone={state.tone}>{state.word}</StatusBadge>
      {#if !d.revoked_at}
        <Button
          variant="ghost"
          size="sm"
          class="hover:bg-destructive/10 hover:text-destructive"
          aria-label="Revoke {d.name}"
          onclick={() => {
            target = d;
            open = true;
          }}>Revoke</Button
        >
      {/if}
    {/snippet}
    {#snippet empty()}
      <CompactEmpty icon={LaptopIcon} text={noDevices}>
        <Button variant="outline" size="sm" href={href("/device")}>Approve a device</Button>
      </CompactEmpty>
    {/snippet}
  </ItemList>
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
