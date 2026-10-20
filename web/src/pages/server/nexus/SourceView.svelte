<!--
  One Nexus server, top to bottom: the header (the status and its line, Sync now or Cancel the
  sync, Switch over, Delete); every Nexus repository in one pick table, in the order a save sets
  them up (repositories and proxies, then groups with their members, then what this server cannot
  copy), each row with its state after a save, saved with one button that also starts a sync; the
  running or chosen sync with its progress and log; the history of syncs; the connection, saved
  from the SaveBar. The parent keys it by id.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { toast } from "svelte-sonner";
  import { api, errorMessage, type NexusMapping, type NexusSourceDetail, type NexusSourceRepo, type NexusSync } from "$lib/api";
  import { Keyset, Resource, type TableSource } from "$lib/load.svelte";
  import { Draft } from "$lib/unsaved.svelte";
  import { href, navigate, repoSettingsPath, router } from "$lib/router.svelte";
  import { formatCount, formatRelative } from "$lib/format";
  import { count, someNames } from "$lib/words";
  import { cn } from "$lib/utils";
  import { Button } from "$lib/components/ui/button";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import { Input } from "$lib/components/ui/input";
  import * as InputGroup from "$lib/components/ui/input-group";
  import * as Select from "$lib/components/ui/select";
  import { Spinner } from "$lib/components/ui/spinner";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import { Separator } from "$lib/components/ui/separator";
  import FormatIcon from "$lib/components/FormatIcon.svelte";
  import FormPage from "$lib/components/FormPage.svelte";
  import FormSection from "$lib/components/FormSection.svelte";
  import FormGrid from "$lib/components/FormGrid.svelte";
  import FormRow from "$lib/components/FormRow.svelte";
  import DataTable, { type Column } from "$lib/components/DataTable.svelte";
  import Cell from "$lib/components/Cell.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import StatusBadge, { type Tone } from "$lib/components/StatusBadge.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import FileSize from "$lib/components/FileSize.svelte";
  import OptionRow from "$lib/components/OptionRow.svelte";
  import SaveBar from "$lib/components/SaveBar.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import ShortFormDialog from "$lib/components/ShortFormDialog.svelte";
  import SyncDetail from "./SyncDetail.svelte";
  import { durationText, everyText, howText, kindText, progressLine, sourceStatus, syncState } from "./words";
  import ArrowRightLeftIcon from "@lucide/svelte/icons/arrow-right-left";
  import RefreshIcon from "@lucide/svelte/icons/refresh-cw";
  import HistoryIcon from "@lucide/svelte/icons/history";
  import SearchIcon from "@lucide/svelte/icons/search";

  let { id, sync }: { id: number; sync: number | null } = $props();

  const fid = $props.id();
  const path = untrack(() => `/settings/nexus/${id}`);

  const source = new Resource(() => api.nexus.get(id));
  const syncs = new Keyset((p) => api.nexus.syncs(id, p));

  const d = $derived(source.data);
  const mirroring = $derived(d?.state === "mirroring");
  const running = $derived(!!d?.running_sync_id);
  const editable = $derived(mirroring && !running);

  function reloadAll() {
    source.reload();
    syncs.reload();
  }

  // --- the sync on the page: the chosen one, else the running one, else the newest ---
  const latest = $derived(d ? d.running_sync_id || d.last_sync?.id || null : null);
  const shown = $derived(sync ?? latest);
  const filter = $derived(router.query.get("repo") ?? "");
  const filterHref = (repo: string | null) => href(path, { sync: shown ? String(shown) : undefined, repo: repo ?? undefined });

  // The progress of the running sync, from the sync detail, so the status line moves with it.
  let live = $state<NexusSync | null>(null);
  const lastSync = $derived(live && d?.last_sync && live.id === d.last_sync.id ? live : (d?.last_sync ?? null));

  // A clock for the relative times of the status line.
  let now = $state(Date.now());
  $effect(() => {
    const t = setInterval(() => (now = Date.now()), 30_000);
    return () => clearInterval(t);
  });

  // When the next scheduled sync is due, read the source again, so a sync that starts shows.
  $effect(() => {
    const at = d?.next_sync_at;
    if (!at || running) return;
    const ms = new Date(at).getTime() - Date.now();
    const timer = setTimeout(reloadAll, ms > 0 ? ms + 5000 : 30_000);
    return () => clearTimeout(timer);
  });

  // --- the repositories to copy: one list, in the order a save sets them up ---
  type Opt = { target: string; anon: boolean; answers: Record<string, string>; user: string; pw: string; members: string[] };
  /** The picked Nexus repositories, groups included (Nexus names are unique across its kinds). */
  let picked = $state(new Set<string>());
  let opts = $state<Record<string, Opt>>({});
  let query = $state("");

  const all = $derived(d?.repos ?? []);
  const byName = $derived(new Map(all.map((r) => [r.name, r])));
  const isGroup = (r: NexusSourceRepo) => r.type === "group";
  /** Repositories and proxies first, then the groups that read them, then what this server cannot copy. */
  const ordered = $derived([
    ...all.filter((r) => r.supported && !isGroup(r)),
    ...all.filter((r) => r.supported && isGroup(r)),
    ...all.filter((r) => !r.supported),
  ]);
  const groupRows = $derived(all.filter((r) => r.supported && isGroup(r)));
  const shownRows = $derived.by(() => {
    const q = query.trim().toLowerCase();
    return q ? ordered.filter((r) => r.name.toLowerCase().includes(q)) : ordered;
  });

  // The pick last seen by the effect below, so it knows which groups were just picked.
  let seenPick = new Set<string>();

  function initPicks(s: NexusSourceDetail) {
    picked = new Set(s.repos.filter((r) => r.target).map((r) => r.name));
    seenPick = picked;
    opts = Object.fromEntries(
      s.repos.map((r): [string, Opt] => [
        r.name,
        {
          target: r.target || r.name,
          anon: r.public,
          answers: Object.fromEntries(r.questions.filter((q) => q.default).map((q) => [q.key, q.default!])),
          user: r.remote_username,
          pw: "",
          members: r.members_known ? [] : [...r.target_members],
        },
      ]),
    );
  }

  const isNew = (r: NexusSourceRepo) => !r.target && picked.has(r.name);
  // The questions of the target format (format.Importer Plan): a choice shows in its own column,
  // a secret as a field below the table. Formats name their own questions, so the column takes
  // the label when every choice shares it.
  const choiceOf = (r: NexusSourceRepo) => r.questions.find((q) => q.type === "choice");
  const choiceLabels = $derived(new Set(all.filter((r) => r.supported && !r.target).flatMap((r) => choiceOf(r)?.label ?? [])));
  const choiceLabel = $derived(choiceLabels.size === 1 ? [...choiceLabels][0] : "Options");
  const hasChoice = $derived(choiceLabels.size > 0);

  /** A Nexus repository that a group here can read: a supported repository or proxy. */
  const memberOK = (name: string) => {
    const r = byName.get(name);
    return !!r && r.supported && !isGroup(r);
  };
  /** The repositories here that a group gets: its picked Nexus members (or those chosen by hand), in order. */
  function groupMembers(g: NexusSourceRepo, pick = picked): string[] {
    const names = g.members_known ? g.members : (opts[g.name]?.members ?? []);
    return names.filter((m) => memberOK(m) && pick.has(m));
  }
  /** The repositories that can be members of a group whose members Nexus does not show. */
  const candidates = (g: NexusSourceRepo) => all.filter((r) => memberOK(r.name) && r.target_format === g.target_format);
  const groupPickable = (g: NexusSourceRepo) => g.supported && (g.members_known ? g.members.some(memberOK) : candidates(g).length > 0);
  const pickable = (r: NexusSourceRepo) => (isGroup(r) ? groupPickable(r) : r.supported);

  // Picking a group picks its members, since a save sets them up first. A group whose last picked
  // member is unpicked leaves the pick.
  $effect(() => {
    const cur = picked;
    untrack(() => {
      const next = new Set(cur);
      for (const g of groupRows) {
        if (g.members_known && next.has(g.name) && !seenPick.has(g.name)) for (const m of g.members) if (memberOK(m)) next.add(m);
      }
      for (const g of groupRows) if (g.members_known && next.has(g.name) && groupMembers(g, next).length === 0) next.delete(g.name);
      seenPick = next;
      if (next.size !== cur.size || [...next].some((n) => !cur.has(n))) picked = next;
    });
  });

  const same = (a: string[], b: string[]) => a.length === b.length && a.every((x, i) => x === b[i]);
  const pickedRows = $derived(ordered.filter((r) => r.supported && picked.has(r.name)));
  const added = $derived(pickedRows.filter((r) => !r.target));
  // A copied repository that Nexus no longer lists cannot stay in the mapping.
  const removed = $derived(all.filter((r) => r.target && !(r.supported && picked.has(r.name))));
  const membersChanged = $derived(groupRows.filter((g) => g.target && picked.has(g.name) && !same(groupMembers(g), g.target_members)));
  const dirty = $derived(added.length > 0 || removed.length > 0 || membersChanged.length > 0);

  /** What Nexus changed in the members of a group that this server copies already. */
  function nexusChange(g: NexusSourceRepo): string {
    if (!g.target || !g.members_known) return "";
    const plus = g.members.filter((m) => !g.target_members.includes(m));
    const minus = g.target_members.filter((m) => !g.members.includes(m));
    const parts = [];
    if (plus.length) parts.push(`added ${someNames(plus)}`);
    if (minus.length) parts.push(`removed ${someNames(minus)}`);
    if (parts.length) return `Nexus ${parts.join(" and ")}.`;
    return same(g.members, g.target_members) ? "" : "Nexus has the members in another order.";
  }

  /** The words after a row's name: why it cannot be copied, the remote of a proxy, the members of a group. */
  function noteOf(r: NexusSourceRepo): { text: string; warn?: boolean } {
    if (!r.supported) return { text: r.reason || "This server cannot copy it." };
    if (r.type === "proxy") return { text: r.remote_url };
    if (!isGroup(r)) return { text: "" };
    const changed = nexusChange(r);
    if (changed && picked.has(r.name)) return { text: changed, warn: true };
    if (!r.members_known) {
      const mine = groupMembers(r);
      return { text: picked.has(r.name) && mine.length ? `Reads ${someNames(mine)}` : "Nexus does not show its members" };
    }
    if (r.members.length === 0) return { text: "Has no members" };
    if (!picked.has(r.name)) return { text: `Reads ${someNames(r.members)}` };
    const mine = groupMembers(r);
    const out = r.members.filter((m) => !mine.includes(m));
    return { text: `Reads ${someNames(mine)}${out.length ? `; leaves out ${someNames(out)}` : ""}` };
  }

  /** The state of a row after a save, or null for a row that is not copied. */
  function stateOf(r: NexusSourceRepo): { word: string; tone: Tone; title: string } | null {
    if (r.target && !(r.supported && picked.has(r.name))) return { word: "Stops copying", tone: "warning", title: `Saving stops copying it. ${r.target} keeps its files.` };
    if (r.target && isGroup(r) && !same(groupMembers(r), r.target_members))
      return { word: "Members change", tone: "warning", title: `Saving sets the members of ${r.target}.` };
    if (r.target) return { word: "Copied", tone: "success", title: `Copies into ${r.target}` };
    if (!r.supported || !picked.has(r.name)) return null;
    const what = isGroup(r) ? "group" : r.type === "proxy" ? "proxy" : "repository";
    return { word: `New ${what}`, tone: "info", title: `Saving creates this ${what} here.` };
  }

  /** What the one button does, in a sentence. */
  const saveWhat = $derived.by(() => {
    const parts: string[] = [];
    if (added.length) parts.push(`creates ${count(added.length, "repository", "repositories")} here`);
    if (membersChanged.length) parts.push(`sets the members of ${count(membersChanged.length, "group", "groups")}`);
    if (removed.length) parts.push(`stops copying ${count(removed.length, "repository", "repositories")}`);
    if (!parts.length) return "";
    if (pickedRows.length) parts.push("starts a sync");
    return `Saving ${someNames(parts)}.`;
  });

  const nameRE = /^[a-z0-9][a-z0-9._-]{0,63}$/;
  const problem = $derived.by(() => {
    for (const r of added) {
      const t = opts[r.name]?.target.trim() ?? "";
      if (!t) return `Enter the name here of ${r.name}.`;
      if (!nameRE.test(t)) return `Change the name here of ${r.name}: use lowercase letters, digits, dots, dashes and underscores.`;
    }
    const seen = new Set<string>();
    for (const r of pickedRows) {
      const t = r.target || (opts[r.name]?.target.trim() ?? "");
      if (seen.has(t)) return `Two repositories have the name ${t} here. Give one of them another name.`;
      seen.add(t);
    }
    for (const g of groupRows) if (picked.has(g.name) && !g.members_known && groupMembers(g).length === 0) return `Choose the members of ${g.name}.`;
    return "";
  });

  const saveLabel = $derived(added.length > 0 ? `Copy ${count(pickedRows.length, "repository", "repositories")}` : "Save");

  function mapping(r: NexusSourceRepo): NexusMapping {
    const o = opts[r.name];
    const create = !r.target;
    const m: NexusMapping = { source_repo: r.name, target: r.target || o.target.trim(), create, anonymous_read: o.anon };
    if (create && r.questions.length) m.answers = { ...o.answers };
    if (create && r.type === "proxy" && r.remote_username && o.pw) {
      m.remote_username = o.user.trim();
      m.remote_password = o.pw;
    }
    if (r.type === "group" && !r.members_known) m.members = groupMembers(r);
    return m;
  }

  function setPublic(names: Set<string>, pub: boolean) {
    for (const n of names) {
      const r = all.find((x) => x.name === n);
      if (r && !r.target && opts[n]) opts[n].anon = pub;
    }
  }

  let saving = $state(false);
  let saveError = $state("");
  let stopOpen = $state(false);

  async function saveRepos() {
    const maps = pickedRows.map(mapping);
    const next = await api.nexus.setRepos(id, maps);
    source.data = next;
    initPicks(next);
    live = null;
    syncs.reload();
    navigate(path, { replace: true });
    toast.success(next.running_sync_id ? `Started a sync of ${count(maps.length, "repository", "repositories")}` : "Saved the repositories to copy");
  }

  function startSave() {
    if (removed.length > 0) {
      stopOpen = true;
      return;
    }
    saving = true;
    saveError = "";
    saveRepos()
      .catch((e) => (saveError = errorMessage(e)))
      .finally(() => (saving = false));
  }

  // --- the members of a group that Nexus does not show ---
  let membersOpen = $state(false);
  let membersOf = $state<NexusSourceRepo | null>(null);
  let membersPick = $state<string[]>([]);
  function chooseMembers(g: NexusSourceRepo) {
    membersOf = g;
    membersPick = groupMembers(g);
    membersOpen = true;
  }
  function keepMembers() {
    const g = membersOf;
    if (!g || !opts[g.name]) return;
    const chosen = candidates(g)
      .map((r) => r.name)
      .filter((n) => membersPick.includes(n));
    opts[g.name].members = chosen;
    picked = new Set([...picked, ...chosen]);
  }

  // --- the connection ---
  type Conn = { base_url: string; username: string; password: string };
  let draft = $state<Draft<Conn> | null>(null);
  let inited = false;
  $effect(() => {
    const data = source.data;
    if (!data) return;
    untrack(() => {
      if (!inited || !dirty || data.running_sync_id) initPicks(data);
      inited = true;
      if (!draft || draft.count === 0) draft = new Draft({ base_url: data.base_url, username: "", password: "" });
    });
  });

  async function saveConnection() {
    if (!draft || !d) return;
    const v = draft.value;
    const was = draft.saved;
    if ((v.username === "") !== (v.password === "")) throw new Error("Enter both the username and the password, or neither to keep the saved ones.");
    await api.nexus.update(id, {
      base_url: v.base_url.trim() !== was.base_url ? v.base_url.trim() : undefined,
      username: v.username || undefined,
      password: v.password || undefined,
    });
    draft.commit({ base_url: v.base_url.trim(), username: "", password: "" });
    toast.success(`Saved ${d.name}`);
    source.reload();
  }

  // --- the actions of the header ---
  let syncBusy = $state(false);
  let actionError = $state("");
  async function syncNow() {
    syncBusy = true;
    actionError = "";
    try {
      await api.nexus.sync(id);
      toast.success("Started a sync");
      live = null;
      navigate(path, { replace: true });
      reloadAll();
    } catch (err) {
      actionError = errorMessage(err);
    } finally {
      syncBusy = false;
    }
  }

  let cancelOpen = $state(false);
  async function cancelSync() {
    if (!d?.running_sync_id) return;
    await api.nexus.cancelSync(d.running_sync_id);
    toast.success("Cancelled the sync");
    reloadAll();
  }

  let switchOpen = $state(false);
  async function switchOver() {
    const s = await api.nexus.cutOver(id);
    toast.success("Started the switch-over");
    navigate(path, { query: { sync: String(s.id) }, replace: true });
    reloadAll();
  }

  let deleteOpen = $state(false);
  function askDelete() {
    if (running) toast.info("Cancel the sync first.");
    else deleteOpen = true;
  }
  async function remove() {
    await api.nexus.delete(id);
    toast.success(`Deleted the Nexus server ${d?.name ?? ""}`);
    navigate("/settings/nexus", { force: true });
  }

  /** The line next to the status badge. */
  const statusLine = $derived.by(() => {
    if (!d) return "";
    const s = lastSync;
    switch (d.status) {
      case "no_repos":
        return "Pick the Nexus repositories to copy.";
      case "syncing":
        return s ? progressLine(s) : "Copying.";
      case "up_to_date": {
        let line = s?.finished_at ? `Checked ${formatRelative(s.finished_at, Math.max(now, Date.parse(s.finished_at)))}, ${count(s.imported, "new file", "new files")}.` : "";
        if (d.next_sync_at) line += ` Next check ${new Date(d.next_sync_at).getTime() > now ? formatRelative(d.next_sync_at, now) : "soon"}.`;
        return line.trim();
      }
      case "failed":
        return d.last_error;
      case "cut_over":
        return "Uploads go to this server.";
    }
  });

  const lastFailed = $derived(d?.last_sync?.failed ?? 0);
  const syncHref = (s: NexusSync) => href(path, { sync: String(s.id) });
  const tookText = (s: NexusSync) => (s.finished_at ? durationText(new Date(s.finished_at).getTime() - new Date(s.started_at).getTime()) : "");
  const quietDanger = "hover:bg-destructive/10 hover:text-destructive";
  const link = "rounded-sm text-link underline-offset-4 hover:underline";

  const repoSource: TableSource<NexusSourceRepo> = {
    get items() {
      return shownRows;
    },
    get loading() {
      return source.loading && !source.data;
    },
    error: "",
  };
  const repoColumns = $derived<Column[]>([
    { label: "Nexus repository", grow: true },
    { label: "Kind", hideOnPhone: true },
    { label: "State", hideOnPhone: true },
    { label: "Name here" },
    { label: "Who can download", hideOnPhone: true },
    ...(hasChoice ? [{ label: choiceLabel, hideOnPhone: true }] : []),
  ]);
