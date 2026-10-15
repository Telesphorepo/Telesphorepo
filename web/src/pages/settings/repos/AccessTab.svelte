<!--
  Repository settings, Access (docs/design/information-architecture.md, "The repo Access tab"):
  Download (Public, people who can download and not publish, from where) and Publish (people who can publish, CI
  pipelines, from where). Every edit is in place and goes into one draft, and the save bar saves
  it after a confirm that lists each change. Administrators can always download and publish; the
  tables list the people and pipelines that were given access.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { toast } from "svelte-sonner";
  import { api, type AccessPipeline, type Repo, type RepoAccess, type User } from "$lib/api";
  import { Resource, type TableSource } from "$lib/load.svelte";
  import { Draft } from "$lib/unsaved.svelte";
  import { href, repoSettingsPath } from "$lib/router.svelte";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Separator } from "$lib/components/ui/separator";
  import FormSection from "$lib/components/FormSection.svelte";
  import FormGrid from "$lib/components/FormGrid.svelte";
  import FormField from "$lib/components/FormField.svelte";
  import SwitchRow from "$lib/components/SwitchRow.svelte";
  import DataTable from "$lib/components/DataTable.svelte";
  import Cell from "$lib/components/Cell.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import SaveBar from "$lib/components/SaveBar.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import ShortFormDialog from "$lib/components/ShortFormDialog.svelte";
  import AccessPart from "./AccessPart.svelte";
  import FromWhere from "./FromWhere.svelte";
  import ChoiceDialog from "./ChoiceDialog.svelte";
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

  const draft = new Draft<AccessDraft>({ public: false, grants: {}, ci: [], read: [], publish: [] });
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
  const everyone = $derived(Object.entries(d.grants).map(([username, level]) => ({ username, level })).sort((a, b) => a.username.localeCompare(b.username)));
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
  const setLevel = (name: string, level: Level | undefined) => {
    const g = { ...draft.value.grants };
    if (level) g[name] = level;
    else delete g[name];
    draft.value.grants = g;
  };

  // --- add a person ---
  let adding = $state<null | "download" | "publish">(null);
  let addOpen = $state(false);
  let addName = $state("");
  function openAdd(scope: "download" | "publish") {
    adding = scope;
    addName = "";
    addOpen = true;
  }
  const addProblem = $derived.by(() => {
    const name = addName.trim();
    if (!name) return "";
    const u = userOf(name);
    if (!u && users.data && users.data.next === null) return `No person named ${name} exists. Add them in People first.`;
    if (u?.is_admin) return `${name} is an administrator and can always download and publish.`;
    if (u?.disabled) return `${name} is turned off. Turn the account on in People first.`;
    const level: Level = adding === "publish" ? "write" : "read";
    if (d.grants[name] === "write") return level === "read" ? `${name} can publish, so ${name} can download already.` : `${name} is in the list already.`;
    if (level === "read" && d.grants[name]) return `${name} is in the list already.`;
    if (level === "write" && u && u.kind !== "local") return `${name} signs in with single sign-on, and such accounts cannot publish. They can download.`;
    return "";
  });
  const candidates = $derived((users.data?.items ?? []).filter((u) => !u.is_admin && !u.disabled && !d.grants[u.username]));

  // --- save ---
  let confirmOpen = $state(false);
  const changes = $derived(ready ? changesOf(repo.name, draft.saved, d, pipelines, lists.data) : []);
  // A save that takes access away from someone is red.
  const narrows = $derived.by(() => {
    const s = draft.saved;
    if (s.public && !d.public) return true;
    if (Object.entries(s.grants).some(([u, l]) => !d.grants[u] || (l === "write" && d.grants[u] === "read"))) return true;
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
      if (v.public !== saved.public) await api.repos.update(repo.name, { anonymous_read: v.public });
      for (const u of new Set([...Object.keys(saved.grants), ...Object.keys(v.grants)])) {
        if (saved.grants[u] === v.grants[u]) continue;
        if (v.grants[u]) await api.repos.permissions.set(repo.name, u, v.grants[u]);
        else await api.repos.permissions.delete(repo.name, u);
      }
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
          <Button variant="outline" size="sm" onclick={() => openAdd("download")}><PlusIcon />Add person</Button>
        {/snippet}
        <DataTable
          source={downloaders}
          caption="People who can download from {repo.name}"
          key={(p) => p.username}
          columns={[{ label: "Person", grow: true }, { label: "", srLabel: "Actions" }]}
        >
          {#snippet row(p)}
            <Cell grow strong class="max-sm:min-w-20">
              <span class="flex min-w-0 items-center gap-2">
                {#if !draft.saved.grants[p.username]}<span aria-hidden="true" class="size-1.5 shrink-0 rounded-full bg-warning" title="Not saved yet"></span><span class="sr-only">Not saved:</span>{/if}
                <a class="truncate underline-offset-4 hover:underline" href={personHref(p.username)}>{p.username}</a>
              </span>
            </Cell>
            <Cell actions>
              <Button
                variant="ghost"
                size="icon-sm"
                class="hover:text-destructive"
                title="Remove"
                aria-label="Remove the access of {p.username}"
                onclick={() => setLevel(p.username, undefined)}><XIcon /></Button
              >
            </Cell>
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
          <Button variant="outline" size="sm" onclick={() => openAdd("publish")}><PlusIcon />Add person</Button>
        {/snippet}
        <DataTable
          source={publishers}
          caption="People who can publish to {repo.name}"
          key={(p) => p.username}
          columns={[{ label: "Person", grow: true }, { label: "", srLabel: "Actions" }]}
        >
          {#snippet row(p)}
            {@const u = userOf(p.username)}
            <Cell grow strong class="max-sm:min-w-20">
              <span class="flex min-w-0 items-center gap-2">
                {#if draft.saved.grants[p.username] !== "write"}<span aria-hidden="true" class="size-1.5 shrink-0 rounded-full bg-warning" title="Not saved yet"></span><span class="sr-only">Not saved:</span>{/if}
                <a class="truncate underline-offset-4 hover:underline" href={personHref(p.username)}>{p.username}</a>
                {#if u && u.kind !== "local"}
                  <StatusBadge tone="warning" title="Single sign-on accounts hold no publish tokens">Cannot publish: single sign-on</StatusBadge>
                {/if}
              </span>
            </Cell>
            <Cell actions>
              <Button
                variant="ghost"
                size="icon-sm"
                class="hover:text-destructive"
                title="Stop publishing (keeps download)"
                aria-label="Stop {p.username} from publishing"
                onclick={() => setLevel(p.username, "read")}><XIcon /></Button
              >
            </Cell>
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

<ShortFormDialog
  bind:open={addOpen}
  title={adding === "publish" ? `Let a person publish to ${repo.name}` : `Give a person access to ${repo.name}`}
  submitLabel="Add person"
  canSubmit={!!addName.trim() && !addProblem}
  onSubmit={() => setLevel(addName.trim(), adding === "publish" ? "write" : "read")}
>
  <FormField label="Username" for="{id}-add-user" error={addProblem || undefined}>
    <Input id="{id}-add-user" list="{id}-people" autocomplete="off" spellcheck={false} bind:value={addName} />
  </FormField>
  <datalist id="{id}-people">
    {#each candidates as u (u.id)}<option value={u.username}></option>{/each}
  </datalist>
  <p class="text-meta text-muted-foreground">Save the page to keep the change.</p>
</ShortFormDialog>

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
