<!--
  "From where" of the Access tab, for downloads or for publishes: the network lists and the
  addresses that requests must come from. No row means any network. Add network list and Add
  addresses each open a short form in a dialog. The parent holds the rules in its draft and saves
  them; a list or an address added here shows at once and saves with the page.

    <FromWhere id="read" repo="maven" verb="download" lists={ipLists.data} bind:rules={draft.value.read} />
-->
<script lang="ts">
  import type { IPList, IPRule } from "$lib/api";
  import type { TableSource } from "$lib/load.svelte";
  import { formatCount } from "$lib/format";
  import { rangeSize } from "$lib/network";
  import { href } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { Button } from "$lib/components/ui/button";
  import DataTable from "$lib/components/DataTable.svelte";
  import Cell from "$lib/components/Cell.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import RangePaste from "$lib/components/RangePaste.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import ShortFormDialog from "$lib/components/ShortFormDialog.svelte";
  import AccessPart from "./AccessPart.svelte";
  import ChoiceDialog from "./ChoiceDialog.svelte";
  import GlobeIcon from "@lucide/svelte/icons/globe";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import XIcon from "@lucide/svelte/icons/x";

  let {
    id,
    repo,
    verb,
    lists,
    listsError = "",
    saved,
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
    rules?: IPRule[];
  } = $props();

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
          link: auth.can("network") ? href(`/settings/networks/list/${encodeURIComponent(r.value)}`) : undefined,
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
  title="From where"
  help={rules.length ? `${whom} must come from one of these networks, for everyone.` : undefined}
>
  {#snippet actions()}
    <Button variant="outline" size="sm" onclick={() => (listOpen = true)}><PlusIcon />Add network list</Button>
    <Button variant="outline" size="sm" onclick={() => (pasteOpen = true)}><PlusIcon />Add addresses</Button>
  {/snippet}

  <DataTable
    {source}
    caption="Networks that can {verb} with {repo}"
    key={(r) => r.key}
    columns={[{ label: "Network", class: "w-64 min-w-64 max-sm:min-w-0" }, { label: "Details", grow: true, hideOnPhone: true }, { label: "", srLabel: "Actions" }]}
  >
    {#snippet row(r)}
      <Cell strong class="w-64 max-w-64 min-w-64 max-sm:min-w-0">
        <span class="flex min-w-0 items-center gap-2">
          {#if !isSaved(r.rule)}<span class="size-1.5 shrink-0 rounded-full bg-warning" title="Not saved yet"></span><span class="sr-only">Not saved:</span>{/if}
          {#if r.link}
            <a href={r.link} class="truncate underline-offset-4 hover:underline">{r.title}</a>
          {:else}
            <span class="truncate tabular-nums">{r.title}</span>
          {/if}
        </span>
      </Cell>
      <Cell grow hideOnPhone muted>
        <span class="flex min-w-0 items-center gap-2">
          {#if r.missing}<StatusBadge tone="danger">Missing</StatusBadge>{:else if r.empty}<StatusBadge tone="warning">Allows nobody</StatusBadge>{/if}
          <span class="truncate" title={r.facts}>{r.facts}</span>
        </span>
      </Cell>
      <Cell actions>
        <Button variant="ghost" size="icon-sm" class="hover:text-destructive" aria-label="Remove {r.title}" title="Remove" onclick={() => remove(r.rule)}>
          <XIcon />
        </Button>
      </Cell>
    {/snippet}
    {#snippet empty()}
      <CompactEmpty
        icon={GlobeIcon}
        title="Any network"
        text={verb === "download"
          ? "Add a network list or addresses to allow downloads only from them."
          : "Add a network list, such as GitHub Actions, or addresses to allow publishing only from them."}
      />
    {/snippet}
  </DataTable>
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
  newHref={auth.can("network") ? href("/settings/networks/new") : undefined}
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
