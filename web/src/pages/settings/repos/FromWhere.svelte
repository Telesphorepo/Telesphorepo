<!--
  "From where" of the Access tab, for downloads or for publishes: one line that says where
  requests may come from ("From any network", "Only from GitHub Actions and 10.0.0.0/8"), and
  for a person with Edit settings, Change, which opens the network lists and the addresses that
  requests must come from (no row: any network) with Add network list and Add addresses, each a
  short form in a dialog. The parent holds the rules in its draft and saves them; a list or an
  address added here shows at once and saves with the page.

    <FromWhere id="read" repo="maven" verb="download" editable lists={ipLists.data} saved={draft.saved.read} bind:rules={draft.value.read} />
-->
<script lang="ts">
  import type { IPList, IPRule } from "$lib/api";
  import type { TableSource } from "$lib/load.svelte";
  import { formatCount } from "$lib/format";
  import { rangeSize } from "$lib/network";
  import { href } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { Button } from "$lib/components/ui/button";
  import ItemList from "$lib/components/ItemList.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import RangePaste from "$lib/components/RangePaste.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import ShortFormDialog from "$lib/components/ShortFormDialog.svelte";
  import AccessPart from "./AccessPart.svelte";
  import ChoiceDialog from "./ChoiceDialog.svelte";
  import { whereFrom } from "./access";
  import GlobeIcon from "@lucide/svelte/icons/globe";
  import ListIcon from "@lucide/svelte/icons/list";
  import NetworkIcon from "@lucide/svelte/icons/network";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import XIcon from "@lucide/svelte/icons/x";

  let {
    id,
    repo,
    verb,
    lists,
    listsError = "",
    saved,
    editable = false,
    rules = $bindable([]),
  }: {
    /** Prefix of the element ids, unique on the page. */
    id: string;
    repo: string;
    /** "download" or "publish". */
    verb: "download" | "publish";
    /** Every network list (GET /api/ip-lists); undefined while it loads. */
    lists: IPList[] | undefined;
    listsError?: string;
    /** The rules as saved, to mark the unsaved rows. */
    saved: IPRule[];
    /** The person can change the rules (Edit settings). */
    editable?: boolean;
    rules?: IPRule[];
  } = $props();

  // The editor opens on Change, and stays open while its rules differ from the saved ones.
  let editing = $state(false);
  const key = (r: IPRule) => `${r.kind}:${r.value}:${r.note}`;
  $effect(() => {
    if (rules.map(key).join() !== saved.map(key).join()) editing = true;
  });

  type Row = { key: string; rule: IPRule; title: string; facts: string; link?: string; missing?: boolean; empty?: boolean };

  const listOfName = (name: string) => lists?.find((l) => l.name === name);
  const rangeCount = (n: number) => `${formatCount(n)} ${n === 1 ? "range" : "ranges"}`;
  function listFacts(l: IPList | undefined): string {
    if (!l) return "Network list";
    return l.kind === "preset" ? `Network list · ${rangeCount(l.count)} · updated every day` : `Network list · ${rangeCount(l.count)}`;
  }

  const rows = $derived<Row[]>(
    rules.map((r) => {
      if (r.kind === "list") {
        const l = listOfName(r.value);
        return {
          key: "list:" + r.value,
          rule: r,
          title: l?.title ?? r.value,
          facts: lists && !l ? "This network list does not exist any more. Remove it." : listFacts(l),
          link: auth.can("network.manage") ? href(`/settings/networks/list/${encodeURIComponent(r.value)}`) : undefined,
          missing: !!lists && !l,
          empty: !!l && l.count === 0,
        };
      }
      return { key: "cidr:" + r.value, rule: r, title: r.value, facts: [r.note, rangeSize(r.value)].filter(Boolean).join(" · ") };
    }),
  );
  const source: TableSource<Row> = {
    get items() {
      return rows;
    },
    loading: false,
    error: "",
  };

  const isSaved = (r: IPRule) => saved.some((s) => s.kind === r.kind && s.value === r.value);
  const addable = $derived(
    (lists ?? [])
      .filter((l) => !rules.some((r) => r.kind === "list" && r.value === l.name))
      .map((l) => ({ id: l.name, label: l.title, detail: [l.description, rangeCount(l.count)].filter(Boolean).join(" · ") })),
  );

  let listOpen = $state(false);
  let pasteOpen = $state(false);
  let found = $state<string[]>([]);

  const addLists = (names: string[]) => (rules = [...rules, ...names.map((value) => ({ kind: "list" as const, value, note: "" }))]);

  function addRanges() {
    const have = new Set(rules.filter((r) => r.kind === "cidr").map((r) => r.value));
    rules = [...rules, ...found.filter((v) => !have.has(v)).map((v) => ({ kind: "cidr" as const, value: v, note: "" }))];
  }

  const remove = (r: IPRule) => (rules = rules.filter((x) => !(x.kind === r.kind && x.value === r.value)));
  const whom = $derived(verb === "download" ? "Downloads, the file list and search" : "Uploads, deletes and new publish tokens");
  const limits = $derived(`${verb === "download" ? "Downloads from" : "Publishes to"} ${repo} must then come from one of the allowed networks.`);
