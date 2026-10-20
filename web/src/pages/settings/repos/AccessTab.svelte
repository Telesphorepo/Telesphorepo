<!--
  Repository settings, Access (docs/design/information-architecture.md, "The repo Access tab"):
  People (each person with a permission here, once, with the phrase of their set), Download
  (Public, from where) and Publish (CI pipelines, from where; a hosted repository only, because
  nobody publishes to a group or a proxy). Each part shows only to a person with its permission:
  People to Manage access, Public and the network rules to Edit settings, CI pipelines to CI
  pipelines.

  The permissions of people change at once: Give access finds people and gives them a set in one
  short form, Change edits the set of one person, and picked rows lose their access after a
  confirm. Every other edit is in place and goes into one draft, and the save bar saves it after a
  confirm that lists each change. Holders of All repositories can always do everything; the
  tables list the people and pipelines that were given access.
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
  import { Separator } from "$lib/components/ui/separator";
  import FormSection from "$lib/components/FormSection.svelte";
  import FormGrid from "$lib/components/FormGrid.svelte";
  import SwitchRow from "$lib/components/SwitchRow.svelte";
  import DataTable from "$lib/components/DataTable.svelte";
  import Cell from "$lib/components/Cell.svelte";
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
  import { changesOf, countOf, draftOf, pipelineFacts, pipelineOf, type AccessDraft } from "./access";
  import UsersIcon from "@lucide/svelte/icons/users-round";
  import WorkflowIcon from "@lucide/svelte/icons/workflow";
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

  const access = new Resource(() => api.repos.access(repo.name));
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

  // --- rows ---
  const sourceOf = <T,>(items: () => T[]): TableSource<T> => ({
    get items() {
      return items();
    },
    loading: false,
    error: "",
  });
  const people = sourceOf<AccessPerson>(() => access.data?.people ?? []);
  const ciRows = sourceOf<AccessPipeline>(() =>
    d.ci.map((cid) => pipelines.get(cid) ?? { id: cid, rule: `CI pipeline ${cid}`, system: "other", issuer: "", disabled: false }),
  );
  const addableCI = $derived(
    [...pipelines.values()]
      .filter((p) => !d.ci.includes(p.id))
      .sort((a, b) => a.rule.localeCompare(b.rule))
      .map((p) => ({ id: String(p.id), label: p.rule, detail: pipelineFacts(p) })),
  );
  let ciOpen = $state(false);
  const newPipeline = $derived(href("/settings/ci-pipelines/new", { repo: repo.name, back: repoSettingsPath(repo.name, "access") }));

  const personHref = (name: string) => (auth.can("people.manage") ? href(`/settings/people/${encodeURIComponent(name)}`) : undefined);
  const peopleCount = (n: number) => plural(n, "person", "people");

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
  function askRemove(names: Set<string>) {
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
  const granted = $derived((access.data?.people ?? []).map((p) => p.username));
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

  const adminsHelp = (n: number) =>
    n === 1 ? "The 1 person with All repositories can do everything here too." : `The ${n} people with All repositories can do everything here too.`;
</script>

{#if access.error && !access.data}
  <ErrorState error={access.error} title="The access of {repo.name} did not load" onRetry={access.reload} />
{:else if !access.data || !ready}
  <Loading rows={8} />
{:else}
  {@const a = access.data}
  {#if canAccess}
    <FormSection title="People" description={adminsHelp(a.download.admins)}>
      {#snippet actions()}
        <Button variant="outline" size="sm" onclick={() => (addOpen = true)}><PlusIcon />Give access</Button>
      {/snippet}
      <DataTable
        source={people}
        caption="People with permissions on {repo.name}"
        key={(p) => p.username}
        columns={[{ label: "Person", class: "w-56 min-w-56 max-sm:min-w-0" }, { label: "Permissions", grow: true }, { label: "", srLabel: "Actions" }]}
        bind:picked
        pickLabel={(p) => p.username}
      >
        {#snippet row(p)}
          {@const link = personHref(p.username)}
          <Cell strong class="w-56 max-w-56 min-w-56 max-sm:min-w-20">
            {#if link}
              <a class="block truncate underline-offset-4 hover:underline" href={link}>{p.username}</a>
            {:else}
              <span class="block truncate">{p.username}</span>
            {/if}
          </Cell>
          <Cell grow muted text={repoPhrase(p.perms)} />
          <Cell actions>
            <Button variant="ghost" size="sm" aria-label="Change the permissions of {p.username}" onclick={() => edit(p)}>Change</Button>
          </Cell>
        {/snippet}
        {#snippet bulk()}
          <Button variant="ghost" size="sm" class="hover:bg-destructive/10 hover:text-destructive" onclick={() => askRemove(picked)}>Remove access</Button>
        {/snippet}
        {#snippet empty()}
          <CompactEmpty icon={UsersIcon} text="Give access to the people who download, publish or manage {repo.name}." />
        {/snippet}
      </DataTable>
    </FormSection>
  {/if}

  {#if canSettings}
    <FormSection title="Download" description={a.download.notes.join(" ") || undefined}>
      <div class="flex flex-col gap-6">
        <FormGrid>
          <SwitchRow id="{id}-public" label="Public" help="Anyone can download without signing in." bind:checked={draft.value.public} />
        </FormGrid>
        <Separator />
        <FromWhere id="{id}-read" repo={repo.name} verb="download" lists={lists.data} {listsError} saved={draft.saved.read} bind:rules={draft.value.read} />
      </div>
    </FormSection>
  {/if}

  <!-- Nobody publishes to a group or a proxy. -->
  {#if repo.kind === "hosted" && (canSettings || canPipelines)}
    <FormSection title="Publish" description={a.publish.blocked?.code === "operation" ? "Nobody can publish until the copy from Nexus ends. The access below applies after that." : a.publish.blocked?.message}>
      {#snippet actions()}
        {#if a.publish.blocked?.code === "operation" && auth.can("nexus.manage")}<Button variant="outline" size="sm" href={href("/settings/nexus")}>Open Nexus migration</Button>{/if}
      {/snippet}
      <div class="flex flex-col gap-6">
        {#if canPipelines}
          <AccessPart title="CI pipelines" help="A CI job publishes with the token of its CI system, without the account of a person.">
            {#snippet actions()}
              <Button variant="outline" size="sm" onclick={() => (ciOpen = true)}><PlusIcon />Add CI pipeline</Button>
            {/snippet}
            <DataTable
              source={ciRows}
              caption="CI pipelines that can publish to {repo.name}"
              key={(p) => p.id}
              columns={[{ label: "CI pipeline", class: "w-64 min-w-64 max-sm:min-w-0" }, { label: "Conditions", grow: true, hideOnPhone: true }, { label: "", srLabel: "Actions" }]}
            >
              {#snippet row(p)}
                <Cell strong class="w-64 max-w-64 min-w-64 max-sm:min-w-0">
                  <span class="flex min-w-0 items-center gap-2">
                    {#if !draft.saved.ci.includes(p.id)}<span aria-hidden="true" class="size-1.5 shrink-0 rounded-full bg-warning" title="Not saved yet"></span><span class="sr-only">Not saved:</span>{/if}
                    <a class="truncate underline-offset-4 hover:underline" href={href(`/settings/ci-pipelines/${p.id}`)}>{p.rule}</a>
                    {#if p.disabled}<StatusBadge>Off</StatusBadge>{/if}
                  </span>
                </Cell>
                <Cell grow hideOnPhone muted text={pipelineFacts(p)} />
                <Cell actions>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    class="hover:text-destructive"
                    title="Remove from {repo.name}"
                    aria-label="Remove {p.rule} from {repo.name}"
                    onclick={() => (draft.value.ci = draft.value.ci.filter((x) => x !== p.id))}><XIcon /></Button
                  >
                </Cell>
              {/snippet}
              {#snippet empty()}
                <CompactEmpty icon={WorkflowIcon} text="Let a GitHub Actions or Jenkins job publish here with its own token: no password, no stored secret." />
              {/snippet}
            </DataTable>
          </AccessPart>
        {/if}
        {#if canPipelines && canSettings}<Separator />{/if}
        {#if canSettings}
          <FromWhere id="{id}-publish" repo={repo.name} verb="publish" lists={lists.data} {listsError} saved={draft.saved.publish} bind:rules={draft.value.publish} />
        {/if}
      </div>
    </FormSection>
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
  title="Remove the access of {peopleCount(removing.length)} to {repo.name}?"
  description="{someNames(removing)}."
  confirmLabel="Remove access of {peopleCount(removing.length)}"
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
  exclude={granted}
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
