<!--
  Repository settings, Access: two cards, Download and Publish, each one list with one Add.

  - Download: the Public switch (Edit settings) in the header. While public, nothing more. Else
    Who can download: the addresses and ranges (a label edited in place), the network lists, the people
    with download (one with Publish shows "Publishes" and is removed in the Publish card), and the
    holders of All repositories as one row. Add opens the grant dialog with Custom address and Add
    my address (Edit settings), the network lists (Edit settings) and people (Manage access). Turning
    Public on while there are networks asks first, then removes them.
  - Publish: Who can publish: the people with Publish, the CI pipelines (CI pipelines), then the
    holders of All repositories as one row that opens to their names. Add opens the grant dialog
    with people and CI pipelines. When nobody can publish (a group or a proxy, or an operation such
    as a Nexus copy), the card says why, and offers no CI pipeline.

  Every change applies at once: the switch, an added or removed row, a label. The networks change
  through one PUT at a time, each on the networks the last one left. A person's name opens their
  permissions; removing a person or a CI pipeline asks first.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { toast } from "svelte-sonner";
  import { api, errorMessage, type AccessPerson, type AccessPipeline, type IPList, type IPRule, type Page, type Repo, type Trust } from "$lib/api";
  import { Resource, type TableSource } from "$lib/load.svelte";
  import { auth } from "$lib/auth.svelte";
  import { repoPermissions, repoPhrase } from "$lib/permissions";
  import { href } from "$lib/router.svelte";
  import { count as plural, someNames } from "$lib/words";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Switch } from "$lib/components/ui/switch";
  import { Label } from "$lib/components/ui/label";
  import Section from "$lib/components/Section.svelte";
  import ItemList from "$lib/components/ItemList.svelte";
  import UserAvatar from "$lib/components/UserAvatar.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import AccessPart from "./AccessPart.svelte";
  import GrantDialog, { bySet, type GrantChoice, type GrantItem } from "./GrantDialog.svelte";
  import { pipelineFacts, pipelineOf, rulesOf, sameRule } from "./access";
  import UsersIcon from "@lucide/svelte/icons/users-round";
  import BanIcon from "@lucide/svelte/icons/ban";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import ListIcon from "@lucide/svelte/icons/list";
  import NetworkIcon from "@lucide/svelte/icons/network";
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

  // With the holders of All repositories, so the tab names everyone who has access.
  const access = new Resource(() => api.repos.access(repo.name, { admins: true }));
  // CI pipelines answer 403 to a person who holds CI pipelines nowhere.
  const trusts = new Resource<Page<Trust>>(() =>
    untrack(() => canPipelines) ? api.publish.trusts.list({ limit: 200 }) : Promise.resolve({ items: [], next: null }),
  );

  const publicNow = $derived(access.data?.download.public ?? false);
  const nets = $derived(access.data ? rulesOf(access.data) : []);
  const blocked = $derived(access.data?.publish.blocked ?? null);
  const ciOnRepo = $derived((access.data?.publish.ci ?? []).map((c) => "ci:" + c.id));

  // --- who ---
  const admins = $derived((access.data?.download.people ?? []).filter((p) => p.via === "all_repos"));
  const downloaders = $derived((access.data?.download.people ?? []).filter((p) => p.via === "permission"));
  const grants = $derived((access.data?.people ?? []).filter((p) => p.via === "permission"));
  const canPublish = (p: AccessPerson) => p.perms.includes("repo.publish");
  const peopleCount = (n: number) => plural(n, "person", "people");
  const adminCount = (n: number) => plural(n, "administrator", "administrators");
  let adminsOpen = $state(false);

  /** One row of a list: `kind` is the muted word at its end; without `remove` it has no ×. */
  type Row = {
    key: string;
    title: string;
    kind: string;
    rule?: IPRule;
    person?: AccessPerson;
    link?: string;
    off?: boolean;
    admins?: true;
    remove?: () => void;
  };
  const sourceOf = <T,>(items: () => T[]): TableSource<T> => ({
    get items() {
      return items();
    },
    loading: false,
    error: "",
  });
  const personRow = (p: AccessPerson, kind: string, removable: boolean): Row => ({
    key: "person:" + p.username,
    title: p.username,
    kind,
    person: p,
    remove: removable && canAccess ? () => askRemove([p.username]) : undefined,
  });
  const adminsRow = (): Row => ({ key: "admins", title: adminCount(admins.length), kind: "All repositories", admins: true });

  const listTitle = (name: string) => access.data?.download.networks.lists.find((l) => l.name === name)?.title ?? name;
  const downloadRows = sourceOf<Row>(() => [
    ...nets.map(
      (r): Row => ({
        key: `${r.kind}:${r.value}`,
        title: r.kind === "list" ? listTitle(r.value) : r.value,
        kind: r.kind === "list" ? "network list" : "",
        rule: r,
        link: r.kind === "list" && auth.can("network.manage") ? href(`/settings/networks/list/${encodeURIComponent(r.value)}`) : undefined,
        remove: canSettings ? () => removeRule(r) : undefined,
      }),
    ),
    ...downloaders.map((p) => personRow(p, canPublish(p) ? "Publishes" : "Person", !canPublish(p))),
    ...(admins.length ? [adminsRow()] : []),
  ]);
  const publishRows = sourceOf<Row>(() => [
    ...grants.filter(canPublish).map((p) => personRow(p, "Person", true)),
    ...(blocked
      ? []
      : (access.data?.publish.ci ?? []).map(
          (c): Row => ({
            key: "ci:" + c.id,
            title: c.rule,
            kind: "CI pipeline",
            off: c.disabled,
            link: canPipelines ? href(`/settings/ci-pipelines/${c.id}`) : undefined,
            remove: canPipelines ? () => ((ciRemoving = c), (ciRemoveOpen = true)) : undefined,
          }),
        )),
    ...(admins.length ? [adminsRow()] : []),
    ...(adminsOpen ? admins.map((p): Row => ({ key: "admin:" + p.username, title: p.username, kind: "All repositories" })) : []),
  ]);

  // --- the allowed networks: one PUT at a time ---
  let queue: Promise<unknown> = Promise.resolve();
  /** Puts what `next` makes of the networks the last change left; null changes nothing. */
  function putNets(next: (cur: IPRule[]) => IPRule[] | null): Promise<void> {
    const run = queue.then(async () => {
      const rules = next(nets);
      if (!rules) return;
      try {
        await api.repos.ipRules.set(repo.name, { read: rules });
      } finally {
        await access.reload();
      }
    });
    queue = run.catch(() => {});
    return run;
  }
  function removeRule(r: IPRule) {
    putNets((cur) => cur.filter((x) => !sameRule(x, r))).catch((e) =>
      toast.error(`${r.kind === "list" ? listTitle(r.value) : r.value} was not removed: ${errorMessage(e)}`),
    );
  }
  function relabel(r: IPRule, input: HTMLInputElement) {
    const note = input.value.trim();
    if (note === r.note) return;
    putNets((cur) => cur.map((x) => (sameRule(x, r) ? { ...x, note } : x))).catch((e) => {
      input.value = r.note;
      toast.error(`The label of ${r.value} did not save: ${errorMessage(e)}`);
    });
  }

  // --- Public ---
  let switching = $state<boolean | null>(null);
  let publicOpen = $state(false);
  const cidrCount = $derived(nets.filter((r) => r.kind === "cidr").length);
  const listCount = $derived(nets.filter((r) => r.kind === "list").length);
  const removedNets = $derived(
    [cidrCount ? plural(cidrCount, "address", "addresses") : "", listCount ? plural(listCount, "network list", "network lists") : ""].filter(Boolean).join(" and "),
  );
  async function setPublic(v: boolean) {
    if (v && nets.length) {
      publicOpen = true;
      return;
    }
    switching = v;
    try {
      await api.repos.setPublic([repo.name], v);
      await access.reload();
      onChanged();
    } catch (e) {
      toast.error(`${repo.name} did not turn ${v ? "public" : "private"}: ${errorMessage(e)}`);
    } finally {
      switching = null;
    }
  }
  async function makePublic() {
    await putNets(() => []);
    await api.repos.setPublic([repo.name], true);
    await access.reload();
    onChanged();
  }

  // --- people ---
  async function afterPeopleChange(names: string[]) {
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

  /** Gives each chosen person their permissions, in one request. */
  async function give(items: GrantItem[]) {
    const people = items.filter((i) => i.kind === "grant");
    if (!people.length) return;
    const groups = bySet(people);
    await api.grants.setGroups(groups.map((g) => ({ users: g.ids, repos: [repo.name], perms: g.perms })));
    toast.success(`Gave ${peopleCount(people.length)} access to ${repo.name}${groups.length === 1 ? `: ${repoPhrase(groups[0].perms)}` : ""}.`);
  }

  // --- Add: the grant dialog ---
  // Search leaves out the people listed here: a grant or All repositories.
  const listedPeople = $derived([...(access.data?.people ?? []), ...admins].map((p) => p.username));
  async function findPeople(q: string, after?: string): Promise<Page<GrantChoice>> {
    if (!canAccess) return { items: [], next: null };
    const page = await api.people.search(q, { after, limit: 20 });
    return { next: page.next, items: page.items.map((p) => ({ id: p.username, label: p.username })) };
  }
  const starts = (q: string, ...names: string[]) => !q || names.some((n) => n.toLowerCase().startsWith(q));

  let downloadOpen = $state(false);
  let ipLists: Promise<IPList[]> | null = null;
  const allLists = () =>
    (ipLists ??= api.ipLists.list().catch((e) => {
      ipLists = null;
      throw e;
    }));
  async function findForDownload(q: string, after?: string): Promise<Page<GrantChoice>> {
    const head: GrantChoice[] = [];
    if (!after && canSettings) {
      head.push({ id: "address", label: "Custom address", kind: "address" });
      for (const l of await allLists())
        if (starts(q, l.title, l.name)) head.push({ id: "list:" + l.name, label: l.title, detail: "network list", kind: "plain" });
    }
    const people = await findPeople(q, after);
    return { next: people.next, items: [...head, ...people.items] };
  }
  async function myRanges(): Promise<string[]> {
    const me = await api.network.whoami();
    const others = await Promise.all(me.probes.map((u) => api.network.whoamiAt(u)));
    return [...new Set([me, ...others].map((w) => w?.cidr ?? "").filter(Boolean))];
  }
  async function addDownload(items: GrantItem[]) {
    // The server masks each address, so a duplicate shows here, not as a refused PUT.
    const typed = await Promise.all(
      items
        .filter((i) => i.kind === "address")
        .map(async (i): Promise<IPRule> => {
          const p = await api.ipRanges.parse(i.value ?? "");
          if (!p.ranges[0]) throw new Error(p.invalid[0]?.reason ?? `${i.value} is not an address or a range.`);
          return { kind: "cidr", value: p.ranges[0], note: i.note ?? "" };
        }),
    );
    const rules: IPRule[] = [
      ...typed,
      ...items.filter((i) => i.kind === "plain" && i.id.startsWith("list:")).map((i): IPRule => ({ kind: "list", value: i.id.slice(5), note: "" })),
    ];
    if (rules.length)
      await putNets((cur) => {
        const fresh: IPRule[] = [];
        for (const r of rules) if (![...cur, ...fresh].some((x) => sameRule(x, r))) fresh.push(r);
        return fresh.length ? [...cur, ...fresh] : null;
      });
    await give(items);
    await afterPeopleChange(items.filter((i) => i.kind === "grant").map((i) => i.id));
  }

  let publishOpen = $state(false);
  async function findForPublish(q: string, after?: string): Promise<Page<GrantChoice>> {
    const head: GrantChoice[] = [];
    if (!after && canPipelines && !blocked) {
      const items = trusts.data?.items ?? (await api.publish.trusts.list({ limit: 200 })).items;
      head.push(
        ...items
          .map(pipelineOf)
          .filter((p) => starts(q, p.rule))
          .sort((a, b) => a.rule.localeCompare(b.rule))
          .map((p): GrantChoice => ({ id: "ci:" + p.id, label: p.rule, detail: pipelineFacts(p), kind: "plain" })),
      );
    }
    const people = await findPeople(q, after);
    return { next: people.next, items: [...head, ...people.items] };
  }
  async function addPublish(items: GrantItem[]) {
    for (const i of items.filter((i) => i.kind === "plain" && i.id.startsWith("ci:"))) {
      const tid = Number(i.id.slice(3));
      const t = await api.publish.trusts.get(tid);
      await api.publish.trusts.update(tid, { repos: [...new Set([...t.repos, repo.name])] });
    }
    await give(items);
    await trusts.reload();
    await afterPeopleChange(items.filter((i) => i.kind === "grant").map((i) => i.id));
  }

  // --- remove a CI pipeline ---
  let ciRemoving = $state<AccessPipeline | null>(null);
  let ciRemoveOpen = $state(false);
  async function removeCI() {
    if (!ciRemoving) return;
    const t = await api.publish.trusts.get(ciRemoving.id);
    await api.publish.trusts.update(ciRemoving.id, { repos: t.repos.filter((r) => r !== repo.name) });
    await Promise.all([access.reload(), trusts.reload()]);
  }

  const addLabel = (n: number) => (n ? `Add ${plural(n, "item", "items")}` : "Add");
  const downloadFind = $derived(canAccess && canSettings ? "Find a person or a network list" : canSettings ? "Find a network list" : "Find a person");
  const publishFind = $derived(canAccess && canPipelines && !blocked ? "Find a person or a CI pipeline" : canAccess ? "Find a person" : "Find a CI pipeline");
</script>

<!-- One list of a card: each row its name, its kind muted at the end, and × where the viewer may remove it. -->
{#snippet who(rows: TableSource<Row>, caption: string)}
  <ItemList source={rows} {caption} key={(r) => r.key} href={(r) => r.link}>
    {#snippet title(r)}
      {#if r.rule?.kind === "cidr"}
        <span class="font-mono">{r.title}</span>
      {:else if r.person && canAccess}
        <button type="button" class="rounded-sm outline-none hover:underline focus-visible:ring-[3px] focus-visible:ring-ring/50" aria-label="Permissions of {r.title}" onclick={() => r.person && edit(r.person)}>{r.title}</button>
      {:else}
        {r.title}
      {/if}
    {/snippet}
    {#snippet badges(r)}{#if r.off}<StatusBadge>Off</StatusBadge>{/if}{/snippet}
    {#snippet trailing(r)}
      {#if r.rule?.kind === "cidr" && canSettings}
        {@const rule = r.rule}
        <Input
          class="h-8 w-28 border-transparent bg-transparent text-right text-meta shadow-none hover:border-input focus-visible:border-input sm:w-40"
          placeholder="Add a label"
          aria-label="Label of {rule.value}"
          autocomplete="off"
          value={rule.note}
          onchange={(e) => relabel(rule, e.currentTarget)}
        />
      {:else if r.rule?.kind === "cidr"}
        <span class="text-meta text-muted-foreground">{r.rule.note}</span>
      {:else}
        <span class="text-meta text-muted-foreground">{r.kind}</span>
      {/if}
      {#if r.admins && rows === publishRows}
        <Button variant="ghost" size="sm" aria-expanded={adminsOpen} onclick={() => (adminsOpen = !adminsOpen)}>
          {adminsOpen ? "Hide" : "Show"}<ChevronDownIcon class="transition-transform {adminsOpen ? 'rotate-180' : ''}" />
        </Button>
      {/if}
      {#if r.remove}
        {@const remove = r.remove}
        <Button variant="ghost" size="icon-sm" class="hover:bg-destructive/10 hover:text-destructive" aria-label="Remove {r.title}" title="Remove" onclick={remove}><XIcon /></Button>
      {/if}
    {/snippet}
    {#snippet empty()}<CompactEmpty icon={UsersIcon} text="Nobody yet." />{/snippet}
  </ItemList>
{/snippet}

{#snippet mark(c: GrantChoice)}
  {#if c.kind === "address"}<NetworkIcon class="size-5 text-muted-foreground" />
  {:else if c.id.startsWith("list:")}<ListIcon class="size-5 text-muted-foreground" />
  {:else if c.id.startsWith("ci:")}<WorkflowIcon class="size-5 text-muted-foreground" />
  {:else}<UserAvatar name={c.id} size="sm" />{/if}
{/snippet}

{#if access.error && !access.data}
  <ErrorState error={access.error} title="The access of {repo.name} did not load" onRetry={access.reload} />
{:else if !access.data}
  <Loading rows={8} />
{:else}
  {@const a = access.data}
  <!-- While public the card is its header alone. -->
  <Section title="Download" class="[&>[data-slot=card-content]:empty]:hidden">
    {#snippet actions()}
      {#if canSettings}
        <div class="flex h-8 items-center gap-3">
          <Label for="{id}-public" class="text-body">Public</Label>
          <Switch id="{id}-public" disabled={switching !== null} bind:checked={() => switching ?? publicNow, (v) => setPublic(v)} />
        </div>
      {:else if publicNow}
        <StatusBadge>Public</StatusBadge>
      {/if}
    {/snippet}
    {#if !publicNow}
      <AccessPart title="Who can download">
        {#snippet actions()}
          {#if canAccess || canSettings}<Button variant="outline" size="sm" onclick={() => (downloadOpen = true)}><PlusIcon />Add</Button>{/if}
        {/snippet}
        {@render who(downloadRows, `Who can download from ${repo.name}`)}
        {#if a.download.notes.length}<p class="text-meta text-muted-foreground">{a.download.notes.join(" ")}</p>{/if}
      </AccessPart>
    {/if}
  </Section>

  <Section title="Publish">
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
      {#if !blocked || publishRows.items.length || canAccess}
        <AccessPart title="Who can publish">
          {#snippet actions()}
            {#if canAccess || (canPipelines && !blocked)}<Button variant="outline" size="sm" onclick={() => (publishOpen = true)}><PlusIcon />Add</Button>{/if}
          {/snippet}
          {@render who(publishRows, `Who can publish to ${repo.name}`)}
        </AccessPart>
      {/if}
    </div>
  </Section>
{/if}

<ConfirmDialog
  bind:open={publicOpen}
  title="Make {repo.name} public?"
  confirmLabel="Make public"
  changes={[`Removes ${removedNets}`, `Anyone can download every file of ${repo.name} without signing in`]}
  onConfirm={makePublic}
/>

<ConfirmDialog
  bind:open={removeOpen}
  title="Remove the access of {removing.length === 1 ? removing[0] : peopleCount(removing.length)} to {repo.name}?"
  description={removing.length === 1 ? undefined : `${someNames(removing)}.`}
  confirmLabel="Remove access of {removing.length === 1 ? removing[0] : peopleCount(removing.length)}"
  destructive
  changes={[
    `${removing.length === 1 ? "This person loses" : "These people lose"} every permission on ${repo.name}${publicNow ? ", but can still download, because it is public" : ""}`,
    "Their read tokens lose this access too",
  ]}
  onConfirm={removeAccess}
/>

<ConfirmDialog
  bind:open={ciRemoveOpen}
  title="Remove {ciRemoving?.rule ?? ''} from {repo.name}?"
  confirmLabel="Remove CI pipeline"
  destructive
  changes={[`The CI pipeline can no longer publish to ${repo.name}; its open publish tokens end`]}
  onConfirm={removeCI}
/>

<GrantDialog
  bind:open={downloadOpen}
  title="Add who can download from {repo.name}"
  noun={["item", "items"]}
  find={downloadFind}
  search={findForDownload}
  exclude={[...listedPeople, ...nets.filter((r) => r.kind === "list").map((r) => "list:" + r.value)]}
  {held}
  permissions={canAccess}
  mine={canSettings ? myRanges : undefined}
  leading={mark}
  submitLabel={addLabel}
  onGive={addDownload}
/>

<GrantDialog
  bind:open={publishOpen}
  title="Add who can publish to {repo.name}"
  noun={["item", "items"]}
  find={publishFind}
  search={findForPublish}
  exclude={[...listedPeople, ...ciOnRepo]}
  start={["repo.download", "repo.publish"]}
  {held}
  permissions={canAccess}
  leading={mark}
  submitLabel={addLabel}
  onGive={addPublish}
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