</script>

{#snippet who(r: NexusSourceRepo, o: Opt)}
  <Select.Root type="single" value={o.anon ? "anyone" : "access"} onValueChange={(v) => (o.anon = v === "anyone")}>
    <Select.Trigger size="sm" class="w-32" aria-label="Who can download {r.name}">
      <span class="truncate">{o.anon ? "Public" : "Private"}</span>
    </Select.Trigger>
    <Select.Content>
      <Select.Item value="anyone" label="Public" />
      <Select.Item value="access" label="Private" />
    </Select.Content>
  </Select.Root>
{/snippet}

{#snippet bulk()}
  {#if editable}<Button variant="outline" size="sm" onclick={() => setPublic(picked, true)}>Make public</Button>
  <Button variant="outline" size="sm" onclick={() => setPublic(picked, false)}>Make private</Button>{/if}
{/snippet}

{#snippet repoRow(r: NexusSourceRepo)}
  {@const o = opts[r.name]}
  {@const note = noteOf(r)}
  {@const st = stateOf(r)}
  <Cell grow>
    <span class="flex min-w-0 items-baseline gap-2">
      <span class={cn("shrink-0", r.supported ? "font-medium" : "text-muted-foreground")}>{r.name}</span>
      {#if note.text}
        <span class={cn("min-w-0 truncate", note.warn ? "text-warning" : "text-muted-foreground")} title={note.text}>{note.text}</span>
      {/if}
      {#if editable && isGroup(r) && !r.members_known && picked.has(r.name)}
        <button type="button" class={cn(link, "shrink-0 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50")} onclick={() => chooseMembers(r)}>
          {groupMembers(r).length ? "Change members" : "Choose members"}
        </button>
      {/if}
    </span>
  </Cell>
  <Cell hideOnPhone muted>
    <span class="flex items-center gap-2 whitespace-nowrap">
      <FormatIcon format={r.target_format} />
      {kindText(r.format, r.type)}
    </span>
  </Cell>
  <Cell hideOnPhone>
    {#if st}
      <StatusBadge tone={st.tone} title={st.title}>{st.word}</StatusBadge>
    {:else if r.supported}
      <span class="text-muted-foreground">Not copied</span>
    {/if}
  </Cell>
  <Cell>
    {#if r.target}
      <a class={cn(link, removed.includes(r) && "text-muted-foreground line-through")} href={href(repoSettingsPath(r.target))}>{r.target}</a>
    {:else if o && isNew(r)}
      {#if editable}
        <Input class="h-8 w-48 max-sm:w-36" aria-label="Name here for {r.name}" autocomplete="off" spellcheck={false} bind:value={o.target} />
      {:else}
        {o.target}
      {/if}
    {/if}
  </Cell>
  <Cell hideOnPhone>
    {#if editable && o && isNew(r)}{@render who(r, o)}{/if}
  </Cell>
  {#if hasChoice}
    {@const q = choiceOf(r)}
    <Cell hideOnPhone>
      {#if editable && o && isNew(r) && q}
        {@const chosen = q.options?.find((x) => x.value === o.answers[q.key])}
        <Select.Root type="single" value={o.answers[q.key] ?? ""} onValueChange={(v) => (o.answers[q.key] = v)}>
          <Select.Trigger size="sm" class="w-48" aria-label="{q.label} of {r.name}">
            <span class="truncate">{chosen?.label ?? "Choose"}</span>
          </Select.Trigger>
          <Select.Content>
            {#each q.options ?? [] as x (x.value)}
              <Select.Item value={x.value} label={x.label} />
            {/each}
          </Select.Content>
        </Select.Root>
      {/if}
    </Cell>
  {/if}
{/snippet}

{#snippet repoFooter()}
  {#if running}
    <p class="text-meta text-muted-foreground">You can change the repositories when this sync ends.</p>
  {:else}
    <Button disabled={!dirty || !!problem || saving} onclick={startSave}>
      {#if saving}<Spinner />{/if}
      {saveLabel}
    </Button>
    {#if dirty && problem}
      <p class="text-meta text-warning">{problem}</p>
    {:else if saveWhat}
      <p class="text-meta text-muted-foreground">{saveWhat}</p>
    {/if}
    {#if saveError}<ErrorState inline error={saveError} />{/if}
  {/if}
{/snippet}

{#if source.loading && !d}
  <PageHeader title="Nexus server" />
  <Loading rows={6} />
{:else if source.error && !d}
  <PageHeader title="Nexus server" />
  <ErrorState error={source.error} title="The Nexus server did not load" onRetry={source.reload} />
{:else if d && draft}
  {@const v = draft.value}
  <PageHeader title={d.name} description={d.base_url}>
    {#snippet meta()}
      <StatusBadge tone={sourceStatus[d.status].tone}>{sourceStatus[d.status].word}</StatusBadge>
      {#if statusLine}
        <span class={cn("text-meta tabular-nums", d.status === "failed" ? "text-destructive" : "text-muted-foreground")}>{statusLine}</span>
      {/if}
    {/snippet}
    {#snippet actions()}
      {#if mirroring}
        {#if running}
          <Button variant="outline" onclick={() => (cancelOpen = true)}>Cancel the sync</Button>
        {:else}
          <Button variant="outline" disabled={syncBusy || d.repo_count === 0} onclick={syncNow}>
            {#if syncBusy}<Spinner />{:else}<RefreshIcon />{/if}
            Sync now
          </Button>
        {/if}
        <Button variant="outline" disabled={running || d.repo_count === 0} onclick={() => (switchOpen = true)}>Switch over</Button>
      {/if}
      <Button variant="ghost" class={quietDanger} aria-disabled={running} title={running ? "Cancel the sync first" : undefined} onclick={askDelete}>Delete</Button>
    {/snippet}
  </PageHeader>

  {#if actionError}<div class="mb-6"><ErrorState inline error="The sync did not start: {actionError}" /></div>{/if}

  <Section
    title="Repositories to copy"
    description="Repositories and proxies are set up first, then the groups that read them. Picking a group picks its members."
    footer={mirroring ? repoFooter : undefined}
  >
    {#snippet actions()}
      {#if ordered.length > 10}
        <InputGroup.Root class="sm:w-64">
          <InputGroup.Addon><SearchIcon /></InputGroup.Addon>
          <InputGroup.Input bind:value={query} type="search" placeholder="Filter by name" aria-label="Filter the Nexus repositories" />
        </InputGroup.Root>
      {/if}
    {/snippet}
    {#if d.nexus_error}
      <div class="mb-4"><ErrorState inline error="Nexus did not list its repositories, so this shows only the copied ones: {d.nexus_error}" /></div>
    {/if}
    <DataTable
      source={repoSource}
      caption="Nexus repositories, proxies and groups, in the order a save sets them up"
      key={(r) => r.name}
      columns={repoColumns}
      bind:picked
      {pickable}
      pickLabel={(r) => r.name}
      bulk={mirroring ? bulk : undefined}
      pickOff={!editable}
      pickOnRow
      selectAllLabel="Select all supported"
    >
      {#snippet row(r)}{@render repoRow(r)}{/snippet}
      {#snippet empty()}
        <CompactEmpty icon={ArrowRightLeftIcon} text={query.trim() ? "No Nexus repository has this name." : "Nexus lists no repositories. Check the permissions of the account on the Nexus server."} />
      {/snippet}
    </DataTable>

    {#if editable}
      {@const fresh = pickedRows.filter((r) => isNew(r))}
      {@const secrets = fresh.flatMap((r) => r.questions.filter((q) => q.type === "secret").map((q) => ({ r, q })))}
      {@const notes = fresh.filter((r) => typeof r.facts.note === "string")}
      {@const remotes = fresh.filter((r) => r.type === "proxy" && r.remote_username)}
      {#if secrets.length || remotes.length || notes.length}
        <Separator class="my-6" />
        <h3 class="text-label">What Nexus does not give</h3>
        {#if secrets.length || remotes.length}
          <div class="mt-4">
            <FormGrid>
              {#each secrets as { r, q } (r.name + "/" + q.key)}
                {@const o = opts[r.name]}
                <FormRow label="{q.label} of {r.name}" for="{fid}-q-{r.name}-{q.key}" help={q.help}>
                  <Input
                    id="{fid}-q-{r.name}-{q.key}"
                    type="password"
                    autocomplete="new-password"
                    aria-describedby="{fid}-q-{r.name}-{q.key}-help"
                    value={o.answers[q.key] ?? ""}
                    oninput={(e) => (o.answers[q.key] = e.currentTarget.value)}
                  />
                </FormRow>
              {/each}
              {#each remotes as r (r.name)}
                {@const o = opts[r.name]}
                <FormRow label="Remote username of {r.name}" for="{fid}-ru-{r.name}" help="The remote needs a username and password. Nexus gives the username, but not the password.">
                  <Input id="{fid}-ru-{r.name}" autocomplete="off" aria-describedby="{fid}-ru-{r.name}-help" bind:value={o.user} />
                </FormRow>
                <FormRow label="Remote password of {r.name}" for="{fid}-rp-{r.name}" help="Without the password, the proxy asks the remote with no username and password.">
                  <Input id="{fid}-rp-{r.name}" type="password" autocomplete="new-password" aria-describedby="{fid}-rp-{r.name}-help" bind:value={o.pw} />
                </FormRow>
              {/each}
            </FormGrid>
          </div>
        {/if}
        {#each notes as r (r.name)}
          <p class="mt-4 text-meta text-pretty text-muted-foreground"><span class="font-medium text-foreground">{r.name}</span>: {String(r.facts.note)}</p>
        {/each}
      {/if}
    {/if}
  </Section>

  {#if shown !== null}
    {#key shown}
      <SyncDetail
        id={shown}
        {filter}
        {filterHref}
        onClose={sync !== null && sync !== latest ? () => navigate(path, { replace: true }) : undefined}
        onChanged={reloadAll}
        onProgress={(s) => (live = s)}
      />
    {/key}
  {/if}

  <Section title="Syncs">
    {#if mirroring}
      <p class="mb-4 text-meta text-muted-foreground">
        This server checks Nexus for new files every {everyText(d.sync_interval)}.
        <a class={link} href={href("/settings/general")}>Change the interval</a>
      </p>
    {/if}
    {#if !mirroring && d.cutover_failed > 0}
      <p class="mb-4 text-meta text-muted-foreground">
        {formatCount(d.cutover_failed)} files failed in the final sync and are not on this server.
        <a class={link} href={href(path, { sync: String(d.cutover_sync_id) })}>Show them</a>
      </p>
    {/if}
    <DataTable
      source={syncs}
      caption="Syncs"
      key={(s) => s.id}
      href={syncHref}
      selected={(s) => s.id === shown}
      columns={[
        { label: "Started", grow: true },
        { label: "How", hideOnPhone: true },
        { label: "Took", align: "right", hideOnPhone: true },
        { label: "New files", align: "right" },
        { label: "Size", align: "right", hideOnPhone: true },
        { label: "Failed", align: "right" },
        { label: "State" },
      ]}
    >
      {#snippet row(s)}
        <Cell grow>
          <a href={syncHref(s)} class="rounded-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"><RelativeTime at={s.started_at} /></a>
        </Cell>
        <Cell hideOnPhone muted text={howText(s)} />
        <Cell num muted hideOnPhone>{tookText(s)}</Cell>
        <Cell num>{formatCount(s.imported)}</Cell>
        <Cell num muted hideOnPhone><FileSize bytes={s.bytes} /></Cell>
        <Cell num class={s.failed > 0 ? "text-destructive" : "text-muted-foreground"}>{formatCount(s.failed)}</Cell>
        <Cell><StatusBadge tone={syncState[s.state].tone}>{syncState[s.state].word}</StatusBadge></Cell>
      {/snippet}
      {#snippet empty()}
        <CompactEmpty
          icon={HistoryIcon}
          text={mirroring ? "No sync yet. Pick repositories above and save them to start the first sync." : "This Nexus server never synced."}
        />
      {/snippet}
    </DataTable>
  </Section>

  {#if mirroring}
    <FormPage>
      <FormSection title="Connection">
        <FormGrid>
          <FormRow label="Address" for="{fid}-url">
            <Input id="{fid}-url" type="url" bind:value={v.base_url} />
          </FormRow>
          <FormRow label="Username" for="{fid}-user" help="Leave it empty to keep the saved account.">
            <Input id="{fid}-user" autocomplete="off" placeholder="Unchanged" aria-describedby="{fid}-user-help" bind:value={v.username} />
          </FormRow>
          <FormRow label="Password" for="{fid}-pass" help="A new username needs its password.">
            <Input id="{fid}-pass" type="password" autocomplete="new-password" placeholder="Unchanged" aria-describedby="{fid}-pass-help" bind:value={v.password} />
          </FormRow>
        </FormGrid>
      </FormSection>
    </FormPage>
  {/if}

  <SaveBar count={draft.count} onDiscard={draft.reset} onSave={saveConnection} />

  <ConfirmDialog
    bind:open={stopOpen}
    title="Stop copying {count(removed.length, 'repository', 'repositories')}?"
    description="{someNames(removed.map((r) => r.name))}."
    confirmLabel={added.length ? saveLabel : `Stop copying ${count(removed.length, "repository", "repositories")}`}
    destructive
    changes={[
      "Stops copying them from Nexus",
      "Keeps their files on this server",
      "Allows uploads to them on this server",
      ...(added.length ? [`Starts a sync of the ${count(pickedRows.length, "picked repository", "picked repositories")}`] : []),
    ]}
    onConfirm={saveRepos}
  />

  <ConfirmDialog
    bind:open={cancelOpen}
    title="Cancel this sync?"
    confirmLabel="Cancel the sync"
    destructive
    changes={["Stops copying files", "Keeps the files already copied"]}
    onConfirm={cancelSync}
  />

  <ConfirmDialog
    bind:open={switchOpen}
    title="Switch over from {d.name}?"
    confirmLabel="Run the final sync and switch over"
    destructive
    changes={[
      "Make sure every build uses this server before you continue",
      "Runs a final sync of every picked repository",
      "Allows uploads to the copied repositories on this server",
      "Stops copying from Nexus, and deletes the saved Nexus password",
      "Keeps files here that were deleted in Nexus",
      ...(lastFailed > 0 ? [`Leaves out ${formatCount(lastFailed)} files that failed in the last sync`] : []),
    ]}
    onConfirm={switchOver}
  />

  <ConfirmDialog
    bind:open={deleteOpen}
    title="Delete the Nexus server {d.name}?"
    confirmLabel="Delete Nexus server"
    destructive
    changes={["Deletes the Nexus server, its sync history and its saved password", "Keeps the copied repositories and their files"]}
    onConfirm={remove}
  />

  {#if membersOf}
    {@const g = membersOf}
    <ShortFormDialog
      bind:open={membersOpen}
      title="Members of {g.name}"
      description="The Nexus account cannot read the members of this group. Choose the repositories it reads; they are copied too."
      submitLabel="Keep these members"
      canSubmit={membersPick.length > 0}
      onSubmit={keepMembers}
    >
      {#each candidates(g) as r (r.name)}
        <OptionRow id="{fid}-m-{r.name}" label={r.name} description={kindText(r.format, r.type)}>
          {#snippet control()}
            <Checkbox
              id="{fid}-m-{r.name}"
              checked={membersPick.includes(r.name)}
              onCheckedChange={(c) => (membersPick = c === true ? [...membersPick, r.name] : membersPick.filter((n) => n !== r.name))}
            />
          {/snippet}
        </OptionRow>
      {:else}
        <p class="text-meta text-muted-foreground">Nexus has no repository of this format that this server can copy.</p>
      {/each}
    </ShortFormDialog>
  {/if}
{/if}
