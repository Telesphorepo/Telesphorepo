<!--
  Repository settings, Access: the answers to who can download and who can publish, as the server
  and the unsaved edits say now, then every person with access.

  - Who can download: Anyone (public), or the people with Download and the administrators (the
    holders of All repositories, one row that opens to their names), then from where. The Public
    switch sits in the card's header (Edit settings); Change opens the networks (Edit settings).
  - Who can publish: the people with Publish, the CI pipelines (CI pipelines: Add CI pipeline,
    remove one) and the administrators, then from where. When nobody can publish (a group or a
    proxy, or an operation such as a Nexus copy), the card says why in place of the answer.
  - People and permissions (Manage access): each person with a grant, with the phrase of their
    set, Change and Remove; then the administrators, read-only with a badge. Give access finds
    people and gives them a set in one short form. These changes apply at once; picked rows lose
    their access after a confirm.

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
  import { repoPermissions, repoPhrase } from "$lib/permissions";
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
  import GrantDialog from "./GrantDialog.svelte";
  import ConditionBadges from "../../server/ci/ConditionBadges.svelte";
  import { and, changesOf, countOf, draftOf, pipelineConditions, pipelineFacts, pipelineHead, pipelineOf, type AccessDraft } from "./access";
  import UsersIcon from "@lucide/svelte/icons/users-round";
  import WorkflowIcon from "@lucide/svelte/icons/workflow";
  import GlobeIcon from "@lucide/svelte/icons/globe";
  import LockIcon from "@lucide/svelte/icons/lock";
  import UploadIcon from "@lucide/svelte/icons/upload";
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
  const granted = (xs: AccessPerson[] | undefined) => (xs ?? []).filter((p) => p.via === "permission");
  const admins = $derived((access.data?.download.people ?? []).filter((p) => p.via === "all_repos"));
  const downloaders = $derived(granted(access.data?.download.people));
  const publishers = $derived(granted(access.data?.publish.people));
  const ciNow = $derived(d.ci.map((cid) => pipelines.get(cid) ?? { id: cid, rule: `CI pipeline ${cid}`, system: "other", issuer: "", disabled: false }));
  const peopleCount = (n: number) => plural(n, "person", "people");
  const adminCount = (n: number) => plural(n, "administrator", "administrators");

  /** "3 people and 2 administrators", "Only administrators". */
  function whoOf(parts: string[]): string {
    if (!parts.length) return admins.length ? "Only administrators" : "Nobody";
    if (admins.length) parts.push(adminCount(admins.length));
    const s = and(parts);
    return s[0].toUpperCase() + s.slice(1);
  }
  const downloadWho = $derived(d.public ? "Anyone" : whoOf(downloaders.length ? [peopleCount(downloaders.length)] : []));
  const publishWho = $derived(
    whoOf([
      ...(publishers.length ? [peopleCount(publishers.length)] : []),
      ...(ciNow.filter((p) => !p.disabled).length ? [plural(ciNow.filter((p) => !p.disabled).length, "CI pipeline", "CI pipelines")] : []),
    ]),
  );
  // The administrators open in place, in each card on its own.
  let adminsOpen = $state({ download: false, publish: false });

  // --- rows ---
  const sourceOf = <T,>(items: () => T[]): TableSource<T> => ({
    get items() {
      return items();
    },
    loading: false,
    error: "",
  });
  const people = sourceOf<AccessPerson>(() => [...(access.data?.people ?? []), ...admins]);
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

  async function afterPeopleChange(names: string[]) {
    picked = new Set();
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
  const findPeople = async (q: string) => (await api.people.search(q, { limit: 20 })).items.map((p) => ({ id: p.username, label: p.username }));
  async function give(names: string[], perms: PermId[]) {
    await api.grants.set(names, [repo.name], perms);
    toast.success(`Gave ${peopleCount(names.length)} access to ${repo.name}: ${repoPhrase(perms)}.`);
    await afterPeopleChange(names);
  }

  let editing = $state<AccessPerson | null>(null);
  let editOpen = $state(false);
  const edit = (p: AccessPerson) => ((editing = p), (editOpen = true));
  async function change(names: string[], perms: PermId[]) {
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

<!-- People as avatars and names, then the administrators as one button that shows them. -->
{#snippet faces(list: AccessPerson[], which: "download" | "publish")}
  {@const shown = list.slice(0, 12)}
  {@const open = adminsOpen[which]}
  <ul class="flex flex-wrap items-center gap-2" aria-label="People">
    {#each [...shown, ...(open ? admins : [])] as p (p.username + p.via)}
      {@const link = personHref(p.username)}
      <li>
        <svelte:element
          this={link ? "a" : "span"}
          href={link}
          title={p.via === "all_repos" ? "All repositories" : repoPhrase(p.perms)}
          class="flex h-8 items-center gap-2 rounded-lg bg-muted pr-3 pl-1 text-body {link ? 'transition-colors hover:bg-hover' : ''}"
        >
          <UserAvatar name={p.username} size="sm" />{p.username}
        </svelte:element>
      </li>
    {/each}
    {#if list.length > shown.length}<li class="text-meta text-muted-foreground">and {list.length - shown.length} more</li>{/if}
    {#if admins.length}
      <li>
        <Button variant="ghost" size="sm" aria-expanded={open} onclick={() => (adminsOpen[which] = !open)}>
          {open ? "Hide administrators" : adminCount(admins.length)}<ChevronDownIcon class="transition-transform {open ? 'rotate-180' : ''}" />
        </Button>
      </li>
    {/if}
  </ul>
{/snippet}

{#if access.error && !access.data}
  <ErrorState error={access.error} title="The access of {repo.name} did not load" onRetry={access.reload} />
{:else if !access.data || !ready}
  <Loading rows={8} />
{:else}
  {@const a = access.data}
  {@const blocked = a.publish.blocked}
  <Section title="Who can download">
    {#snippet actions()}
      {#if canSettings}
        <div class="flex h-8 items-center gap-3">
          <Label for="{id}-public" class="text-body">Public</Label>
          <Switch id="{id}-public" bind:checked={draft.value.public} />
        </div>
      {/if}
    {/snippet}
    <div class="flex flex-col gap-6">
      <AccessPart
        icon={d.public ? GlobeIcon : LockIcon}
        title={downloadWho}
        help={[d.public ? "No sign-in and no read token needed." : "They sign in, or use a read token in a build.", ...a.download.notes].join(" ")}
      >
        {#if !d.public && (downloaders.length || admins.length)}<div class="pl-8">{@render faces(downloaders, "download")}</div>{/if}
      </AccessPart>
      <FromWhere id="{id}-read" repo={repo.name} verb="download" editable={canSettings} lists={lists.data} {listsError} saved={draft.saved.read} bind:rules={draft.value.read} />
    </div>
  </Section>

  <Section title="Who can publish">
    {#snippet actions()}
      {#if blocked?.code === "operation" && auth.can("nexus.manage")}<Button variant="outline" size="sm" href={href("/settings/nexus")}>Open Nexus migration</Button>{/if}
    {/snippet}
    <div class="flex flex-col gap-6">
      {#if blocked}
        <AccessPart
          icon={BanIcon}
          title="Nobody"
          help={blocked.code === "operation" ? `${blocked.message} The access below applies after that.` : blocked.message}
        />
      {:else}
        <AccessPart icon={UploadIcon} title={publishWho} help={publishers.length || admins.length ? "People who publish can download too." : undefined}>
          {#if publishers.length || admins.length}<div class="pl-8">{@render faces(publishers, "publish")}</div>{/if}
        </AccessPart>
      {/if}
      <!-- A group or a proxy takes no publishes, so it has no pipelines and no networks for them. -->
      {#if blocked?.code !== "read_only_kind"}
        <Separator />
        <AccessPart title="CI pipelines" help="A CI job publishes with the token of its CI system, without the account of a person.">
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

  {#if canAccess}
    <Section title="People and permissions" description="Each change here applies at once.">
      {#snippet actions()}
        <Button variant="outline" size="sm" onclick={() => (addOpen = true)}><PlusIcon />Give access</Button>
      {/snippet}
      <ItemList
        source={people}
        caption="People with access to {repo.name}"
        key={(p) => p.username}
        href={(p) => personHref(p.username)}
        bind:picked
        pickable={(p) => p.via === "permission"}
        pickLabel={(p) => p.username}
      >
        {#snippet leading(p)}<UserAvatar name={p.username} size="md" />{/snippet}
        {#snippet title(p)}{p.username}{/snippet}
        {#snippet badges(p)}{#if p.via === "all_repos"}<StatusBadge title="Holds All repositories: change it on the person's page">All repositories</StatusBadge>{/if}{/snippet}
        {#snippet meta(p)}{repoPhrase(p.perms)}{/snippet}
        {#snippet trailing(p)}
          {#if p.via === "permission"}
            <Button variant="ghost" size="sm" aria-label="Change the permissions of {p.username}" onclick={() => edit(p)}>Change</Button>
            <!-- On a phone a row has room for one action: Remove is in the pick and in Change there. -->
            <Button variant="ghost" size="sm" class="hidden hover:bg-destructive/10 hover:text-destructive sm:inline-flex" aria-label="Remove the access of {p.username}" onclick={() => askRemove([p.username])}>Remove</Button>
          {/if}
        {/snippet}
        {#snippet bulk()}
          <Button variant="ghost" size="sm" class="hover:bg-destructive/10 hover:text-destructive" onclick={() => askRemove(picked)}>Remove access</Button>
        {/snippet}
        {#snippet empty()}
          <CompactEmpty icon={UsersIcon} text="Give access to the people who download, publish or manage {repo.name}." />
        {/snippet}
      </ItemList>
    </Section>
  {/if}

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
/>

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
