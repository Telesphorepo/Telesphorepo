<!--
  People (People permission): every account as a list row, with its avatar, its role as a badge
  (Administrator, the phrase of its server permissions, or none for a member) and, under the name,
  its email, how it signs in (when that differs between people) and when it was added. A row opens
  the person's page (/settings/people/:name), where the account, its server permissions and its
  permissions on each repository change. New account opens a short form; after Create, the new
  person's page opens to set the permissions. Single sign-on accounts appear after their first
  sign-in. The search field keeps the people whose name starts with what is typed: the server
  answers it (GET /api/users?q=), 250 ms after the last key. Pick rows (not your own, not the only
  administrator's) to disable people (after a confirm) or enable them at once.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { toast } from "svelte-sonner";
  import type { PageProps } from "$lib/router.svelte";
  import { href } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { serverPhrase } from "$lib/permissions";
  import { api, errorMessage, type User } from "$lib/api";
  import { Keyset, Resource } from "$lib/load.svelte";
  import { count, someNames } from "$lib/words";
  import { Button } from "$lib/components/ui/button";
  import * as InputGroup from "$lib/components/ui/input-group";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Toolbar from "$lib/components/Toolbar.svelte";
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
  import SearchIcon from "@lucide/svelte/icons/search";
  import SearchXIcon from "@lucide/svelte/icons/search-x";

  let {}: PageProps = $props();

  // The search asks the server 250 ms after the last key; a new prefix starts a new list.
  let filter = $state("");
  let prefix = $state("");
  $effect(() => {
    const f = filter.trim().toLowerCase();
    const t = setTimeout(() => (prefix = f), 250);
    return () => clearTimeout(t);
  });
  let users = $state<Keyset<User>>(untrack(() => new Keyset((p) => api.users.list(p))));
  let first = true;
  $effect(() => {
    const q = prefix;
    if (first) {
      first = false;
      return;
    }
    users = untrack(() => new Keyset((p) => api.users.list(p, q)));
    picked = new Set();
  });
  // The search shows once there is someone besides you, also while it finds no one.
  let any = $state(false);
  $effect(() => {
    if (users.items.length > 1) any = true;
  });
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

{#if any || prefix}
  <Toolbar>
    <InputGroup.Root class="sm:w-72">
      <InputGroup.Addon><SearchIcon /></InputGroup.Addon>
      <InputGroup.Input bind:value={filter} type="search" placeholder="Search by name" aria-label="Search people by the start of the name" />
    </InputGroup.Root>
  </Toolbar>
{/if}

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
      {#if u.email}{u.email}{" · "}{/if}{#if mixedSignIn}{u.kind === "oidc" ? "Single sign-on" : "Password"}{" · "}{/if}added <RelativeTime at={u.created_at} />
    {/snippet}
    {#snippet bulk()}
      <Button variant="outline" size="sm" disabled={busy} onclick={enable}>Enable</Button>
      <Button variant="ghost" size="sm" class="hover:bg-destructive/10 hover:text-destructive" disabled={busy} onclick={() => (disableOpen = true)}>Disable</Button>
    {/snippet}
    {#snippet empty()}
      {#if prefix}
        <CompactEmpty icon={SearchXIcon} text="No one's name starts with “{prefix}”." />
      {:else}
        <CompactEmpty icon={UsersIcon} text="Only you so far. Add an account for each person who publishes or downloads private files.">
          <Button variant="outline" onclick={startCreate}><PlusIcon />New account</Button>
        </CompactEmpty>
      {/if}
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
