<!--
  One person (People). The identity card shows the avatar, the role (Administrator, the phrase of
  the server permissions, or Member), how the person signs in and a disabled account, with the
  account actions. Server permissions holds the picker of the server permissions; the save bar
  saves it after a confirm. The Repositories card lists every repository the person can use, with
  the phrase of their permissions there. Give access opens a short form that gives the person
  permissions on many repositories at once; Change edits one repository; picked rows get one set
  of permissions, or lose their access after a confirm. A public repository shows because anyone
  can download from it. Each change of repository permissions applies at once.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { toast } from "svelte-sonner";
  import { api, ApiError, type Repo, type PermId, type User, type UserRepoAccess } from "$lib/api";
  import { Keyset, Resource, type TableSource } from "$lib/load.svelte";
  import { href, repoPath, repoSettingsPath } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { allReposPermissions, labelOf, pickerGroups, repoPhrase, serverPhrase, sortServerPermissions } from "$lib/permissions";
  import { count, someNames } from "$lib/words";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import FormSection from "$lib/components/FormSection.svelte";
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
  import SaveBar from "$lib/components/SaveBar.svelte";
  import PermissionPicker from "$lib/components/PermissionPicker.svelte";
  import GrantDialog from "../../settings/repos/GrantDialog.svelte";
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
  const allRepos = $derived(allReposPermissions().some((p) => !!u?.permissions?.includes(p)));
  // The server permissions of the caller: they give and remove only these.
  const mine = $derived((auth.user?.permissions ?? []) as PermId[]);
  // People changes the password of a person only when it holds every permission of that person.
  const covers = $derived(!!u && (u.permissions ?? []).every((p) => mine.includes(p)));

  // --- server permissions ---
  let serverSaved = $state<PermId[]>([]);
  let serverValue = $state<PermId[]>([]);
  $effect(() => {
    const p = u?.permissions;
    if (p) untrack(() => ((serverSaved = sortServerPermissions(p)), (serverValue = sortServerPermissions(p))));
  });
  const added = $derived(serverValue.filter((p) => !serverSaved.includes(p)));
  const removed = $derived(serverSaved.filter((p) => !serverValue.includes(p)));
  let serverConfirm = $state(false);
  async function saveServer() {
    const next = await api.users.setPermissions(person, serverValue);
    user.data = next;
    toast.success(`Saved the server permissions of ${person}`);
    if (self) await auth.load();
    await access.reload();
  }

  // --- repositories ---
  const access = new Keyset<UserRepoAccess>((p) => api.users.access(person, p));
  // The repositories the caller can give access to, for the Give access dialog.
  const repos = new Resource<Repo[]>(() => api.repos.list());

  // A public repository without a grant shows with no permissions of its own.
  type Row = { repo: string; format: string; public: boolean; perms: PermId[]; granted: boolean };
  const rows = $derived(
    access.items
      .filter((r) => r.via === "permission" || r.public)
      .map((r): Row => ({ repo: r.repo, format: r.format, public: r.public, perms: r.perms ?? [], granted: r.via === "permission" })),
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
  const held = (names: string[]) => auth.heldOnAll(names);
  // The repository settings when the caller can open them, else the repository's files.
  const repoHref = (repo: string) => href(auth.managesRepo(repo) ? repoSettingsPath(repo, "access") : repoPath(repo));

  let picked = $state(new Set<string>());
  const pickedNames = $derived([...picked].sort((a, b) => a.localeCompare(b)));

  async function afterChange() {
    picked = new Set();
    if (self) await auth.load();
    await access.reload();
  }

  let removeOpen = $state(false);
  async function removeAccess() {
    const names = pickedNames;
    await api.grants.set([person], names, []);
    toast.success(`Removed the access of ${person} to ${repoCount(names.length)}.`);
    await afterChange();
  }

  // --- give access and change permissions ---
  let giveOpen = $state(false);
  const granted = $derived(new Set(rows.filter((r) => r.granted).map((r) => r.repo)));
  const choices = $derived(
    (repos.data ?? [])
      .filter((r) => !granted.has(r.name) && auth.canRepo(r.name, "repo.access"))
      .sort((a, b) => a.name.localeCompare(b.name))
      .map((r) => ({ id: r.name, label: r.name, detail: r.anonymous_read ? "Public: anyone can download" : undefined })),
  );
  async function give(names: string[], perms: PermId[]) {
    await api.grants.set([person], names, perms);
    toast.success(`Gave ${person} access to ${repoCount(names.length)}: ${repoPhrase(perms)}.`);
    await afterChange();
  }

  let changeTargets = $state<string[]>([]);
  let changeStart = $state<PermId[]>([]);
  let changeOpen = $state(false);
  function openChange(names: string[], start: PermId[]) {
    changeTargets = names;
    changeStart = start;
    changeOpen = true;
  }
  async function change(names: string[], perms: PermId[]) {
    await api.grants.set([person], names, perms);
    toast.success(
      perms.length
        ? `Changed the permissions of ${person} on ${repoCount(names.length)}: ${repoPhrase(perms)}.`
        : `Removed the access of ${person} to ${repoCount(names.length)}.`,
    );
    await afterChange();
  }

  // --- the account ---
  type Kind = "disable" | "enable";
  let action = $state<Kind | null>(null);
  let confirmAccount = $state(false);
  const ask = (k: Kind) => ((action = k), (confirmAccount = true));
  const accountText = $derived.by(() => {
    const n = person;
    switch (action) {
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
    // The state decides which repositories the person can use.
    await Promise.all([user.reload(), afterChange()]);
  }

  let pwOpen = $state(false);
  let password = $state("");
  async function resetPassword() {
    user.data = await api.users.update(person, { password });
    toast.success(`Reset the password of ${person}`);
  }

  const notYou = "Another person with People can do this.";
  const notCovered = "You can do this only for a person whose permissions you all hold.";
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
            {serverPhrase(u.permissions) || "Member"}
            {#if u.disabled}<StatusBadge tone="warning">Disabled</StatusBadge>{/if}
          </p>
          <p class="mt-1 truncate text-meta text-muted-foreground" title={u.email || undefined}>
            {sso ? "Signs in with single sign-on" : "Signs in with a password"}{#if u.email} · {u.email}{/if}
          </p>
        </div>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        {#if !sso}
          <Button
            variant="outline"
            size="sm"
            disabled={!covers}
            title={covers ? undefined : notCovered}
            onclick={() => ((password = ""), (pwOpen = true))}>Reset password</Button
          >
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
  </Section>

  <FormSection
    title="Server permissions"
    description={u.permissions_managed
      ? "Single sign-on manages these permissions."
      : "Check the parts of the server that this person manages. Every one together makes an administrator."}
  >
    <PermissionPicker id="{id}-server" groups={pickerGroups("server")} held={u.permissions_managed ? [] : mine} columns={2} bind:value={serverValue} />
  </FormSection>

  <Section title="Repositories">
    {#snippet actions()}
      {#if !allRepos && !u.disabled}<Button variant="outline" size="sm" onclick={() => (giveOpen = true)}><PlusIcon />Give access</Button>{/if}
    {/snippet}
    {#if allRepos}
      <CompactEmpty icon={ShieldCheckIcon} text="With All repositories, {person} holds every permission on every repository." />
    {:else if u.disabled}
      <CompactEmpty icon={UserXIcon} text="A disabled account cannot use any repository. Enable the account to restore its access." />
    {:else}
      <DataTable
        {source}
        caption="Repositories {person} can use"
        key={(r) => r.repo}
        columns={[{ label: "Repository", class: "w-64 min-w-64 max-sm:min-w-0" }, { label: "Permissions", grow: true }, { label: "", srLabel: "Actions" }]}
        bind:picked
        pickable={(r) => auth.canRepo(r.repo, "repo.access")}
        pickLabel={(r) => r.repo}
      >
        {#snippet row(r)}
          <Cell strong class="w-64 max-w-64 min-w-64 max-sm:min-w-20">
            <span class="flex min-w-0 items-center gap-2">
              {#if r.format}<FormatIcon format={r.format} class="shrink-0 text-muted-foreground" />{/if}
              <a class="truncate underline-offset-4 hover:underline" href={repoHref(r.repo)}>{r.repo}</a>
              {#if !r.public}<LockIcon class="size-3.5 shrink-0 text-muted-foreground" aria-label="Private" />{/if}
            </span>
          </Cell>
          <Cell grow muted text={r.granted ? repoPhrase(r.perms) : "Download, because it is public"} />
          <Cell actions>
            {#if auth.canRepo(r.repo, "repo.access")}
              <Button variant="ghost" size="sm" aria-label="Change the permissions of {person} on {r.repo}" onclick={() => openChange([r.repo], r.granted ? r.perms : ["repo.download"])}
                >Change</Button
              >
            {/if}
          </Cell>
        {/snippet}
        {#snippet bulk()}
          <Button variant="outline" size="sm" onclick={() => openChange(pickedNames, ["repo.download"])}>Change permissions</Button>
          <Button variant="ghost" size="sm" class="hover:bg-destructive/10 hover:text-destructive" onclick={() => (removeOpen = true)}>Remove access</Button>
        {/snippet}
        {#snippet empty()}
          <CompactEmpty icon={DatabaseIcon} text="No repository is public, and {person} has no access yet. Give {person} access to a repository." />
        {/snippet}
      </DataTable>
    {/if}
  </Section>

  <SaveBar count={added.length + removed.length} onDiscard={() => (serverValue = [...serverSaved])} onSave={() => (serverConfirm = true)} />
{/if}

<ConfirmDialog
  bind:open={serverConfirm}
  title="Save the server permissions of {person}?"
  confirmLabel="Save permissions"
  destructive={removed.length > 0}
  changes={[
    ...added.map((p) => `${person} gets ${labelOf(p)}`),
    ...removed.map((p) => `${person} loses ${labelOf(p)}`),
  ]}
  onConfirm={saveServer}
/>

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
    `${person} loses every permission on ${pickedNames.length === 1 ? "this repository" : "these repositories"}, but can still download from a public one`,
    `The read tokens of ${person} lose this access too`,
  ]}
  onConfirm={removeAccess}
/>

<GrantDialog
  bind:open={giveOpen}
  title="Give {person} access"
  noun={["repository", "repositories"]}
  items={choices}
  loading={repos.loading && !repos.data}
  error={repos.error ? `The repositories did not load: ${repos.error}` : ""}
  none="{person} has access to every repository that you manage already."
  {held}
  submitLabel={(n) => (n ? `Give access to ${repoCount(n)}` : "Give access")}
  onGive={give}
/>

<GrantDialog
  bind:open={changeOpen}
  title={changeTargets.length === 1 ? `Permissions of ${person} on ${changeTargets[0]}` : `Permissions of ${person} on ${repoCount(changeTargets.length)}`}
  description={changeTargets.length === 1 ? "Clear every permission to remove the access." : "Each repository gets this set. Clear every permission to remove the access."}
  noun={["repository", "repositories"]}
  targets={changeTargets}
  start={changeStart}
  {held}
  submitLabel={(_, perms) => (perms.length ? "Save permissions" : "Remove access")}
  onGive={change}
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
