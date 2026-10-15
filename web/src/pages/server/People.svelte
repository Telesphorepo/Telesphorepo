<!--
  People (administrators): every account and its role. A row opens the person's page
  (/settings/people/:name), where an administrator changes the account and its access to each
  repository. New account opens a short form; after Create, the new person's page opens. Single
  sign-on accounts appear after their first sign-in.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import type { PageProps } from "$lib/router.svelte";
  import { href, navigate } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { api, type User } from "$lib/api";
  import { Keyset } from "$lib/load.svelte";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Switch } from "$lib/components/ui/switch";
  import { Label } from "$lib/components/ui/label";
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
  let newAdmin = $state(false);

  function startCreate() {
    newName = newPassword = "";
    newAdmin = false;
    createOpen = true;
  }

  async function create() {
    const u = await api.users.create({ username: newName.trim(), password: newPassword, is_admin: newAdmin });
    toast.success(`Created the account ${u.username}`);
    navigate(personPath(u.username));
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
      <Cell>
        {#if u.disabled}
          <StatusBadge tone="warning">Turned off</StatusBadge>
        {:else}
          {u.is_admin ? "Administrator" : "Member"}
        {/if}
      </Cell>
      {#if mixedSignIn}<Cell hideOnPhone muted>{u.kind === "oidc" ? "Single sign-on" : "Password"}</Cell>{/if}
      <Cell num muted hideOnPhone><RelativeTime at={u.created_at} /></Cell>
    {/snippet}
    {#snippet empty()}
      <CompactEmpty icon={UsersIcon} text="Only you so far. Add an account for each person who publishes or downloads private files.">
        <Button variant="outline" onclick={startCreate}><PlusIcon />New account</Button>
      </CompactEmpty>
    {/snippet}
  </DataTable>
</Section>

<ShortFormDialog
  bind:open={createOpen}
  title="New account"
  description="The person signs in with this name and password, then sets up two-step verification."
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
  <div class="flex items-center gap-3">
    <Switch id="{id}-admin" bind:checked={newAdmin} />
    <Label for="{id}-admin">Administrator</Label>
  </div>
</ShortFormDialog>