</script>

<AccessPart
  icon={NetworkIcon}
  title={rules.length ? `Only from ${whereFrom(rules, lists)}` : "From any network"}
  help={rules.length ? `${whom} must come from one of these networks, for everyone, administrators too.` : `For ${whom.toLowerCase()}.`}
>
  {#snippet actions()}
    {#if editable && editing}
      <Button variant="outline" size="sm" onclick={() => (listOpen = true)}><PlusIcon />Add network list</Button>
      <Button variant="outline" size="sm" onclick={() => (pasteOpen = true)}><PlusIcon />Add addresses</Button>
    {/if}
    {#if editable}
      <Button variant="ghost" size="sm" aria-expanded={editing} onclick={() => (editing = !editing)}>{editing ? "Done" : "Change"}</Button>
    {/if}
  {/snippet}

  {#if editing}
  <ItemList {source} caption="Networks that can {verb} with {repo}" key={(r) => r.key} href={(r) => r.link}>
    {#snippet leading(r)}{#if r.rule.kind === "list"}<ListIcon class="size-5" />{:else}<NetworkIcon class="size-5" />{/if}{/snippet}
    {#snippet title(r)}<span class="tabular-nums">{r.title}</span>{/snippet}
    {#snippet badges(r)}
      {#if r.missing}<StatusBadge tone="danger">Missing</StatusBadge>{:else if r.empty}<StatusBadge tone="warning">Allows nobody</StatusBadge>{/if}
      {#if !isSaved(r.rule)}<span class="size-1.5 rounded-full bg-warning" title="Not saved yet"></span><span class="sr-only">Not saved</span>{/if}
    {/snippet}
    {#snippet meta(r)}{#if r.facts}<span title={r.facts}>{r.facts}</span>{/if}{/snippet}
    {#snippet trailing(r)}
      <Button variant="ghost" size="icon-sm" class="hover:text-destructive" aria-label="Remove {r.title}" title="Remove" onclick={() => remove(r.rule)}>
        <XIcon />
      </Button>
    {/snippet}
    {#snippet empty()}
      <CompactEmpty
        icon={GlobeIcon}
        text={verb === "download"
          ? "Add a network list or addresses to allow downloads only from them."
          : "Add a network list, such as GitHub Actions, or addresses to allow publishing only from them."}
      />
    {/snippet}
  </ItemList>
  {/if}
</AccessPart>

<ChoiceDialog
  bind:open={listOpen}
  title="Add network lists"
  description={limits}
  noun={["network list", "network lists"]}
  items={addable}
  loading={!lists && !listsError}
  error={listsError ? `The network lists did not load: ${listsError}` : ""}
  none={lists?.length ? "Every network list is on this repository already." : "No network list exists yet."}
  newLabel="New network list"
  newHref={auth.can("network.manage") ? href("/settings/networks/new") : undefined}
  onAdd={addLists}
/>

<ShortFormDialog
  bind:open={pasteOpen}
  title="Add addresses"
  description={limits}
  submitLabel={found.length ? `Add ${rangeCount(found.length)}` : "Add"}
  canSubmit={found.length > 0}
  onSubmit={addRanges}
>
  {#if pasteOpen}
    <RangePaste id="{id}-paste" label="Addresses" buttons={false} bind:found />
  {/if}
  <p class="text-meta text-muted-foreground">Save the page to keep the change.</p>
</ShortFormDialog>
