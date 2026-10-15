<!--
  One person (administrators). The identity card shows the avatar, the role, how the person signs
  in and a disabled account, with the actions that change the account. The Repositories card
  lists every repository the person can use, with the level of each. Give access opens a short
  form that gives the person access to many repositories at once. Pick rows to change the level
  or to remove the access (after a confirm); each change applies at once. A public repository
  shows because anyone can download from it; its access cannot be removed, only raised to
  publish.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { toast } from "svelte-sonner";
  import { api, ApiError, errorMessage, type Repo, type User, type UserRepoAccess } from "$lib/api";
  import { Keyset, Resource, type TableSource } from "$lib/load.svelte";
  import { href, repoSettingsPath } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { count, someNames } from "$lib/words";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import DataTable from "$lib/components/DataTable.svelte";
  import Cell from "$lib/components/Cell.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import FormatIcon from "$lib/components/FormatIcon.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import UserAvatar from "$lib/components/UserAvatar.svelte";
  import FormField from "$lib/components/FormField.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import ShortFormDialog from "$lib/components/ShortFormDialog.svelte";
  import GrantDialog from "../../settings/repos/GrantDialog.svelte";
  import { levelLabel, type Level } from "../../settings/repos/access";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import LockIcon from "@lucide/svelte/icons/lock";
  import DatabaseIcon from "@lucide/svelte/icons/database";
  import ShieldCheckIcon from "@lucide/svelte/icons/shield-check";
  import UserXIcon from "@lucide/svelte/icons/user-x";
  import SearchXIcon from "@lucide/svelte/icons/search-x";

  let { name }: { name: string } = $props();

  const id = $props.id();
  const person = untrack(() => name);

  // null: no account of that name.
  const user = new Resource<User | null>(async () => {
    try {
      return await api.users.get(person);
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) return null;
      throw e;
    }
  });
  const u = $derived(user.data);
  const self = $derived(person === auth.user?.username);
  const sso = $derived(u?.kind === "oidc");

  // --- repositories ---
  const access = new Keyset<UserRepoAccess>((p) => api.users.access(person, p));
  // Every repository, for the Give access dialog.
  const allRepos = new Resource<Repo[]>(() => api.repos.list());

  // level is null for a public repository without a level given to the person.
  type Row = { repo: string; format: string; public: boolean; level: Level | null };
  const rows = $derived(
    access.items
      .filter((r) => r.via === "permission" || r.public)
      .map((r): Row => ({ repo: r.repo, format: r.format, public: r.public, level: r.via === "permission" ? r.level : null })),
  );
  const source: TableSource<Row> = {
    get items() {
      return rows;
    },
    get loading() {
      return access.loading;
    },
    get error() {
      return access.error;
    },
    reload: () => access.reload(),
    get hasMore() {
      return access.hasMore;
    },
    get loadingMore() {
      return access.loadingMore;
    },
    more: () => access.more(),
  };

  const repoCount = (n: number) => count(n, "repository", "repositories");
  const verbOf = (level: Level) => (level === "write" ? "publish to" : "download from");

  let picked = $state(new Set<string>());
  const pickedNames = $derived([...picked].sort((a, b) => a.localeCompare(b)));
  let busy = $state(false);

  async function afterChange() {
    picked = new Set();
    await access.reload();
  }

  async function changeTo(level: Level) {
    const names = pickedNames;
    busy = true;
    try {
      await api.permissions.set([person], names, level);
      toast.success(`${person} can now ${verbOf(level)} ${repoCount(names.length)}.`);
      await afterChange();
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      busy = false;
    }
  }

  let removeOpen = $state(false);
  async function removeAccess() {
    const names = pickedNames;
    await api.permissions.delete([person], names);
    toast.success(`Removed the access of ${person} to ${repoCount(names.length)}.`);
    await afterChange();
  }

  // --- give access ---
  let giveOpen = $state(false);
  const granted = $derived(new Set(rows.filter((r) => r.level).map((r) => r.repo)));
  const choices = $derived(
    (allRepos.data ?? [])
      .filter((r) => !granted.has(r.name))
      .sort((a, b) => a.name.localeCompare(b.name))
      .map((r) => ({ id: r.name, label: r.name, detail: r.anonymous_read ? "Public: anyone can download" : undefined })),
  );
  async function give(repos: string[], level: Level) {
    await api.permissions.set([person], repos, level);
    toast.success(`${person} can now ${verbOf(level)} ${repoCount(repos.length)}.`);
    await afterChange();
  }

  // --- the account ---
  type Kind = "make-admin" | "remove-admin" | "disable" | "enable";
  let action = $state<Kind | null>(null);
  let confirmAccount = $state(false);
  const ask = (k: Kind) => ((action = k), (confirmAccount = true));
  const accountText = $derived.by(() => {
    const n = person;
    switch (action) {
      case "make-admin":
        return {
          title: `Make ${n} an administrator?`,
          label: "Make administrator",
          destructive: true,
          changes: [`${n} can manage people, repositories and server settings`, `${n} can download from and publish to every repository`],
          run: () => api.users.update(n, { is_admin: true }),
          done: `Made ${n} an administrator`,
        };
      case "remove-admin":
        return {
          title: `Remove the administrator rights of ${n}?`,
          label: "Remove administrator rights",
          destructive: true,
          changes: [`${n} can no longer manage people, repositories or settings`, `${n} keeps only the access given on each repository`],
          run: () => api.users.update(n, { is_admin: false }),
          done: `Removed the administrator rights of ${n}`,
        };
      case "disable":
        return {
          title: `Disable ${n}?`,
          label: "Disable account",
          destructive: true,
          changes: [`${n} is signed out and cannot sign in`, `The read tokens, devices and publish tokens of ${n} stop working`],
          run: () => api.users.setDisabled([n], true),
          done: `${n} is now disabled.`,
        };
      case "enable":
        return {
          title: `Enable ${n}?`,
          label: "Enable account",
          destructive: false,
          changes: [`${n} can sign in again`],
          run: () => api.users.setDisabled([n], false),
          done: `${n} can sign in again.`,
        };
      default:
        return { title: "", label: "", destructive: false, changes: [] as string[], run: async () => {}, done: "" };
    }
  });

  async function applyAccount() {
    const t = accountText;
    await t.run();
    toast.success(t.done);
    // The role and the state decide which repositories the person can use.
    await Promise.all([user.reload(), afterChange()]);
  }

  let pwOpen = $state(false);
  let password = $state("");
  async function resetPassword() {
    user.data = await api.users.update(person, { password });
    toast.success(`Reset the password of ${person}`);
  }

  const notYou = "Another administrator can do this.";
