<!--
  People (People permission): every account as a list row, with its avatar, its role as a badge
  (Administrator, the phrase of its server permissions, or none for a member) and, under the name,
  its email, how it signs in (when that differs between people) and when it was added. A row opens
  the person's page (/settings/people/:name), where the account, its server permissions and its
  permissions on each repository change. New account opens a short form; after Create, the new
  person's page opens to set the permissions. Single sign-on accounts appear after their first
  sign-in. Pick rows (not your own, not the only administrator's) to disable people (after a
  confirm) or enable them at once.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import type { PageProps } from "$lib/router.svelte";
  import { href } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { serverPhrase } from "$lib/permissions";
  import { api, errorMessage, type User } from "$lib/api";
  import { Keyset, Resource } from "$lib/load.svelte";
  import { count, someNames } from "$lib/words";
  import { Button } from "$lib/components/ui/button";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import ItemList from "$lib/components/ItemList.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import UserAvatar from "$lib/components/UserAvatar.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import NewAccountDialog from "./people/NewAccountDialog.svelte";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import UsersIcon from "@lucide/svelte/icons/users-round";

  let {}: PageProps = $props();

  const users = new Keyset((p) => api.users.list(p));
  // The only enabled administrator cannot be disabled; the server refuses it too.
  const admins = new Resource(() => api.users.administrators());
  const personPath = (name: string) => `/settings/people/${encodeURIComponent(name)}`;
  const rowHref = (u: User) => href(personPath(u.username));
  // How people sign in shows only when it differs between them.
  const mixedSignIn = $derived(users.items.some((u) => u.kind === "oidc"));

  let createOpen = $state(false);
  const startCreate = () => (createOpen = true);

  // --- bulk actions ---
  let picked = $state(new Set<number>());
  const names = $derived(users.items.filter((u) => picked.has(u.id)).map((u) => u.username));
  const people = (n: number) => count(n, "person", "people");
  let busy = $state(false);
  let disableOpen = $state(false);

  async function setDisabled(disabled: boolean) {
    const list = names;
    try {
      await api.users.setDisabled(list, disabled);
      toast.success(disabled ? `${count(list.length, "person is", "people are")} now disabled.` : `${count(list.length, "person can", "people can")} sign in again.`);
      picked = new Set();
    } finally {
      await Promise.all([users.reload(), admins.reload()]);
    }
  }

  async function enable() {
    busy = true;
    try {
      await setDisabled(false);
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      busy = false;
    }
  }
</script>

<PageHeader title="People">
  {#snippet actions()}<Button onclick={startCreate}><PlusIcon />New account</Button>{/snippet}
</PageHeader>

<Section>
  <ItemList
    source={users}
    caption="People"
    key={(u) => u.id}
    href={rowHref}
    bind:picked
    pickable={(u) => u.username !== auth.user?.username && !(admins.data?.length === 1 && admins.data[0] === u.username)}
    pickLabel={(u) => u.username}
  >
    {#snippet leading(u)}<UserAvatar name={u.username} size="md" />{/snippet}
    {#snippet title(u)}{u.username}{#if u.username === auth.user?.username}<span class="text-muted-foreground">{" · you"}</span>{/if}{/snippet}
    {#snippet badges(u)}
      {#if u.disabled}
        <StatusBadge tone="warning">Disabled</StatusBadge>
      {:else if serverPhrase(u.permissions)}
        <StatusBadge tone="neutral">{serverPhrase(u.permissions)}</StatusBadge>
      {/if}
    {/snippet}
    {#snippet meta(u)}
      {#if u.email}{u.email} · {/if}{#if mixedSignIn}{u.kind === "oidc" ? "Single sign-on" : "Password"} · {/if}added <RelativeTime at={u.created_at} />
    {/snippet}
    {#snippet bulk()}
      <Button variant="outline" size="sm" disabled={busy} onclick={enable}>Enable</Button>
      <Button variant="ghost" size="sm" class="hover:bg-destructive/10 hover:text-destructive" disabled={busy} onclick={() => (disableOpen = true)}>Disable</Button>
    {/snippet}
    {#snippet empty()}
      <CompactEmpty icon={UsersIcon} text="Only you so far. Add an account for each person who publishes or downloads private files.">
        <Button variant="outline" onclick={startCreate}><PlusIcon />New account</Button>
      </CompactEmpty>
    {/snippet}
  </ItemList>
</Section>

<ConfirmDialog
  bind:open={disableOpen}
  title="Disable {people(names.length)}?"
  description="They are signed out and cannot sign in."
  confirmLabel="Disable {people(names.length)}"
  destructive
  changes={[`Disables ${someNames(names)}`, "Their read tokens, devices and publish tokens stop working"]}
  onConfirm={() => setDisabled(true)}
/>

<NewAccountDialog bind:open={createOpen} />
