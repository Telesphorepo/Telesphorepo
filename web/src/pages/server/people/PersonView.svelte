<!--
  One person (administrators). The identity card shows the avatar, the role, how the person signs
  in and a turned-off account, with the actions that change the account. The Repositories
  card lists every repository the person can use, with the level of each. An administrator adds
  the person to a repository, changes the level or removes the access in place, the same way as on
  a repository's Access tab: the edits go into one draft, and the save bar saves them after a
  confirm that lists each change. A public repository shows because anyone can download from it;
  its access cannot be removed, only raised to publish.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { toast } from "svelte-sonner";
  import { api, ApiError, type Repo, type User, type UserPatch, type UserRepoAccess } from "$lib/api";
  import { Keyset, Resource, type TableSource } from "$lib/load.svelte";
  import { Draft } from "$lib/unsaved.svelte";
  import { href, repoSettingsPath } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import * as Select from "$lib/components/ui/select";
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
  import SaveBar from "$lib/components/SaveBar.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import ShortFormDialog from "$lib/components/ShortFormDialog.svelte";
  import { levelLabel, type Level } from "../../settings/repos/access";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import XIcon from "@lucide/svelte/icons/x";
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
  // Every repository, for the add dialog and for the rows that the draft adds.
  const allRepos = new Resource<Repo[]>(() => api.repos.list());
  const repoOf = (r: string) => allRepos.data?.find((x) => x.name === r);

  type Grants = Record<string, Level>;
  const draft = new Draft<{ grants: Grants }>({ grants: {} });
  const d = $derived(draft.value.grants);
  const saved = $derived(draft.saved.grants);
  const grantsOf = (items: UserRepoAccess[]): Grants => Object.fromEntries(items.filter((r) => r.via === "permission").map((r) => [r.repo, r.level]));

  // A page that "Load more" adds brings its grants into the draft, saved and edited alike.
  let seen = new Set<string>();
  $effect(() => {
    const fresh = access.items.filter((r) => !seen.has(r.repo));
    if (!fresh.length) return;
    untrack(() => {
      for (const r of fresh) seen.add(r.repo);
      const g = grantsOf(fresh);
      draft.saved = { grants: { ...draft.saved.grants, ...g } };
      draft.value = { grants: { ...draft.value.grants, ...g } };
    });
  });

  type Row = { repo: string; format: string; public: boolean; level: Level | null };
  const rows = $derived.by(() => {
    const out: Row[] = [];
    const listed = new Set<string>();
    for (const r of access.items) {
      listed.add(r.repo);
      if (d[r.repo] || r.public) out.push({ repo: r.repo, format: r.format, public: r.public, level: d[r.repo] ?? null });
    }
    for (const [repo, level] of Object.entries(d)) {
      if (listed.has(repo)) continue;
      const x = repoOf(repo);
      out.push({ repo, format: x?.format ?? "", public: x?.anonymous_read ?? false, level });
    }
    return out.sort((a, b) => a.repo.localeCompare(b.repo));
  });
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

  const setLevel = (repo: string, level: Level | undefined) => {
    const g = { ...draft.value.grants };
    if (level) g[repo] = level;
    else delete g[repo];
    draft.value = { grants: g };
  };
  function choose(r: Row, level: Level) {
    // A public repository needs no grant to download.
    if (level === "read" && r.public && !saved[r.repo]) setLevel(r.repo, undefined);
    else setLevel(r.repo, level);
  }

  const changed = $derived([...new Set([...Object.keys(saved), ...Object.keys(d)])].filter((r) => saved[r] !== d[r]).sort());
  const isPublic = (repo: string) => rows.find((r) => r.repo === repo)?.public ?? repoOf(repo)?.anonymous_read ?? false;
  const changes = $derived(
    changed.map((repo) => {
      const was = saved[repo];
      const now = d[repo];
      if (!was) {
        if (now === "read") return `${person} can download from ${repo}`;
        return isPublic(repo) ? `${person} can publish to ${repo}` : `${person} can download from and publish to ${repo}`;
      }
      if (!now) {
        if (isPublic(repo)) return was === "write" ? `${person} can no longer publish to ${repo}, and can still download because it is public` : `The access given on ${repo} ends; ${person} can still download because it is public`;
        return `${person} can no longer ${was === "write" ? "download from or publish to" : "download from"} ${repo}; their read tokens lose it too`;
      }
      return now === "write" ? `${person} can publish to ${repo}` : `${person} can no longer publish to ${repo}, and can still download`;
    }),
  );
  const narrows = $derived(changed.some((r) => !d[r] || (saved[r] === "write" && d[r] === "read")));

  async function rebuild(keep?: Grants) {
    await access.reload();
    seen = new Set(access.items.map((r) => r.repo));
    const g = grantsOf(access.items);
    draft.commit({ grants: g });
    if (keep) draft.value = { grants: keep };
  }

  let confirmSave = $state(false);
  async function save() {
    const want = $state.snapshot(draft.value.grants) as Grants;
    const had = $state.snapshot(draft.saved.grants) as Grants;
    let done = false;
    try {
      for (const repo of changed) {
        if (want[repo]) await api.repos.permissions.set(repo, person, want[repo]);
        else if (had[repo]) await api.repos.permissions.delete(repo, person);
      }
      done = true;
    } finally {
      // Whatever was saved is now the saved state; edits that did not save stay unsaved.
      await rebuild(done ? undefined : want);
    }
    toast.success(`Saved the access of ${person}`);
  }

  // --- add to a repository ---
  let addOpen = $state(false);
  let addRepo = $state("");
  let addLevel = $state<Level>("read");
  function openAdd() {
    addRepo = "";
    addLevel = "read";
    addOpen = true;
  }
  const candidates = $derived((allRepos.data ?? []).filter((r) => !d[r.name]).map((r) => r.name).sort());
  const addProblem = $derived.by(() => {
    const r = addRepo.trim();
    if (!r) return "";
    const x = repoOf(r);
    if (!x && allRepos.data) return `No repository named ${r} exists.`;
    if (d[r]) return `${person} has access to ${r} already. Change the level in the list.`;
    if (x?.anonymous_read && addLevel === "read") return `${r} is public, so ${person} can download from it already.`;
    return "";
  });

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
          patch: { is_admin: true } as UserPatch,
          done: `Made ${n} an administrator`,
        };
      case "remove-admin":
        return {
          title: `Remove the administrator rights of ${n}?`,
          label: "Remove administrator rights",
          destructive: true,
          changes: [`${n} can no longer manage people, repositories or settings`, `${n} keeps only the access given on each repository`],
          patch: { is_admin: false },
          done: `Removed the administrator rights of ${n}`,
        };
      case "disable":
        return {
          title: `Turn off ${n}?`,
          label: "Turn off account",
          destructive: true,
          changes: [`${n} can no longer sign in`, `The read tokens, devices and publish tokens of ${n} stop working`],
          patch: { disabled: true },
          done: `Turned off ${n}`,
        };
      case "enable":
        return { title: `Turn on ${n}?`, label: "Turn on account", destructive: false, changes: [`${n} can sign in again`], patch: { disabled: false }, done: `Turned on ${n}` };
      default:
        return { title: "", label: "", destructive: false, changes: [] as string[], patch: {}, done: "" };
    }
  });

  async function applyAccount() {
    const t = accountText;
    user.data = await api.users.update(person, t.patch);
    toast.success(t.done);
    // The role and the state decide which repositories the person can use.
    await rebuild();
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
            {#if u.disabled}<StatusBadge tone="warning">Turned off</StatusBadge>{/if}
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
          <Button variant="outline" size="sm" onclick={() => ask("enable")}>Turn on account</Button>
        {:else}
          <Button
            variant="ghost"
            size="sm"
            class="hover:bg-destructive/10 hover:text-destructive"
            disabled={self}
            title={self ? notYou : undefined}
            onclick={() => ask("disable")}>Turn off account</Button
          >
        {/if}
      </div>
    </div>
    {#if sso}<p class="mt-4 text-meta text-muted-foreground">Every single sign-on account is an administrator. The sign-in options decide who can sign in.</p>{/if}
  </Section>

  <Section title="Repositories" description={sso && !u.is_admin && !u.disabled ? "Single sign-on accounts can download but cannot publish." : undefined}>
    {#snippet actions()}
      {#if !u.is_admin && !u.disabled}<Button variant="outline" size="sm" onclick={openAdd}><PlusIcon />Add to repository</Button>{/if}
    {/snippet}
    {#if u.is_admin}
      <CompactEmpty icon={ShieldCheckIcon} text="Administrators can download from and publish to every repository." />
    {:else if u.disabled}
      <CompactEmpty icon={UserXIcon} text="A turned-off account cannot use any repository. Turn the account on to restore its access." />
    {:else}
      <DataTable
        {source}
        caption="Repositories {person} can use"
        key={(r) => r.repo}
        columns={[{ label: "Repository", grow: true }, { label: "Access", class: "w-40" }, { label: "", srLabel: "Actions" }]}
      >
        {#snippet row(r)}
          <Cell grow strong class="max-sm:min-w-20">
            <span class="flex min-w-0 items-center gap-2">
              {#if saved[r.repo] !== d[r.repo]}<span aria-hidden="true" class="size-1.5 shrink-0 rounded-full bg-warning" title="Not saved yet"></span><span class="sr-only">Not saved:</span>{/if}
              {#if r.format}<FormatIcon format={r.format} class="shrink-0 text-muted-foreground" />{/if}
              <a class="truncate underline-offset-4 hover:underline" href={href(repoSettingsPath(r.repo, "access"))}>{r.repo}</a>
              {#if !r.public}<LockIcon class="size-3.5 shrink-0 text-muted-foreground" aria-label="Private" />{/if}
              {#if sso && r.level === "write"}<StatusBadge tone="warning" title="Single sign-on accounts hold no publish tokens">Cannot publish: single sign-on</StatusBadge>{/if}
            </span>
          </Cell>
          <Cell class="py-0">
            <Select.Root type="single" value={r.level ?? "read"} onValueChange={(v) => choose(r, v as Level)}>
              <Select.Trigger size="sm" class="w-40 max-sm:w-32" aria-label="Access of {person} to {r.repo}">{levelLabel[r.level ?? "read"]}</Select.Trigger>
              <Select.Content>
                <Select.Item value="read" label={levelLabel.read}>{levelLabel.read}</Select.Item>
                <Select.Item value="write" label={levelLabel.write} disabled={sso}>{levelLabel.write}</Select.Item>
              </Select.Content>
            </Select.Root>
          </Cell>
          <Cell actions>
            <Button
              variant="ghost"
              size="icon-sm"
              class="hover:text-destructive"
              disabled={!r.level}
              title={r.level ? "Remove" : "Anyone can download from a public repository."}
              aria-label="Remove the access of {person} to {r.repo}"
              onclick={() => setLevel(r.repo, undefined)}><XIcon /></Button
            >
          </Cell>
        {/snippet}
        {#snippet empty()}
          <CompactEmpty icon={DatabaseIcon} text="No repository is public, and {person} has no access yet. Add {person} to a repository." />
        {/snippet}
      </DataTable>
    {/if}
  </Section>

  <SaveBar count={changed.length} onDiscard={draft.reset} onSave={() => (confirmSave = true)} />
{/if}

<ConfirmDialog bind:open={confirmSave} title="Save the access of {person}?" confirmLabel="Save access" destructive={narrows} {changes} onConfirm={save} />

<ConfirmDialog
  bind:open={confirmAccount}
  title={accountText.title}
  confirmLabel={accountText.label}
  destructive={accountText.destructive}
  changes={accountText.changes}
  onConfirm={applyAccount}
/>

<ShortFormDialog
  bind:open={addOpen}
  title="Add {person} to a repository"
  submitLabel="Add to repository"
  canSubmit={!!addRepo.trim() && !addProblem}
  onSubmit={() => setLevel(addRepo.trim(), addLevel)}
>
  <FormField label="Repository" for="{id}-add-repo" error={addProblem || undefined}>
    <Input id="{id}-add-repo" list="{id}-repos" autocomplete="off" spellcheck={false} bind:value={addRepo} />
  </FormField>
  <datalist id="{id}-repos">
    {#each candidates as c (c)}<option value={c}></option>{/each}
  </datalist>
  <FormField label="Access" for="{id}-add-level" hint={sso ? "Single sign-on accounts cannot publish." : undefined}>
    <Select.Root type="single" value={addLevel} onValueChange={(v) => (addLevel = v as Level)}>
      <Select.Trigger id="{id}-add-level" class="w-full">{levelLabel[addLevel]}</Select.Trigger>
      <Select.Content>
        <Select.Item value="read" label={levelLabel.read}>{levelLabel.read}</Select.Item>
        <Select.Item value="write" label={levelLabel.write} disabled={sso}>{levelLabel.write}</Select.Item>
      </Select.Content>
    </Select.Root>
  </FormField>
  <p class="text-meta text-muted-foreground">Save the page to keep the change.</p>
</ShortFormDialog>

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
