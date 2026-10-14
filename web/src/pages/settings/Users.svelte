<!-- Admin: users. Create a local user, make or remove an admin, disable or enable, reset a password. -->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import { toast } from "svelte-sonner";
  import { auth } from "$lib/auth.svelte";
  import { api, errorMessage, type User, type UserPatch } from "$lib/api";
  import { Keyset } from "$lib/load.svelte";
  import * as Table from "$lib/components/ui/table";
  import * as Dialog from "$lib/components/ui/dialog";
  import * as DropdownMenu from "$lib/components/ui/dropdown-menu";
  import { Badge } from "$lib/components/ui/badge";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import { Switch } from "$lib/components/ui/switch";
  import { Spinner } from "$lib/components/ui/spinner";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import KeysetTable from "$lib/components/KeysetTable.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import UsersIcon from "@lucide/svelte/icons/users";
  import EllipsisIcon from "@lucide/svelte/icons/ellipsis";

  let { params, query }: PageProps = $props();

  const id = $props.id();
  const users = new Keyset((p) => api.users.list(p));

  // --- create ---
  let creating = $state(false);
  let newName = $state("");
  let newPassword = $state("");
  let newAdmin = $state(false);
  let createBusy = $state(false);
  let createError = $state("");

  async function create(e: SubmitEvent) {
    e.preventDefault();
    createBusy = true;
    createError = "";
    try {
      const u = await api.users.create({ username: newName.trim(), password: newPassword, is_admin: newAdmin });
      toast.success(`Created user ${u.username}`);
      creating = false;
      newName = newPassword = "";
      newAdmin = false;
      await users.reload();
    } catch (e) {
      createError = errorMessage(e);
    } finally {
      createBusy = false;
    }
  }

  // --- change a user ---
  type Action = { user: User; kind: "make-admin" | "remove-admin" | "disable" | "enable" | "password" };
  let action = $state<Action | null>(null);
  let confirmOpen = $state(false);
  let password = $state("");

  const ask = (user: User, kind: Action["kind"]) => {
    action = { user, kind };
    password = "";
    confirmOpen = true;
  };

  const text = $derived.by(() => {
    if (!action) return { title: "", label: "", destructive: false, changes: [] as string[] };
    const n = action.user.username;
    switch (action.kind) {
      case "make-admin":
        return {
          title: `Make ${n} an admin`,
          label: `Make ${n} an admin`,
          destructive: true,
          changes: [`${n} can manage users, repositories, settings and storage`, `${n} can read and write every repository`],
        };
      case "remove-admin":
        return {
          title: `Remove admin rights of ${n}`,
          label: `Remove admin rights`,
          destructive: true,
          changes: [`${n} can no longer manage users, repositories or settings`, `${n} keeps only the access that repository permissions give`],
        };
      case "disable":
        return {
          title: `Disable ${n}`,
          label: `Disable ${n}`,
          destructive: true,
          changes: [`${n} can no longer sign in`, `Open sessions and read tokens of ${n} stop working`],
        };
      case "enable":
        return { title: `Enable ${n}`, label: `Enable ${n}`, destructive: false, changes: [`${n} can sign in again`] };
      case "password":
        return {
          title: `Reset the password of ${n}`,
          label: "Reset password",
          destructive: false,
          changes: [`Replaces the password of ${n}`, "The old password stops working"],
        };
    }
  });

  async function apply() {
    if (!action) return;
    const { user, kind } = action;
    const patch: UserPatch =
      kind === "make-admin"
        ? { is_admin: true }
        : kind === "remove-admin"
          ? { is_admin: false }
          : kind === "disable"
            ? { disabled: true }
            : kind === "enable"
              ? { disabled: false }
              : { password };
    if (kind === "password" && !password) throw new Error("Enter the new password.");
    const updated = await api.users.update(user.username, patch);
    users.items = users.items.map((u) => (u.id === updated.id ? updated : u));
    toast.success(
      kind === "password"
        ? `Reset the password of ${user.username}`
        : kind === "disable"
          ? `Disabled ${user.username}`
          : kind === "enable"
            ? `Enabled ${user.username}`
            : kind === "make-admin"
              ? `Made ${user.username} an admin`
              : `Removed admin rights of ${user.username}`,
    );
  }
</script>

