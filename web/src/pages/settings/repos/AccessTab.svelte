<!--
  Repository settings, Access: two cards, Download and Publish, each one list with one Add.

  - Download: the Public switch (Edit settings) in the header. While public, nothing more. Else
    Who can download: the addresses and ranges as a RangeGrid (each with its label under it, edited
    in place), then a list of the network lists, the people with download (one with Publish shows
    "Publishes" and is removed in the Publish card), and the holders of All repositories as one
    row. Add opens the grant dialog with Custom address and Add my address (Edit settings), the
    network lists (Edit settings) and people (Manage access). Turning Public on while there are
    networks asks first, then removes them.
  - Publish: Who can publish: the people with Publish, the CI pipelines (CI pipelines), then the
    holders of All repositories as one row that opens to their names. Add opens the grant dialog
    with people and CI pipelines. When nobody can publish (a group or a proxy, or an operation such
    as a Nexus copy), the card says why, and offers no CI pipeline.

  Once the two cards hold more than ten rows, Find who has access over them asks the server for the
  people, CI pipelines, network lists and addresses whose name (a list's title, an address's label)
  holds the text (GET /api/repos/{name}/access?q=, 250 ms after the last key), and both cards show
  only those; the holders of All repositories show by name. A card that finds nothing says so in
  one line. The changes keep working on the whole access: the switch, Add and every row's action.

  Every change applies at once: the switch, an added or removed row, a label. The networks change
  through one PUT at a time, each on the networks the last one left. A person's name opens their
  permissions; removing a person or a CI pipeline asks first. Add lists the people who have access
  too, with what they hold: picking one changes their permissions.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { toast } from "svelte-sonner";
  import { api, errorMessage, type AccessPerson, type AccessPipeline, type IPRule, type Page, type Repo, type RepoAccess, type Trust } from "$lib/api";
  import { Resource, Searched, type TableSource } from "$lib/load.svelte";
  import { auth } from "$lib/auth.svelte";
  import { repoPermissions, repoPhrase } from "$lib/permissions";
  import { href } from "$lib/router.svelte";
  import { count as plural, someNames } from "$lib/words";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import * as InputGroup from "$lib/components/ui/input-group";
  import { Switch } from "$lib/components/ui/switch";
  import { Label } from "$lib/components/ui/label";
  import Section from "$lib/components/Section.svelte";
  import Toolbar from "$lib/components/Toolbar.svelte";
  import ItemList from "$lib/components/ItemList.svelte";
  import RangeGrid from "$lib/components/RangeGrid.svelte";
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
  import ShieldCheckIcon from "@lucide/svelte/icons/shield-check";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import XIcon from "@lucide/svelte/icons/x";
  import SearchIcon from "@lucide/svelte/icons/search";
  import SearchXIcon from "@lucide/svelte/icons/search-x";

  let { repo, onChanged }: { repo: Repo; onChanged: () => void } = $props();

  const id = $props.id();
  // What the person can see and change here.
  const canAccess = $derived(auth.canRepo(repo.name, "repo.access"));
  const canSettings = $derived(auth.canRepo(repo.name, "repo.settings"));
  const canPipelines = $derived(auth.canRepo(repo.name, "ci.pipelines"));
  // The permissions the person can give and remove here.
  const held = $derived(auth.allRepos() ? repoPermissions().map((p) => p.name) : repo.permissions ?? []);

  // With the holders of All repositories, so the tab names everyone who has access. The changes
  // work on this whole access; a search only chooses what the cards show.
  const access = new Resource(() => api.repos.access(repo.name, { admins: true }));
  /** One report as a source of a Searched: its one item, the report. */
  const one = (r: Resource<RepoAccess>): TableSource<RepoAccess> => ({
    get items() {
      return r.data ? [r.data] : [];
    },
    get loading() {
      return r.loading && !r.data;
    },
    get error() {
      return r.error;
    },
    reload: r.reload,
  });
  // What the cards show: the whole access, or what the server finds for the typed text.
  const found = new Searched<RepoAccess>((q) => one(q ? new Resource(() => api.repos.access(repo.name, { admins: true, q })) : access));
  const shown = $derived(found.items[0] ?? access.data);
  /** Loads the access again, and the search on screen with it. */
  const reload = () => Promise.all([access.reload(), found.query ? found.reload() : undefined]);
  // CI pipelines answer 403 to a person who holds CI pipelines nowhere.
  const trusts = new Resource<Page<Trust>>(() =>
    untrack(() => canPipelines) ? api.publish.trusts.list({ limit: 200 }) : Promise.resolve({ items: [], next: null }),
  );

  const publicNow = $derived(access.data?.download.public ?? false);
  const nets = $derived(access.data ? rulesOf(access.data) : []);
  const blocked = $derived(access.data?.publish.blocked ?? null);
  const ciOnRepo = $derived((access.data?.publish.ci ?? []).map((c) => "ci:" + c.id));

  // --- who ---
  const grantsOf = (a: RepoAccess | undefined) => (a?.people ?? []).filter((p) => p.via === "permission");
  const admins = $derived((access.data?.download.people ?? []).filter((p) => p.via === "all_repos"));
  // What the cards show: of the whole access, or of the search.
  const shownAdmins = $derived((shown?.download.people ?? []).filter((p) => p.via === "all_repos"));
  const downloaders = $derived((shown?.download.people ?? []).filter((p) => p.via === "permission"));
  const grants = $derived(grantsOf(shown));
  const shownNets = $derived(shown ? rulesOf(shown) : []);
  // The search shows once the cards hold more than ten rows, and stays while a search runs.
  const searchable = $derived(
    (access.data ? access.data.people.length + access.data.publish.ci.length + nets.length > 10 : false) || !!found.text.trim() || !!found.query,
  );
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
  const adminRow = (p: AccessPerson): Row => ({ key: "admin:" + p.username, title: p.username, kind: "All repositories" });
  // The holders of All repositories: one row, or by name those a search finds.
  const adminRows = (): Row[] => (found.query ? shownAdmins.map(adminRow) : admins.length ? [adminsRow()] : []);

  const listTitle = (name: string) =>
    (shown?.download.networks.lists.find((l) => l.name === name) ?? access.data?.download.networks.lists.find((l) => l.name === name))?.title ?? name;
  // The addresses and ranges: a grid of their own over the rows of the lists and the people.
  const addresses = sourceOf<IPRule>(() => shownNets.filter((r) => r.kind === "cidr"));
  const downloadRows = sourceOf<Row>(() => [
    ...shownNets
      .filter((r) => r.kind === "list")
      .map(
        (r): Row => ({
          key: `list:${r.value}`,
          title: listTitle(r.value),
          kind: "network list",
          rule: r,
          link: auth.can("network.manage") ? href(`/settings/networks/list/${encodeURIComponent(r.value)}`) : undefined,
          remove: canSettings ? () => removeRule(r) : undefined,
        }),
      ),
    ...downloaders.map((p) => personRow(p, canPublish(p) ? "Publishes" : "Person", !canPublish(p))),
    ...adminRows(),
  ]);
  const publishRows = sourceOf<Row>(() => [
    ...grants.filter(canPublish).map((p) => personRow(p, "Person", true)),
    ...(blocked
      ? []
      : (shown?.publish.ci ?? []).map(
          (c): Row => ({
            key: "ci:" + c.id,
            title: c.rule,
            kind: "CI pipeline",
            off: c.disabled,
            link: canPipelines ? href(`/settings/ci-pipelines/${c.id}`) : undefined,
            remove: canPipelines ? () => ((ciRemoving = c), (ciRemoveOpen = true)) : undefined,
          }),
        )),
    ...adminRows(),
    ...(adminsOpen && !found.query ? admins.map(adminRow) : []),
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
        await reload();
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
      await reload();
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
    await reload();
    onChanged();
  }

  // --- people ---
  async function afterPeopleChange(names: string[]) {
    await reload();
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

  /** Gives each chosen person their permissions, or changes those of one who had a grant, in one request. */
  async function give(items: GrantItem[]) {
    const people = items.filter((i) => i.kind === "grant");
    if (!people.length) return;
    const groups = bySet(people);
    await api.grants.setGroups(groups.map((g) => ({ users: g.ids, repos: [repo.name], perms: g.perms })));
    toast.success(gaveText(people));
  }
  /** "Gave alice access to maven: Download.", "Changed the permissions of bob on maven: Download, publish.", or both in one sentence. */
  function gaveText(people: GrantItem[]): string {
    const fresh = people.filter((p) => !p.had);
    const changed = people.filter((p) => p.had);
    const who = (xs: GrantItem[]) => (xs.length === 1 ? xs[0].id : peopleCount(xs.length));
    const set = (xs: GrantItem[]) => {
      const sets = bySet(xs);
      return sets.length === 1 ? `: ${repoPhrase(sets[0].perms)}` : "";
    };
    if (!changed.length) return `Gave ${who(fresh)} access to ${repo.name}${set(fresh)}.`;
    if (!fresh.length) return `Changed the permissions of ${who(changed)} on ${repo.name}${set(changed)}.`;
    return `Gave ${who(fresh)} access to ${repo.name} and changed the permissions of ${who(changed)}.`;
  }

  // --- Add: the grant dialog ---
  // Search lists the people who have access too: a grant with what it holds (picking it changes
  // it), and the holders of All repositories, who cannot be picked. From the whole access.
  const holding = $derived(new Map(grantsOf(access.data).map((p) => [p.username, p.perms])));
  const adminNames = $derived(new Set(admins.map((p) => p.username)));
  async function findPeople(q: string, after?: string): Promise<Page<GrantChoice>> {
    if (!canAccess) return { items: [], next: null };
    const page = await api.people.search(q, { after, limit: 20 });
    return {
      next: page.next,
      items: page.items.map((p) => ({
        id: p.username,
        label: p.username,
        has: holding.get(p.username),
        fixed: adminNames.has(p.username) ? "All repositories" : undefined,
      })),
    };
  }
  let downloadOpen = $state(false);
  async function findForDownload(q: string, after?: string): Promise<Page<GrantChoice>> {
    const head: GrantChoice[] = [];
    if (!after && canSettings) {
      head.push({ id: "address", label: "Custom address", kind: "address" });
      // The network lists whose name or title holds q, found by the server.
      for (const l of await api.ipLists.list(q))
        head.push({ id: "list:" + l.name, label: l.title, detail: "network list", kind: "plain" });
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
      // The first CI pipelines whose name holds q, in the server's order (names that start with q first).
      const { items } = await api.publish.trusts.list({ limit: 20 }, q || undefined);
      head.push(...items.map(pipelineOf).map((p): GrantChoice => ({ id: "ci:" + p.id, label: p.rule, detail: pipelineFacts(p), kind: "plain" })));
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
    await Promise.all([reload(), trusts.reload()]);
  }

  const addLabel = (n: number) => (n ? `Add ${plural(n, "item", "items")}` : "Add");
  const downloadFind = $derived(canAccess && canSettings ? "Find a person or a network list" : canSettings ? "Find a network list" : "Find a person");
  const publishFind = $derived(canAccess && canPipelines && !blocked ? "Find a person or a CI pipeline" : canAccess ? "Find a person" : "Find a CI pipeline");
</script>

<!-- One list of a card: each row an icon of its kind (an avatar for a person), its name, its kind
     muted at the end, and × where the viewer may remove it. -->
{#snippet who(rows: TableSource<Row>, caption: string, none: string)}
  <ItemList source={rows} {caption} key={(r) => r.key} href={(r) => r.link}>
    {#snippet leading(r)}
      {#if r.rule}<ListIcon class="size-5" />
      {:else if r.key.startsWith("ci:")}<WorkflowIcon class="size-5" />
      {:else if r.admins}<ShieldCheckIcon class="size-5" />
      {:else}<UserAvatar name={r.title} size="sm" />{/if}
    {/snippet}
    {#snippet title(r)}
      {#if r.person && canAccess}
        <button type="button" class="rounded-sm outline-none hover:underline focus-visible:ring-[3px] focus-visible:ring-ring/50" aria-label="Permissions of {r.title}" onclick={() => r.person && edit(r.person)}>{r.title}</button>
      {:else}
        {r.title}
      {/if}
    {/snippet}
    {#snippet badges(r)}{#if r.off}<StatusBadge>Off</StatusBadge>{/if}{/snippet}
    {#snippet trailing(r)}
      <span class="text-meta text-muted-foreground">{r.kind}</span>
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
    {#snippet empty()}
      {#if found.query}<CompactEmpty icon={SearchXIcon} text="{none} holds “{found.query}”." />
      {:else}<CompactEmpty icon={UsersIcon} text="Nobody yet." />{/if}
    {/snippet}
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
  {#if searchable}
    <Toolbar>
      <InputGroup.Root class="sm:w-72">
        <InputGroup.Addon><SearchIcon /></InputGroup.Addon>
        <InputGroup.Input
          bind:value={found.text}
          type="search"
          placeholder="Find who has access"
          aria-label="Find who has access to {repo.name}: a person, a CI pipeline, a network list or an address"
          onkeydown={(e) => e.key === "Enter" && found.flush()}
        />
      </InputGroup.Root>
    </Toolbar>
  {/if}
  {#if found.query && found.error}
    <!-- The cards cannot show what the search did not answer. -->
    <ErrorState error={found.error} title="The search did not load" onRetry={found.reload} />
  {:else}
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
          {#if addresses.items.length}
            <!-- The rows under the grid reach into the card's padding; this keeps their fill off the cells. -->
            <div class={downloadRows.items.length ? "pb-2" : undefined}>
              <RangeGrid
                source={addresses}
                caption="Addresses that can download from {repo.name}"
                text={(r) => r.value}
                onremove={canSettings ? removeRule : undefined}
                wide={canSettings || addresses.items.some((r) => r.note)}
              >
                {#snippet note(rule)}
                  {#if canSettings}
                    <Input
                      class="-mx-2 h-7 w-[calc(100%+1rem)] border-transparent bg-transparent px-2 text-meta shadow-none hover:border-input focus-visible:border-input"
                      placeholder="Add a label"
                      aria-label="Label of {rule.value}"
                      autocomplete="off"
                      maxlength={200}
                      value={rule.note}
                      onchange={(e) => relabel(rule, e.currentTarget)}
                    />
                  {:else if rule.note}
                    <span class="line-clamp-2 break-words" title={rule.note}>{rule.note}</span>
                  {/if}
                {/snippet}
              </RangeGrid>
            </div>
          {/if}
          {#if downloadRows.items.length || !addresses.items.length}{@render who(downloadRows, `Who can download from ${repo.name}`, "No person, network list or address")}{/if}
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
            {@render who(publishRows, `Who can publish to ${repo.name}`, canPipelines && !blocked ? "No person or CI pipeline" : "No person")}
          </AccessPart>
        {/if}
      </div>
    </Section>
  {/if}
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
  exclude={nets.filter((r) => r.kind === "list").map((r) => "list:" + r.value)}
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
  exclude={ciOnRepo}
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
