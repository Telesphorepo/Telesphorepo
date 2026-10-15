<!--
  Repository settings, Access (docs/design/information-architecture.md, "The repo Access tab"):
  Download (Public, people who can download and not publish, from where) and Publish (people who can publish, CI
  pipelines, from where). The access of people changes at once: Add people gives several people
  access in one short form, and picked rows change their level or lose their access (after a
  confirm). Every other edit is in place and goes into one draft, and the save bar saves it after
  a confirm that lists each change. Administrators can always download and publish; the tables
  list the people and pipelines that were given access.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { toast } from "svelte-sonner";
  import { api, errorMessage, type AccessPipeline, type Repo, type RepoAccess, type User } from "$lib/api";
  import { Resource, type TableSource } from "$lib/load.svelte";
  import { Draft } from "$lib/unsaved.svelte";
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
  import { changesOf, countOf, draftOf, pipelineFacts, pipelineOf, type AccessDraft, type Level } from "./access";
  import UsersIcon from "@lucide/svelte/icons/users-round";
  import UploadIcon from "@lucide/svelte/icons/upload";
  import WorkflowIcon from "@lucide/svelte/icons/workflow";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import XIcon from "@lucide/svelte/icons/x";

  let { repo, onChanged }: { repo: Repo; onChanged: () => void } = $props();

  const id = $props.id();
  const access = new Resource(() => api.repos.access(repo.name));
  const trusts = new Resource(() => api.publish.trusts.list({ limit: 200 }));
  const lists = new Resource(() => api.ipLists.list());
  // Usernames for the add dialogs, and who signs in with single sign-on (cannot publish).
  const users = new Resource(() => api.users.list({ limit: 200 }));

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
  const userOf = (name: string): User | undefined => users.data?.items.find((u) => u.username === name);

  // --- rows ---
  type PersonRow = { username: string; level: Level };
  const sourceOf = <T,>(items: () => T[]): TableSource<T> => ({
    get items() {
      return items();
    },
    loading: false,
    error: "",
  });
  // The people given access, as the server answers them; a publisher can download too, so download.people holds everyone.
  const everyone = $derived(
    (access.data?.download.people ?? [])
      .filter((p) => p.via === "permission")
      .map((p): PersonRow => ({ username: p.username, level: p.level }))
      .sort((a, b) => a.username.localeCompare(b.username)),
  );
  // Each person shows once: a publisher can download too, so the Download list holds only the rest.
  const downloaders = sourceOf<PersonRow>(() => everyone.filter((p) => p.level === "read"));
  const publishers = sourceOf<PersonRow>(() => everyone.filter((p) => p.level === "write"));
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

  const personHref = (name: string) => href(`/settings/people/${encodeURIComponent(name)}`);
  const peopleCount = (n: number) => plural(n, "person", "people");
  const verbOf = (level: Level) => (level === "write" ? "publish to" : "download from");

  // --- the access of people: changes apply at once ---
  let pickedRead = $state(new Set<string>());
  let pickedWrite = $state(new Set<string>());
  let busy = $state(false);

  async function afterPeopleChange() {
    pickedRead = new Set();
    pickedWrite = new Set();
    await access.reload();
    onChanged();
  }

  async function changeTo(names: string[], level: Level) {
    busy = true;
    try {
      await api.permissions.set(names, [repo.name], level);
      toast.success(`${peopleCount(names.length)} can now ${verbOf(level)} ${repo.name}.`);
      await afterPeopleChange();
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      busy = false;
    }
  }

  let removing = $state<string[]>([]);
  let removeOpen = $state(false);
  function askRemove(names: Set<string>) {
    removing = [...names].sort((a, b) => a.localeCompare(b));
    removeOpen = true;
  }
  async function removeAccess() {
    const names = removing;
    await api.permissions.delete(names, [repo.name]);
    toast.success(`Removed the access of ${peopleCount(names.length)} to ${repo.name}.`);
    await afterPeopleChange();
  }

  // --- add people ---
  let addLevel = $state<Level>("read");
  let addOpen = $state(false);
  function openAdd(level: Level) {
    addLevel = level;
    addOpen = true;
  }
  const granted = $derived(new Set(everyone.map((p) => p.username)));
  const candidates = $derived(
    (users.data?.items ?? [])
      .filter((u) => !u.is_admin && !u.disabled && !granted.has(u.username))
      .sort((a, b) => a.username.localeCompare(b.username))
      .map((u) => ({ id: u.username, label: u.username, detail: u.email || undefined })),
  );
  async function give(names: string[], level: Level) {
    await api.permissions.set(names, [repo.name], level);
    toast.success(`${peopleCount(names.length)} can now ${verbOf(level)} ${repo.name}.`);
    await afterPeopleChange();
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

  const adminsHelp = (n: number, verb: string) =>
    n === 1 ? `The 1 administrator can always ${verb} too.` : `The ${n} administrators can always ${verb} too.`;
</script>

{#if access.error && !access.data}
  <ErrorState error={access.error} title="The access of {repo.name} did not load" onRetry={access.reload} />
{:else if !access.data || !ready}
  <Loading rows={8} />
{:else}
  {@const a = access.data}
  <FormSection title="Download">
    <div class="flex flex-col gap-6">
      <FormGrid>
        <SwitchRow id="{id}-public" label="Public" help="Anyone can download without signing in." bind:checked={draft.value.public} />
      </FormGrid>
      <Separator />
      <AccessPart title="People who can download" help="People who can publish can download too. {adminsHelp(a.download.admins, 'download')}">
        {#snippet actions()}
          <Button variant="outline" size="sm" onclick={() => openAdd("read")}><PlusIcon />Add people</Button>
        {/snippet}
        <DataTable
          source={downloaders}
          caption="People who can download from {repo.name}"
          key={(p) => p.username}
          columns={[{ label: "Person", grow: true }]}
          bind:picked={pickedRead}
          pickLabel={(p) => p.username}
        >
          {#snippet row(p)}
            <Cell grow strong class="max-sm:min-w-20">
              <a class="block truncate underline-offset-4 hover:underline" href={personHref(p.username)}>{p.username}</a>
            </Cell>
          {/snippet}
          {#snippet bulk()}
            <Button variant="outline" size="sm" disabled={busy} onclick={() => changeTo([...pickedRead], "write")}>Change to Publish</Button>
            <Button variant="ghost" size="sm" class="hover:bg-destructive/10 hover:text-destructive" disabled={busy} onclick={() => askRemove(pickedRead)}>Remove access</Button>
          {/snippet}
          {#snippet empty()}
            <CompactEmpty
              icon={UsersIcon}
              text={d.public ? "Anyone can download now. People in this list keep access if the repository becomes private." : "Add the people who need the files."}
            />
          {/snippet}
        </DataTable>
      </AccessPart>
      <Separator />
      <FromWhere id="{id}-read" repo={repo.name} verb="download" lists={lists.data} listsError={lists.error} saved={draft.saved.read} bind:rules={draft.value.read} />
    </div>
  </FormSection>

  <FormSection title="Publish" description={a.publish.locked_by ? "Nobody can publish until the copy from Nexus ends. The access below applies after that." : undefined}>
    {#snippet actions()}
      {#if a.publish.locked_by}<Button variant="outline" size="sm" href={href("/settings/nexus")}>Open Nexus migration</Button>{/if}
    {/snippet}
    <div class="flex flex-col gap-6">
      <AccessPart title="People who can publish" help="People in this list publish from a computer or the browser. {adminsHelp(a.publish.admins, 'publish')}">
        {#snippet actions()}
          <Button variant="outline" size="sm" onclick={() => openAdd("write")}><PlusIcon />Add people</Button>
        {/snippet}
        <DataTable
          source={publishers}
          caption="People who can publish to {repo.name}"
          key={(p) => p.username}
          columns={[{ label: "Person", grow: true }]}
          bind:picked={pickedWrite}
          pickLabel={(p) => p.username}
        >
          {#snippet row(p)}
            {@const u = userOf(p.username)}
            <Cell grow strong class="max-sm:min-w-20">
              <span class="flex min-w-0 items-center gap-2">
                <a class="truncate underline-offset-4 hover:underline" href={personHref(p.username)}>{p.username}</a>
                {#if u && u.kind !== "local"}
                  <StatusBadge tone="warning" title="Single sign-on accounts hold no publish tokens">Cannot publish: single sign-on</StatusBadge>
                {/if}
              </span>
            </Cell>
          {/snippet}
          {#snippet bulk()}
            <Button variant="outline" size="sm" disabled={busy} onclick={() => changeTo([...pickedWrite], "read")}>Change to Download</Button>
            <Button variant="ghost" size="sm" class="hover:bg-destructive/10 hover:text-destructive" disabled={busy} onclick={() => askRemove(pickedWrite)}>Remove access</Button>
          {/snippet}
          {#snippet empty()}
            <CompactEmpty icon={UploadIcon} text="Add the people who release." />
          {/snippet}
        </DataTable>
      </AccessPart>
      <Separator />
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
      <Separator />
      <FromWhere id="{id}-publish" repo={repo.name} verb="publish" lists={lists.data} listsError={lists.error} saved={draft.saved.publish} bind:rules={draft.value.publish} />
    </div>
  </FormSection>

  {#if d.read.length || d.publish.length}
  <p class="mt-6 text-meta text-muted-foreground">
    The server sees each visitor's own address only when it trusts the proxy in front of it. Check
    <a class="text-link underline-offset-4 hover:underline" href={href("/settings/proxy")}>Network and proxy</a>.
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
    `${removing.length === 1 ? "This person" : "These people"} can no longer download from or publish to ${repo.name}${draft.saved.public ? ", except downloads, because it is public" : ""}`,
    "Their read tokens lose this access too",
  ]}
  onConfirm={removeAccess}
/>

<GrantDialog
  bind:open={addOpen}
  title="Give people access to {repo.name}"
  noun={["person", "people"]}
  items={candidates}
  loading={users.loading && !users.data}
  error={users.error ? `The people did not load: ${users.error}` : ""}
  none="Everyone who is not an administrator has access to {repo.name} already."
  startLevel={addLevel}
  submitLabel={(n) => (n ? `Give ${peopleCount(n)} access` : "Give access")}
  onGive={give}
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