</script>

{#if u === undefined && user.error}
  <PageHeader title={person} />
  <ErrorState error={user.error} title="The account of {person} did not load" onRetry={user.reload} />
{:else if u === undefined}
  <PageHeader title={person} />
  <Loading rows={6} />
{:else if u === null}
  <PageHeader title={person} />
  <EmptyState icon={SearchXIcon} title="Person not found" description="No account named {person} exists. It may have been renamed or never created.">
    <Button variant="outline" href={href("/settings/people")}>Show all people</Button>
  </EmptyState>
{:else}
  <PageHeader title={u.username}>
    {#snippet meta()}{#if self}<span class="text-muted-foreground">you</span>{/if}{/snippet}
  </PageHeader>

  <Section>
    <div class="flex flex-wrap items-center gap-x-6 gap-y-4">
      <div class="flex min-w-0 grow basis-64 items-center gap-4">
        <UserAvatar name={u.username} size="lg" />
        <div class="min-w-0">
          <p class="flex flex-wrap items-center gap-2 text-section">
            {u.is_admin ? "Administrator" : "Member"}
            {#if u.disabled}<StatusBadge tone="warning">Disabled</StatusBadge>{/if}
          </p>
          <p class="mt-1 truncate text-meta text-muted-foreground" title={u.email || undefined}>
            {sso ? "Signs in with single sign-on" : "Signs in with a password"}{#if u.email} · {u.email}{/if}
          </p>
        </div>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        {#if !sso}
          {#if u.is_admin}
            <Button variant="outline" size="sm" disabled={self} title={self ? notYou : undefined} onclick={() => ask("remove-admin")}>Remove administrator rights</Button>
          {:else}
            <Button variant="outline" size="sm" onclick={() => ask("make-admin")}>Make administrator</Button>
          {/if}
          <Button variant="outline" size="sm" onclick={() => ((password = ""), (pwOpen = true))}>Reset password</Button>
        {/if}
        {#if u.disabled}
          <Button variant="outline" size="sm" onclick={() => ask("enable")}>Enable account</Button>
        {:else}
          <Button
            variant="ghost"
            size="sm"
            class="hover:bg-destructive/10 hover:text-destructive"
            disabled={self}
            title={self ? notYou : undefined}
            onclick={() => ask("disable")}>Disable account</Button
          >
        {/if}
      </div>
    </div>
    {#if sso}<p class="mt-4 text-meta text-muted-foreground">Every single sign-on account is an administrator. The sign-in options decide who can sign in.</p>{/if}
  </Section>

  <Section title="Repositories" description={sso && !u.is_admin && !u.disabled ? "Single sign-on accounts can download but cannot publish." : undefined}>
    {#snippet actions()}
      {#if !u.is_admin && !u.disabled}<Button variant="outline" size="sm" onclick={() => (giveOpen = true)}><PlusIcon />Give access</Button>{/if}
    {/snippet}
    {#if u.is_admin}
      <CompactEmpty icon={ShieldCheckIcon} text="Administrators can download from and publish to every repository." />
    {:else if u.disabled}
      <CompactEmpty icon={UserXIcon} text="A disabled account cannot use any repository. Enable the account to restore its access." />
    {:else}
      <DataTable
        {source}
        caption="Repositories {person} can use"
        key={(r) => r.repo}
        columns={[{ label: "Repository", grow: true }, { label: "Access", class: "w-40" }]}
        bind:picked
        pickLabel={(r) => r.repo}
      >
        {#snippet row(r)}
          <Cell grow strong class="max-sm:min-w-20">
            <span class="flex min-w-0 items-center gap-2">
              {#if r.format}<FormatIcon format={r.format} class="shrink-0 text-muted-foreground" />{/if}
              <a class="truncate underline-offset-4 hover:underline" href={href(repoSettingsPath(r.repo, "access"))}>{r.repo}</a>
              {#if !r.public}<LockIcon class="size-3.5 shrink-0 text-muted-foreground" aria-label="Private" />{/if}
              {#if sso && r.level === "write"}<StatusBadge tone="warning" title="Single sign-on accounts hold no publish tokens">Cannot publish: single sign-on</StatusBadge>{/if}
            </span>
          </Cell>
          <Cell class="w-40" text={levelLabel[r.level ?? "read"]} />
        {/snippet}
        {#snippet bulk()}
          <Button variant="outline" size="sm" disabled={busy} onclick={() => changeTo("read")}>Change to Download</Button>
          <Button variant="outline" size="sm" disabled={busy || sso} title={sso ? "Single sign-on accounts cannot publish." : undefined} onclick={() => changeTo("write")}>Change to Publish</Button>
          <Button variant="ghost" size="sm" class="hover:bg-destructive/10 hover:text-destructive" disabled={busy} onclick={() => (removeOpen = true)}>Remove access</Button>
        {/snippet}
        {#snippet empty()}
          <CompactEmpty icon={DatabaseIcon} text="No repository is public, and {person} has no access yet. Give {person} access to a repository." />
        {/snippet}
      </DataTable>
    {/if}
  </Section>
{/if}

<ConfirmDialog
  bind:open={confirmAccount}
  title={accountText.title}
  confirmLabel={accountText.label}
  destructive={accountText.destructive}
  changes={accountText.changes}
  onConfirm={applyAccount}
/>

<ConfirmDialog
  bind:open={removeOpen}
  title="Remove the access of {person} to {repoCount(pickedNames.length)}?"
  description="{someNames(pickedNames)}."
  confirmLabel="Remove access to {repoCount(pickedNames.length)}"
  destructive
  changes={[
    `${person} can no longer download from or publish to ${pickedNames.length === 1 ? "this repository" : "these repositories"}, except for downloads from a public one`,
    `The read tokens of ${person} lose this access too`,
  ]}
  onConfirm={removeAccess}
/>

<GrantDialog
  bind:open={giveOpen}
  title="Give {person} access"
  noun={["repository", "repositories"]}
  items={choices}
  loading={allRepos.loading && !allRepos.data}
  error={allRepos.error ? `The repositories did not load: ${allRepos.error}` : ""}
  none="{person} has access to every repository already."
  canPublish={!sso}
  publishNote="Single sign-on accounts cannot publish."
  submitLabel={(n) => (n ? `Give access to ${repoCount(n)}` : "Give access")}
  onGive={give}
/>

<ShortFormDialog
  bind:open={pwOpen}
  title="Reset the password of {person}"
  description="The old password stops working at once."
  submitLabel="Reset password"
  canSubmit={!!password}
  onSubmit={resetPassword}
>
  <FormField label="New password" for="{id}-newpw">
    <Input id="{id}-newpw" type="password" autocomplete="new-password" bind:value={password} />
  </FormField>
</ShortFormDialog>