<PageHeader title="Users" description="Local accounts and the SSO accounts that signed in.">
  {#snippet actions()}
    <Button onclick={() => (creating = true)}>
      <PlusIcon />
      New user
    </Button>
  {/snippet}
</PageHeader>

<KeysetTable list={users} columns={5} caption="Users">
  {#snippet head()}
    <Table.Head>User</Table.Head>
    <Table.Head>Role</Table.Head>
    <Table.Head>Status</Table.Head>
    <Table.Head>Created</Table.Head>
    <Table.Head class="w-px"><span class="sr-only">Actions</span></Table.Head>
  {/snippet}
  {#snippet row(u)}
    {@const self = u.username === auth.user?.username}
    <Table.Row>
      <Table.Cell>
        <div class="flex flex-col">
          <span class="font-medium">
            {u.username}
            {#if self}<span class="font-normal text-muted-foreground">(you)</span>{/if}
          </span>
          {#if u.email}<span class="text-xs text-muted-foreground">{u.email}</span>{/if}
        </div>
      </Table.Cell>
      <Table.Cell>
        <div class="flex flex-wrap gap-1.5">
          {#if u.is_admin}<Badge>Admin</Badge>{:else}<Badge variant="outline">User</Badge>{/if}
          <Badge variant="secondary">{u.kind === "oidc" ? "SSO" : "Local"}</Badge>
        </div>
      </Table.Cell>
      <Table.Cell>
        {#if u.disabled}<Badge variant="destructive">Disabled</Badge>{:else}<span class="text-muted-foreground">Active</span>{/if}
      </Table.Cell>
      <Table.Cell><RelativeTime at={u.created_at} /></Table.Cell>
      <Table.Cell>
        <DropdownMenu.Root>
          <DropdownMenu.Trigger>
            {#snippet child({ props })}
              <Button {...props} variant="ghost" size="icon-sm" aria-label="Actions for {u.username}">
                <EllipsisIcon />
              </Button>
            {/snippet}
          </DropdownMenu.Trigger>
          <DropdownMenu.Content align="end">
            {#if u.kind === "local"}
              {#if u.is_admin}
                <DropdownMenu.Item disabled={self} onSelect={() => ask(u, "remove-admin")}>Remove admin rights</DropdownMenu.Item>
              {:else}
                <DropdownMenu.Item onSelect={() => ask(u, "make-admin")}>Make admin</DropdownMenu.Item>
              {/if}
              <DropdownMenu.Item onSelect={() => ask(u, "password")}>Reset password</DropdownMenu.Item>
            {/if}
            {#if u.disabled}
              <DropdownMenu.Item onSelect={() => ask(u, "enable")}>Enable</DropdownMenu.Item>
            {:else}
              <DropdownMenu.Item variant="destructive" disabled={self} onSelect={() => ask(u, "disable")}>Disable</DropdownMenu.Item>
            {/if}
          </DropdownMenu.Content>
        </DropdownMenu.Root>
      </Table.Cell>
    </Table.Row>
  {/snippet}
  {#snippet empty()}
    <EmptyState title="No users yet" description="Create a user to give a person or a build access." icon={UsersIcon}>
      <Button onclick={() => (creating = true)}>
        <PlusIcon />
        Create a user
      </Button>
    </EmptyState>
  {/snippet}
</KeysetTable>

<Dialog.Root bind:open={creating}>
  <Dialog.Content class="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-lg">
    <Dialog.Header>
      <Dialog.Title>New user</Dialog.Title>
      <Dialog.Description>The user signs in with this username and password.</Dialog.Description>
    </Dialog.Header>
    <form class="flex flex-col gap-4" onsubmit={create}>
      <div class="flex flex-col gap-2">
        <Label for="{id}-name">Username</Label>
        <Input id="{id}-name" class="max-w-sm" bind:value={newName} autocomplete="off" spellcheck={false} required />
      </div>
      <div class="flex flex-col gap-2">
        <Label for="{id}-pw">Password</Label>
        <Input id="{id}-pw" class="max-w-sm" type="password" bind:value={newPassword} autocomplete="new-password" required />
      </div>
      <div class="flex items-center gap-3">
        <Switch id="{id}-admin" bind:checked={newAdmin} />
        <Label for="{id}-admin">Admin</Label>
      </div>
      {#if createError}<ErrorState inline error={createError} />{/if}
      <Dialog.Footer>
        <Button type="button" variant="outline" disabled={createBusy} onclick={() => (creating = false)}>Cancel</Button>
        <Button type="submit" disabled={createBusy || !newName.trim() || !newPassword}>
          {#if createBusy}<Spinner />{/if}
          Create user
        </Button>
      </Dialog.Footer>
    </form>
  </Dialog.Content>
</Dialog.Root>

<ConfirmDialog
  bind:open={confirmOpen}
  title={text.title}
  confirmLabel={text.label}
  destructive={text.destructive}
  changes={text.changes}
  onConfirm={apply}
>
  {#if action?.kind === "password"}
    <div class="flex flex-col gap-2">
      <Label for="{id}-newpw">New password</Label>
      <Input id="{id}-newpw" class="max-w-sm" type="password" bind:value={password} autocomplete="new-password" />
    </div>
  {/if}
</ConfirmDialog>
