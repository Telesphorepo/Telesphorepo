<!--
  Repository settings, Access: two cards, Download and Publish. Each person shows once, in the card
  of their highest access, with what they hold beyond it as badges (Delete files, Edit settings,
  Manage access, Manage CI pipelines).

  - Download: the Public switch (Edit settings) and Give access (Manage access) in the header; the
    people whose grant has no Publish (while private, or when someone holds such a grant); then
    from where (Change opens the networks; Edit settings).
  - Publish: the people with Publish, then the holders of All repositories as one read-only row
    ("2 administrators") that opens to their names; the CI pipelines (Add CI pipeline, remove one);
    then from where. When nobody can publish (a group or a proxy, or an operation such as a Nexus
    copy), the card says why in place of the pipelines and the networks.
  - A person's Change and Remove (Manage access), Give access and the bulk pick apply at once;
    picked rows lose their access after a confirm. Give access (the grant dialog) gives each
    chosen person their own permissions in one request.

  The Public switch, the networks and the CI pipelines go into one draft, and the save bar saves
  it after a confirm that lists each change.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { toast } from "svelte-sonner";
  import { api, type AccessPerson, type AccessPipeline, type Page, type Repo, type RepoAccess, type PermId, type Trust } from "$lib/api";
  import { Resource, type TableSource } from "$lib/load.svelte";
  import { Draft } from "$lib/unsaved.svelte";
  import { auth } from "$lib/auth.svelte";
  import { labelOf, repoPermissions, repoPhrase, sortRepoPermissions } from "$lib/permissions";
  import { href, repoSettingsPath } from "$lib/router.svelte";
  import { count as plural, someNames } from "$lib/words";
  import { Button } from "$lib/components/ui/button";
  import { Switch } from "$lib/components/ui/switch";
  import { Label } from "$lib/components/ui/label";
  import { Separator } from "$lib/components/ui/separator";
  import Section from "$lib/components/Section.svelte";
  import ItemList from "$lib/components/ItemList.svelte";
  import UserAvatar from "$lib/components/UserAvatar.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import SaveBar from "$lib/components/SaveBar.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import AccessPart from "./AccessPart.svelte";
  import FromWhere from "./FromWhere.svelte";
  import ChoiceDialog from "./ChoiceDialog.svelte";
  import GrantDialog, { bySet, type GrantItem } from "./GrantDialog.svelte";
  import ConditionBadges from "../../server/ci/ConditionBadges.svelte";
  import { changesOf, countOf, draftOf, pipelineConditions, pipelineFacts, pipelineHead, pipelineOf, type AccessDraft } from "./access";
  import UsersIcon from "@lucide/svelte/icons/users-round";
  import WorkflowIcon from "@lucide/svelte/icons/workflow";
  import ShieldCheckIcon from "@lucide/svelte/icons/shield-check";
  import BanIcon from "@lucide/svelte/icons/ban";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import XIcon from "@lucide/svelte/icons/x";

  let { repo, onChanged }: { repo: Repo; onChanged: () => void } = $props();

  const id = $props.id();
  // What the person can see and change here.
  const canAccess = $derived(auth.canRepo(repo.name, "repo.access"));
  const canSettings = $derived(auth.canRepo(repo.name, "repo.settings"));
  const canPipelines = $derived(auth.canRepo(repo.name, "ci.pipelines"));
  // The permissions the person can give and remove here.
  const held = $derived(auth.allRepos() ? repoPermissions().map((p) => p.name) : repo.permissions ?? []);

  // With the holders of All repositories, so the tab names everyone who has access.
  const access = new Resource(() => api.repos.access(repo.name, { admins: true }));
  // CI pipelines answer 403 to a person who holds CI pipelines nowhere.
  const trusts = new Resource<Page<Trust>>(() =>
    untrack(() => canPipelines) ? api.publish.trusts.list({ limit: 200 }) : Promise.resolve({ items: [], next: null }),
  );
  // Network lists need the Network permission; without it the rules show by name.
  // A holder of settings on the repo reads every network list, to pick the lists of its rules.
  const lists = new Resource(() => (auth.canRepo(repo.name, "repo.settings") ? api.ipLists.list() : Promise.resolve(undefined)));
  const listsError = $derived(lists.error);

  const draft = new Draft<AccessDraft>({ public: false, ci: [], read: [], publish: [] });
  let ready = $state(false);
  $effect(() => {
    const a = access.data;
    if (a && !ready) untrack(() => (draft.commit(draftOf(a)), (ready = true)));
  });

  const d = $derived(draft.value);
  const count = $derived(ready ? countOf(draft.saved, draft.value) : 0);

  /** Every CI pipeline by id: the ones that list this repo (as the server reads them), then every other rule. */
  const pipelines = $derived.by(() => {
    const m = new Map<number, AccessPipeline>();
    for (const t of trusts.data?.items ?? []) m.set(t.id, pipelineOf(t));
    for (const p of access.data?.publish.ci ?? []) m.set(p.id, p);
    return m;
  });

  // --- who ---
  // Each person shows once: under Download when their highest access is download, else under
  // Publish; the holders of All repositories hold everything, so they are one row under Publish.
  const admins = $derived((access.data?.download.people ?? []).filter((p) => p.via === "all_repos"));
  const grants = $derived((access.data?.people ?? []).filter((p) => p.via === "permission"));
  const canPublish = (p: AccessPerson) => p.perms.includes("repo.publish");
  const ciNow = $derived(d.ci.map((cid) => pipelines.get(cid) ?? { id: cid, rule: `CI pipeline ${cid}`, system: "other", issuer: "", disabled: false }));
  const peopleCount = (n: number) => plural(n, "person", "people");
  const adminCount = (n: number) => plural(n, "administrator", "administrators");
  /** What a person holds beyond the access of their section, as short badges. */
  const beyond = (p: AccessPerson, base: PermId[]) => sortRepoPermissions(p.perms.filter((x) => !base.includes(x))).map(labelOf);
  let adminsOpen = $state(false);

  // --- rows ---
  const sourceOf = <T,>(items: () => T[]): TableSource<T> => ({
    get items() {
      return items();
    },
    loading: false,
    error: "",
  });
  type Row = { key: string; person?: AccessPerson; admins?: true };
  const downloadRows = sourceOf<Row>(() => grants.filter((p) => !canPublish(p)).map((p) => ({ key: p.username, person: p })));
  const publishRows = sourceOf<Row>(() => [
    ...grants.filter(canPublish).map((p) => ({ key: p.username, person: p })),
    ...(admins.length ? [{ key: " admins", admins: true as const }] : []),
    ...(adminsOpen ? admins.map((p) => ({ key: ` admin ${p.username}`, person: p })) : []),
  ]);
  const ciRows = sourceOf<AccessPipeline>(() => ciNow);
  const addableCI = $derived(
    [...pipelines.values()]
      .filter((p) => !d.ci.includes(p.id))
      .sort((a, b) => a.rule.localeCompare(b.rule))
      .map((p) => ({ id: String(p.id), label: p.rule, detail: pipelineFacts(p) })),
  );
  let ciOpen = $state(false);
  const newPipeline = $derived(href("/settings/ci-pipelines/new", { repo: repo.name, back: repoSettingsPath(repo.name, "access") }));

  const personHref = (name: string) => (auth.can("people.manage") ? href(`/settings/people/${encodeURIComponent(name)}`) : undefined);

  // --- the permissions of people: changes apply at once ---
  let picked = $state(new Set<string>());
  let pickedPub = $state(new Set<string>());

  async function afterPeopleChange(names: string[]) {
    picked = new Set();
    pickedPub = new Set();
    await access.reload();
    // A change of your own permissions changes what the Settings show you.
    if (auth.user && names.includes(auth.user.username)) await auth.load();
    onChanged();
  }

  let removing = $state<string[]>([]);
  let removeOpen = $state(false);
  function askRemove(names: Iterable<string>) {
    removing = [...names].sort((a, b) => a.localeCompare(b));
    removeOpen = true;
  }
  async function removeAccess() {
    const names = removing;
    await api.grants.set(names, [repo.name], []);
    toast.success(`Removed the access of ${peopleCount(names.length)} to ${repo.name}.`);
    await afterPeopleChange(names);
  }

  // --- give access and change one person ---
  let addOpen = $state(false);
  // Search leaves out the people listed here: a grant or All repositories.
  const listed = $derived([...(access.data?.people ?? []), ...admins].map((p) => p.username));
  async function findPeople(q: string, after?: string) {
    const page = await api.people.search(q, { after, limit: 20 });
    return { next: page.next, items: page.items.map((p) => ({ id: p.username, label: p.username })) };
  }
  async function give(items: GrantItem[]) {
    const names = items.map((i) => i.id);
    const groups = bySet(items);
    await api.grants.setGroups(groups.map((g) => ({ users: g.ids, repos: [repo.name], perms: g.perms })));
    toast.success(`Gave ${peopleCount(names.length)} access to ${repo.name}${groups.length === 1 ? `: ${repoPhrase(groups[0].perms)}` : ""}.`);
    await afterPeopleChange(names);
  }

  let editing = $state<AccessPerson | null>(null);
  let editOpen = $state(false);
  const edit = (p: AccessPerson) => ((editing = p), (editOpen = true));
  async function change(items: GrantItem[]) {
    const names = items.map((i) => i.id);
    const perms = items[0]?.perms ?? [];
    await api.grants.set(names, [repo.name], perms);
    toast.success(perms.length ? `Changed the permissions of ${names[0]} on ${repo.name}.` : `Removed the access of ${names[0]} to ${repo.name}.`);
    await afterPeopleChange(names);
  }

  // --- save ---
  let confirmOpen = $state(false);
  const changes = $derived(ready ? changesOf(repo.name, draft.saved, d, pipelines, lists.data) : []);
  // A save that takes access away from someone is red.
  const narrows = $derived.by(() => {
    const s = draft.saved;
    if (s.public && !d.public) return true;
    if (s.ci.some((x) => !d.ci.includes(x))) return true;
    // A network rule narrows when it starts, or when it loses an entry and still holds others.
    const key = (r: { kind: string; value: string }) => `${r.kind}:${r.value}`;
    return (["read", "publish"] as const).some(
      (k) => (s[k].length === 0 && d[k].length > 0) || (d[k].length > 0 && s[k].some((r) => !d[k].some((x) => key(x) === key(r)))),
    );
  });

  async function save() {
    const saved = draft.saved;
    const v = $state.snapshot(draft.value) as AccessDraft;
    try {
      if (v.public !== saved.public) await api.repos.setPublic([repo.name], v.public);
      if (JSON.stringify([saved.read, saved.publish]) !== JSON.stringify([v.read, v.publish]))
        await api.repos.ipRules.set(repo.name, { read: v.read, publish: v.publish });
      const touched = [...v.ci.filter((x) => !saved.ci.includes(x)), ...saved.ci.filter((x) => !v.ci.includes(x))];
      for (const tid of touched) {
        const t = await api.publish.trusts.get(tid);
        const repos = v.ci.includes(tid) ? [...new Set([...t.repos, repo.name])] : t.repos.filter((r) => r !== repo.name);
        await api.publish.trusts.update(tid, { repos });
      }
    } finally {
      // Whatever was saved is now the saved state; edits that did not save stay unsaved.
      await Promise.all([access.reload(), trusts.reload()]);
      if (access.data) {
        draft.saved = draftOf(access.data);
        draft.value = v;
      }
      onChanged();
    }
    draft.commit(draftOf(access.data as RepoAccess));
    toast.success(`Saved the access of ${repo.name}`);
  }
