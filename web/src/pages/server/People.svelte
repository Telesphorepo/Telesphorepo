<!--
  People (People permission): every account and its role (Administrator, the phrase of its server
  permissions, or nothing). A row opens the person's page (/settings/people/:name), where the
  account, its server permissions and its permissions on each repository change. New account opens
  a short form; after Create, the new person's page opens to set the permissions. Single
  sign-on accounts appear after their first sign-in. Pick rows (not your own) to disable people
  (after a confirm) or enable them at once.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import type { PageProps } from "$lib/router.svelte";
  import { href, navigate } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { serverPhrase } from "$lib/permissions";
  import { api, errorMessage, type User } from "$lib/api";
  import { Keyset } from "$lib/load.svelte";
  import { count, someNames } from "$lib/words";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import DataTable from "$lib/components/DataTable.svelte";
  import Cell from "$lib/components/Cell.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import UserAvatar from "$lib/components/UserAvatar.svelte";
  import FormField from "$lib/components/FormField.svelte";
  import ShortFormDialog from "$lib/components/ShortFormDialog.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import UsersIcon from "@lucide/svelte/icons/users-round";

  let {}: PageProps = $props();

  const id = $props.id();
  const users = new Keyset((p) => api.users.list(p));
  const personPath = (name: string) => `/settings/people/${encodeURIComponent(name)}`;
  const rowHref = (u: User) => href(personPath(u.username));
  // How people sign in shows only when it differs between them.
  const mixedSignIn = $derived(users.items.some((u) => u.kind === "oidc"));

  let createOpen = $state(false);
  let newName = $state("");
  let newPassword = $state("");

  function startCreate() {
    newName = newPassword = "";
    createOpen = true;
  }

  async function create() {
    const u = await api.users.create({ username: newName.trim(), password: newPassword, permissions: [] });
    toast.success(`Created the account ${u.username}`);
    navigate(personPath(u.username));
  }

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
      await users.reload();
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

<PageHeader title="People" description="Everyone who can sign in, and the repositories each person can use.">
  {#snippet actions()}<Button onclick={startCreate}><PlusIcon />New account</Button>{/snippet}
</PageHeader>

<Section>
  <DataTable
    source={users}
    caption="People"
    key={(u) => u.id}
    href={rowHref}
    bind:picked
    pickable={(u) => u.username !== auth.user?.username}
    pickLabel={(u) => u.username}
    columns={[
      { label: "Name", grow: true },
      { label: "Role" },
      ...(mixedSignIn ? [{ label: "Signs in with", hideOnPhone: true }] : []),
      { label: "Added", align: "right", hideOnPhone: true },
    ]}
  >
    {#snippet row(u)}
      <Cell grow>
        <span class="flex min-w-0 items-center gap-2">
          <UserAvatar name={u.username} />
          <a href={rowHref(u)} class="shrink-0 rounded-sm font-medium outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50">{u.username}</a>
          {#if u.username === auth.user?.username}<span class="shrink-0 text-muted-foreground">you</span>{/if}
          {#if u.email}<span class="min-w-0 truncate text-muted-foreground">{u.email}</span>{/if}
        </span>
      </Cell>
      {#if u.disabled}
        <Cell><StatusBadge tone="warning">Disabled</StatusBadge></Cell>
      {:else}
        <Cell class="max-w-64" text={serverPhrase(u.permissions)} />
      {/if}
      {#if mixedSignIn}<Cell hideOnPhone muted>{u.kind === "oidc" ? "Single sign-on" : "Password"}</Cell>{/if}
      <Cell num muted hideOnPhone><RelativeTime at={u.created_at} /></Cell>
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
  </DataTable>
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

<ShortFormDialog
  bind:open={createOpen}
  title="New account"
  description="The person signs in with this name and password. Set the permissions on the next page."
  submitLabel="Create account"
  canSubmit={!!newName.trim() && !!newPassword}
  onSubmit={create}
>
  <FormField label="Username" for="{id}-name">
    <Input id="{id}-name" autocomplete="off" spellcheck={false} bind:value={newName} />
  </FormField>
  <FormField label="Password" for="{id}-pw">
    <Input id="{id}-pw" type="password" autocomplete="new-password" bind:value={newPassword} />
  </FormField>
</ShortFormDialog>