</script>

<!-- The people of one section: each person once, with what they hold beyond the section's access as badges. -->
{#snippet people(rows: TableSource<Row>, base: PermId[], which: "download" | "publish", emptyText: string)}
  <ItemList
    source={rows}
    caption={which === "download" ? `People who download from ${repo.name}` : `People who publish to ${repo.name}`}
    key={(r) => r.key}
    href={(r) => (r.person ? personHref(r.person.username) : undefined)}
    bind:picked={() => (which === "download" ? picked : pickedPub), (v) => (which === "download" ? (picked = v) : (pickedPub = v))}
    pickable={(r) => canAccess && r.person?.via === "permission"}
    pickLabel={(r) => r.person?.username ?? ""}
  >
    {#snippet leading(r)}
      {#if r.person}<UserAvatar name={r.person.username} size="md" />{:else}<span class="flex size-8 items-center justify-center"><ShieldCheckIcon class="size-5" /></span>{/if}
    {/snippet}
    {#snippet title(r)}{r.person ? r.person.username : adminCount(admins.length)}{/snippet}
    {#snippet badges(r)}
      {#if r.person?.via === "permission"}
        {#each beyond(r.person, base) as label (label)}<StatusBadge>{label}</StatusBadge>{/each}
      {/if}
    {/snippet}
    {#snippet meta(r)}{#if r.admins}Every permission, through All repositories{/if}{/snippet}
    {#snippet trailing(r)}
      {#if r.admins}
        <Button variant="ghost" size="sm" aria-expanded={adminsOpen} onclick={() => (adminsOpen = !adminsOpen)}>
          {adminsOpen ? "Hide" : "Show"}<ChevronDownIcon class="transition-transform {adminsOpen ? 'rotate-180' : ''}" />
        </Button>
      {:else if r.person?.via === "permission" && canAccess}
        {@const p = r.person}
        <Button variant="ghost" size="sm" aria-label="Change the permissions of {p.username}" onclick={() => edit(p)}>Change</Button>
        <!-- On a phone a row has room for one action: Remove is in the pick and in Change there. -->
        <Button variant="ghost" size="sm" class="hidden hover:bg-destructive/10 hover:text-destructive sm:inline-flex" aria-label="Remove the access of {p.username}" onclick={() => askRemove([p.username])}>Remove</Button>
      {/if}
    {/snippet}
    {#snippet bulk()}
      <Button variant="ghost" size="sm" class="hover:bg-destructive/10 hover:text-destructive" onclick={() => askRemove(which === "download" ? picked : pickedPub)}>Remove access</Button>
    {/snippet}
    {#snippet empty()}<CompactEmpty icon={UsersIcon} text={emptyText} />{/snippet}
  </ItemList>
{/snippet}

{#if access.error && !access.data}
  <ErrorState error={access.error} title="The access of {repo.name} did not load" onRetry={access.reload} />
{:else if !access.data || !ready}
  <Loading rows={8} />
{:else}
  {@const a = access.data}
  {@const blocked = a.publish.blocked}
  <Section title="Download" description={[d.public ? "No sign-in and no read token needed." : "People sign in, or use a read token in a build.", ...a.download.notes].join(" ")}>
    {#snippet actions()}
      {#if canSettings}
        <div class="flex h-8 items-center gap-3">
          <Label for="{id}-public" class="text-body">Public</Label>
          <Switch id="{id}-public" bind:checked={draft.value.public} />
        </div>
      {/if}
      {#if canAccess}<Button variant="outline" size="sm" onclick={() => (addOpen = true)}><PlusIcon />Give access</Button>{/if}
    {/snippet}
    <div class="flex flex-col gap-6">
      <!-- Public: anyone downloads, so the list shows only when someone holds a grant of download alone. -->
      {#if !d.public || downloadRows.items.length}
        {@render people(
          downloadRows,
          ["repo.download"],
          "download",
          publishRows.items.length ? "Only the people under Publish." : "Nobody yet. Give access to the people who download it.",
        )}
        <Separator />
      {/if}
      <FromWhere id="{id}-read" repo={repo.name} verb="download" editable={canSettings} lists={lists.data} {listsError} saved={draft.saved.read} bind:rules={draft.value.read} />
    </div>
  </Section>

  <Section title="Publish" description={blocked ? undefined : "People who publish can download too."}>
    {#snippet actions()}
      {#if blocked?.code === "operation" && auth.can("nexus.manage")}<Button variant="outline" size="sm" href={href("/settings/nexus")}>Open Nexus migration</Button>{/if}
    {/snippet}
    <div class="flex flex-col gap-6">
      {#if blocked}
        <AccessPart
          icon={BanIcon}
          title="Nobody can publish now"
          help={blocked.code === "operation" && publishRows.items.length ? `${blocked.message} The people below can publish after that.` : blocked.message}
        />
      {/if}
      {#if !blocked || publishRows.items.length}
        {@render people(publishRows, ["repo.download", "repo.publish"], "publish", "Nobody yet. Give access to the people who publish here.")}
      {/if}
      <!-- While nothing can publish, the reason above takes the place of the pipelines and the networks. -->
      {#if !blocked}
        <Separator />
        <!-- With no pipeline the empty row says what one is. -->
        <AccessPart title="CI pipelines" help={ciNow.length ? "A CI job publishes with the token of its CI system, without the account of a person." : undefined}>
          {#snippet actions()}
            {#if canPipelines}<Button variant="outline" size="sm" onclick={() => (ciOpen = true)}><PlusIcon />Add CI pipeline</Button>{/if}
          {/snippet}
          <ItemList
            source={ciRows}
            caption="CI pipelines that can publish to {repo.name}"
            key={(p) => p.id}
            href={(p) => (canPipelines ? href(`/settings/ci-pipelines/${p.id}`) : undefined)}
          >
            {#snippet leading()}<WorkflowIcon class="size-5" />{/snippet}
            {#snippet title(p)}{p.rule}{/snippet}
            {#snippet badges(p)}
              {#if p.disabled}<StatusBadge>Off</StatusBadge>{/if}
              {#if !draft.saved.ci.includes(p.id)}<span class="size-1.5 rounded-full bg-warning" title="Not saved yet"></span><span class="sr-only">Not saved</span>{/if}
            {/snippet}
            {#snippet meta(p)}<span title={pipelineFacts(p)}>{pipelineHead(p)}</span>{/snippet}
            {#snippet trailing(p)}
              <ConditionBadges items={pipelineConditions(p)} max={3} nowrap class="hidden max-w-80 md:flex" />
              {#if canPipelines}
                <Button
                  variant="ghost"
                  size="icon-sm"
                  class="hover:text-destructive"
                  title="Remove from {repo.name}"
                  aria-label="Remove {p.rule} from {repo.name}"
                  onclick={() => (draft.value.ci = draft.value.ci.filter((x) => x !== p.id))}><XIcon /></Button
                >
              {/if}
            {/snippet}
            {#snippet empty()}
              <CompactEmpty icon={WorkflowIcon} text="Let a GitHub Actions or Jenkins job publish here with its own token: no password, no stored secret." />
            {/snippet}
          </ItemList>
        </AccessPart>
        <Separator />
        <FromWhere id="{id}-publish" repo={repo.name} verb="publish" editable={canSettings} lists={lists.data} {listsError} saved={draft.saved.publish} bind:rules={draft.value.publish} />
      {/if}
    </div>
  </Section>

  {#if canSettings && (d.read.length || d.publish.length)}
    <p class="mt-6 text-meta text-muted-foreground">
      The server sees each visitor's own address only when it trusts the proxy in front of it.
      {#if auth.can("network.manage")}
        Check <a class="text-link underline-offset-4 hover:underline" href={href("/settings/proxy")}>Network and proxy</a>.
      {/if}
    </p>
  {/if}

  <SaveBar {count} onDiscard={draft.reset} onSave={() => (confirmOpen = true)} />
{/if}

<ConfirmDialog bind:open={confirmOpen} title="Save the access of {repo.name}?" confirmLabel="Save access" destructive={narrows} {changes} onConfirm={save} />

<ConfirmDialog
  bind:open={removeOpen}
  title="Remove the access of {removing.length === 1 ? removing[0] : peopleCount(removing.length)} to {repo.name}?"
  description={removing.length === 1 ? undefined : `${someNames(removing)}.`}
  confirmLabel="Remove access of {removing.length === 1 ? removing[0] : peopleCount(removing.length)}"
  destructive
  changes={[
    `${removing.length === 1 ? "This person loses" : "These people lose"} every permission on ${repo.name}${draft.saved.public ? ", but can still download, because it is public" : ""}`,
    "Their read tokens lose this access too",
  ]}
  onConfirm={removeAccess}
/>

<GrantDialog
  bind:open={addOpen}
  title="Give people access to {repo.name}"
  noun={["person", "people"]}
  search={findPeople}
  exclude={listed}
  {held}
  submitLabel={(n) => (n ? `Give ${peopleCount(n)} access` : "Give access")}
  onGive={give}
>
  {#snippet leading(c)}<UserAvatar name={c.id} size="sm" />{/snippet}
</GrantDialog>

<GrantDialog
  bind:open={editOpen}
  title="Permissions of {editing?.username ?? ''} on {repo.name}"
  description="Clear every permission to remove the access."
  noun={["person", "people"]}
  targets={editing ? [editing.username] : []}
  start={editing?.perms ?? []}
  {held}
  submitLabel={(_, perms) => (perms.length ? "Save permissions" : "Remove access")}
  onGive={change}
/>

<ChoiceDialog
  bind:open={ciOpen}
  title="Add CI pipelines"
  description="Each CI pipeline that you add can publish to {repo.name}."
  noun={["CI pipeline", "CI pipelines"]}
  items={addableCI}
  loading={trusts.loading && !trusts.data}
  error={trusts.error ? `The CI pipelines did not load: ${trusts.error}` : ""}
  none={pipelines.size ? `Every CI pipeline can publish to ${repo.name} already.` : "No CI pipeline exists yet."}
  newLabel="New CI pipeline"
  newHref={newPipeline}
  onAdd={(ids) => (draft.value.ci = [...draft.value.ci, ...ids.map(Number)].sort((x, y) => x - y))}
/>
